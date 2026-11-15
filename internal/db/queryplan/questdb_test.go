package queryplan

import (
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func questDBPlanResult(lines ...string) contract.SQLResult {
	set := contract.ResultSet{Columns: []contract.Column{{Name: "QUERY PLAN"}}}
	for _, line := range lines {
		set.Rows = append(set.Rows, []contract.Cell{{Kind: contract.Text, Text: line}})
	}
	return contract.SQLResult{Sets: []contract.ResultSet{set}}
}

func TestQuestDBNativePlanPreservesScanHierarchyAndLabels(t *testing.T) {
	r := questDBPlanResult("Filter filter: seq=9007199254740993L", "    PageFrame", "        Row forward scan", "        Frame forward scan on: native_rows")
	p, err := Parse("questdb", r)
	if err != nil || p.Engine != "questdb" || len(p.Nodes) != 1 || len(p.Nodes[0].Children) != 1 || len(p.Nodes[0].Children[0].Children) != 2 || p.Nodes[0].Children[0].Children[1].Label != "Frame forward scan on: native_rows" {
		t.Fatal("native hierarchy changed", p, err)
	}
	var walk func(contract.PlanNode)
	walk = func(n contract.PlanNode) {
		if n.Cost != nil || n.Rows != nil || len(n.Metrics) != 0 {
			t.Fatal("invented native optimizer metrics")
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(p.Nodes[0])
	grouped := questDBPlanResult("GroupBy vectorized: false", "  values: [sum(id)]", "    PageFrame", "        Row forward scan", "        Frame forward scan on: native_rows")
	gp, err := Parse("questdb", grouped)
	if err != nil || gp.Nodes[0].Detail != "values: [sum(id)]" || len(gp.Nodes[0].Children) != 1 {
		t.Fatal("native aggregate property changed", gp, err)
	}
	q, err := Build("questdb", "SELECT 'a;b' FROM events LIMIT 1,2")
	if err != nil || q != "EXPLAIN SELECT 'a;b' FROM events LIMIT 1,2" {
		t.Fatal("native nonexecuting plan request changed", q, err)
	}
	if _, err := Build("questdb", "SELECT 1; INSERT INTO events VALUES(1)"); err == nil {
		t.Fatal("multiple statements admitted")
	}
}

func TestQuestDBPlanRejectsIncompleteHierarchyAndDisplayOverflow(t *testing.T) {
	for _, r := range []contract.SQLResult{
		questDBPlanResult(), questDBPlanResult("    child"), questDBPlanResult("Root", "        skipped"), questDBPlanResult("Root", "  ambiguous"), questDBPlanResult("Root", "      misplaced: property"), questDBPlanResult("Root", "other root"), questDBPlanResult("Root\nforged"), questDBPlanResult("\tRoot"), questDBPlanResult(" "), questDBPlanResult(strings.Repeat("x", (8<<20)+1)),
	} {
		if _, err := Parse("questdb", r); err == nil {
			t.Fatal("incomplete hierarchy accepted")
		}
	}
	for _, mutate := range []func(*contract.SQLResult){
		func(r *contract.SQLResult) { r.Truncated = true },
		func(r *contract.SQLResult) { r.Sets[0].Truncation = []string{"rows"} },
		func(r *contract.SQLResult) { r.Sets[0].Rows[0][0].Truncated = true },
		func(r *contract.SQLResult) { r.Sets[0].Rows[0][0].Kind = contract.Null },
		func(r *contract.SQLResult) { r.Sets[0].Columns[0].Name = "unrelated" },
		func(r *contract.SQLResult) { r.Sets = append(r.Sets, r.Sets[0]) },
	} {
		r := questDBPlanResult("Root")
		mutate(&r)
		if _, err := Parse("questdb", r); err == nil {
			t.Fatal("truncated or unrelated receipt accepted")
		}
	}
	lines := []string{"Root"}
	for depth := 1; depth <= 64; depth++ {
		lines = append(lines, strings.Repeat("    ", depth)+"child")
	}
	if _, err := Parse("questdb", questDBPlanResult(lines...)); err != nil {
		t.Fatal(err)
	}
	lines = append(lines, strings.Repeat("    ", 65)+"too deep")
	if _, err := Parse("questdb", questDBPlanResult(lines...)); err == nil {
		t.Fatal("depth overflow accepted")
	}
	lines = make([]string, 10001)
	lines[0] = "Root"
	for i := 1; i < len(lines); i++ {
		lines[i] = "    child"
	}
	if _, err := Parse("questdb", questDBPlanResult(lines...)); err == nil {
		t.Fatal("node overflow accepted")
	}
}
