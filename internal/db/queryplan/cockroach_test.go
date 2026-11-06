package queryplan

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"strings"
	"testing"
)

func cockroachPlanResult(text string) contract.SQLResult {
	set := contract.ResultSet{}
	for _, line := range strings.Split(text, "\n") {
		set.Rows = append(set.Rows, []contract.Cell{{Kind: contract.Text, Text: line}})
	}
	return contract.SQLResult{Sets: []contract.ResultSet{set}}
}
func TestCockroachNativeOptimizerTreeKeepsMetricsAndOrder(t *testing.T) {
	q, err := Build("cockroachdb", "SELECT * FROM records")
	if err != nil || q != "EXPLAIN (OPT, VERBOSE) SELECT * FROM records" {
		t.Fatal(q, err)
	}
	if _, err = Build("cockroachdb", "SELECT 1; DELETE FROM records"); err == nil {
		t.Fatal("multiple statements explained")
	}
	r := cockroachPlanResult("project\n ├── stats: [rows=333.333]\n ├── cost: 1091.90333\n └── select\n      ├── stats: [rows=333.333, null(3)=0]\n      ├── cost: 1088.55\n      ├── scan records\n      │    ├── columns: id:1\n      │    └── stats: [rows=1000]\n      └── filters\n           └── note:2 = 'a: b'")
	p, err := Parse("cockroachdb", r)
	if err != nil || len(p.Nodes) != 1 || *p.Nodes[0].Rows != 333.333 || *p.Nodes[0].Cost != 1091.90333 || p.Nodes[0].Children[0].Children[0].Label != "scan records" || p.Nodes[0].Children[0].Children[1].Children[0].Label != "note:2 = 'a: b'" {
		t.Fatal("native tree lost", err, p)
	}
	for _, text := range []string{"", " └── scan records", "scan\n           └── select", "scan\n ├── cost: NaN", "scan\n ├── cost: 1e999", "scan\n ├── stats: [rows=unknown]", "scan\n ├── cost: 1\n └── cost: 2", "scan\nscan", "scan\n └── stats: [rows=-1]"} {
		if _, err = Parse("cockroachdb", cockroachPlanResult(text)); err == nil {
			t.Fatalf("invalid plan accepted: %q", text)
		}
	}
	r.Truncated = true
	if _, err = Parse("cockroachdb", r); err == nil {
		t.Fatal("truncated tree accepted")
	}
}
