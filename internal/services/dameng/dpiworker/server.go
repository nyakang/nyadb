package dpiworker

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

// Dispatch enters the private worker before bootstrap.Run starts native UI.
// The worker has no network or profile configuration in its command line.
func Dispatch() bool {
	if len(os.Args) != 2 || os.Args[1] != entryFlag {
		return false
	}
	code := 0
	if err := serve(os.Stdin, os.Stdout, os.Getenv("DM_SVC_PATH"), dpi.NewConnector); err != nil {
		code = 1
	}
	os.Exit(code)
	return true
}

type connectorFactory func(dpi.Options, string) (driver.Connector, error)

type scope struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type server struct {
	dir          string
	mu           sync.Mutex
	scopes       map[uint64]scope
	last         uint64
	conn         driver.Conn
	rows         map[uint64]driver.Rows
	statements   map[uint64]driver.Stmt
	transactions map[uint64]driver.Tx
	owners       map[uint64]uint64
	poisoned     bool
	next         uint64
	factory      connectorFactory
}

func (s *server) cancel(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.scopes[id]; ok {
		v.cancel()
	}
}

func (s *server) release(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.scopes[id]; ok {
		v.cancel()
		delete(s.scopes, id)
	}
}

func (s *server) cancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.scopes {
		v.cancel()
	}
}

func serve(in io.Reader, out io.Writer, dir string, factory connectorFactory) error {
	if err := validateEnvironment(dir); err != nil {
		return err
	}
	s := &server{dir: dir, factory: factory, scopes: make(map[uint64]scope), rows: make(map[uint64]driver.Rows), statements: make(map[uint64]driver.Stmt), transactions: make(map[uint64]driver.Tx), owners: make(map[uint64]uint64)}
	defer func() {
		s.cancelAll()
		if s.conn != nil && !s.poisoned && validateEnvironment(s.dir) == nil {
			s.conn.Close()
		}
	}()
	type job struct {
		req request
		ctx context.Context
	}
	jobs := make(chan job)
	stop := make(chan struct{})
	defer close(stop)
	readDone := make(chan error, 1)
	go func() {
		defer s.cancelAll()
		for {
			var r request
			if err := readFrame(in, &r); err != nil {
				readDone <- err
				return
			}
			if r.Method == "cancel" {
				s.cancel(r.ID)
				if r.Owner != 0 {
					s.cancel(r.Owner)
				}
				continue
			}
			// Register before dispatch: a following cancel can never race ahead
			// of its original native operation or retained rows/transaction.
			if r.ID == 0 || r.ID <= s.last {
				readDone <- errProtocol
				return
			}
			s.last = r.ID
			ctx, cancel := context.WithCancel(context.Background())
			if r.Deadline != 0 {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(0, r.Deadline))
			}
			s.mu.Lock()
			s.scopes[r.ID] = scope{ctx, cancel}
			s.mu.Unlock()
			select {
			case jobs <- job{r, ctx}:
			case <-stop:
				cancel()
				return
			}
		}
	}()
	for {
		select {
		case err := <-readDone:
			if err == io.EOF {
				return nil
			}
			return err
		case j := <-jobs:
			r, keep := s.execute(j.ctx, j.req)
			if !keep {
				s.release(j.req.ID)
			}
			if err := writeFrame(out, r); err != nil {
				return err
			}
			if s.poisoned {
				return errors.New("DM8 private native configuration changed; original worker retired")
			}
			if j.req.Method == "close" {
				return nil
			}
		}
	}
}

func (s *server) execute(ctx context.Context, q request) (response, bool) {
	r := response{ID: q.ID}
	var err error
	keep := false
	if err = validateEnvironment(s.dir); err != nil {
		s.poisoned = true
		s.cancelAll()
		r.Error = encodeError(errors.Join(err, dpi.ErrSessionClosed))
		return r, false
	}
	if err = ctx.Err(); err != nil {
		r.Error = encodeError(err)
		return r, false
	}
	if q.Method == "init" {
		if s.conn != nil || q.Options == nil {
			err = errProtocol
		} else {
			var c driver.Connector
			c, err = s.factory(*q.Options, q.Password)
			q.Password = ""
			if err == nil {
				s.conn, err = c.Connect(ctx)
			}
		}
	} else if s.conn == nil {
		err = dpi.ErrSessionClosed
	} else {
		var values []driver.NamedValue
		values, err = decodeValues(q.Values)
		if err == nil && q.Method != "exec" && q.Method != "stmt-exec" {
			for _, v := range q.Values {
				if v.Kind == "out" {
					err = errProtocol
					break
				}
			}
		}
		if err == nil {
			switch q.Method {
			case "ping":
				err = s.conn.(driver.Pinger).Ping(ctx)
			case "reset":
				err = s.conn.(driver.SessionResetter).ResetSession(ctx)
			case "close":
				err = s.conn.Close()
			case "prepare":
				var st driver.Stmt
				st, err = s.conn.(driver.ConnPrepareContext).PrepareContext(ctx, q.SQL)
				if err == nil {
					s.next++
					r.Handle = s.next
					r.Inputs = st.NumInput()
					s.statements[r.Handle] = st
				}
			case "stmt-close":
				if st := s.statements[q.Handle]; st != nil {
					err = st.Close()
					delete(s.statements, q.Handle)
				}
			case "exec", "stmt-exec":
				var result driver.Result
				if q.Method == "exec" {
					result, err = s.conn.(driver.ExecerContext).ExecContext(ctx, q.SQL, values)
				} else if st := s.statements[q.Handle]; st != nil {
					result, err = st.(driver.StmtExecContext).ExecContext(ctx, values)
				} else {
					err = dpi.ErrSessionClosed
				}
				if err == nil {
					r.Outputs, err = encodeOutputs(values)
					if err != nil {
						err = errors.Join(err, dpi.ErrOutcomeUnknown, dpi.ErrSessionClosed, s.conn.Close())
						break
					}
					var countErr error
					r.Affected, countErr = result.RowsAffected()
					r.AffectedError = encodeError(countErr)
				}
			case "query", "stmt-query":
				var rows driver.Rows
				if q.Method == "query" {
					rows, err = s.conn.(driver.QueryerContext).QueryContext(ctx, q.SQL, values)
				} else if st := s.statements[q.Handle]; st != nil {
					rows, err = st.(driver.StmtQueryContext).QueryContext(ctx, values)
				} else {
					err = dpi.ErrSessionClosed
				}
				if err == nil {
					r.Columns, err = columnsFrom(rows)
					if err != nil {
						err = errors.Join(err, rows.Close())
					} else {
						s.next++
						r.Handle = s.next
						s.rows[r.Handle] = rows
						s.owners[r.Handle] = q.ID
						keep = true
					}
				}
			case "next":
				if rows := s.rows[q.Handle]; rows != nil {
					v := make([]driver.Value, len(rows.Columns()))
					err = rows.Next(v)
					if err == io.EOF {
						r.EOF, err = true, nil
					} else if err == nil {
						for _, v := range v {
							var b value
							b, err = encodeValue(v)
							if err != nil {
								break
							}
							r.Values = append(r.Values, b)
						}
					}
				} else {
					err = dpi.ErrSessionClosed
				}
			case "rows-close":
				if rows := s.rows[q.Handle]; rows != nil {
					err = rows.Close()
					delete(s.rows, q.Handle)
					s.release(s.owners[q.Handle])
					delete(s.owners, q.Handle)
				}
			case "begin":
				var tx driver.Tx
				tx, err = s.conn.(driver.ConnBeginTx).BeginTx(ctx, q.Tx)
				if err == nil {
					s.next++
					r.Handle = s.next
					s.transactions[r.Handle] = tx
					s.owners[r.Handle] = q.ID
					keep = true
				}
			case "commit", "rollback":
				if tx := s.transactions[q.Handle]; tx != nil {
					if contextual, ok := tx.(dpi.ContextTransaction); ok {
						if q.Method == "commit" {
							err = contextual.CommitContext(ctx)
						} else {
							err = contextual.RollbackContext(ctx)
						}
					} else if q.Method == "commit" {
						err = tx.Commit()
					} else {
						err = tx.Rollback()
					}
					delete(s.transactions, q.Handle)
					s.release(s.owners[q.Handle])
					delete(s.owners, q.Handle)
				} else {
					err = dpi.ErrSessionClosed
				}
			default:
				err = errProtocol
			}
		}
	}
	if s.conn != nil {
		r.Valid = s.conn.(driver.Validator).IsValid()
	}
	r.Error = encodeError(err)
	return r, keep
}

func decodeValues(values []value) ([]driver.NamedValue, error) {
	if len(values) > 4096 {
		return nil, errProtocol
	}
	out := make([]driver.NamedValue, len(values))
	outputCount := 0
	for i, v := range values {
		var x any
		var err error
		if v.Kind == "out" {
			x, err = decodeOutput(v)
			outputCount++
		} else {
			x, err = decodeValue(v)
		}
		if err != nil {
			return nil, err
		}
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: x}
	}
	if outputCount > dpi.MaxOutputParameters {
		return nil, errProtocol
	}
	return out, nil
}
