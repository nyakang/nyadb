package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func captureNodeProperties(t *testing.T, s *State, d *Document, name string) {
	t.Helper()
	output := os.Getenv("NYADB_NEO4J_PUBLIC_IMAGES")
	if output == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, size[0], size[1])
			tt.Frame()
			if name == "node-properties" && (!tt.HasText("a.a_exact (2)") || !tt.HasText("9223372036854775807")) {
				t.Fatal("expanded node property/precision missing from native result grid", language)
			}
			if name == "node-property-limit" && !tt.HasText(truncationText(selectedResult(d).Truncation)) {
				t.Fatal("bounded expansion silently hid native node data", language)
			}
			if name == "node-native-result" && !tt.HasText(locales.Text("Neo4j 原生图结果 JSON")) {
				t.Fatal("original native graph result missing from snapshot", language)
			}
			folder := filepath.Join(output, "node-properties", language)
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(filepath.Join(folder, fmt.Sprintf("%s-%dx%d.png", name, size[0], size[1])), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(file, tt.Image())
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				t.Fatal("cannot record native property result evidence", err, closeErr)
			}
		}
	}
}

func TestNeo4jNativePublicCypherNodePropertiesOriginalRowsSnapshotsAndExport(t *testing.T) {
	f := neo4jWorkbenchFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	label := "NyaQueryProperties_" + f.Nonce + "_猫`\\u0060"
	empty, wide := label+"_empty", label+"_wide"
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE any(label IN labels(n) WHERE label IN $labels) DETACH DELETE n", Parameters: map[string]any{"labels": []string{label, empty, wide}}})
	zone, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatal(err)
	}
	blob := bytes.Repeat([]byte{0, 255}, 1400)
	properties := map[string]any{
		"a_exact": int64(9223372036854775807), "b_blob": blob, "c_empty": "", "d_mixed": int64(3),
		"e_memo": strings.Repeat("猫", 3000), "f_datetime": time.Date(2026, 7, 3, 1, 2, 3, 123456789, zone),
		"g_point":    nd.Point3D{X: 113.5, Y: 22.3, Z: 10.5, SpatialRefId: 4979},
		"h_duration": nd.Duration{Months: 2, Days: 3, Seconds: 4, Nanos: 123456789},
		"i_list":     []int64{9223372036854775807, -9223372036854775808}, "j_empty_bytes": []byte{}, "k_empty_list": []int64{},
		"z_猫`\\u0060": "literal key",
	}
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "UNWIND range(0,2) AS i CREATE (n:$($label)) SET n=$properties, n._ordinal=i", Parameters: map[string]any{"label": label, "properties": properties}})
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE $label IN labels(n) AND n._ordinal=2 SET n.d_mixed='heterogeneous', n.z_late_only='last row', n.c_empty=null", Parameters: map[string]any{"label": label}})
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (n:$($label))", Parameters: map[string]any{"label": empty}})
	p := neo4jWorkbenchProfile(t, f)
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	handle, err := m.ConnectSession(context.Background(), p, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	runtime := EngineRuntime(m.Session)
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Namespace: m.Namespace, Runtime: func() Runtime { return runtime }, Connected: m.IsConnected})
	defer s.CloseAll()
	query := "MATCH (n) WHERE $label IN labels(n) MATCH (e) WHERE $empty IN labels(e) RETURN n AS a, n.a_exact AS `a.a_exact`, n AS b, CASE WHEN n._ordinal=1 THEN 'scalar' ELSE n END AS mixed, CASE WHEN n._ordinal=1 THEN null ELSE n END AS nullable, e AS empty ORDER BY n._ordinal"
	d := s.Open(1, query, p.ID, "neo4j", false)
	d.layoutMode = "result"
	parameters, _ := json.Marshal(map[string]any{"label": label, "empty": empty})
	d.graphParameters = string(parameters)
	wait := func(target *Document) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for (target.Loading() || target.prepareResults != nil) && time.Now().Before(deadline) {
			s.Consume()
			time.Sleep(time.Millisecond)
		}
		if target.Loading() || target.prepareResults != nil || target.Error() != "" {
			t.Fatal("native property query did not settle", target.Error())
		}
	}
	s.Prepare(d)
	wait(d)
	s.Run(nil, d)
	wait(d)
	set := selectedResult(d)
	column := func(name string) int {
		index := slices.IndexFunc(set.Columns, func(c contract.Column) bool { return c.Name == name })
		if index < 0 {
			t.Fatal("missing native result property", name)
		}
		return index
	}
	cell := func(row int, name string) contract.Cell { return set.Rows[row][column(name)] }
	if d.result.Graph == nil || !d.result.Graph.Completed || len(d.result.Graph.Columns) != 6 || len(d.result.Graph.Rows) != 3 || d.result.Steps[0].SQL != query || !slices.Equal(set.DefaultHiddenColumns, []int{0, 2, 4}) || !d.hiddenResultColumns["sql_0"] || d.hiddenResultColumns["sql_3"] || d.hiddenResultColumns["sql_5"] {
		t.Fatal("original native aliases, pure/mixed/empty nodes or completion changed")
	}
	if cell(0, "a.a_exact (2)").Kind != contract.Integer || cell(0, "a.a_exact (2)").Text != "9223372036854775807" || cell(0, "a.c_empty").Kind != contract.Text || cell(2, "a.c_empty").Kind != contract.Null || cell(0, "a.z_late_only").Kind != contract.Null || cell(2, "a.z_late_only").Text != "last row" || !bytes.Equal(cell(0, "a.b_blob").Bytes, blob) || cell(0, "a.j_empty_bytes").Kind != contract.Binary || len(cell(0, "a.j_empty_bytes").Bytes) != 0 || cell(0, "a.z_猫`\\u0060").Text != "literal key" {
		t.Fatal("native property values lost precision, missing/empty, bytes or literal keys")
	}
	if cell(0, "a.d_mixed").Kind != contract.Integer || cell(2, "a.d_mixed").Kind != contract.Text || cell(1, "mixed.a_exact").Kind != contract.Null || cell(1, "nullable.a_exact").Kind != contract.Null || !strings.Contains(cell(0, "a.f_datetime").Text, "Europe/Stockholm") || !strings.Contains(cell(0, "a.g_point").Text, "4979") || !strings.Contains(cell(0, "a.i_list").Text, "-9223372036854775808") || !strings.Contains(cell(0, "a.k_empty_list").Text, "list") {
		t.Fatal("mixed/nullable/temporal/spatial/list projection narrowed")
	}
	exactID := fmt.Sprintf("sql_%d", column("a.a_exact (2)"))
	emptyID := fmt.Sprintf("sql_%d", column("a.c_empty"))
	d.grid.Columns.Order = []string{exactID, emptyID, fmt.Sprintf("sql_%d", column("a.b_blob"))}
	d.selectedRow = 0
	d.selection.Add(0)
	d.grid.ColumnSelection = []string{exactID, emptyID}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 1280, 800)
	if err = tt.Click(locales.Text("复制结果选区")); err != nil || tt.Clipboard() != "9223372036854775807\t" {
		t.Fatal("native copied properties conflated exact/empty cells", err, tt.Clipboard())
	}
	d.selection.Clear()
	d.selection.Add(2)
	d.selectedRow = 2
	if err = tt.Click(locales.Text("复制结果选区")); err != nil || tt.Clipboard() != "9223372036854775807\tNULL" {
		t.Fatal("native copied missing property became empty", err, tt.Clipboard())
	}
	d.selectedRow = 0
	d.selection.Clear()
	d.selection.Add(0)
	d.grid.ColumnSelection = nil
	encoded, err := EncodeResult(resultData(d, false), "JSON")
	if err != nil || !bytes.Contains(encoded, []byte("Europe/Stockholm")) || !bytes.Contains(encoded, []byte("-9223372036854775808")) || !bytes.Contains(encoded, []byte("a.a_exact (2)")) {
		t.Fatal("complete native property export lost typed originals", err)
	}
	var exported struct {
		Columns []contract.Column
		Rows    [][]contract.Cell
	}
	if json.Unmarshal(encoded, &exported) != nil || len(exported.Columns) != len(set.Columns) || exported.Rows[0][0].Kind != contract.JSON || !bytes.Equal(exported.Rows[0][column("a.b_blob")].Bytes, blob) || exported.Rows[2][column("a.c_empty")].Kind != contract.Null {
		t.Fatal("full cached export lost original node, full bytes or NULL")
	}
	captureNodeProperties(t, s, d, "node-properties")
	if !s.pinResult(d) || len(d.snapshots) != 1 {
		t.Fatal("native property snapshot failed", d.Error())
	}
	snap := d.snapshots[0].Document
	if err = tt.RightClick("a.a_exact (2)"); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem(locales.Text("列"), "a"); err != nil || d.hiddenResultColumns["sql_0"] {
		t.Fatal("user could not expose original native node column", err)
	}
	d.grid.Columns.Order = []string{"sql_0", exactID, emptyID}
	d.grid.Columns.Widths = map[string]float32{exactID: 270}
	captureNodeProperties(t, s, d, "node-source-columns")
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE $label IN labels(n) SET n.a_exact='9223372036854775807'", Parameters: map[string]any{"label": label}})
	s.Run(nil, d)
	wait(d)
	if d.hiddenResultColumns["sql_0"] || d.grid.Columns.Widths[exactID] != 270 || selectedResult(d).Rows[0][column("a.a_exact (2)")].Kind != contract.Text || snap.result.Sets[0].Rows[0][column("a.a_exact (2)")].Kind != contract.Integer || m.Generation(p.ID) != handle.Generation() {
		t.Fatal("new observed type replaced user layout, original snapshot or session")
	}
	snap.graphNative = true
	snap.layoutMode = "result"
	captureNodeProperties(t, s, snap, "node-native-result")
	wideProperties := map[string]any{}
	for i := 0; i < 1024; i++ {
		wideProperties[fmt.Sprintf("key%04d", i)] = int64(i)
	}
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (n:$($label)) SET n=$properties", Parameters: map[string]any{"label": wide, "properties": wideProperties}})
	wideQuery := "MATCH (n) WHERE $label IN labels(n) RETURN n"
	w := s.Open(2, wideQuery, p.ID, "neo4j", false)
	w.layoutMode = "result"
	parameters, _ = json.Marshal(map[string]any{"label": wide})
	w.graphParameters = string(parameters)
	s.Prepare(w)
	wait(w)
	s.Run(nil, w)
	wait(w)
	if !w.result.Graph.Completed || w.NeedsRecovery() || len(w.result.Graph.Rows[0][0].Node.Properties) != 1024 || len(selectedResult(w).Columns) != 1 || !slices.Contains(selectedResult(w).Truncation, graphresult.PropertyProjectionLimit) || w.hiddenResultColumns["sql_0"] {
		t.Fatal("wide property projection changed native completion or silently lost original node")
	}
	captureNodeProperties(t, s, w, "node-property-limit")
}
