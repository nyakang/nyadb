package engines

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/pgdistributed"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type distributionFixture struct {
	rows       [][]driver.Value
	columns    []string
	err        error
	queries    int
	closedRows int
	query      string
	args       []driver.NamedValue
}

func (f *distributionFixture) Connect(context.Context) (driver.Conn, error) {
	return distributionConn{f}, nil
}
func (*distributionFixture) Driver() driver.Driver { return distributionDriver{} }

type distributionDriver struct{}

func (distributionDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unexpected catalog reconnect")
}

type distributionConn struct{ fixture *distributionFixture }

func (distributionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected statement")
}
func (distributionConn) Close() error              { return nil }
func (distributionConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (c distributionConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.fixture.queries++
	c.fixture.query, c.fixture.args = q, append([]driver.NamedValue(nil), args...)
	return &distributionRows{fixture: c.fixture}, nil
}

type distributionRows struct {
	fixture *distributionFixture
	next    int
}

func (r *distributionRows) Columns() []string {
	if r.fixture.columns != nil {
		return r.fixture.columns
	}
	return []string{"code", "columns", "key_count"}
}
func (r *distributionRows) Close() error { r.fixture.closedRows++; return nil }
func (r *distributionRows) Next(dest []driver.Value) error {
	if r.next == len(r.fixture.rows) {
		if r.fixture.err != nil {
			return r.fixture.err
		}
		return io.EOF
	}
	copy(dest, r.fixture.rows[r.next])
	r.next++
	return nil
}

func TestDistributedMetadataKeepsOriginalScopeAndCatalogIdentifiers(t *testing.T) {
	for _, product := range []pgdistributed.Product{pgdistributed.Cloudberry, pgdistributed.OpenTenBase} {
		code := "p"
		if product == pgdistributed.OpenTenBase {
			code = "H"
		}
		fixture := &distributionFixture{rows: [][]driver.Value{{code, `["a\nb","a,b","quoted\"key"]`, int64(3)}}}
		db := sql.OpenDB(fixture)
		t.Cleanup(func() { db.Close() })
		s := &Session{id: 7, catalog: "original", strategy: postgresDialect{}, kind: connections.Kind(product)}
		o := s.Namespace("schema'; DROP SCHEMA other; --").Object("table", "same\nname")
		policy, err := s.readDistributedPolicy(context.Background(), db, o)
		if err != nil || policy.Product != product || len(policy.Columns) != 3 || policy.Columns[0] != "a\nb" || policy.Columns[1] != "a,b" || policy.Columns[2] != `quoted"key` {
			t.Fatal(policy, err)
		}
		if fixture.queries != 1 || fixture.closedRows != 1 || len(fixture.args) != 2 || fixture.args[0].Value != o.Schema || fixture.args[1].Value != o.Name || strings.Contains(fixture.query, o.Schema) || strings.Contains(fixture.query, o.Name) {
			t.Fatal("catalog escaped original bound scope")
		}
		for _, wrong := range []contract.ObjectRef{
			{ProfileID: 8, Catalog: o.Catalog, Schema: o.Schema, Kind: "table", Name: o.Name},
			{ProfileID: 7, Catalog: "replacement", Schema: o.Schema, Kind: "table", Name: o.Name},
			{ProfileID: 7, Catalog: o.Catalog, Schema: o.Schema, Kind: "view", Name: o.Name},
		} {
			if _, err := s.readDistributedPolicy(context.Background(), db, wrong); err == nil {
				t.Fatal("cross-session or unsupported object accepted")
			}
		}
		if fixture.queries != 1 {
			t.Fatal("rejected identity queried native catalogs")
		}
		s.closed = true
		if _, err := s.readDistributedPolicy(context.Background(), db, o); !errors.Is(err, ErrRevoked) {
			t.Fatal("closed original session retained catalog access", err)
		}
	}
}

func TestDistributedMetadataRejectsUnknownPartialAndOversizedRows(t *testing.T) {
	for _, fixture := range []*distributionFixture{
		{rows: [][]driver.Value{{"p", `null`, int64(0)}}},
		{rows: [][]driver.Value{{"p", `["a","a"]`, int64(2)}}},
		{rows: [][]driver.Value{{"p", `["a",null]`, int64(2)}}},
		{rows: [][]driver.Value{{"p", `["a\u0000b"]`, int64(1)}}},
		{rows: [][]driver.Value{{"p", `not json`, int64(0)}}},
		{rows: [][]driver.Value{{"p", strings.Repeat("x", (256<<10)+1), int64(0)}}},
		{rows: [][]driver.Value{{"unknown", `[]`, int64(0)}}},
		{rows: [][]driver.Value{{"p", `[]`, int64(1)}}}, // missing attribute must not become RANDOMLY
		{rows: [][]driver.Value{{"p", `["a"]`, int64(2)}}},
		{rows: [][]driver.Value{{"p", `[]`, int64(0)}, {"r", `[]`, int64(0)}}},
		{rows: [][]driver.Value{{"p", `[]`, int64(0)}}, err: io.ErrUnexpectedEOF},
	} {
		db := sql.OpenDB(fixture)
		s := &Session{id: 7, catalog: "original", strategy: postgresDialect{}, kind: connections.Kind(pgdistributed.Cloudberry)}
		if _, err := s.readDistributedPolicy(context.Background(), db, s.Namespace("public").Object("table", "events")); err == nil {
			t.Fatal("accepted incomplete native metadata")
		}
		if fixture.closedRows != 1 {
			t.Fatal("failed read retained driver rows")
		}
		db.Close()
	}
	fixture := &distributionFixture{}
	db := sql.OpenDB(fixture)
	defer db.Close()
	s := &Session{id: 7, catalog: "original", strategy: postgresDialect{}, kind: connections.Kind(pgdistributed.Cloudberry)}
	o := s.Namespace("public").Object("table", "events")
	if policy, err := s.readDistributedPolicy(context.Background(), db, o); err != nil || policy != nil {
		t.Fatal("absent policy was invented", policy, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.readDistributedPolicy(ctx, db, o); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled metadata read proceeded", err)
	}
}
