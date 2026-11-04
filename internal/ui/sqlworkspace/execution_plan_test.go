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
