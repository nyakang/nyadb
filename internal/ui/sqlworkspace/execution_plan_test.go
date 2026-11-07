package sqlworkspace

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"strings"
	"testing"
)

func TestExplainPreservesOrdinaryReadingAndNeverAddsAnalyze(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }, PlanParser: func(int64) func(contract.SQLResult) (*contract.ExecutionPlan, error) {
		return func(r contract.SQLResult) (*contract.ExecutionPlan, error) { return queryplan.Parse("sqlite", r) }
	}})
	defer s.CloseAll()
	d := s.Open(2, "SELECT * FROM t", 1, "main", false)
	ordinary := contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "id"}}, Rows: [][]contract.Cell{{{Kind: contract.Integer, Text: "1"}}}}}}
	d.setResult(&ordinary)
	d.selectedRow = 0
	d.selection.Add(0)
	s.Executor = func(_ context.Context, _ int64, _ string, sql string) (contract.SQLResult, error) {
		if strings.Contains(sql, "ANALYZE") {
			t.Error("implicit ANALYZE")
		}
		return contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "id"}, {Name: "parent"}, {Name: "unused"}, {Name: "detail"}}, Rows: [][]contract.Cell{{{Text: "2"}, {Text: "0"}, {Text: "0"}, {Text: "SCAN t"}}}}}}, nil
	}
	sql, err := queryplan.Build("sqlite", d.Editor.Buffer.String())
	if err != nil {
		t.Fatal(err)
	}
	s.runStatement(nil, d, sql)
	consumeUntil(t, s, func() bool { return !d.loading })
	if d.plan == nil || !d.planVisible {
		t.Fatal("missing structured plan")
	}
	d.showOrdinaryResult(true)
	if d.result != &ordinary || d.selectedRow != 0 || d.selection.Len() != 1 {
		t.Fatal("EXPLAIN lost ordinary result selection")
	}
	d.showOrdinaryResult(false)
	s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) {
		return contract.SQLResult{}, errors.New("server format unsupported")
	}
	s.runStatement(nil, d, sql)
	consumeUntil(t, s, func() bool { return !d.loading })
	d.showOrdinaryResult(true)
	if d.result != &ordinary {
		t.Fatal("failed EXPLAIN discarded ordinary result")
	}
}

func TestDorisPlanFallbackKeepsOriginalAndOrdinaryReadings(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }, PlanParser: func(int64) func(contract.SQLResult) (*contract.ExecutionPlan, error) {
		return func(r contract.SQLResult) (*contract.ExecutionPlan, error) { return queryplan.Parse("doris", r) }
	}})
	defer s.CloseAll()
	d := s.Open(3, "SELECT 1", 1, "native_database", false)
	ordinary := contract.SQLResult{Sets: []contract.ResultSet{{Rows: [][]contract.Cell{{{Kind: contract.Integer, Text: "1"}}}}}}
	d.setResult(&ordinary)
	d.selectedRow = 0
	d.selection.Add(0)
	raw := contract.SQLResult{Sets: []contract.ResultSet{{Rows: [][]contract.Cell{{{Kind: contract.Text, Text: "cost = 0.0"}}, {{Kind: contract.Text, Text: "PhysicalResultSink[29] ( outputExprs=[__literal_0#1] )"}}, {{Kind: contract.Text, Text: "+--PhysicalOneRowRelation[25]@0 ( expressions=[1 AS `1`#1] )"}}}}}}
	s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) { return raw, nil }
	text, err := queryplan.Build("doris", d.Editor.Buffer.String())
	if err != nil {
		t.Fatal(err)
	}
	s.runStatement(nil, d, text)
	consumeUntil(t, s, func() bool { return !d.loading })
	if d.plan == nil || !d.planVisible || d.planError != "" {
		t.Fatal("Doris native plan did not enter shared structure view")
	}
	d.showOrdinaryResult(true)
	if d.result != &ordinary || d.selection.Len() != 1 || d.selectedRow != 0 {
		t.Fatal("Doris plan lost original selection")
	}
	// A server response outside the verified layout is retained as original
	// rows. No query rerun or alternative EXPLAIN is submitted automatically.
	raw.Sets[0].Rows[2][0].Truncated = true
	calls := 0
	s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) { calls++; return raw, nil }
	s.runStatement(nil, d, text)
	consumeUntil(t, s, func() bool { return !d.loading })
	if calls != 1 || d.plan != nil || d.planVisible || d.planError == "" || d.result.Sets[0].Rows[2][0].Text != raw.Sets[0].Rows[2][0].Text {
		t.Fatal("incomplete plan was rendered, discarded or retried")
	}
	d.showOrdinaryResult(true)
	if d.result != &ordinary || d.selection.Len() != 1 {
		t.Fatal("plan fallback replaced ordinary query result")
	}
}
