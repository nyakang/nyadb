package queryplan

import (
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func doltPlanResult(lines ...string) contract.SQLResult {
	set := contract.ResultSet{Columns: []contract.Column{{Name: "plan", DatabaseType: "LONGTEXT"}}}
	for _, line := range lines {
		set.Rows = append(set.Rows, []contract.Cell{{Kind: contract.Text, Text: line}})
	}
	return contract.SQLResult{Sets: []contract.ResultSet{set}}
}

func TestDoltNativeTreePreservesPropertiesAndUnknownMetrics(t *testing.T) {
	// Real Dolt tree fixtures from the disposable product server.
	for _, result := range []contract.SQLResult{
		doltPlanResult("IndexedTableAccess(records)", " ├─ index: [records.id]", " ├─ filters: [{[1, 1]}]", " └─ columns: [id]"),
		doltPlanResult("Project", " ├─ columns: [1]", " └─ Table", "     └─ name: "),
	} {
		plan, err := Parse("dolt", result)
		if err != nil || plan.Engine != "dolt" || len(plan.Nodes) != 1 {
			t.Fatal(plan, err)
		}
		var lines []string
		var walk func(contract.PlanNode)
		walk = func(n contract.PlanNode) {
			if n.Cost != nil || n.Rows != nil || len(n.Metrics) != 0 {
				t.Fatal("invented Dolt metrics")
			}
			lines = append(lines, n.Label)
			for _, child := range n.Children {
				walk(child)
			}
		}
		walk(plan.Nodes[0])
		if len(lines) != len(result.Sets[0].Rows) || lines[len(lines)-1] != strings.TrimLeft(result.Sets[0].Rows[len(lines)-1][0].Text, " └─") {
			t.Fatal("lost native plan properties", lines)
		}
	}
	// Continuation markers retain explicit sibling hierarchy.
	r := doltPlanResult("Root", " ├─ Left", " │   └─ leaf", " └─ Right")
	p, err := Parse("dolt", r)
	if err != nil || len(p.Nodes[0].Children) != 2 || p.Nodes[0].Children[0].Children[0].Label != "leaf" {
		t.Fatal(p, err)
	}
}

func TestDoltPlanRejectsTruncationMalformedBranchesAndOversize(t *testing.T) {
	for _, r := range []contract.SQLResult{
		doltPlanResult(),
		doltPlanResult("Root", " ├─ dangling"),
		doltPlanResult("Root", " └─ last", " └─ extra"),
		doltPlanResult("Root", "     └─ skipped depth"),
		doltPlanResult("Root", " ├─ Left", "     └─ wrong continuation", " └─ Right"),
		doltPlanResult("Root", " └─ "),
		doltPlanResult("Root\nforged"),
		doltPlanResult("Root", " └─ tab\tproperty"),
		doltPlanResult(strings.Repeat("x", (8<<20)+1)),
	} {
		if _, err := Parse("dolt", r); err == nil {
			t.Fatal("accepted malformed plan")
		}
	}
	for _, mutate := range []func(*contract.SQLResult){
		func(r *contract.SQLResult) { r.Truncated = true },
		func(r *contract.SQLResult) { r.Sets[0].Truncation = []string{"rows"} },
		func(r *contract.SQLResult) { r.Sets[0].Rows[0][0].Truncated = true },
		func(r *contract.SQLResult) { r.Sets[0].Rows[0][0].Kind = contract.Binary },
		func(r *contract.SQLResult) { r.Sets[0].Columns[0].Name = "unrelated" },
		func(r *contract.SQLResult) { r.Sets = append(r.Sets, r.Sets[0]) },
	} {
		r := doltPlanResult("Root")
		mutate(&r)
		if _, err := Parse("dolt", r); err == nil {
			t.Fatal("accepted incomplete or unrelated result")
		}
	}
	lines := []string{"Root"}
	for depth := 0; depth < 64; depth++ {
		lines = append(lines, strings.Repeat("    ", depth)+" └─ child")
	}
	if _, err := Parse("dolt", doltPlanResult(lines...)); err != nil {
		t.Fatal(err)
	}
	lines = append(lines, strings.Repeat("    ", 64)+" └─ too deep")
	if _, err := Parse("dolt", doltPlanResult(lines...)); err == nil {
		t.Fatal("accepted excessive depth")
	}
	lines = make([]string, 10001)
	lines[0] = "Root"
	for i := 1; i < len(lines); i++ {
		lines[i] = " ├─ child"
	}
	lines[len(lines)-1] = " └─ last"
	if _, err := Parse("dolt", doltPlanResult(lines...)); err == nil {
		t.Fatal("accepted excessive nodes")
	}
}

func TestDoltPlanUsesNativeReadOnlyExplainAndMySQLQuoting(t *testing.T) {
	q, err := Build("dolt", "SELECT ';' AS `a;b`")
	if err != nil || q != "EXPLAIN FORMAT=tree SELECT ';' AS `a;b`" {
		t.Fatal(q, err)
	}
	for _, sql := range []string{"SELECT 1; DELETE FROM records", strings.Repeat("x", 1<<20), "SELECT '\x00'", "SELECT '\xff'"} {
		if _, err := Build("dolt", sql); err == nil {
			t.Fatal("accepted invalid plan request")
		}
	}
}
