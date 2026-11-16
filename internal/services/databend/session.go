package databend

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

// Provider binds one profile to its original installed process. Publication in
// builtin/chooser/URI entry points is independent of this adapter.
type Provider struct {
	Store   *driverstore.Store
	Started func(int) error
	// Observe receives private copies of complete original receipts. Callers
	// must be concurrency-safe; presentation is never accessed from workers.
	Observe func(string, agentbridge.Receipt)
}

type Session struct {
	profile connections.Profile
	process *ManagedProcess
	queries *agentbridge.QueryPool
	observe func(string, agentbridge.Receipt)
	once    sync.Once
	err     error
}

func (p Provider) Open(ctx context.Context, profile connections.Profile, secret string) (contract.Session, error) {
	if ctx == nil || profile.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	profile, err := connections.NormalizeProfile(profile)
	if err != nil || profile.Kind != connections.Databend {
		return nil, connections.ErrInvalid
	}
	if profile.DatabendOptions.DriverVersion == "" || profile.DatabendOptions.DriverSHA256 == "" {
		return nil, ErrManagedDriver
	}
	config, err := connections.DatabendProfileManagedConfig(profile)
	if err != nil {
		return nil, err
	}
	store := p.Store
	if store == nil {
		if profile.DatabendOptions.DriverDirectory == "" {
			store, err = driverstore.Default()
		} else {
			store, err = driverstore.New(profile.DatabendOptions.DriverDirectory)
		}
		if err != nil {
			return nil, ErrManagedDriver
		}
	}
	process, err := StartManaged(ctx, store, config, secret, p.Started)
	if err != nil {
		return nil, err
	}
	s := &Session{profile: profile, process: process, observe: p.Observe}
	s.observed("handshake", process.StartupReceipt())
	err = s.withMetadata(ctx, func(child context.Context, original *ManagedSession) error {
		q, err := s.query(child, original, "SELECT version(), current_database()", 1)
		if err != nil {
			return err
		}
		if len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 2 ||
			!strings.HasPrefix(q.Sets[0].Rows[0][0].Text, "Databend Query ") || q.Sets[0].Rows[0][1].Text != profile.Database {
			return contract.ErrUnsupported
		}
		return nil
	})
	if err == nil {
		a := profile.DatabendOptions
		s.queries, err = agentbridge.NewQueryPool(process.runtime.Client, map[string]any{}, agentbridge.QueryConfig{
			InitializeSQL: "USE " + quoteName(profile.Database), DefaultSchema: profile.Database,
			MaxLeases: a.SQLMaxConnections, MaxRows: 10000, TimeoutSeconds: a.QueryTimeoutSeconds,
			Split: sqltext.SplitDatabend, Decoder: DecodeJDBCValue,
			RetainUnsentSteps: true,
			OpenConnection: func(child context.Context) (*agentbridge.Session, func(), error) {
				original, receipts, err := process.OpenSession(child, false)
				dispatched := false
				for _, receipt := range receipts {
					s.observed("query-open", receipt)
					dispatched = dispatched || receipt.Dispatched
				}
				if err != nil {
					if dispatched {
						err = errors.Join(contract.ErrSQLSessionLost, err)
					}
					return nil, nil, err
				}
				return original.original, func() { _, _ = original.Close() }, nil
			},
			ObserveStatement: func(_ string, receipt agentbridge.Receipt) {
				s.observed("query-executor", receipt)
			},
			DecodeStatement: decodeStatement,
		})
	}
	if err != nil || ctx.Err() != nil {
		_ = s.Close()
		if err == nil {
			err = ctx.Err()
		}
		return nil, err
	}
	return s, nil
}

func decodeStatement(source string, receipt agentbridge.Receipt) (agentbridge.QuerySnapshot, error) {
	q, err := agentbridge.DecodeQuery(receipt, DecodeJDBCValue)
	if err != nil {
		return q, err
	}
	if kind := sqltext.DatabendMutationKind(source); kind != "" {
		observed, err := DecodeMutationReceipt(kind, receipt)
		if err != nil || observed.State != contract.DataWriteAcknowledged {
			return q, errors.Join(agentbridge.ErrNativeResult, err)
		}
		q.SQL.AffectedRows = observed.AffectedRows
		q.SQL.Sets[0].AffectedRows = observed.AffectedRows
		q.SQL.SetLegacyView()
	}
	return q, nil
}

func (s *Session) observed(method string, receipt agentbridge.Receipt) {
	if s.observe != nil {
		s.observe(method, receipt.Clone())
	}
}
func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Alive() bool        { return s.process.Alive() }
func (s *Session) PID() int           { return s.process.PID() }
func (s *Session) Namespace(database string) contract.NamespaceRef {
	if database == "" {
		database = s.profile.Database
	}
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Schema: database}
}
func (s *Session) Capabilities() contract.Capabilities {
	return contract.Capabilities{Query: true, Metadata: true, Schemas: true}
}
func (s *Session) ReserveQuery(document uint64) (contract.QueryLease, error) {
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	lease, err := s.queries.ReserveQuery(document)
	if err != nil {
		return nil, err
	}
	return &queryLease{original: lease}, nil
}
func (s *Session) Close() error {
	s.once.Do(func() {
		if s.queries != nil {
			s.queries.Close()
		}
		s.err = s.process.Close()
	})
	return s.err
}

func (s *Session) withMetadata(ctx context.Context, fn func(context.Context, *ManagedSession) error) error {
	original, receipts, err := s.process.OpenSession(ctx, true)
	for _, receipt := range receipts {
		s.observed("metadata-open", receipt)
	}
	if err != nil {
		return err
	}
	defer original.Close()
	return fn(ctx, original)
}
func (s *Session) query(ctx context.Context, original *ManagedSession, source string, maxRows int) (contract.SQLResult, error) {
	r, err := original.Call(ctx, "execute_query", map[string]any{"sql": source, "maxRows": maxRows, "timeoutSecs": s.profile.DatabendOptions.QueryTimeoutSeconds})
	s.observed("metadata-query", r)
	if err != nil {
		return contract.SQLResult{NativeJSON: string(r.Result)}, err
	}
	q, err := agentbridge.DecodeQuery(r, DecodeJDBCValue)
	return q.SQL, err
}

func quoteName(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

var _ contract.Session = (*Session)(nil)
var _ contract.QueryProvider = (*Session)(nil)

type queryLease struct{ original contract.QueryLease }

func (l *queryLease) Preconnect(ctx context.Context) error { return l.original.Preconnect(ctx) }
func (l *queryLease) Execute(ctx context.Context, schema, source string) (contract.SQLResult, error) {
	return l.original.Execute(ctx, schema, source)
}
func (l *queryLease) ExecuteStream(ctx context.Context, schema, source string, sink func(int, []contract.Column, []contract.Cell) error) (contract.SQLResult, error) {
	return l.original.ExecuteStream(ctx, schema, source, sink)
}
func (l *queryLease) ExecuteFile(ctx context.Context, schema string, reader io.Reader) (contract.SQLResult, error) {
	if ctx == nil || reader == nil {
		return contract.SQLResult{}, connections.ErrInvalid
	}
	if err := l.CheckQueryLease(); err != nil {
		return contract.SQLResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return contract.SQLResult{}, err
	}
	raw, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return contract.SQLResult{}, errors.Join(agentbridge.ErrLimit, err)
	}
	return l.Execute(ctx, schema, string(raw))
}
func (l *queryLease) Release() { l.original.Release() }
func (l *queryLease) Replace() (contract.QueryLease, error) {
	replacement, err := l.original.Replace()
	if err != nil {
		return nil, err
	}
	return &queryLease{original: replacement}, nil
}
func (l *queryLease) CheckQueryLease() error {
	return l.original.(contract.QueryLeaseStatus).CheckQueryLease()
}
func (l *queryLease) OriginalQueryReceipts() []agentbridge.Receipt {
	return l.original.(interface{ OriginalQueryReceipts() []agentbridge.Receipt }).OriginalQueryReceipts()
}
