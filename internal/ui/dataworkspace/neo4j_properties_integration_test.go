package dataworkspace

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
)

func TestNeo4jNativePublicDataPropertyColumnsAndOriginalFields(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "NyaProperties_" + f.Nonce + "_猫`\\u0060"
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE $name IN labels(n) DETACH DELETE n", map[string]any{"name": name})
	zone, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatal(err)
	}
	blob := bytes.Repeat([]byte{0, 255}, 1400)
	properties := map[string]any{"a_exact": int64(9223372036854775807), "b_blob": blob, "c_empty": "", "d_mixed": int64(3), "e_memo": strings.Repeat("猫", 3000), "f_datetime": time.Date(2026, 7, 3, 1, 2, 3, 123456789, zone), "g_point": nd.Point3D{X: 113.5, Y: 22.3, Z: 10.5, SpatialRefId: 4979}, "h_duration": nd.Duration{Months: 2, Days: 3, Seconds: 4, Nanos: 123456789}, "i_list": []int64{9223372036854775807, -9223372036854775808}, "element_id": "property collides with source name", "z_猫`\\u0060": "literal property key"}
	r := neo4jDataRun(t, admin, "UNWIND range(0,2) AS i CREATE (n:$($name)) SET n=$properties RETURN n", map[string]any{"name": name, "properties": properties})
	ids := make([]string, 0, len(r.Rows))
	for _, row := range r.Rows {
		ids = append(ids, row[0].Node.ElementID)
	}
	slices.Sort(ids)
	neo4jDataRun(t, admin, "MATCH (n) WHERE $name IN labels(n) AND elementId(n)=$id SET n.d_mixed='heterogeneous', n.z_late_only='last entity', n.c_empty=null RETURN n", map[string]any{"name": name, "id": ids[2]})
	profile := neo4jDataProfile(t, f)
	manager := dbruntime.New()
	dbruntime.RegisterBuiltins(manager, nil)
	defer manager.Close()
	handle, err := manager.ConnectSession(context.Background(), profile, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	active := Session{ProfileID: profile.ID, Generation: handle.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, RuntimeFor: manager.Data})
	defer s.CloseAll()
	d := s.OpenObject(1, manager.Namespace(profile.ID, "neo4j").Object("label", name), handle.Capabilities())
	d.limit = 1
	s.Load(ui.Services{}, d, false)
	waitNeo4jData(t, s, d)
	if d.err != "" || len(d.page.Rows) != 1 || d.page.Rows[0][0].Text != ids[0] {
		t.Fatal("original property page failed", d.err)
	}
	column := func(name string) int {
		index := slices.Index(d.page.Columns, name)
		if index < 0 {
			t.Fatal("missing native property column", name)
		}
		return index
	}
	cell := func(name string) contract.DataCell { return d.page.Rows[0][column(name)] }
	if cell("property.a_exact").Value.Kind != contract.Integer || cell("property.a_exact").Text != "9223372036854775807" || cell("property.c_empty").Null || cell("property.c_empty").Text != "" || !cell("property.z_late_only").Null || !cell("property.b_blob").Truncated || len(cell("property.b_blob").Value.Bytes) != 1024 || cell("property.element_id").Text != "property collides with source name" || cell("property.z_猫`\\u0060").Text != "literal property key" {
		t.Fatal("property columns lost exact/missing/empty/binary/literal identity")
	}
	if !strings.Contains(d.page.ColumnMetadata[column("property.d_mixed")].Type, "INTEGER") || !strings.Contains(d.page.ColumnMetadata[column("property.d_mixed")].Type, "STRING") || !strings.Contains(cell("property.f_datetime").Text, "Europe/Stockholm") || !strings.Contains(cell("property.g_point").Text, "4979") || !strings.Contains(cell("property.i_list").Text, "-9223372036854775808") {
		t.Fatal("native property types or temporal/spatial/list details narrowed")
	}
	originalPage, originalRef := d.page, cloneRowRef(*d.page.RowRefs[0])
	captureNeo4jData(t, s, d, "property-columns")
	if d.grid.Columns.Order[1] != "property.a_exact" {
		t.Fatal("native property columns were not presented before raw JSON")
	}
	d.selectedRow = 0
	d.recordFormMode = true
	captureNeo4jData(t, s, d, "property-record-form")
	d.recordFormMode = false
	d.grid.Cells.Cursor = "property.z_late_only"
	s.ShowField(d)
	captureNeo4jData(t, s, d, "property-fields", func(c *ui.Context) {
		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Grow(1).MinWidth(0).Children(func() { s.View(c, d) })
			ui.Box(c).Width(280).Children(func() { s.InspectorView(c, d) })
		})
	})
	bound := s.runtimeFor(d)
	chunk, err := bound.Chunk(context.Background(), profile.ID, "neo4j", name, "property.b_blob", originalRef, 2000, 1024)
	if err != nil || !chunk.Binary || chunk.TotalUnits != 2800 || chunk.NextOffset != 2800 || len(chunk.Text) != 1600 {
		t.Fatal("native property field changed byte bounds", err)
	}
	var exported [][]contract.DataValue
	fields := []string{"property.a_exact", "property.c_empty", "property.z_late_only", "property.b_blob"}
	err = bound.Exporter(context.Background(), profile.ID, "neo4j", name, contract.DataOptions{Object: d.Object()}, fields, func(row []contract.DataValue) error { exported = append(exported, row); return nil })
	if err != nil || len(exported) != 3 || exported[0][0].Kind != contract.Integer || exported[0][0].Text != "9223372036854775807" || exported[0][1].Null || !exported[0][2].Null || !exported[0][3].Binary || !bytes.Equal(exported[0][3].Bytes, blob) || !exported[2][1].Null || exported[2][2].Text != "last entity" {
		t.Fatal("full native property export truncated or conflated missing/empty/binary", err)
	}
	neo4jDataRun(t, admin, "MATCH (n) WHERE $name IN labels(n) AND elementId(n)=$id SET n.new_after_page='later inventory' RETURN n", map[string]any{"name": name, "id": ids[2]})
	s.changeMySQLDataPage(ui.Services{}, d, 1)
	waitNeo4jData(t, s, d)
	if d.err != "" || !slices.Equal(d.page.Columns, originalPage.Columns) || slices.Contains(d.page.Columns, "property.new_after_page") {
		t.Fatal("native paging adopted another property inventory", d.err)
	}
	s.Refresh(ui.Services{}, d)
	waitNeo4jData(t, s, d)
	if d.err != "" || !slices.Contains(d.page.Columns, "property.new_after_page") {
		t.Fatal("explicit refresh did not observe new native property", d.err)
	}
	row, err := bound.RowReader(context.Background(), profile.ID, "neo4j", name, originalRef)
	if err != nil || len(row.Columns) != len(originalPage.Columns) || row.Values["property.a_exact"].Text != "9223372036854775807" {
		t.Fatal("original field read adopted refreshed projection", err)
	}
	if _, exists := row.Values["property.new_after_page"]; exists {
		t.Fatal("new property changed signed original projection")
	}
	neo4jDataRun(t, admin, "MATCH (n) WHERE $name IN labels(n) AND elementId(n)=$id SET n.a_exact=1 RETURN n", map[string]any{"name": name, "id": ids[0]})
	if _, err = bound.Chunk(context.Background(), profile.ID, "neo4j", name, "property.a_exact", originalRef, 0, 1024); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("changed native property replaced original field without explicit page read", err)
	}
}
