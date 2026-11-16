package cassandra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

type Options struct {
	Driver  connections.ManagedDriver
	Started func(int) error
}

// Process is one physical document's original process and native session. It
// never reopens after cancellation/loss. Recovery constructs a new Process.
// Distinct documents must call OpenManaged separately, since native logical
// sessions with matching settings otherwise share Cassandra transports.
type Process struct {
	client       *agentbridge.Client
	closeRuntime func() error
	session      *agentbridge.Session
	config       AgentConfig
	opening      []agentbridge.Receipt
	closed       atomic.Bool
}

func OpenManaged(ctx context.Context, c AgentConfig, secret string, o Options) (*Process, error) {
	if ctx == nil || len(secret) > 65536 || !utf8.ValidString(secret) || strings.ContainsRune(secret, 0) || o.Driver.Key != "cassandra" || o.Driver.JDBCClass != "" || !o.Driver.Supports("cassandra-v2") {
		return nil, ErrConfig
	}
	c, err := connections.NormalizeCassandraAgentConfig(c)
	if err != nil {
		return nil, err
	}
	if (c.Username == "") != (secret == "") {
		return nil, ErrConfig
	}
	store, err := driverstore.New(o.Driver.Directory)
	if err != nil {
		return nil, err
	}
	lease, err := store.Acquire(ctx, "cassandra", o.Driver.Version, o.Driver.SHA256)
	if err != nil {
		return nil, err
	}
	if lease.Manifest().Runtime != "native" {
		lease.Close()
		return nil, ErrConfig
	}
	runtime, err := lease.StartObserved(ctx, "cassandra-v2", agentbridge.Options{MaxFrameBytes: 32 << 20, CancelWait: 5 * time.Second, RetainOriginalResponses: true, ErrorCodes: agentbridge.CQLCodes}, o.Started)
	if err != nil {
		return nil, err
	}
	return openOriginalProcess(ctx, c, secret, runtime.Client, runtime.Close)
}

// openOriginalProcess also serves local source diagnostics. Only OpenManaged
// supplies the installed package lease; diagnostics cannot publish a provider.
func openOriginalProcess(ctx context.Context, c AgentConfig, secret string, client *agentbridge.Client, closeRuntime func() error) (*Process, error) {
	p := &Process{client: client, closeRuntime: closeRuntime, config: c}
	fail := func(err error) (*Process, error) { return nil, errors.Join(err, p.Close()) }
	h := client.Handshake()
	for _, capability := range []string{"metadata", "query", "paged_query", "structured_error_v1"} {
		if !h.Supports(capability) {
			return fail(agentbridge.ErrVersion)
		}
	}
	p.opening = append(p.opening, client.HandshakeReceipt())
	s, r, err := client.OpenSession(ctx, connectionParams(c, secret))
	p.opening = append(p.opening, r)
	if err != nil {
		return fail(fmt.Errorf("original Cassandra session open: %w", err))
	}
	p.session = s
	r, err = s.Call(ctx, "connection_info", nil)
	p.opening = append(p.opening, r)
	if err != nil {
		return fail(fmt.Errorf("original Cassandra connection metadata: %w", err))
	}
	var info struct {
		Version      string                                       `json:"version"`
		Mode         string                                       `json:"compatibilityMode"`
		DatabaseInfo struct{ ProductName, ProductVersion string } `json:"databaseInfo"`
	}
	if json.Unmarshal(r.Result, &info) != nil || info.Mode != "cql" || info.DatabaseInfo.ProductName != "Apache Cassandra" || info.Version == "" || info.Version != info.DatabaseInfo.ProductVersion {
		return fail(agentbridge.ErrProtocol)
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	return p, nil
}

func (p *Process) PID() int                       { return p.client.PID() }
func (p *Process) Alive() bool                    { return !p.closed.Load() && p.session != nil && p.session.Alive() }
func (p *Process) Exited() <-chan struct{}        { return p.client.Exited() }
func (p *Process) Configuration() string          { return configurationFingerprint(p.config) }
func (p *Process) ReadyResponse() json.RawMessage { return p.client.ReadyResponse() }
func (p *Process) OpeningReceipts() []agentbridge.Receipt {
	result := make([]agentbridge.Receipt, len(p.opening))
	for i, r := range p.opening {
		result[i] = r.Clone()
	}
	return result
}
func (p *Process) Close() error { p.closed.Store(true); return p.closeRuntime() }

type QueryResult struct {
	agentbridge.QuerySnapshot
	Receipt agentbridge.Receipt
}

func (p *Process) Query(ctx context.Context, keyspace, sql string, maxRows, timeoutSeconds int) (QueryResult, error) {
	var result QueryResult
	if !validName(keyspace, true) || len(sql) > 1<<20 || !utf8.ValidString(sql) || strings.ContainsRune(sql, 0) || strings.TrimSpace(sql) == "" || maxRows < 1 || maxRows > 10000 || timeoutSeconds < 0 || timeoutSeconds > 300 {
		return result, ErrConfig
	}
	if !p.Alive() {
		return result, agentbridge.ErrClosed
	}
	r, err := p.session.Call(ctx, "execute_query", map[string]any{"sql": sql, "schema": keyspace, "maxRows": maxRows, "timeoutSecs": timeoutSeconds})
	result.Receipt = r
	result.Native = append(json.RawMessage(nil), r.Result...)
	// A completed original result can win a cancellation race. Keep both the
	// cancellation fact and the real complete executor result for the host.
	if r.ExecutorAcknowledged && r.RemoteError == nil {
		var projection error
		result.QuerySnapshot, projection = agentbridge.DecodeQuery(r, DecodeValue)
		err = errors.Join(err, projection)
	}
	return result, err
}

func (p *Process) OpenCursor(ctx context.Context, options agentbridge.QueryCursorOptions) (*agentbridge.QueryCursor, agentbridge.QueryPageSnapshot, error) {
	if !p.Alive() {
		return nil, agentbridge.QueryPageSnapshot{}, agentbridge.ErrClosed
	}
	if len(options.SQL) > 1<<20 || !utf8.ValidString(options.SQL) || strings.ContainsRune(options.SQL, 0) || options.TimeoutSeconds > 300 {
		return nil, agentbridge.QueryPageSnapshot{}, ErrConfig
	}
	options.Decoder = DecodeValue
	return p.session.OpenQueryCursor(ctx, options)
}

// Metadata dispatch is limited to read-only native methods. Logged batches
// and arbitrary native control methods cannot be reached through this API.
func (p *Process) Metadata(ctx context.Context, method, keyspace, object string) (agentbridge.Receipt, error) {
	if !p.Alive() {
		return agentbridge.Receipt{}, agentbridge.ErrClosed
	}
	if !validName(keyspace, true) || !validName(object, true) {
		return agentbridge.Receipt{}, ErrConfig
	}
	switch method {
	case "connection_info", "list_databases", "list_schemas", "list_data_types":
		if keyspace != "" || object != "" {
			return agentbridge.Receipt{}, ErrConfig
		}
	case "list_tables", "list_objects":
		if keyspace == "" || object != "" {
			return agentbridge.Receipt{}, ErrConfig
		}
	case "get_columns", "get_table_ddl", "list_indexes", "list_triggers":
		if keyspace == "" || object == "" {
			return agentbridge.Receipt{}, ErrConfig
		}
	default:
		return agentbridge.Receipt{}, ErrConfig
	}
	return p.session.Call(ctx, method, map[string]string{"schema": keyspace, "table": object})
}
