package sqlworkspace

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestNeo4jNativePublicStructuredPlansOriginalUIRowsSnapshotsExportAndGeneration(t *testing.T) {
	f := neo4jWorkbenchFixtureTest(t)
	p := neo4jWorkbenchProfile(t, f)
	admin, e := neo4j.Open(context.Background(), f.Config, f.Secret)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	marker := "nya_plans_" + f.Nonce
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n:NyaPlans {marker:$marker}) DETACH DELETE n", Parameters: map[string]any{"marker": marker}})
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	if _, e = m.ConnectSession(context.Background(), p, f.Secret); e != nil {
		t.Fatal(e)
	}
	active := true
	runtime := EngineRuntime(m.Session)
	s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: m.IsConnected, Namespace: m.Namespace, Active: func(*Document) bool { return active }, QueryDialect: func(int64) string { return "neo4j" }, PlanParser: func(int64) func(contract.SQLResult) (*contract.ExecutionPlan, error) {
		return func(r contract.SQLResult) (*contract.ExecutionPlan, error) { return queryplan.Parse("neo4j", r) }
	}})
	defer s.CloseAll()
	source := "CREATE (n:NyaPlans {marker:$marker,exact:$exact}) RETURN n,$exact AS exact"
	d := s.Open(1, "EXPLAIN "+source, p.ID, "neo4j", false)
	d.graphParameters = fmt.Sprintf(`{"marker":%q,"exact":9223372036854775807}`, marker)
	s.Prepare(d)
	waitNativeNeighbors(t, s, d)
	s.Run(nil, d)
	waitNativeNeighbors(t, s, d)
	if d.plan == nil || d.plan.Engine != "neo4j" || !d.planVisible || d.result.Graph.Summary.Counters["nodes_created"] != 0 {
		t.Fatal("EXPLAIN did not reach native shared structure", d.Error())
	}
	capture := func(stage string, doc *Document, labels ...string) {
		t.Helper()
		folder := os.Getenv("NYADB_NEO4J_PLAN_IMAGES")
		if folder == "" {
			return
		}
		if e := os.MkdirAll(folder, 0700); e != nil {
			t.Fatal(e)
		}
		defer i18n.SetLocaleFunc(nil)
		for _, lang := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return lang })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				viewDoc := doc
				if doc.snapshotOwner != nil {
					viewDoc = doc.snapshotOwner
					viewDoc.snapshotIndex = 1
				}
				tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, viewDoc) }, size[0], size[1])
				tt.Frame()
				for _, label := range labels {
					r, ok := tt.Find(locales.Text(label))
					if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("native plan control missing or clipped", stage, lang, size, label, r)
					}
				}
				file, e := os.Create(filepath.Join(folder, fmt.Sprintf("%s-%dx%d-%s.png", lang, size[0], size[1], stage)))
				if e != nil {
					t.Fatal(e)
				}
				e = png.Encode(file, tt.Image())
				file.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	capture("plan-explain", d, "结构计划", "原生计划", "查询结果")
	d.Editor.Buffer.Set("PROFILE " + source)
	s.Run(nil, d)
	waitNativeNeighbors(t, s, d)
	if d.plan == nil || !d.result.Graph.Completed || d.result.Graph.Uncertain || d.result.Graph.Summary.Counters["nodes_created"] != 1 || d.result.Graph.Rows[0][1].Text != "9223372036854775807" {
		t.Fatal("PROFILE lost write completion or original rows", d.Error())
	}
	original := d.result.Graph.Clone()
	capture("plan-profile", d, "结构计划", "原生计划", "查询结果")
	d.planVisible = false
	d.graphPlanNative = true
	capture("plan-native", d, "复制完整原生计划", "导出完整原生计划")
	raw, e := nativePlanJSON(d)
	if e != nil {
		t.Fatal(e)
	}
	d.Editor.Buffer.Set("RETURN 'later text'")
	d.graphParameters = `{"exact":1}`
	var exported string
	s.persistence.files = Files{SavePath: func(string) (string, error) { return "owned.plan.json", nil }, Write: func(_, data string) error { exported = data; return nil }}
	s.exportNativePlan(d)
	consumeUntil(t, s, func() bool { s.consumeIO(ui.Services{}); return len(s.persistence.jobs) == 0 })
	if exported != raw || strings.Contains(exported, "later text") {
		t.Fatal("export followed current draft")
	}
	d.graphPlanNative, d.planVisible = false, false
	d.selectedRow = 0
	d.selection.Add(0)
	capture("plan-query-results", d, "结构计划", "原生计划", "查询结果")
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	snap := d.snapshots[0].Document
	snap.planVisible = true
	defer func() { d.snapshotIndex = 0 }()
	capture("plan-snapshot", snap, "结构计划", "原生计划", "查询结果")
	d.snapshotIndex = 0
	if !reflect.DeepEqual(original.Rows, d.result.Graph.Rows) || d.selection.Len() != 1 || d.selectedRow != 0 {
		t.Fatal("viewing plan modified original selection or rows")
	}
	// The native completion is parked before the UI consumes it. Replacement
	// must retain its confirmed receipt and reject automatic use of a new lease.
	d.Editor.Buffer.Set("PROFILE RETURN 42 AS original")
	s.Run(nil, d)
	active = false
	deadline := time.Now().Add(20 * time.Second)
	for len(d.results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(d.results) != 1 {
		t.Fatal("original native completion not buffered")
	}
	s.Consume()
	if !d.Loading() {
		t.Fatal("parked native plan published")
	}
	fresh, e := m.ConnectSession(context.Background(), p, f.Secret)
	if e != nil {
		t.Fatal(e)
	}
	active = true
	s.Consume()
	if d.Loading() || !d.NeedsRecovery() || d.plan != nil || d.result == nil || !d.result.Graph.Completed || d.result.Graph.Uncertain || d.result.Graph.Summary.Plan.Query != "PROFILE RETURN 42 AS original" {
		t.Fatal("replacement changed original receipt or accepted stale plan", d.Error())
	}
	capture("plan-stale", d, "原生计划", "查询结果")
	snapRaw, e := nativePlanJSON(snap)
	if e != nil || snapRaw != raw {
		t.Fatal("snapshot changed after replacement", e)
	}
	s.Run(nil, d)
	if d.Loading() {
		t.Fatal("stale native plan replayed")
	}
	s.Recover(d)
	waitNativeNeighbors(t, s, d)
	if d.NeedsRecovery() || m.Generation(p.ID) != fresh.Generation() {
		t.Fatal("explicit original recovery failed", d.Error())
	}
	s.Close(d)
	count := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n:NyaPlans {marker:$marker}) RETURN count(n)", Parameters: map[string]any{"marker": marker}})
	if count.Rows[0][0].Text != "1" || !m.IsConnected(p.ID) {
		t.Fatal("view/export/recovery replayed PROFILE or old close killed replacement")
	}
}
