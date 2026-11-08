package cassandra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

type queryLease struct {
	s              *Session
	document       uint64
	ops            sync.Mutex
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	client         *Client
	released, lost bool
	selected       string
}

func (s *Session) ReserveQuery(document uint64) (contract.QueryLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return nil, contract.ErrRevoked
	}
	if document == 0 {
		return nil, ErrInvalid
	}
	if len(s.leases) >= 32 {
		return nil, contract.ErrSQLSessionLimit
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &queryLease{s: s, document: document, ctx: ctx, cancel: cancel, selected: s.profile.Database}
	s.leases[l] = true
	return l, nil
}
func (l *queryLease) Release() {
	l.mu.Lock()
	l.released = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock()
	if l.client != nil {
		l.client.Close()
		l.client = nil
	}
	l.ops.Unlock()
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
}
func (l *queryLease) Replace() (contract.QueryLease, error) {
	l.Release()
	return l.s.ReserveQuery(l.document)
}
func (l *queryLease) request(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil, nil, contract.ErrRevoked
	}
	if l.lost {
		return nil, nil, contract.ErrSQLSessionLost
	}
	child, done, err := l.s.request(ctx)
	if err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithCancel(child)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}
func operationError(err error) error {
	if errors.Is(err, ErrRetired) || errors.Is(err, contract.ErrRevoked) {
		return errors.Join(contract.ErrSQLSessionLost, err)
	}
	return err
}
func (l *queryLease) markLost(err error) error {
	err = operationError(err)
	if errors.Is(err, contract.ErrSQLSessionLost) {
		l.mu.Lock()
		l.lost = true
		l.mu.Unlock()
	}
	return err
}
func (l *queryLease) connect(ctx context.Context) error {
	if l.client != nil {
		return nil
	}
	c, err := l.s.open(ctx)
	if err != nil {
		return l.markLost(err)
	}
	l.client = c
	return nil
}
func (l *queryLease) Preconnect(ctx context.Context) error {
	l.ops.Lock()
	defer l.ops.Unlock()
	ctx, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err = l.connect(ctx); err != nil {
		return err
	}
	_, err = l.s.validateNode(ctx, l.client)
	return l.markLost(err)
}
func (l *queryLease) Execute(ctx context.Context, namespace, source string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, namespace, source, nil)
}
func (l *queryLease) ExecuteFile(ctx context.Context, namespace string, reader io.Reader) (contract.SQLResult, error) {
	if reader == nil {
		return contract.SQLResult{}, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(reader, MaxStatement+1))
	if err != nil {
		return contract.SQLResult{}, errors.New("Cassandra CQL file cannot be read")
	}
	return l.Execute(ctx, namespace, string(raw))
}
func (l *queryLease) ExecuteStream(ctx context.Context, namespace, source string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, err error) {
	parts, err := sqltext.SplitCassandra(source)
	if err != nil {
		return result, err
	}
	if namespace != "" {
		if err = l.s.checkNamespace(l.s.Namespace(namespace)); err != nil {
			return result, err
		}
	}
	l.ops.Lock()
	defer l.ops.Unlock()
	ctx, done, err := l.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	if err = l.connect(ctx); err != nil {
		return result, err
	}
	if _, err = l.s.validateNode(ctx, l.client); err != nil {
		return result, l.markLost(err)
	}
	if namespace != "" && namespace != l.selected {
		r, e := l.client.execute(ctx, Request{Statement: "USE " + quoteName(namespace)}, true)
		if e != nil {
			return result, l.markLost(e)
		}
		if r.Kind != 3 || r.Keyspace != namespace {
			l.client.retire()
			return result, l.markLost(errors.Join(ErrRetired, ErrProtocol))
		}
		l.selected = namespace
	}
	start := time.Now()
	defer func() { result.Duration = time.Since(start); result.SetLegacyView() }()
	result.AffectedRows = -1
	budget := 8 << 20
	for _, source := range parts {
		step := contract.StatementOutcome{SQL: source, State: "completed", AffectedRows: -1}
		set := contract.ResultSet{AffectedRows: -1}
		request := Request{Statement: source, PageSize: 500}
		reasons := map[string]bool{}
		pages := 0
		acknowledged := false
		for {
			pages++
			if pages > 100000 {
				err = ErrBounds
				break
			}
			var r *Result
			r, err = l.client.Execute(ctx, request)
			if err != nil {
				break
			}
			acknowledged = true
			if r.Kind == 3 {
				l.selected = r.Keyspace
			}
			if r.Kind != 2 {
				switch r.Kind {
				case 1:
					set.Columns = []contract.Column{{Name: "acknowledgement", DatabaseType: "CQL VOID"}}
					set.Rows = [][]contract.Cell{{{Kind: contract.Text, Text: "VOID"}}}
				case 3:
					set.Columns = []contract.Column{{Name: "Keyspace", DatabaseType: "CQL Keyspace"}}
					set.Rows = [][]contract.Cell{{{Kind: contract.Text, Text: r.Keyspace}}}
				case 5:
					set.Columns = []contract.Column{{Name: "change"}, {Name: "target"}, {Name: "Keyspace"}, {Name: "name"}, {Name: "native_schema_change", DatabaseType: "CQL schema change"}}
					s := r.SchemaChange
					raw, _ := json.Marshal(s)
					set.Rows = [][]contract.Cell{{{Kind: contract.Text, Text: s.Change}, {Kind: contract.Text, Text: s.Target}, {Kind: contract.Text, Text: s.Keyspace}, {Kind: contract.Text, Text: s.Name}, {Kind: contract.JSON, Text: string(raw)}}}
				}
				budget -= previewColumnsSize(set.Columns)
				for _, row := range set.Rows {
					budget -= len(row) * 64
					for _, cell := range row {
						budget -= len(cell.Text) + len(cell.Bytes)
					}
				}
				if budget < 0 {
					err = ErrBounds
					break
				}
				if sink != nil {
					for _, row := range set.Rows {
						err = sink(len(result.Sets), append([]contract.Column(nil), set.Columns...), cloneCells(row))
						if err != nil {
							break
						}
					}
				}
				break
			}
			columns := nativeColumns(r.Columns)
			if pages > 1 && !sameColumns(set.Columns, columns) {
				err = ErrMetadataChanged
				break
			}
			set.Columns = columns
			if pages == 1 {
				budget -= previewColumnsSize(columns)
				if budget < 0 {
					err = ErrBounds
					break
				}
			}
			for _, row := range r.Rows {
				cells := make([]contract.Cell, len(row))
				for i, v := range row {
					cells[i], err = nativeCell(v)
					if err != nil {
						break
					}
				}
				if err != nil {
					break
				}
				if sink != nil {
					err = sink(len(result.Sets), append([]contract.Column(nil), columns...), cloneCells(cells))
					if err != nil {
						break
					}
				}
				size := len(cells) * 64
				for i, c := range cells {
					cells[i] = previewCell(c)
					size += len(cells[i].Text) + len(cells[i].Bytes)
					if cells[i].Truncated {
						reasons["cell"] = true
					}
				}
				if len(set.Rows) >= 5000 {
					reasons["rows"] = true
				} else if size > budget {
					reasons["bytes"] = true
				} else {
					budget -= size
					set.Rows = append(set.Rows, cells)
				}
			}
			if err != nil || r.Next == nil {
				break
			}
			if sink == nil && (reasons["rows"] || reasons["bytes"]) {
				break
			}
			if request.Cursor != nil && string(request.Cursor.state) == string(r.Next.state) {
				l.client.retire()
				err = errors.Join(ErrRetired, ErrProtocol)
				break
			}
			request.Cursor = r.Next.Clone()
		}
		for _, reason := range []string{"rows", "bytes", "cell"} {
			if reasons[reason] {
				set.Truncation = append(set.Truncation, reason)
			}
		}
		if len(set.Columns) > 0 {
			result.Sets = append(result.Sets, set)
		}
		if err != nil {
			err = l.markLost(err)
			step.State = "failed"
			if acknowledged {
				step.State = "completed"
			}
			if errors.Is(err, contract.ErrSQLSessionLost) {
				step.State = "uncertain"
			}
			var native *ServerError
			if errors.As(err, &native) {
				err = &contract.QueryError{Cause: err, Diagnostic: contract.QueryDiagnostic{Code: fmt.Sprintf("0x%04x", native.Code), SQL: source, Statement: len(result.Steps) + 1}}
			}
			step.Error = err.Error()
		}
		result.Steps = append(result.Steps, step)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
func cloneCells(cells []contract.Cell) []contract.Cell {
	out := make([]contract.Cell, len(cells))
	for i, c := range cells {
		out[i] = c.Clone()
	}
	return out
}
func sameColumns(a, b []contract.Column) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func previewColumnsSize(columns []contract.Column) int {
	n := len(columns) * 64
	for _, col := range columns {
		n += len(col.Name) + len(col.DatabaseType)
	}
	return n
}

var _ contract.QueryFileLease = (*queryLease)(nil)
