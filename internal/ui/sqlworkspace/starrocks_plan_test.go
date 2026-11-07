package sqlworkspace

import (
	"context"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
)

func TestStarRocksPlanPreservesReadingsAndNeverRetriesFallback(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }, PlanParser: func(int64) func(contract.SQLResult) (*contract.ExecutionPlan, error) {
		return func(r contract.SQLResult) (*contract.ExecutionPlan, error) { return queryplan.Parse("starrocks", r) }
	}})
	defer s.CloseAll()
	d := s.Open(3, "SELECT 1", 1, "native_database", false)
	ordinary := contract.SQLResult{Sets: []contract.ResultSet{{Rows: [][]contract.Cell{{{Kind: contract.Integer, Text: "1"}}}}}}
	d.setResult(&ordinary)
	d.selectedRow = 0
	d.selection.Add(0)
	const text = "PLAN COST\n  CPU: 0.0\n  Memory: 0.0\n\nPLAN FRAGMENT 0(F00)\n  Input Partition: UNPARTITIONED\n  RESULT SINK\n\n  0:UNION\n     constant exprs: 1\n     cardinality: 1"
	raw := contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "Explain String"}}}}}
	for _, line := range strings.Split(text, "\n") {
		raw.Sets[0].Rows = append(raw.Sets[0].Rows, []contract.Cell{{Kind: contract.Text, Text: line}})
	}
	calls := 0
	s.Executor = func(_ context.Context, _ int64, _ string, q string) (contract.SQLResult, error) {
		calls++
		if q != "EXPLAIN COSTS SELECT 1" {
			t.Error("plan execution changed the native estimated request")
		}
		return raw, nil
	}
	q, err := queryplan.Build("starrocks", d.Editor.Buffer.String())
	if err != nil {
		t.Fatal(err)
	}
	s.runStatement(nil, d, q)
	consumeUntil(t, s, func() bool { return !d.loading })
	if calls != 1 || d.plan == nil || !d.planVisible || d.planError != "" {
		t.Fatal("StarRocks plan did not enter the shared structure view")
	}
	d.showOrdinaryResult(true)
	if d.result != &ordinary || d.selectedRow != 0 || d.selection.Len() != 1 {
		t.Fatal("plan replaced ordinary reading or selection")
	}
	d.showOrdinaryResult(false)
	// Lost producer fragments must not be drawn as a complete plan or cause
	// submission of a second EXPLAIN. The acknowledged raw response remains.
	raw.Sets[0].Rows[8][0].Text = "  0:EXCHANGE"
	s.runStatement(nil, d, q)
	consumeUntil(t, s, func() bool { return !d.loading })
	if calls != 2 || d.plan != nil || d.planVisible || d.planError == "" || d.result.Sets[0].Rows[8][0].Text != "  0:EXCHANGE" {
		t.Fatal("incomplete plan was rendered, discarded or retried")
	}
	d.showOrdinaryResult(true)
	if d.result != &ordinary || d.selection.Len() != 1 || d.selectedRow != 0 {
		t.Fatal("raw fallback replaced ordinary reading")
	}
}
