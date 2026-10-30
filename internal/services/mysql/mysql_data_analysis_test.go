package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLDataAnalysisBindsFiltersAndKeepsWholeResultGrouping(t *testing.T) {
	columns := []MySQLStructureColumn{{Name: "label`field", DataType: "varchar"}, {Name: "amount", DataType: "decimal"}}
	payload := "x' OR 1=1 --"
	from, args, err := mysqlDataAnalysisFrom("db`name", "records", columns, []MySQLDataFilterRule{
		{Column: "label`field", Mode: "contains", Value: payload},
		{Column: "amount", Mode: "is_not_null"},
	})
	if err != nil || strings.Contains(from, payload) || !reflect.DeepEqual(args, []any{payload}) ||
		!strings.Contains(from, "`db``name`.`records`") || !strings.Contains(from, "LOCATE(?, `label``field`) > 0") {
		t.Fatalf("analysis did not preserve qualified/parameterized identity: %s %v %v", from, args, err)
	}
	aggregate, once, distribution, err := mysqlDataAnalysisQueries(from, columns[0])
	if err != nil || !strings.Contains(aggregate, "COUNT(DISTINCT `label``field`)") || strings.Contains(aggregate, "LIMIT") ||
		!strings.Contains(once, "HAVING COUNT(*)=1") || !strings.Contains(distribution, "GROUP BY `label``field`") ||
		!strings.Contains(distribution, "SUBSTRING(CAST(`v` AS CHAR),1,128)") || !strings.HasSuffix(distribution, "LIMIT 10") {
		t.Fatalf("complete aggregates grouped previews or applied paging: %s\n%s\n%s", aggregate, once, distribution)
	}
}

func TestMySQLDataAnalysisNumericAndBinaryAndJSONExpressions(t *testing.T) {
	from := " FROM `db`.`table` WHERE 1=1"
	aggregate, _, _, err := mysqlDataAnalysisQueries(from, MySQLStructureColumn{Name: "value", DataType: "decimal"})
	if err != nil || !strings.Contains(aggregate, "CAST(MIN(`value`) AS CHAR)") || !strings.Contains(aggregate, "CAST(AVG(`value`) AS CHAR)") {
		t.Fatal("numeric aggregates rounded through a client float or omitted average")
	}
	for _, kind := range []string{"blob", "geometry", "json"} {
		aggregate, _, distribution, err := mysqlDataAnalysisQueries(from, MySQLStructureColumn{Name: "value", DataType: kind})
		if err != nil {
			t.Fatal(err)
		}
		if kind == "json" {
			if !strings.Contains(aggregate, "COLLATE utf8mb4_bin") || !strings.Contains(distribution, "COLLATE utf8mb4_bin") {
				t.Fatal("JSON grouping silently used the connection's text collation")
			}
		} else if !strings.Contains(aggregate, "COUNT(DISTINCT CAST(`value` AS BINARY))") || !strings.Contains(distribution, "SUBSTRING(`v`,1,128)") {
			t.Fatal("binary/geometry grouping did not compare complete bytes")
		}
	}
}

func TestMySQLDataAnalysisRejectsUnqualifiedOrUnsupportedFilters(t *testing.T) {
	columns := []MySQLStructureColumn{{Name: "amount", DataType: "decimal"}}
	for _, rule := range []MySQLDataFilterRule{
		{Column: "missing", Mode: "equal", Value: "x"},
		{Column: "amount", Mode: "raw_sql", Value: "x"},
		{Column: "amount", Mode: "contains", Value: "x"},
		{Column: "amount", Mode: "equal", Value: strings.Repeat("x", 4097)},
	} {
		if _, _, err := mysqlDataAnalysisFrom("db", "records", columns, []MySQLDataFilterRule{rule}); err == nil {
			t.Fatalf("invalid filter reached aggregate SQL: %+v", rule)
		}
	}
	if _, _, err := mysqlDataAnalysisFrom("", "records", columns, nil); err == nil {
		t.Fatal("unqualified analysis target accepted")
	}
}

type analysisFixtureConnector struct {
	begin     chan driver.TxOptions
	calls     chan structureCall
	committed chan struct{}
	block     bool
}

func (c analysisFixtureConnector) Connect(context.Context) (driver.Conn, error) {
	return analysisFixtureConn{c}, nil
}
func (analysisFixtureConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type analysisFixtureConn struct{ cfg analysisFixtureConnector }

func (analysisFixtureConn) Close() error { return nil }
func (analysisFixtureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (analysisFixtureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("expected context transaction")
}
func (c analysisFixtureConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.cfg.begin <- options
	return analysisFixtureTx{c.cfg.committed}, nil
}
func (c analysisFixtureConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "information_schema.COLUMNS") {
		return dataFixtureConn{}.QueryContext(ctx, query, args)
	}
	if c.cfg.calls != nil {
		c.cfg.calls <- structureCall{query: query, args: args}
	}
	if c.cfg.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	switch {
	case strings.HasPrefix(query, "SELECT COUNT(*), COUNT(*)-COUNT("):
		return &structureRows{cols: []string{"rows", "null", "distinct", "min", "max", "mean", "zero"}, values: [][]driver.Value{{int64(4), int64(1), int64(2), "1.25", "2.75", "2.25000000", int64(0)}}}, nil
	case strings.HasPrefix(query, "SELECT CAST(AVG(`v`) AS CHAR)"):
		return &structureRows{cols: []string{"median"}, values: [][]driver.Value{{"2.75"}}}, nil
	case strings.HasPrefix(query, "SELECT CAST(STDDEV_POP("):
		return &structureRows{cols: []string{"stddev"}, values: [][]driver.Value{{"0.7071067811865476"}}}, nil
	case strings.HasPrefix(query, "SELECT COUNT(*) FROM ("):
		return &structureRows{cols: []string{"singletons"}, values: [][]driver.Value{{int64(1)}}}, nil
	case strings.HasPrefix(query, "SELECT SUBSTRING("):
		return &structureRows{cols: []string{"v", "n", "truncated"}, values: [][]driver.Value{{"2.75", int64(2), false}, {"1.25", int64(1), false}}}, nil
	}
	return nil, errors.New("unexpected aggregate query")
}

type analysisFixtureTx struct{ committed chan struct{} }

func (t analysisFixtureTx) Commit() error { close(t.committed); return nil }
func (analysisFixtureTx) Rollback() error { return nil }

func connectedAnalysisFixture(t *testing.T, fixture analysisFixtureConnector) *MySQLSessionManager {
	t.Helper()
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(fixture), nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 15}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestMySQLDataAnalysisRunsBoundAggregatesInReadOnlySnapshot(t *testing.T) {
	fixture := analysisFixtureConnector{begin: make(chan driver.TxOptions, 1), calls: make(chan structureCall, 6), committed: make(chan struct{})}
	m := connectedAnalysisFixture(t, fixture)
	result, err := m.AnalyzeMySQLData(context.Background(), 15, "db", "records", MySQLDataOptions{Filters: []MySQLDataFilterRule{{Column: "note", Mode: "equal", Value: "bound"}}}, []string{"id"})
	if err != nil {
		t.Fatal(err)
	}
	options := <-fixture.begin
	if !options.ReadOnly || options.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) || result.Rows != 4 || len(result.Columns) != 1 || result.Columns[0].Singletons != 1 || result.Columns[0].Numeric != 3 || result.Columns[0].Distribution[0].Count != 2 || result.CompletedAt.IsZero() {
		t.Fatalf("aggregate result/snapshot options: %+v %+v", options, result)
	}
	select {
	case <-fixture.committed:
	default:
		t.Fatal("complete read transaction was not settled")
	}
	for range 6 {
		call := <-fixture.calls
		found := false
		for _, arg := range call.args {
			if arg.Value == "bound" {
				found = true
			}
		}
		if !found || strings.Contains(call.query, "'bound'") {
			t.Fatal("bound filter was changed between aggregates")
		}
	}
}

func TestMySQLDataAnalysisAcknowledgesCancellationAndRejectsPaging(t *testing.T) {
	fixture := analysisFixtureConnector{begin: make(chan driver.TxOptions, 1), calls: make(chan structureCall, 1), committed: make(chan struct{}), block: true}
	m := connectedAnalysisFixture(t, fixture)
	if _, err := m.AnalyzeMySQLData(context.Background(), 15, "db", "records", MySQLDataOptions{Limit: 100}, []string{"id"}); !errors.Is(err, ErrMySQLDataPage) {
		t.Fatal("whole-result analysis accepted a page limit")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.AnalyzeMySQLData(ctx, 15, "db", "records", MySQLDataOptions{}, []string{"id"})
		done <- err
	}()
	<-fixture.calls
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("aggregate cancellation did not return")
	}
}

func TestMySQLDataAnalysisNumericRequestsBindMedianAndCenterExactVariance(t *testing.T) {
	from := " FROM `db`.`table` WHERE `label`=?"
	scale := int64(10)
	median, deviation, centered, err := mysqlDataAnalysisNumericQueries(from, MySQLStructureColumn{Name: "amount", DataType: "decimal", NumericScale: &scale})
	if err != nil || !centered || !strings.Contains(median, "IS NOT NULL ORDER BY `amount` ASC LIMIT ? OFFSET ?") || !strings.Contains(deviation, "STDDEV_POP(`amount` - CAST(? AS DECIMAL(65,10)))") || !strings.HasSuffix(deviation, from) {
		t.Fatalf("exact baseline or central row bounds lost: %s %s %v", median, deviation, err)
	}
	_, deviation, centered, err = mysqlDataAnalysisNumericQueries(from, MySQLStructureColumn{Name: "amount", DataType: "double"})
	if err != nil || centered || !strings.Contains(deviation, "STDDEV_POP(`amount`)") {
		t.Fatal("floating statistics changed the stored type")
	}
	if _, _, _, err := mysqlDataAnalysisNumericQueries(from, MySQLStructureColumn{Name: "amount", DataType: "varchar"}); err == nil {
		t.Fatal("text was coerced into a numeric median")
	}
}
