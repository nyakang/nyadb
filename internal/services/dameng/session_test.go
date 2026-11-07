package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type waitingConnector struct{ entered, canceled, acknowledge chan struct{} }

func (c *waitingConnector) Connect(context.Context) (driver.Conn, error) {
	return &waitingConn{connector: c}, nil
}
func (c *waitingConnector) Driver() driver.Driver { return waitingDriver{} }

type waitingDriver struct{}

func (waitingDriver) Open(string) (driver.Conn, error) { return nil, contract.ErrUnsupported }

type waitingConn struct{ connector *waitingConnector }

func (c *waitingConn) Close() error                        { return nil }
func (c *waitingConn) Prepare(string) (driver.Stmt, error) { return nil, contract.ErrUnsupported }
func (c *waitingConn) Begin() (driver.Tx, error)           { return nil, contract.ErrUnsupported }
func (c *waitingConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return &waitingRows{ctx: ctx, connector: c.connector}, nil
}

type waitingRows struct {
	ctx       context.Context
	connector *waitingConnector
	row       int
}

func (r *waitingRows) Columns() []string { return []string{"NAME"} }
func (r *waitingRows) Close() error      { return nil }
func (r *waitingRows) Next(values []driver.Value) error {
	if r.row == 0 {
		values[0], r.row = "SYSDBA", 1
		return nil
	}
	if r.row == 1 {
		r.row++
		close(r.connector.entered)
		<-r.ctx.Done()
		close(r.connector.canceled)
		<-r.connector.acknowledge
		return r.ctx.Err()
	}
	return io.EOF
}

func TestDamengConcurrentCloseWaitsForOriginalMetadataAcknowledgement(t *testing.T) {
	c := &waitingConnector{entered: make(chan struct{}), canceled: make(chan struct{}), acknowledge: make(chan struct{})}
	var once sync.Once
	acknowledge := func() { once.Do(func() { close(c.acknowledge) }) }
	defer acknowledge()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{profile: connections.Profile{ID: 1, Database: "DAMENG", Username: "SYSDBA"}, ctx: ctx, cancel: cancel, db: sql.OpenDB(c), sqlDB: sql.OpenDB(c), closeDone: make(chan struct{}), leases: make(map[*queryLease]struct{}), slots: make(chan struct{}, 1)}
	defer func() { acknowledge(); s.Close() }()
	read := make(chan error, 1)
	go func() { _, err := s.Namespaces(context.Background()); read <- err }()
	select {
	case <-c.entered:
	case <-time.After(3 * time.Second):
		acknowledge()
		t.Fatal("metadata read did not reach its original driver")
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- s.Close() }()
	<-c.canceled
	go func() { second <- s.Close() }()
	select {
	case <-first:
		t.Fatal("close returned before the original driver acknowledged cancellation")
	case <-second:
		t.Fatal("concurrent close skipped original cleanup acknowledgement")
	case <-time.After(30 * time.Millisecond):
	}
	acknowledge()
	for _, done := range []chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("close did not complete after original acknowledgement")
		}
	}
	if err := <-read; !errors.Is(err, context.Canceled) {
		t.Fatal("metadata result lost the original cancellation", err)
	}
	if _, err := s.ReserveQuery(1); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed native session reserved another lease", err)
	}
}
