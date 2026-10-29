package mysql

import (
	"strings"
	"testing"
)

func TestMySQLKeysetLexicographicMixedDirections(t *testing.T) {
	keys := []mysqlOrderKey{{name: "price", desc: true}, {name: "tenant_id"}, {name: "parent_code"}}
	scope := MySQLDataCursor{Database: "db", Table: "child"}
	cursor, err := mysqlNextDataCursor(keys, []any{"2.50", int64(1), []byte("P2")}, scope)
	if err != nil {
		t.Fatal(err)
	}
	sql, args, err := mysqlKeysetPredicate(keys, cursor)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"`price` < ?", "(`price` = ? AND `tenant_id` > ?)", "(`price` = ? AND `tenant_id` = ? AND `parent_code` > ?)"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("predicate %q missing %q", sql, want)
		}
	}
	if len(args) != 6 || string(cursor.Values[2]) != "P2" {
		t.Fatalf("cursor bindings: %+v", args)
	}
	cursor.Columns[1] = "injected"
	if _, _, err = mysqlKeysetPredicate(keys, cursor); err == nil {
		t.Fatal("tampered cursor accepted")
	}
}
