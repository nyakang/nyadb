package sqlworkspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
)

func TestTrinoPlanKeepsOriginalReadingsAndNeverRetriesFallback(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }, PlanParser: func(int64) func(contract.SQLResult) (*contract.ExecutionPlan, error) {
		return func(r contract.SQLResult) (*contract.ExecutionPlan, error) { return queryplan.Parse("trino", r) }
	}})
	defer s.CloseAll()
	d := s.Open(3, "SELECT 1", 1, `"memory"."default"`, false)
	ordinary := contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "n"}}, Rows: [][]contract.Cell{{{Kind: contract.Integer, Text: "9007199254740993"}}}}}}
	d.setResult(&ordinary)
	d.selectedRow = 0
	d.selection.Add(0)
	bytes, err := os.ReadFile(filepath.Join("..", "..", "db", "queryplan", "testdata", "trino", "constant.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw := contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "Query Plan"}}, Rows: [][]contract.Cell{{{Kind: contract.Text, Text: string(bytes)}}}}}}
	calls := 0
	s.Executor = func(_ context.Context, _ int64, _ string, q string) (contract.SQLResult, error) {
		calls++
		if q != "EXPLAIN (TYPE DISTRIBUTED, FORMAT JSON) SELECT 1" {
			t.Error("native plan request changed")
		}
		return raw, nil
	}
	q, err := queryplan.Build("trino", d.Editor.Buffer.String())
	if err != nil {
		t.Fatal(err)
	}
	s.runStatement(nil, d, q)
	consumeUntil(t, s, func() bool { return !d.loading })
	if calls != 1 || d.plan == nil || !d.planVisible || d.planError != "" {
		t.Fatal("native plan did not enter structure view")
	}
	d.showOrdinaryResult(true)
	if d.result != &ordinary || d.selectedRow != 0 || d.selection.Len() != 1 {
		t.Fatal("native plan replaced ordinary reading/selection")
	}
	d.showOrdinaryResult(false)
	raw.Sets[0].Rows[0][0].Text = `{"0":{"broken":"native plan"}}`
	s.runStatement(nil, d, q)
	consumeUntil(t, s, func() bool { return !d.loading })
	if calls != 2 || d.plan != nil || d.planVisible || d.planError == "" || d.result.Sets[0].Rows[0][0].Text != raw.Sets[0].Rows[0][0].Text {
		t.Fatal("invalid plan was drawn, discarded or retried")
	}
	d.showOrdinaryResult(true)
	if d.result != &ordinary || d.selectedRow != 0 || d.selection.Len() != 1 {
		t.Fatal("raw plan fallback replaced ordinary selection")
	}
}
