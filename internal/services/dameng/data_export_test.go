package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestExportRejectsInvalidRequestsWithoutOpeningNativeConnection(t *testing.T) {
	s := &Session{profile: connections.Profile{ID: 1, Database: "DAMENG"}}
	sink := func([]contract.DataValue) error { t.Fatal("invalid export invoked sink"); return nil }
	for _, options := range []contract.DataOptions{{Offset: -1}, {Offset: 1<<53 + 1}, {Limit: -1}, {Limit: 1001}, {After: &contract.DataCursor{}}, {UseKeyset: true}, {Object: contract.ObjectRef{ProfileID: 2}}} {
		if err := s.exportData(context.Background(), 1, "SYSDBA", "T", options, nil, sink); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("invalid export accepted", options, err)
		}
	}
	if err := s.exportData(context.Background(), 1, "SYSDBA", "T", contract.DataOptions{}, nil, nil); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("nil sink accepted", err)
	}
	if err := s.exportJSONL(context.Background(), contract.ObjectRef{}, nil); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("nil writer accepted", err)
	}
	table := &contract.TableStructure{Columns: []contract.StructureColumn{{Name: "T", DataType: "VARCHAR", ValueKind: contract.Text}, {Name: "B", DataType: "BLOB", ValueKind: contract.Binary}}}
	for _, names := range [][]string{{"T", "T"}, {"missing"}, make([]string, 129)} {
		if _, err := exportColumns(table, names); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("invalid projection accepted", err)
		}
	}
	selected, err := exportColumns(table, []string{"B", "T"})
	if err != nil || selected[0].Name != "B" || selected[1].Name != "T" {
		t.Fatal("selected order lost", err)
	}
	all, err := exportColumns(table, nil)
	if err != nil || !reflect.DeepEqual(all, table.Columns) {
		t.Fatal("default projection changed", err)
	}
	table.Columns[0].DataType = "INTERVAL DAY TO SECOND"
	if _, err := exportColumns(table, nil); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("unsupported export type accepted", err)
	}
}

type exportGateConnector struct {
	entered, canceled, acknowledge, closing, closed chan struct{}
}

func (g *exportGateConnector) Connect(context.Context) (driver.Conn, error) {
	return &exportGateConn{g}, nil
}
func (*exportGateConnector) Driver() driver.Driver { return waitingDriver{} }

type exportGateConn struct{ g *exportGateConnector }

func (*exportGateConn) Prepare(string) (driver.Stmt, error) { return nil, contract.ErrUnsupported }
func (*exportGateConn) Begin() (driver.Tx, error)           { return nil, contract.ErrUnsupported }
func (*exportGateConn) Close() error                        { return nil }
func (c *exportGateConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	return &exportGateRows{g: c.g, ctx: ctx, identity: strings.HasPrefix(query, "SELECT ROWID")}, nil
}

type exportGateRows struct {
	g              *exportGateConnector
	ctx            context.Context
	identity, read bool
}

func (*exportGateRows) Columns() []string                     { return []string{"value"} }
func (*exportGateRows) ColumnTypeDatabaseTypeName(int) string { return "BIGINT" }
func (r *exportGateRows) Next(values []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	if r.identity {
		values[0] = int64(42)
		return nil
	}
	close(r.g.entered)
	<-r.ctx.Done()
	close(r.g.canceled)
	<-r.g.acknowledge
	return r.ctx.Err()
}
func (r *exportGateRows) Close() error {
	if r.identity {
		close(r.g.closing)
		<-r.g.closed
	}
	return nil
}

func TestExportAndSessionCloseWaitForOriginalFieldAndCursorAcknowledgement(t *testing.T) {
	g := &exportGateConnector{entered: make(chan struct{}), canceled: make(chan struct{}), acknowledge: make(chan struct{}), closing: make(chan struct{}), closed: make(chan struct{})}
	var fieldOnce, cursorOnce sync.Once
	fieldAck := func() { fieldOnce.Do(func() { close(g.acknowledge) }) }
	cursorAck := func() { cursorOnce.Do(func() { close(g.closed) }) }
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{profile: connections.Profile{ID: 1}, ctx: ctx, cancel: cancel, db: sql.OpenDB(g), sqlDB: sql.OpenDB(g), closeDone: make(chan struct{})}
	defer func() { fieldAck(); cursorAck(); s.Close() }()
	result := make(chan error, 1)
	go func() {
		child, done, err := s.request(context.Background())
		if err != nil {
			result <- err
			return
		}
		defer done()
		conn, err := s.db.Conn(child)
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		result <- exportRowsAt(child, conn, contract.ObjectRef{Schema: "S", Name: "T"}, []contract.StructureColumn{{Name: "V", DataType: "BIGINT", ValueKind: contract.Integer}}, "SELECT ROWID FROM S.T", nil, 0, func([]contract.DataValue) error { return errors.New("unexpected partial row") })
	}()
	select {
	case <-g.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("original field read did not enter")
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-g.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("original read did not receive cancellation")
	}
	select {
	case <-result:
		t.Fatal("export skipped original field acknowledgement")
	case <-closed:
		t.Fatal("session skipped original field acknowledgement")
	case <-time.After(30 * time.Millisecond):
	}
	fieldAck()
	select {
	case <-g.closing:
	case <-time.After(3 * time.Second):
		t.Fatal("original cursor did not close")
	}
	select {
	case <-result:
		t.Fatal("export skipped original cursor acknowledgement")
	case <-closed:
		t.Fatal("session skipped original cursor acknowledgement")
	case <-time.After(30 * time.Millisecond):
	}
	cursorAck()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("export lost cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("export did not settle")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session did not settle")
	}
}
