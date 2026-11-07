package gokb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"io/ioutil"
	"sync"
	"time"
)

// Implement the "QueryerContext" interface
func (cn *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	list := make([]driver.Value, len(args))
	for i, nv := range args {
		list[i] = nv.Value
	}
	finish := cn.watchCancel(ctx)
	r, err := cn.query(query, list)
	if err != nil {
		if finish != nil {
			finish()
		}
		return nil, err
	}
	r.finish = finish
	return r, nil
}

// Implement the "ExecerContext" interface
func (cn *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	list := make([]driver.Value, len(args))
	for i, nv := range args {
		list[i] = nv.Value
	}

	if finish := cn.watchCancel(ctx); finish != nil {
		defer finish()
	}

	return cn.Exec(query, list)
}

// Implement the "ConnBeginTx" interface
func (cn *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var mode string

	switch sql.IsolationLevel(opts.Isolation) {
	case sql.LevelDefault:
		// Don't touch mode: use the server's default
	case sql.LevelReadUncommitted:
		mode = " ISOLATION LEVEL READ UNCOMMITTED"
	case sql.LevelReadCommitted:
		mode = " ISOLATION LEVEL READ COMMITTED"
	case sql.LevelRepeatableRead:
		mode = " ISOLATION LEVEL REPEATABLE READ"
	case sql.LevelSerializable:
		mode = " ISOLATION LEVEL SERIALIZABLE"
	default:
		return nil, fmt.Errorf("kb: isolation level not supported: %d", opts.Isolation)
	}

	if opts.ReadOnly {
		mode += " READ ONLY"
	} else {
		mode += " READ WRITE"
	}

	finish := cn.watchCancel(ctx)
	tx, err := cn.begin(mode)
	if err != nil {
		if finish != nil {
			finish()
		}
		return nil, err
	}
	cn.txnFinish = finish
	return tx, nil
}

func (cn *conn) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if finish := cn.watchCancel(ctx); finish != nil {
		defer finish()
	}
	rows, err := cn.simpleQuery(";")
	if err != nil {
		return driver.ErrBadConn // https://golang.org/pkg/database/sql/driver/#Pinger
	}
	rows.Close()
	return nil
}

func (cn *conn) watchCancel(ctx context.Context) func() {
	if done := ctx.Done(); done != nil {
		finished := make(chan struct{}, 1)
		acknowledged := make(chan struct{})
		socket := cn.c
		cancelled := false
		go func() {
			defer close(acknowledged)
			select {
			case <-done:
				select {
				case finished <- struct{}{}:
				default:
					return
				}
				cancelled = true
				// At this point the function level context is canceled,
				// so it must not be used for the additional network
				// request to cancel the query.
				// Create a new context to pass into the dial.
				ctxCancel, cancel := context.WithTimeout(context.Background(), time.Second*10)
				defer cancel()

				_ = cn.cancel(ctxCancel)
				// Failed delivery must also unblock the captured original read.
				_ = socket.Close()
			case <-finished:
			}
		}()
		var once sync.Once
		return func() {
			once.Do(func() {
				select {
				case <-finished:
				case finished <- struct{}{}:
				}
				<-acknowledged
				// Connection state is changed on the operation's owning thread,
				// after the cancellation goroutine has fully acknowledged cleanup.
				if cancelled {
					cn.bad = true
				}
			})
		}
	}
	return nil
}

func (cn *conn) cancel(ctx context.Context) error {
	c, err := dial(ctx, cn.dialer, cn.opts)
	if err != nil {
		return err
	}
	defer c.Close()
	socket := c
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(callbackDone); socket.Close() })
	defer func() {
		if !stop() {
			<-callbackDone
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		if err := c.SetDeadline(deadline); err != nil {
			return err
		}
	}
	opts := make(values, len(cn.opts))
	for key, value := range cn.opts {
		opts[key] = value
	}

	{
		can := conn{
			c:           c,
			explicitTLS: cn.explicitTLS,
			tlsConfig:   cn.tlsConfig,
		}
		err = can.ssl(opts)
		if err != nil {
			return err
		}

		w := can.writeBuf(0)
		w.int32(80877102) // cancel request code
		w.int32(cn.processID)
		w.int32(cn.secretKey)

		if err := can.sendStartupPacket(w); err != nil {
			return err
		}
		c = can.c
	}

	// Read until EOF to ensure that the server received the cancel.
	{
		_, err := io.Copy(ioutil.Discard, c)
		return err
	}
}

func (cn *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if finish := cn.watchCancel(ctx); finish != nil {
		defer finish()
	}
	return cn.Prepare(query)
}

func (st *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	finish := st.cn.watchCancel(ctx)
	result, err := st.Query(values)
	if err != nil {
		if finish != nil {
			finish()
		}
		return nil, err
	}
	result.(*rows).finish = finish
	return result, nil
}

func (st *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	if finish := st.cn.watchCancel(ctx); finish != nil {
		defer finish()
	}
	return st.Exec(values)
}
