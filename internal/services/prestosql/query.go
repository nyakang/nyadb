package prestosql

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

type queryLease struct {
	session              *Session
	document             uint64
	role                 string
	mu                   sync.Mutex
	ops                  sync.Mutex
	ctx                  context.Context
	cancel               context.CancelFunc
	released, lost, slot bool
	original             *process
}

func (q *queryLease) CheckQueryLease() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.released {
		return contract.ErrRevoked
	}
	if q.lost || q.original != nil && !q.original.runtime.Client.Alive() {
		return contract.ErrSQLSessionLost
	}
	if !q.session.Alive() {
		return contract.ErrRevoked
	}
	return nil
}

func (q *queryLease) request(ctx context.Context) (context.Context, func(), error) {
	if err := q.CheckQueryLease(); err != nil {
		return nil, nil, err
	}
	child, done, err := q.session.request(ctx)
	if err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithCancel(child)
	stop := context.AfterFunc(q.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}

// connect runs under ops. Failed original openings require Replace; no call
// silently creates a new JVM after an original execution/connection attempt.
func (q *queryLease) connect(ctx context.Context) (*process, error) {
	q.mu.Lock()
	original := q.original
	q.mu.Unlock()
	if original != nil {
		return original, nil
	}
	s := q.session
	wait, cancel := context.WithTimeout(ctx, time.Duration(s.config.PoolWaitMillis)*time.Millisecond)
	defer cancel()
	select {
	case <-wait.Done():
		return nil, wait.Err()
	case s.slots <- struct{}{}:
	}
	keep := false
	defer func() {
		if !keep {
			<-s.slots
		}
	}()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s.mu.Lock()
	connection := s.connection
	s.mu.Unlock()
	original, err := s.provider.role(q.role).start(ctx, s.config, connection)
	q.mu.Lock()
	if err != nil || q.released || ctx.Err() != nil || !s.Alive() {
		q.lost = true
		q.mu.Unlock()
		if original != nil {
			_ = original.runtime.Close()
		}
		if err == nil {
			err = contract.ErrRevoked
		}
		return nil, errors.Join(err, ctx.Err())
	}
	q.original, q.slot = original, true
	q.mu.Unlock()
	keep = true
	return original, nil
}

func (q *queryLease) Preconnect(ctx context.Context) error {
	q.ops.Lock()
	defer q.ops.Unlock()
	child, done, err := q.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	_, err = q.connect(child)
	return err
}

func (q *queryLease) Release() {
	q.mu.Lock()
	q.released = true
	q.cancel()
	q.mu.Unlock()
	q.ops.Lock()
	q.mu.Lock()
	original, slot := q.original, q.slot
	q.original, q.slot = nil, false
	q.mu.Unlock()
	if original != nil {
		_ = original.runtime.Close()
	}
	if slot {
		<-q.session.slots
	}
	q.ops.Unlock()
	q.session.mu.Lock()
	if q.session.queries[q.document] == q {
		delete(q.session.queries, q.document)
	}
	q.session.mu.Unlock()
}

func (q *queryLease) Replace() (contract.QueryLease, error) {
	q.Release()
	return q.session.ReserveQuery(q.document)
}

func (q *queryLease) Execute(ctx context.Context, namespace, source string) (contract.SQLResult, error) {
	return q.ExecuteStream(ctx, namespace, source, nil)
}

func (q *queryLease) ExecuteFile(ctx context.Context, namespace string, reader io.Reader) (contract.SQLResult, error) {
	if reader == nil {
		return contract.SQLResult{}, connections.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	if err != nil {
		return contract.SQLResult{}, errors.New("PrestoSQL SQL file cannot be read")
	}
	return q.Execute(ctx, namespace, string(raw))
}

func queryCell(raw json.RawMessage) (contract.Cell, error) {
	if string(raw) == "null" {
		return contract.Cell{Kind: contract.Null}, nil
	}
	if len(raw) == 0 {
		return contract.Cell{}, ErrResponse
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return contract.Cell{}, ErrResponse
		}
		return contract.Cell{Kind: contract.Text, Text: text}, nil
	}
	if string(raw) == "true" || string(raw) == "false" {
		return contract.Cell{Kind: contract.Boolean, Text: string(raw)}, nil
	}
	if raw[0] == '[' || raw[0] == '{' {
		return contract.Cell{Kind: contract.JSON, Text: string(raw)}, nil
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return contract.Cell{}, ErrResponse
	}
	kind := contract.Integer
	if strings.ContainsAny(number.String(), ".eE") {
		kind = contract.Decimal
	}
	return contract.Cell{Kind: kind, Text: number.String()}, nil
}

func (q *queryLease) ExecuteStream(ctx context.Context, namespace, source string, sink func(int, []contract.Column, []contract.Cell) error) (out contract.SQLResult, err error) {
	// The common lexer has the same quote/comment rules as PrestoSQL350.
	// Its newer Trino routine recognition does not authorize native syntax;
	// the original driver/server validates each complete statement.
	parts, err := sqltext.SplitTrino(source)
	if err != nil {
		return out, errors.New("invalid PrestoSQL SQL source")
	}
	q.ops.Lock()
	defer q.ops.Unlock()
	child, done, err := q.request(ctx)
	if err != nil {
		return out, err
	}
	defer done()
	n := q.session.Namespace(namespace)
	if namespace != "" && q.session.checkNamespace(n) != nil {
		return out, connections.ErrInvalid
	}
	original, err := q.connect(child)
	if err != nil {
		return out, err
	}
	started := time.Now()
	var receipts []json.RawMessage
	defer func() {
		out.Duration = time.Since(started)
		out.SetLegacyView()
		if len(receipts) > 0 {
			raw, _ := json.Marshal(receipts)
			out.NativeJSON = string(raw)
		}
	}()
	budget := 16 << 20
	nativeBytes := 0
	for _, statement := range parts {
		if child.Err() != nil {
			return out, child.Err()
		}
		// Bound the batch while retaining the complete final executed receipt.
		// A single frame is already limited by the original process transport.
		if nativeBytes >= 32<<20 {
			return out, jdbcplugin.ErrLimit
		}
		result, receipt, executeErr := original.query(child, jdbcplugin.Query{SQL: statement, Catalog: n.Catalog, Schema: n.Schema, MaxRows: 10000, TimeoutSeconds: q.session.config.QueryTimeoutSeconds})
		step := contract.StatementOutcome{SQL: statement, State: "completed"}
		if errors.Is(executeErr, contract.ErrSQLSessionLost) || !original.runtime.Client.Alive() {
			q.mu.Lock()
			q.lost = true
			q.mu.Unlock()
		}
		if executeErr != nil && !result.completed {
			step.State, step.Error = "failed", executeErr.Error()
			if receipt.Dispatched && (!receipt.ExecutorAcknowledged || errors.Is(executeErr, contract.ErrSQLSessionLost)) {
				step.State = "unknown"
			}
			out.Steps = append(out.Steps, step)
			return out, executeErr
		}
		// Only successful original query data is exposed in the public result.
		// Complete error receipts remain in the injected private observer.
		rawReceipt := receipt.OriginalResponse()
		nativeBytes += len(rawReceipt)
		receipts = append(receipts, rawReceipt)
		step.AffectedRows = result.affectedRows
		out.AffectedRows += result.affectedRows
		out.Steps = append(out.Steps, step)
		set := contract.ResultSet{AffectedRows: result.affectedRows}
		for _, name := range result.columns {
			set.Columns = append(set.Columns, contract.Column{Name: name})
		}
		for _, raw := range result.rows {
			row := make([]contract.Cell, len(raw))
			for i, value := range raw {
				row[i], err = queryCell(value)
				if err != nil {
					_ = original.runtime.Close()
					q.mu.Lock()
					q.lost = true
					q.mu.Unlock()
					return out, errors.Join(contract.ErrSQLSessionLost, err)
				}
				budget -= len(value) + 64
			}
			if sink != nil {
				copyRow := append([]contract.Cell(nil), row...)
				if err := sink(len(out.Sets), append([]contract.Column(nil), set.Columns...), copyRow); err != nil {
					out.Sets = append(out.Sets, set)
					return out, err
				}
			}
			if budget >= 0 {
				set.Rows = append(set.Rows, row)
			}
		}
		if budget < 0 {
			set.Truncation = append(set.Truncation, "preview_bytes")
		}
		if result.truncated {
			set.Truncation = append(set.Truncation, "native_rows")
		}
		out.Sets = append(out.Sets, set)
		if executeErr != nil {
			return out, executeErr
		}
	}
	return out, nil
}

var _ contract.QueryLease = (*queryLease)(nil)
var _ contract.QueryFileLease = (*queryLease)(nil)
var _ contract.QueryLeaseStatus = (*queryLease)(nil)
