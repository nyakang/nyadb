package connections

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestMySQLMultiRuleQueryUsesBoundValuesAndUniqueTiebreak(t *testing.T) {
	calls := make(chan structureCall, 10)
	m := connectedQueryOptionsFixture(t, calls)
	o := MySQLDataOptions{Limit: 2, Filters: []MySQLDataFilterRule{
		{Column: "note", Mode: "contains", Value: "x' OR 1=1"},
		{Column: "id", Mode: "not_equal", Value: "7"},
		{Column: "payload", Mode: "is_not_null"},
	}, Sorts: []MySQLDataSortRule{{Column: "note", Descending: true}, {Column: "id"}}}
	result, err := m.TableDataPageWithOptions(context.Background(), 19, "testdb", "records", o)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 2 || result.KeysetAvailable {
		t.Fatalf("nullable sort must use OFFSET: %+v", result)
	}
	<-calls
	<-calls
	q := <-calls
	for _, want := range []string{"LOCATE(?, `note`) > 0", "`id` <> ?", "`payload` IS NOT NULL",
		"ORDER BY `note` DESC, `id` ASC", "LIMIT ? OFFSET ?"} {
		if !strings.Contains(q.query, want) {
			t.Fatalf("missing %q in %s", want, q.query)
		}
	}
	if strings.Contains(q.query, "OR 1=1") || len(q.args) != 4 || q.args[0].Value != "x' OR 1=1" {
		t.Fatalf("unbound filter or unexpected args: %+v", q)
	}
}

func TestMySQLRuleValidationRejectsMalformedAndLimits(t *testing.T) {
	invalid := []MySQLDataOptions{
		{Filters: []MySQLDataFilterRule{{Column: "note", Mode: "injection", Value: "x"}}},
		{Filters: []MySQLDataFilterRule{{Column: "", Mode: "equal", Value: "x"}}},
		{Filters: []MySQLDataFilterRule{{Column: "note", Mode: "equal", Value: strings.Repeat("z", 4097)}}},
		{Sorts: []MySQLDataSortRule{{Column: "id"}, {Column: "id"}}},
		{Filters: make([]MySQLDataFilterRule, MySQLDataMaxFilterRules+1)},
		{Sorts: make([]MySQLDataSortRule, MySQLDataMaxSortRules+1)},
		{FilterColumn: "note", FilterMode: "equal", Filters: []MySQLDataFilterRule{{Column: "id", Mode: "equal"}}},
	}
	for _, o := range invalid {
		if _, _, err := mysqlDataEffectiveRules(o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
}

// Reuse the established no-network driver fixture.
func connectedQueryOptionsFixture(t *testing.T, calls chan structureCall) *MySQLSessionManager {
	t.Helper()
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(queryOptionsConnector{calls}), nil
	}
	if err := m.Connect(context.Background(), ActiveSettings{ProfileID: 19}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}
