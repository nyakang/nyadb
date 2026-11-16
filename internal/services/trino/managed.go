package trino

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

var ErrDriver = errors.New("Trino managed driver is unavailable; install and select the original driver package")

type Provider struct {
	Store   *driverstore.Store
	Started func(int) error
}

func (p Provider) Open(ctx context.Context, profile connections.Profile, password string) (contract.Session, error) {
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	profile, err := connections.NormalizeProfile(profile)
	if err != nil || profile.Kind != connections.Trino || profile.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	if profile.TrinoOptions.Mode != "jdbc" {
		return openHTTPSession(ctx, profile, password)
	}
	if profile.TLSMode == "disable" && password != "" {
		return nil, connections.ErrInvalid
	}
	a := profile.TrinoOptions
	if a.DriverVersion == "" || a.DriverSHA256 == "" {
		return nil, ErrDriver
	}
	store := p.Store
	if store == nil {
		if a.DriverDirectory == "" {
			store, err = driverstore.Default()
		} else {
			store, err = driverstore.New(a.DriverDirectory)
		}
		if err != nil {
			return nil, errors.Join(ErrDriver, err)
		}
	}
	openCtx, stop := context.WithTimeout(ctx, time.Duration(a.ConnectTimeoutSeconds)*time.Second)
	defer stop()
	lease, err := store.Acquire(openCtx, "trino", a.DriverVersion, a.DriverSHA256)
	if err != nil {
		return nil, errors.Join(ErrDriver, err)
	}
	m := lease.Manifest()
	if m.Runtime != "java" || m.MainClass != "com.dbx.agent.trino.TrinoAgent" {
		lease.Close()
		return nil, ErrDriver
	}
	r, err := lease.StartJDBCObserved(openCtx, "trino", driverstore.JDBCSettings{MaxConnections: a.MaxConnections, MetadataReserve: a.MetadataReserve, WaitTimeout: time.Duration(a.PoolWaitTimeoutMilliseconds) * time.Millisecond}, agentbridge.Options{CancelWait: 15 * time.Second}, p.Started)
	if err != nil {
		lease.Close()
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	s := &Session{profile: profile, ctx: root, cancel: cancel, leases: map[*queryLease]bool{}, namespaces: map[string]contract.NamespaceRef{}}
	b := &managedBinding{owner: s, runtime: r, workload: make(chan struct{}, a.MaxConnections-a.MetadataReserve), metadata: make(chan struct{}, max(1, a.MetadataReserve))}
	params := map[string]any{"connection_string": managedURL(profile), "database": profile.Database, "username": profile.Username, "password": password, "sessionRole": "workload"}
	b.params, err = json.Marshal(params)
	s.managed = b
	if err == nil {
		var reader rowReader
		reader, err = b.reader(openCtx, true)
		if err == nil {
			var rows [][]contract.Cell
			rows, err = metadataRows(openCtx, reader, "SELECT version(), current_user")
			if err == nil && (len(rows) != 1 || len(rows[0]) != 2 || rows[0][0].Text != "484" || rows[0][1].Text != profile.Username) {
				err = contract.ErrUnsupported
			}
			err = errors.Join(err, reader.Close())
		}
	}
	if err == nil {
		b.queries, err = agentbridge.NewQueryPool(r.Client, b.params, agentbridge.QueryConfig{InitializeSQL: "SET TIME ZONE 'UTC'", MaxLeases: a.SQLMaxConnections, MaxRows: 10000, TimeoutSeconds: a.QueryTimeoutSeconds, Split: sqltext.SplitTrino, Decoder: DecodeJDBCValue, AcquireConnection: func(ctx context.Context) (func(), error) { return b.acquire(ctx, false) }})
	}
	if err != nil || openCtx.Err() != nil {
		s.Close()
		if err == nil {
			err = openCtx.Err()
		}
		return nil, managedError(err)
	}
	return s, nil
}

func managedURL(p connections.Profile) string {
	u := &url.URL{Scheme: "trino", Host: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Path: "/" + p.Database}
	if p.TrinoOptions.DefaultSchema != "" {
		u.Path += "/" + p.TrinoOptions.DefaultSchema
	}
	options := url.Values{}
	options.Set("timeout", strconv.Itoa(p.TrinoOptions.ConnectTimeoutSeconds)+"s")
	if p.TLSMode != "disable" {
		options.Set("SSL", "true")
		options.Set("SSLVerification", "FULL")
		if p.TrinoOptions.CAFile != "" {
			options.Set("SSLTrustStorePath", p.TrinoOptions.CAFile)
		}
		if p.TrinoOptions.ServerName != "" {
			options.Set("hostnameInCertificate", p.TrinoOptions.ServerName)
		}
	}
	u.RawQuery = options.Encode()
	return "jdbc:" + u.String()
}

type managedBinding struct {
	owner              *Session
	runtime            *driverstore.Runtime
	params             json.RawMessage
	queries            *agentbridge.QueryPool
	workload, metadata chan struct{}
}

func managedError(err error) error {
	if errors.Is(err, agentbridge.ErrLost) || errors.Is(err, agentbridge.ErrClosed) {
		return errors.Join(contract.ErrSQLSessionLost, err)
	}
	return err
}

func (b *managedBinding) acquire(ctx context.Context, metadata bool) (func(), error) {
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	slots := b.workload
	if metadata {
		slots = b.metadata
	}
	wait, cancel := context.WithTimeout(ctx, time.Duration(b.owner.profile.TrinoOptions.PoolWaitTimeoutMilliseconds)*time.Millisecond)
	defer cancel()
	select {
	case <-wait.Done():
		return nil, wait.Err()
	case <-b.owner.ctx.Done():
		return nil, contract.ErrRevoked
	case slots <- struct{}{}:
	}
	if ctx.Err() != nil || !b.owner.Alive() {
		<-slots
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, contract.ErrRevoked
	}
	var once sync.Once
	return func() { once.Do(func() { <-slots }) }, nil
}

func (b *managedBinding) reader(ctx context.Context, metadata bool) (rowReader, error) {
	release, err := b.acquire(ctx, metadata)
	if err != nil {
		return nil, err
	}
	// params are immutable until all original readers/executors finish.
	var params map[string]any
	if json.Unmarshal(b.params, &params) != nil {
		release()
		return nil, contract.ErrRevoked
	}
	if metadata {
		params["sessionRole"] = "metadata"
	}
	s, _, err := b.runtime.Client.OpenSession(ctx, params)
	if err != nil {
		release()
		return nil, managedError(err)
	}
	return &jdbcReader{session: s, release: release, timeout: b.owner.profile.TrinoOptions.QueryTimeoutSeconds}, nil
}

type jdbcReader struct {
	session  *agentbridge.Session
	release  func()
	timeout  int
	pageSize int
	once     sync.Once
	err      error
}

func (r *jdbcReader) Read(ctx context.Context, source string, consume func([]contract.Column, []contract.Cell) error) (err error) {
	// A captured cursor bounds memory and never reruns SQL between pages.
	pageSize := r.pageSize
	if pageSize == 0 {
		pageSize = 1000
	}
	cursor, page, err := r.session.OpenQueryCursor(ctx, agentbridge.QueryCursorOptions{SQL: source, PageSize: pageSize, MaxRows: math.MaxInt32, TimeoutSeconds: r.timeout, Decoder: DecodeJDBCValue})
	if err != nil {
		return managedError(err)
	}
	defer func() { err = errors.Join(err, cursor.Close()) }()
	for {
		set := page.SQL.Sets[0]
		for _, row := range set.Rows {
			if err := consume(append([]contract.Column(nil), set.Columns...), cloneManagedCells(row)); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		page, err = cursor.Fetch(ctx, pageSize)
		if err != nil {
			return managedError(err)
		}
	}
}

func cloneManagedCells(row []contract.Cell) []contract.Cell {
	out := make([]contract.Cell, len(row))
	for i, v := range row {
		out[i] = v.Clone()
	}
	return out
}
func (r *jdbcReader) Close() error {
	r.once.Do(func() { _, r.err = r.session.Close(); r.release() })
	return r.err
}

type managedQueryLease struct {
	owner    *Session
	original contract.QueryLease
	document uint64
	ops      sync.Mutex
	selected string
}

func (l *managedQueryLease) Preconnect(ctx context.Context) error { return l.original.Preconnect(ctx) }
func (l *managedQueryLease) Release()                             { l.original.Release() }
func (l *managedQueryLease) Replace() (contract.QueryLease, error) {
	l.Release()
	return l.owner.ReserveQuery(l.document)
}
func (l *managedQueryLease) CheckQueryLease() error {
	return l.original.(contract.QueryLeaseStatus).CheckQueryLease()
}
func (l *managedQueryLease) OriginalQueryReceipts() []agentbridge.Receipt {
	return l.original.(interface{ OriginalQueryReceipts() []agentbridge.Receipt }).OriginalQueryReceipts()
}
func (l *managedQueryLease) Execute(ctx context.Context, namespace, source string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, namespace, source, nil)
}
func (l *managedQueryLease) ExecuteFile(ctx context.Context, namespace string, reader io.Reader) (contract.SQLResult, error) {
	if reader == nil {
		return contract.SQLResult{}, connections.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	if err != nil {
		return contract.SQLResult{}, connections.ErrInvalid
	}
	return l.Execute(ctx, namespace, string(raw))
}
func (l *managedQueryLease) ExecuteStream(ctx context.Context, namespace, source string, sink func(int, []contract.Column, []contract.Cell) error) (contract.SQLResult, error) {
	l.ops.Lock()
	defer l.ops.Unlock()
	if namespace != "" && namespace != l.selected {
		n := l.owner.Namespace(namespace)
		if err := l.owner.checkNamespace(n); err != nil {
			return contract.SQLResult{}, err
		}
		if _, err := l.original.Execute(ctx, "", "USE "+namespaceLabel(n.Catalog, n.Schema)); err != nil {
			return contract.SQLResult{}, err
		}
		l.selected = namespace
	}
	return l.original.ExecuteStream(ctx, "", source, sink)
}
