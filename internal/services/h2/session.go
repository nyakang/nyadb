// Package h2 adapts captured managed H2 agent sessions to the shared workbench.
package h2

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

var ErrDriver = errors.New("H2 driver is unavailable; install and select an H2 driver package")

type Provider struct {
	Store   *driverstore.Store
	Started func(int) error
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	return (Provider{}).Open(ctx, p, password)
}

type Session struct {
	mu                      sync.Mutex
	profile                 connections.Profile
	runtime                 *driverstore.Runtime
	queries                 *agentbridge.QueryPool
	params                  json.RawMessage
	metadataParams          json.RawMessage
	catalog, version, scope string
	ctx                     context.Context
	cancel                  context.CancelFunc
	closed                  bool
	active                  sync.WaitGroup
	closeOnce               sync.Once
	closeErr                error
	transport               *tlsProxy
	slots                   chan struct{}
	metadataSlots           chan struct{}
}

func (p Provider) Open(ctx context.Context, profile connections.Profile, password string) (contract.Session, error) {
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	profile, err := connections.NormalizeProfile(profile)
	if err != nil || profile.Kind != connections.H2 || profile.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	a := profile.H2Options
	if a.DriverVersion == "" || a.DriverSHA256 == "" {
		return nil, ErrDriver
	}
	store := p.Store
	if store == nil {
		if a.DriverDirectory != "" {
			store, err = driverstore.New(a.DriverDirectory)
		} else {
			store, err = driverstore.Default()
		}
		if err != nil {
			return nil, ErrDriver
		}
	}
	openCtx, cancel := context.WithTimeout(ctx, time.Duration(a.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	lease, err := store.Acquire(openCtx, "h2", a.DriverVersion, a.DriverSHA256)
	if err != nil {
		return nil, safeError(errors.Join(ErrDriver, err))
	}
	m := lease.Manifest()
	if m.Runtime != "java" || m.MainClass != "com.dbx.agent.h2.H2Agent" {
		_ = lease.Close()
		return nil, ErrDriver
	}
	if a.DriverProfile == "h2-auto" {
		for _, required := range []string{"h2-v1", "h2-v2", "h2-v3"} {
			found := false
			for _, declared := range m.Profiles {
				found = found || declared == required
			}
			if !found {
				_ = lease.Close()
				return nil, ErrDriver
			}
		}
	}
	var customClass string
	var customPaths []string
	if a.DriverProfile == "h2-custom" {
		customClass, customPaths, err = lease.JDBC()
		if err != nil {
			_ = lease.Close()
			return nil, ErrDriver
		}
	}
	var transport *tlsProxy
	connector := profile
	if a.Mode == "ssl" {
		transport, err = startTLSProxy(openCtx, profile)
		if err != nil {
			_ = lease.Close()
			return nil, err
		}
		defer func() {
			if transport != nil {
				transport.Close()
			}
		}()
		connector.Host, connector.Port = transport.endpoint()
		connector.H2Options.Mode, connector.H2Options.TLSCAFile = "tcp", ""
	}
	driverProfile := a.DriverProfile
	if driverProfile == "h2-legacy" {
		driverProfile = "h2-v2"
	}
	r, err := lease.StartJDBCObserved(openCtx, driverProfile, driverstore.JDBCSettings{MaxConnections: a.MaxConnections, MetadataReserve: a.MetadataReserve, WaitTimeout: time.Duration(a.PoolWaitTimeoutMilliseconds) * time.Millisecond}, agentbridge.Options{CancelWait: 15 * time.Second, MaxInFlight: 32}, p.Started)
	if err != nil {
		_ = lease.Close()
		return nil, safeError(err)
	}
	params := map[string]any{"database": profile.Database, "connection_string": connectionURL(connector), "username": profile.Username, "password": password, "driver_profile": a.DriverProfile, "sessionRole": "workload"}
	if a.DriverProfile == "h2-custom" {
		params["jdbc_driver_class"], params["jdbc_driver_paths"] = customClass, customPaths
	}
	workloadParams, err := json.Marshal(params)
	if err != nil {
		r.Close()
		return nil, connections.ErrInvalid
	}
	params["sessionRole"] = "metadata"
	metadataParams, err := json.Marshal(params)
	if err != nil {
		r.Close()
		return nil, connections.ErrInvalid
	}
	root, stop := context.WithCancel(context.Background())
	s := &Session{profile: profile, runtime: r, transport: transport, params: workloadParams, metadataParams: metadataParams, ctx: root, cancel: stop, slots: make(chan struct{}, a.MaxConnections-a.MetadataReserve), metadataSlots: make(chan struct{}, max(1, a.MetadataReserve)), scope: rand.Text()}
	err = s.withMetadata(openCtx, func(child context.Context, session *agentbridge.Session) error {
		q, err := s.query(child, session, "SELECT H2VERSION(), DATABASE()", 1)
		if err != nil {
			return err
		}
		if len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 2 {
			return contract.ErrUnsupported
		}
		s.version, s.catalog = q.Sets[0].Rows[0][0].Text, q.Sets[0].Rows[0][1].Text
		if s.catalog == "" || !sqltext.ValidName(s.catalog) || !admittedVersion(a.DriverProfile, s.version) {
			return contract.ErrUnsupported
		}
		if a.DriverProfile == "h2-auto" {
			// The server version and selected JDBC driver can differ on TCP.
			// Bind later physical requests to the original JDBC metadata, not
			// to a new file-header detection or a guessed server version.
			info, err := session.Call(child, "connection_info", map[string]any{})
			if err != nil {
				return err
			}
			var native struct {
				DatabaseInfo map[string]string `json:"databaseInfo"`
			}
			if json.Unmarshal(info.Result, &native) != nil {
				return contract.ErrUnsupported
			}
			selected := fixedH2Profile(native.DatabaseInfo["driverVersion"])
			if selected == "" {
				return contract.ErrUnsupported
			}
			params["driver_profile"], params["sessionRole"] = selected, "workload"
			s.params, err = json.Marshal(params)
			if err != nil {
				return err
			}
			params["sessionRole"] = "metadata"
			s.metadataParams, err = json.Marshal(params)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.queries, err = agentbridge.NewQueryPool(r.Client, s.params, agentbridge.QueryConfig{InitializeSQL: "SET SCHEMA " + quote(a.DefaultSchema), DefaultSchema: a.DefaultSchema, MaxLeases: a.SQLMaxConnections, MaxRows: 10000, TimeoutSeconds: a.QueryTimeoutSeconds, Split: sqltext.SplitPortable, Decoder: decodeValue, AcquireConnection: s.acquireWorkload})
	}
	if err != nil || openCtx.Err() != nil {
		_ = s.Close()
		if err == nil {
			err = openCtx.Err()
		}
		return nil, safeError(err)
	}
	transport = nil // Ownership moves to the original session only after initialization.
	return s, nil
}

func admittedVersion(profile, version string) bool {
	if profile == "h2-auto" || profile == "h2-custom" {
		return fixedH2Profile(version) != ""
	}
	if profile == "h2-v1" {
		return strings.HasPrefix(version, "1.4.")
	}
	if profile == "h2-v2" || profile == "h2-legacy" {
		return strings.HasPrefix(version, "2.0.") || strings.HasPrefix(version, "2.1.")
	}
	return strings.HasPrefix(version, "2.2.") || strings.HasPrefix(version, "2.3.") || strings.HasPrefix(version, "2.4.")
}

func fixedH2Profile(version string) string {
	if strings.HasPrefix(version, "1.4.") {
		return "h2-v1"
	}
	if strings.HasPrefix(version, "2.0.") || strings.HasPrefix(version, "2.1.") {
		return "h2-v2"
	}
	if strings.HasPrefix(version, "2.2.") || strings.HasPrefix(version, "2.3.") || strings.HasPrefix(version, "2.4.") {
		return "h2-v3"
	}
	return ""
}

func connectionURL(p connections.Profile) string {
	a := p.H2Options
	var url string
	switch a.Mode {
	case "file":
		url = "jdbc:h2:file:" + strings.ReplaceAll(p.Database, "\\", "/")
		if a.AutoServer {
			url += ";AUTO_SERVER=TRUE"
		}
	case "memory":
		url = "jdbc:h2:mem:" + p.Database + ";DB_CLOSE_DELAY=-1"
	default:
		host := p.Host
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		url = fmt.Sprintf("jdbc:h2:%s://%s:%d/%s", a.Mode, host, p.Port, p.Database)
	}
	if a.RequireExisting {
		url += ";IFEXISTS=TRUE"
	}
	if a.ReadOnly {
		url += ";ACCESS_MODE_DATA=r"
	}
	return url
}

func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Catalog() string    { return s.catalog }

func (s *Session) Namespace(name string) contract.NamespaceRef {
	if !s.Alive() {
		return contract.NamespaceRef{ProfileID: s.ProfileID()}
	}
	if name == "" {
		name = s.profile.H2Options.DefaultSchema
	}
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: s.catalog, Schema: name}
}
func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.runtime.Client.Alive()
}
func (s *Session) Capabilities() contract.Capabilities {
	return contract.Capabilities{Query: true, Metadata: true, Schemas: true, Transactions: true, EditableTables: !s.profile.H2Options.ReadOnly, Count: true, FieldChunks: true, Export: true, JSONL: true}
}
func (s *Session) ReserveQuery(doc uint64) (contract.QueryLease, error) {
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	return s.queries.ReserveQuery(doc)
}
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		if s.queries != nil {
			s.queries.Close()
		}
		s.closeErr = s.runtime.Close()
		s.transport.Close()
		s.active.Wait()
		s.mu.Lock()
		s.params = nil
		s.metadataParams = nil
		s.mu.Unlock()
	})
	return s.closeErr
}

func (s *Session) withFeature(ctx context.Context, fn func(context.Context, *agentbridge.Session) error) error {
	return s.withRole(ctx, false, fn)
}

func (s *Session) withMetadata(ctx context.Context, fn func(context.Context, *agentbridge.Session) error) error {
	return s.withRole(ctx, true, fn)
}

// SQL affinity leases and temporary workload features share physical capacity.
// Admission waits locally, so cancellation cannot retire a busy native runtime
// merely because all workload connections belong to other original documents.
func (s *Session) acquireWorkload(ctx context.Context) (func(), error) {
	wait, cancel := context.WithTimeout(ctx, time.Duration(s.profile.H2Options.PoolWaitTimeoutMilliseconds)*time.Millisecond)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
	case <-wait.Done():
		return nil, wait.Err()
	case <-s.ctx.Done():
		return nil, contract.ErrRevoked
	}
	release := func() { <-s.slots }
	if err := wait.Err(); err != nil {
		release()
		return nil, err
	}
	if !s.Alive() {
		release()
		return nil, contract.ErrRevoked
	}
	return release, nil
}

func (s *Session) withRole(ctx context.Context, metadata bool, fn func(context.Context, *agentbridge.Session) error) error {
	if ctx == nil {
		return connections.ErrInvalid
	}
	s.mu.Lock()
	if s.closed || !s.runtime.Client.Alive() {
		s.mu.Unlock()
		return contract.ErrRevoked
	}
	s.active.Add(1)
	params := append(json.RawMessage(nil), s.params...)
	if metadata {
		params = append(json.RawMessage(nil), s.metadataParams...)
	}
	s.mu.Unlock()
	defer s.active.Done()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	var release func()
	if metadata {
		select {
		case s.metadataSlots <- struct{}{}:
		case <-child.Done():
			return child.Err()
		}
		release = func() { <-s.metadataSlots }
	} else {
		var err error
		release, err = s.acquireWorkload(child)
		if err != nil {
			return err
		}
	}
	defer release()
	if err := child.Err(); err != nil {
		return err
	}
	session, _, err := s.runtime.Client.OpenSession(child, params)
	if err != nil {
		return safeError(err)
	}
	defer session.Close()
	if _, err = s.query(child, session, "SET SCHEMA "+quote(s.profile.H2Options.DefaultSchema), 1); err != nil {
		return err
	}
	return safeError(fn(child, session))
}

func (s *Session) query(ctx context.Context, session *agentbridge.Session, sql string, maxRows int) (contract.SQLResult, error) {
	r, err := session.Call(ctx, "execute_query", map[string]any{"sql": sql, "schema": s.profile.H2Options.DefaultSchema, "maxRows": maxRows, "timeoutSecs": s.profile.H2Options.QueryTimeoutSeconds})
	if err != nil {
		return contract.SQLResult{NativeJSON: string(r.Result)}, safeError(err)
	}
	q, err := agentbridge.DecodeQuery(r, decodeValue)
	return q.SQL, safeError(err)
}

func quote(name string) string              { return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\"" }
func qualified(o contract.ObjectRef) string { return quote(o.Schema) + "." + quote(o.Name) }

func safeError(err error) error {
	if err == nil {
		return nil
	}
	var knownErrors []error
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, connections.ErrInvalid, connections.ErrNotFound, ErrDriver, contract.ErrRevoked, contract.ErrUnsupported, contract.ErrSQLSessionLost, contract.ErrDataConflict, contract.ErrDataCommitUncertain, contract.ErrDataEditLimit, contract.ErrDataReadOnly, contract.ErrDataPage, agentbridge.ErrArtifact, agentbridge.ErrLaunch, agentbridge.ErrRequest, agentbridge.ErrLost, agentbridge.ErrClosed, agentbridge.ErrLimit, agentbridge.ErrProtocol, agentbridge.ErrVersion} {
		if errors.Is(err, known) {
			knownErrors = append(knownErrors, known)
		}
	}
	var remote *agentbridge.RemoteError
	if errors.As(err, &remote) {
		knownErrors = append(knownErrors, remote)
	}
	if len(knownErrors) > 0 {
		return errors.Join(knownErrors...)
	}
	if errors.Is(err, agentbridge.ErrNativeResult) {
		return agentbridge.ErrNativeResult
	}
	return errors.New("H2 operation failed; check the original connection, SQL and object permissions")
}

func decodeValue(typ string, raw json.RawMessage) (contract.Cell, error) {
	if strings.EqualFold(typ, "VARCHAR_IGNORECASE") {
		typ = "VARCHAR"
	}
	return agentbridge.DecodeStandardValue(typ, raw)
}
