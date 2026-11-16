package agentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

// QueryConfig is supplied by a product adapter. InitializeSQL must establish
// that product's physical affinity; a logical agentSessionId alone does not.
// Split preserves the native dialect's statement boundaries before dispatch.
type QueryConfig struct {
	InitializeSQL, DefaultSchema       string
	MaxLeases, MaxRows, TimeoutSeconds int
	Split                              func(string) ([]string, error)
	Decoder                            ValueDecoder
	// ReturnAllResults is opt-in: common JDBC agents, including H2, reject
	// that optional dispatch method. Split still preserves every statement.
	ReturnAllResults bool
	// AcquireConnection admits a physical workload lease in the adapter's
	// shared pool. The returned release waits until the original session closes.
	// A waiting acquisition must honor ctx without dispatching native work.
	AcquireConnection func(context.Context) (func(), error)
}

// QueryPool binds SQL documents to one captured runtime. It owns its logical
// sessions, not the Client used by metadata and other original feature leases.
// Closing it revokes all document requests and waits for their executors.
type QueryPool struct {
	mu        sync.Mutex
	client    *Client
	params    json.RawMessage
	config    QueryConfig
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	leases    map[*queryLease]bool
	closeOnce sync.Once
}

func NewQueryPool(client *Client, openParams any, config QueryConfig) (*QueryPool, error) {
	if client == nil || !client.Alive() || strings.TrimSpace(config.InitializeSQL) == "" || config.Split == nil || config.MaxLeases < 1 || config.MaxLeases > 64 || config.MaxRows < 1 || config.MaxRows > 10000 || config.TimeoutSeconds < 0 || config.TimeoutSeconds > 86400 || config.DefaultSchema != "" && !sqltext.ValidName(config.DefaultSchema) {
		return nil, ErrRequest
	}
	raw, err := snapshotParams(openParams, "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &QueryPool{client: client, params: raw, config: config, ctx: ctx, cancel: cancel, leases: map[*queryLease]bool{}}, nil
}

func (p *QueryPool) ReserveQuery(document uint64) (contract.QueryLease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.client.Alive() {
		return nil, contract.ErrRevoked
	}
	if document == 0 {
		return nil, ErrRequest
	}
	if len(p.leases) >= p.config.MaxLeases {
		return nil, contract.ErrSQLSessionLimit
	}
	ctx, cancel := context.WithCancel(p.ctx)
	l := &queryLease{pool: p, document: document, ctx: ctx, cancel: cancel, releasedDone: make(chan struct{})}
	p.leases[l] = true
	return l, nil
}

func (p *QueryPool) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.cancel()
		var leases []*queryLease
		for l := range p.leases {
			leases = append(leases, l)
		}
		p.mu.Unlock()
		for _, l := range leases {
			l.Release()
		}
		p.mu.Lock()
		p.params = nil
		p.mu.Unlock()
	})
}

type queryLease struct {
	pool              *QueryPool
	document          uint64
	ops, mu           sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc
	session           *Session
	released, lost    bool
	releasedDone      chan struct{}
	receipts          []Receipt
	releaseConnection func()
}

func (l *queryLease) CheckQueryLease() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.ctx.Err() != nil {
		return contract.ErrRevoked
	}
	if l.lost || !l.pool.client.Alive() || l.session != nil && !l.session.Alive() {
		return contract.ErrSQLSessionLost
	}
	return nil
}

func (l *queryLease) request(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, ErrRequest
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := l.CheckQueryLease(); err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel() }, nil
}

func (l *queryLease) connect(ctx context.Context) error {
	l.mu.Lock()
	session := l.session
	l.mu.Unlock()
	if session != nil {
		return nil
	}
	var release func()
	if acquire := l.pool.config.AcquireConnection; acquire != nil {
		var err error
		release, err = acquire(ctx)
		if err != nil {
			return err
		}
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	l.pool.mu.Lock()
	params := append(json.RawMessage(nil), l.pool.params...)
	l.pool.mu.Unlock()
	session, receipt, err := l.pool.client.OpenSession(ctx, params)
	if err != nil {
		if receipt.Dispatched {
			l.mu.Lock()
			l.lost = true
			l.mu.Unlock()
			return errors.Join(contract.ErrSQLSessionLost, err)
		}
		return l.settleError(err)
	}
	// A concurrently closing document cancels initialization and then waits on
	// ops before detaching this captured original logical/physical session.
	l.mu.Lock()
	l.session = session
	l.mu.Unlock()
	r, err := session.Call(ctx, "execute_query", map[string]any{"sql": l.pool.config.InitializeSQL, "schema": l.pool.config.DefaultSchema, "maxRows": 1, "timeoutSecs": l.pool.config.TimeoutSeconds})
	if err == nil {
		_, err = DecodeQuery(r, l.pool.config.Decoder)
	}
	if err != nil {
		l.mu.Lock()
		l.lost = true
		l.mu.Unlock()
		_, _ = session.Close()
		return errors.Join(contract.ErrSQLSessionLost, err)
	}
	l.mu.Lock()
	l.releaseConnection = release
	l.mu.Unlock()
	release = nil
	return nil
}

func (l *queryLease) Preconnect(ctx context.Context) error {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	return l.connect(child)
}

// OriginalQueryReceipts returns independent complete receipts for the latest
// admitted execution. Cancellation acknowledgments never replace these.
func (l *queryLease) OriginalQueryReceipts() []Receipt {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneReceipts(l.receipts)
}

func cloneReceipts(receipts []Receipt) []Receipt {
	out := append([]Receipt(nil), receipts...)
	for i := range out {
		out[i].Result = append(json.RawMessage(nil), out[i].Result...)
		out[i].original = append(json.RawMessage(nil), out[i].original...)
		if out[i].RemoteError != nil {
			v := *out[i].RemoteError
			if v.VendorCode != nil {
				n := *v.VendorCode
				v.VendorCode = &n
			}
			out[i].RemoteError = &v
		}
	}
	return out
}

func (l *queryLease) Execute(ctx context.Context, schema, source string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, schema, source, nil)
}

func (l *queryLease) ExecuteStream(ctx context.Context, schema, source string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, err error) {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, err := l.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	if schema != "" && !sqltext.ValidName(schema) {
		return result, ErrRequest
	}
	parts, err := l.pool.config.Split(source)
	if err != nil {
		return result, err
	}
	if len(parts) == 0 || len(parts) > 1024 {
		return result, ErrRequest
	}
	if err = l.connect(child); err != nil {
		return result, err
	}
	l.mu.Lock()
	session := l.session
	l.receipts = nil
	l.mu.Unlock()
	var native []json.RawMessage
	defer func() {
		if len(native) > 0 {
			raw, _ := json.Marshal(native)
			result.NativeJSON = string(raw)
		}
		result.SetLegacyView()
	}()
	budget, remaining := 8<<20, l.pool.config.MaxRows
	for _, statement := range parts {
		if child.Err() != nil {
			return result, child.Err()
		}
		params := map[string]any{"sql": statement, "maxRows": max(1, remaining), "timeoutSecs": l.pool.config.TimeoutSeconds}
		if l.pool.config.ReturnAllResults {
			params["returnAllResults"] = true
		}
		if schema != "" {
			params["schema"] = schema
		}
		r, callErr := session.Call(child, "execute_query", params)
		l.mu.Lock()
		l.receipts = append(l.receipts, cloneReceipts([]Receipt{r})[0])
		l.mu.Unlock()
		if len(r.Result) > 0 {
			native = append(native, append(json.RawMessage(nil), r.Result...))
		}
		step := contract.StatementOutcome{SQL: statement, State: "uncertain"}
		if !r.Dispatched || r.RemoteError != nil && r.RemoteError.OperationOutcome == "not_started" {
			step.State = "not_started"
		}
		var snapshot QuerySnapshot
		var decodeErr error
		if r.ExecutorAcknowledged && r.RemoteError == nil {
			snapshot, decodeErr = DecodeQuery(r, l.pool.config.Decoder)
			if decodeErr == nil {
				step.State = "completed"
				step.AffectedRows = snapshot.SQL.AffectedRows
				if result.AffectedRows > math.MaxInt64-snapshot.SQL.AffectedRows || result.Duration > math.MaxInt64-snapshot.SQL.Duration {
					step.Error = ErrNativeResult.Error()
					result.Steps = append(result.Steps, step)
					return result, ErrNativeResult
				}
				result.AffectedRows += snapshot.SQL.AffectedRows
				result.Duration += snapshot.SQL.Duration
				result.Truncated = result.Truncated || snapshot.SQL.Truncated
				for _, set := range snapshot.SQL.Sets {
					if len(set.Columns) == 0 {
						continue
					}
					index := len(result.Sets)
					bounded := contract.ResultSet{Columns: append([]contract.Column(nil), set.Columns...), Truncation: append([]string(nil), set.Truncation...), AffectedRows: set.AffectedRows}
					for _, row := range set.Rows {
						if remaining == 0 || budget <= 0 {
							bounded.Truncation = append(bounded.Truncation, "agent_workbench_limit")
							result.Truncated = true
							break
						}
						cells := cloneQueryCells(row)
						for i := range cells {
							if len(cells[i].Bytes) > 64<<10 {
								cells[i].Bytes = cells[i].Bytes[:64<<10]
								cells[i].Truncated = true
							}
							if len(cells[i].Text) > 64<<10 {
								cells[i].Text = cells[i].Text[:64<<10]
								for !utf8.ValidString(cells[i].Text) {
									cells[i].Text = cells[i].Text[:len(cells[i].Text)-1]
								}
								cells[i].Truncated = true
							}
							budget -= len(cells[i].Text) + len(cells[i].Bytes) + 64
							if cells[i].Truncated {
								bounded.Truncation = append(bounded.Truncation, "agent_cell_limit")
								result.Truncated = true
							}
						}
						if budget < 0 {
							bounded.Truncation = append(bounded.Truncation, "agent_workbench_limit")
							result.Truncated = true
							break
						}
						if sink != nil {
							if err = sink(index, append([]contract.Column(nil), bounded.Columns...), cloneQueryCells(cells)); err != nil {
								break
							}
						}
						bounded.Rows = append(bounded.Rows, cells)
						remaining--
					}
					result.Sets = append(result.Sets, bounded)
					if err != nil {
						break
					}
				}
			}
		}
		if callErr != nil || decodeErr != nil || err != nil {
			err = errors.Join(callErr, decodeErr, err)
			step.Error = err.Error()
		}
		result.Steps = append(result.Steps, step)
		if err != nil {
			return result, l.settleError(err)
		}
	}
	return result, nil
}

func cloneQueryCells(row []contract.Cell) []contract.Cell {
	copy := make([]contract.Cell, len(row))
	for i, v := range row {
		copy[i] = v.Clone()
	}
	return copy
}

func (l *queryLease) settleError(err error) error {
	if !l.pool.client.Alive() || errors.Is(err, ErrLost) || errors.Is(err, ErrClosed) {
		l.mu.Lock()
		l.lost = true
		l.mu.Unlock()
		return errors.Join(contract.ErrSQLSessionLost, err)
	}
	return err
}

func (l *queryLease) Release() {
	l.mu.Lock()
	if l.released {
		done := l.releasedDone
		l.mu.Unlock()
		<-done
		return
	}
	l.released = true
	l.cancel()
	l.mu.Unlock()
	defer close(l.releasedDone)
	l.ops.Lock()
	l.mu.Lock()
	session := l.session
	l.session = nil
	release := l.releaseConnection
	l.releaseConnection = nil
	l.receipts = nil
	l.mu.Unlock()
	if session != nil {
		_, _ = session.Close()
	}
	if release != nil {
		release()
	}
	l.ops.Unlock()
	l.pool.mu.Lock()
	delete(l.pool.leases, l)
	l.pool.mu.Unlock()
}

func (l *queryLease) Replace() (contract.QueryLease, error) {
	l.Release()
	return l.pool.ReserveQuery(l.document)
}

var _ contract.QueryProvider = (*QueryPool)(nil)
var _ contract.QueryLease = (*queryLease)(nil)
var _ contract.QueryLeaseStatus = (*queryLease)(nil)
