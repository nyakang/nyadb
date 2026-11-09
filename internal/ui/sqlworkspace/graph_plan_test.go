package sqlworkspace

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/locales"
)

func nativePlanUITestResult() contract.SQLResult {
	zero := int64(0)
	graph := contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"exact"}, Rows: [][]contract.GraphValue{{{Kind: "integer", Text: "9223372036854775807"}}}, Summary: contract.GraphSummary{Database: "neo4j", Counters: map[string]int64{"nodes_created": 0}, Bookmarks: []string{"native-bookmark"}, Plan: &contract.GraphExecutionPlan{Mode: "profile", Query: "PROFILE RETURN $exact", Parameters: []contract.GraphProperty{{Name: "exact", Value: contract.GraphValue{Kind: "integer", Text: "9223372036854775807"}}}, Root: contract.GraphPlanOperator{Operator: "ProduceResults", Profile: &contract.GraphPlanProfile{Rows: &zero}, Arguments: []contract.GraphProperty{{Name: "EstimatedRows", Value: contract.GraphValue{Kind: "float", Text: "1"}}}}}}}
	return graphresult.Project(graph.Summary.Plan.Query, graph)
}

func TestNativePlanUIKeepsOriginalRowsSnapshotsExportAndNoExecutionOnView(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }, QueryDialect: func(int64) string { return "neo4j" }, PlanParser: func(int64) func(contract.SQLResult) (*contract.ExecutionPlan, error) {
		return func(r contract.SQLResult) (*contract.ExecutionPlan, error) { return queryplan.Parse("neo4j", r) }
	}})
	defer s.CloseAll()
	d := s.Open(2, "PROFILE RETURN $exact", 85, "neo4j", false)
	calls := 0
	s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) {
		calls++
		return nativePlanUITestResult(), nil
	}
	s.Run(nil, d)
	consumeUntil(t, s, func() bool { return !d.loading })
	if d.plan == nil || !d.planVisible || calls != 1 || d.result.Graph == nil {
		t.Fatal("native plan did not reach shared view")
	}
	original := d.result.Graph.Clone()
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 720, 520)
	if !tt.HasText("ProduceResults") {
		t.Fatal("missing native operator")
	}
	if err := tt.Click(locales.Text("查询结果")); err != nil {
		t.Fatal(err)
	}
	d.selection.Add(0)
	d.selectedRow = 0
	if err := tt.Click(locales.Text("原生计划")); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText(locales.Text("复制完整原生计划")) || !tt.HasText(locales.Text("导出完整原生计划")) {
		t.Fatal("missing original plan actions")
	}
	d.Editor.Buffer.Set("later text")
	d.graphParameters = `{"exact":1}`
	raw, err := nativePlanJSON(d)
	if err != nil || !strings.Contains(raw, "9223372036854775807") || strings.Contains(raw, "later text") || !strings.Contains(raw, "native-bookmark") {
		t.Fatal("native plan followed current editor or parameters", err)
	}
	writes := 0
	s.persistence.files = Files{SavePath: func(string) (string, error) { return "owned.plan.json", nil }, Write: func(_, data string) error {
		writes++
		if data != raw {
			t.Error("export followed later plan")
		}
		return nil
	}}
	s.exportNativePlan(d)
	consumeUntil(t, s, func() bool { s.consumeIO(ui.Services{}); return len(s.persistence.jobs) == 0 })
	if writes != 1 || calls != 1 {
		t.Fatal("plan export executed query")
	}
	if !s.pinResult(d) {
		t.Fatal(d.err)
	}
	snap := d.snapshots[0].Document
	d.plan.Nodes[0].Label = "later"
	d.result.Graph.Summary.Plan.Root.Operator = "later"
	if snap.plan.Nodes[0].Label != "ProduceResults" || snap.result.Graph.Summary.Plan.Root.Operator != "ProduceResults" || snap.result.Graph.Summary.Plan.Parameters[0].Value.Text != "9223372036854775807" {
		t.Fatal("plan snapshot shared state")
	}
	snapshotRaw, e := nativePlanJSON(snap)
	if e != nil || !json.Valid([]byte(snapshotRaw)) {
		t.Fatal(e)
	}
	if calls != 1 || d.selectedRow != 0 || d.selection.Len() != 1 || !reflect.DeepEqual(original.Rows, d.result.Graph.Rows) {
		t.Fatal("plan viewing lost original rows or selection")
	}
}

func TestNativePlanFallbackRetainsConfirmedReceiptAndNeverGuessesPlan(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }, QueryDialect: func(int64) string { return "neo4j" }, PlanParser: func(int64) func(contract.SQLResult) (*contract.ExecutionPlan, error) {
		return func(r contract.SQLResult) (*contract.ExecutionPlan, error) { return queryplan.Parse("neo4j", r) }
	}})
	defer s.CloseAll()
	d := s.Open(2, "PROFILE RETURN $exact", 85, "neo4j", false)
	calls := 0
	s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) {
		calls++
		r := nativePlanUITestResult()
		r.Graph.Summary.Plan.Root.Children = []contract.GraphPlanOperator{{Operator: ""}}
		return r, nil
	}
	s.Run(nil, d)
	consumeUntil(t, s, func() bool { return !d.loading })
	if d.plan != nil || d.planError == "" || d.result.Graph == nil || !d.result.Graph.Completed || d.result.Graph.Uncertain || calls != 1 || d.result.Graph.Summary.Plan == nil {
		t.Fatal("plan fallback lost confirmed receipt or retried")
	}
	s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) {
		calls++
		return graphresult.Project("RETURN 1", contract.GraphResult{Attempted: true, Completed: true}), nil
	}
	s.Run(nil, d)
	consumeUntil(t, s, func() bool { return !d.loading })
	if d.plan != nil || d.planError != "" || d.graphPlanNative || calls != 2 {
		t.Fatal("ordinary native query retained old plan")
	}
}
