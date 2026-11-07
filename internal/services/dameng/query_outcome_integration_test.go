package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

type receiptConnector struct {
	driver.Connector
	armed            atomic.Bool
	commits, inserts atomic.Int64
}

func (c *receiptConnector) Connect(ctx context.Context) (driver.Conn, error) {
	physical, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &receiptConn{Conn: physical, owner: c}, nil
}

type receiptConn struct {
	driver.Conn
	owner *receiptConnector
}

func (c *receiptConn) QueryContext(ctx context.Context, q string, values []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, values)
}
func (c *receiptConn) ExecContext(ctx context.Context, q string, values []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, values)
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(q)), "INSERT INTO") {
		c.owner.inserts.Add(1)
	}
	if strings.EqualFold(strings.TrimSpace(q), "COMMIT") {
		c.owner.commits.Add(1)
		if err == nil && c.owner.armed.CompareAndSwap(true, false) {
			// The native COMMIT already succeeded on its original server handle.
			// Discard its acknowledgement at the runtime boundary, not its work.
			return nil, errors.Join(dpi.ErrOutcomeUnknown, dpi.ErrSessionClosed)
		}
	}
	return result, err
}
func (c *receiptConn) Ping(ctx context.Context) error { return c.Conn.(driver.Pinger).Ping(ctx) }
func (c *receiptConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func TestNativeDamengSQLCommittedButLostReceiptRequiresExplicitRecoveryWithoutReplay(t *testing.T) {
	c, password := nativeFixture(t)
	s := fixtureSession(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := quote("SYSDBA") + "." + quote("NYADB_SQL_RECEIPT_"+nativeSuffix(t))
	if _, err := s.db.ExecContext(ctx, "CREATE TABLE "+name+"(ID BIGINT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.ExecContext(context.Background(), "DROP TABLE "+name) })
	base, err := NewSessionConnector(c, password)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	fault := &receiptConnector{Connector: base}
	s.sqlDB = sql.OpenDB(fault)
	s.sqlDB.SetMaxOpenConns(2)
	s.sqlDB.SetMaxIdleConns(0)
	l, err := s.ReserveQuery(30)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err = l.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	var tx driver.Tx
	err = l.(*queryLease).conn.Raw(func(raw any) error {
		var err error
		tx, err = raw.(driver.ConnBeginTx).BeginTx(ctx, driver.TxOptions{})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = l.Execute(ctx, "SYSDBA", "INSERT INTO "+name+" VALUES(42)"); err != nil {
		t.Fatal(err)
	}
	fault.armed.Store(true)
	out, err := l.Execute(ctx, "SYSDBA", "COMMIT")
	if !errors.Is(err, dpi.ErrOutcomeUnknown) || !errors.Is(err, contract.ErrSQLSessionLost) || errors.Is(err, driver.ErrBadConn) || len(out.Steps) != 1 || out.Steps[0].State != "uncertain" {
		t.Fatal("native unknown COMMIT result was not retained", err, out)
	}
	if _, err = l.Execute(ctx, "SYSDBA", "INSERT INTO "+name+" VALUES(43)"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("lost native physical session accepted another user command", err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+name).Scan(&count); err != nil || count != 1 || fault.commits.Load() != 1 || fault.inserts.Load() != 1 {
		t.Fatal("lost receipt replayed or reverted the actual native COMMIT", err, count)
	}
	replacement, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Release()
	if err = replacement.Preconnect(ctx); err != nil || fault.inserts.Load() != 1 || fault.commits.Load() != 1 {
		t.Fatal("explicit recovery replayed captured SQL", err)
	}
}
