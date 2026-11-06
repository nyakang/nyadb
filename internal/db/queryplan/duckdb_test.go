package queryplan

import (
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDuckDBPlanPreservesServerCardinality(t *testing.T) {
	query, err := Build("duckdb", "SELECT * FROM records")
	if err != nil || query != "EXPLAIN (FORMAT JSON) SELECT * FROM records" {
		t.Fatalf("explain %q %v", query, err)
	}
	row := []contract.Cell{{Text: "physical_plan"}, {Text: `[{"name":"PROJECTION","extra_info":{"Estimated Cardinality":"2"},"children":[{"name":"SEQ_SCAN","extra_info":{"Table":"records"},"children":[]}]}]`}}
	r := contract.SQLResult{Sets: []contract.ResultSet{{Rows: [][]contract.Cell{row}}}}
	p, err := Parse("duckdb", r)
	if err != nil || len(p.Nodes) != 1 || p.Nodes[0].Rows == nil || *p.Nodes[0].Rows != 2 || len(p.Nodes[0].Children) != 1 {
		t.Fatalf("plan %+v %v", p, err)
	}
	r.Truncated = true
	if _, err = Parse("duckdb", r); err == nil {
		t.Fatal("truncated plan accepted")
	}
}
