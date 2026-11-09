package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type extendedNativeSample struct{ kind, label, first, after string }

func extendedNativeSamples() []extendedNativeSample {
	return []extendedNativeSample{
		{"date", "日期", "10000-02-29", "-0001-02-28"},
		{"local_time", "本地时间", "23:59:59.999999999", "01:02:03.000000001"},
		{"local_datetime", "本地日期时间", "10000-02-29T23:59:59.999999999", "-0001-02-28T00:00:00.000000001"},
		{"time", "带偏移时间", `{"value":"01:02:03.000000001+00:20","offset_seconds":1234}`, `{"value":"23:59:59.999999999-00:20","offset_seconds":-1234}`},
		{"datetime", "带时区日期时间", `{"value":"2026-07-03T01:02:03.123456789+02:00","zone":"Europe/Stockholm","offset_seconds":7200}`, `{"value":"1890-01-01T00:00:00+00:09","zone":"Europe/Paris","offset_seconds":561}`},
		{"duration", "时长", `{"months":0,"days":1,"seconds":0,"nanoseconds":0}`, `{"months":-2,"days":3,"seconds":0,"nanoseconds":-1}`},
		{"point2d", "二维空间点", `{"srid":7203,"x":1,"y":2}`, `{"srid":4326,"x":181,"y":-90}`},
		{"point3d", "三维空间点", `{"srid":9157,"x":1,"y":2,"z":3}`, `{"srid":4979,"x":113.5,"y":22.3,"z":-0}`},
	}
}

func extendedNativeValue(t *testing.T, kind, text string) contract.GraphValue {
	t.Helper()
	if element, ok := strings.CutPrefix(kind, "list:"); ok {
		v, err := contract.ParseGraphPropertyList(element, text)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	v, err := contract.ParseGraphPropertyExtended(kind, text)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func extendedNativeParameter(t *testing.T, v contract.GraphValue) any {
	t.Helper()
	if v.Kind == "list" {
		params := make([]any, len(v.Values))
		for i, item := range v.Values {
			params[i] = extendedNativeParameter(t, item)
		}
		return params
	}
	x, err := contract.GraphPropertyExtendedValue(v)
	if err != nil {
		t.Fatal(err)
	}
	switch v.Kind {
	case "date":
		return nd.Date(x.(time.Time))
	case "local_time":
		return nd.LocalTime(x.(time.Time))
	case "local_datetime":
		return nd.LocalDateTime(x.(time.Time))
	case "time":
		return nd.Time(x.(time.Time))
	case "datetime":
		return x
	case "duration":
		d := x.(contract.GraphPropertyDurationValue)
		return nd.Duration{Months: d.Months, Days: d.Days, Seconds: d.Seconds, Nanos: d.Nanoseconds}
	case "point2d", "point3d":
		p := x.(contract.GraphPropertyPointValue)
		if p.Dimensions == 2 {
			return nd.Point2D{SpatialRefId: p.SRID, X: p.X, Y: p.Y}
		}
		return nd.Point3D{SpatialRefId: p.SRID, X: p.X, Y: p.Y, Z: p.Z}
	}
	t.Fatal("unsupported native sample")
	return nil
}

func TestNeo4jNativePublicGraphPropertyExtendedOriginalUIComponentsSnapshotsAndGeneration(t *testing.T) {
	testPublicExtendedProperties(t, false)
}

func TestNeo4jNativePublicGraphPropertyExtendedListsOriginalUIComponentsSnapshotsAndGeneration(t *testing.T) {
	testPublicExtendedProperties(t, true)
}

func testPublicExtendedProperties(t *testing.T, lists bool) {
	samples, stage := extendedNativeSamples(), "extended"
	if lists {
		samples, stage = extendedListNativeSamples(), "extended-list"
	}
	f := neo4jWorkbenchFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	label := "NyaExtended_" + f.Nonce
	if lists {
		label += "_lists"
	}
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": label}})
	created := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "CREATE (a:$($label) {name:'Original 猫',exact:$exact})-[r:NYA_EXTENDED {guard:'unchanged'}]->(b:$($label)) RETURN a,r,b", Parameters: map[string]any{"label": label, "exact": int64(math.MaxInt64)}})
	root, relationship := created.Rows[0][0].Node.ElementID, created.Rows[0][1].Relationship.ElementID
	p := neo4jWorkbenchProfile(t, f)
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	if _, err = m.ConnectSession(context.Background(), p, f.Secret); err != nil {
		t.Fatal(err)
	}
	runtime := EngineRuntime(m.Session)
	active := true
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Namespace: m.Namespace, Runtime: func() Runtime { return runtime }, Connected: m.IsConnected, Active: func(*Document) bool { return active }})
	defer s.CloseAll()
	source := "MATCH (a)-[r:NYA_EXTENDED]->(b) WHERE elementId(a)=$id RETURN a,r,b"
	d := s.Open(1, source, p.ID, "neo4j", false)
	d.layoutMode = "result"
	params, _ := json.Marshal(map[string]any{"id": root})
	d.graphParameters = string(params)
	s.Prepare(d)
	waitNativeNeighbors(t, s, d)
	s.Run(nil, d)
	waitNativeNeighbors(t, s, d)
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
	original, _ := json.Marshal(d.result)
	d.graphVisualOpen = true
	g := d.graphVisualState()
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 1280, 800)
	click := func(text string) {
		t.Helper()
		tt.Frame()
		if err := tt.Click(text); err != nil {
			t.Fatal(err)
		}
	}
	creation := func(kind, label, name, text string) {
		t.Helper()
		d.graphPropertyOpen = false
		click(locales.Text("新增图属性"))
		click(locales.Text("图属性名称"))
		tt.Type(name)
		click(locales.Text("图属性类型"))
		if lists {
			index := 16
			for i, sample := range samples {
				if sample.kind == kind {
					index += i
					break
				}
			}
			tt.Key(0, ui.KeyHome)
			for range index {
				tt.Key(0, ui.KeyDown)
			}
			tt.Key(0, ui.KeyEnter)
		} else {
			click(locales.Text(label))
		}
		if d.graphPropertyDraft == nil || d.graphPropertyDraft.kind != kind {
			t.Fatal("native extended selector lost type", kind)
		}
		click(locales.Text("属性新值"))
		tt.Key(commandModifier(), ui.KeyA)
		tt.Type(text)
		click(locales.Text("预览属性保存"))
		if d.graphPropertyDraft.preview == nil {
			t.Fatal("native extended creation preview failed", d.graphPropertyDraft.error)
		}
	}
	apply := func(confirm string, want contract.GraphValue) {
		t.Helper()
		index := len(d.graphPropertyRecords)
		click(locales.Text(confirm))
		waitNativeNeighbors(t, s, d)
		if d.graphPropertyDraft != nil || len(d.graphPropertyRecords) != index+1 || !d.graphPropertyRecords[index].Receipt.Applied || !d.graphPropertyRecords[index].Receipt.Result.Completed || !reflect.DeepEqual(d.graphPropertyRecords[index].Receipt.Result.Rows[0][2], want) {
			t.Fatal("native extended operation lacks complete precise receipt", d.Error())
		}
	}
	var snapshot *Document
	var snapshotBytes []byte
	for index, sample := range samples {
		key := "n:" + root
		if index%2 == 1 {
			key = "r:" + relationship
		}
		g.focus(key)
		name := "typed ` 猫 " + sample.kind
		first := extendedNativeValue(t, sample.kind, sample.first)
		after := extendedNativeValue(t, sample.kind, sample.after)
		creation(sample.kind, sample.label, name, sample.first)
		if !reflect.DeepEqual(d.graphPropertyDraft.preview.After, first) {
			t.Fatal("UI input lost temporal/spatial components", sample.kind)
		}
		captureGraphProperty(t, s, d, stage+"-"+strings.TrimPrefix(sample.kind, "list:"))
		apply("确认新增图属性", first)
		d.graphPropertyOpen = false
		click(name + " " + locales.Text("编辑图属性"))
		if d.graphPropertyDraft == nil {
			t.Fatal("native extended continued editing unavailable", sample.kind)
		}
		d.graphPropertyDraft.text = sample.after
		click(locales.Text("预览属性保存"))
		if d.graphPropertyDraft.preview == nil || !reflect.DeepEqual(d.graphPropertyDraft.preview.Before, first) || !reflect.DeepEqual(d.graphPropertyDraft.preview.After, after) {
			t.Fatal("native extended edit preview lost old/new parts", sample.kind)
		}
		apply("确认保存图属性", after)
		if sample.kind == "datetime" || sample.kind == "list:datetime" {
			if !s.pinResult(d) {
				t.Fatal(d.Error())
			}
			snapshot = d.snapshots[0].Document
			snapshotBytes, _ = json.Marshal(snapshot.graphPropertyRecords)
		}
		d.graphPropertyOpen = false
		click(name + " " + locales.Text("移除图属性"))
		click(locales.Text("预览属性保存"))
		apply("确认移除图属性", contract.GraphValue{Kind: "null"})
	}
	// The original provider rejects its own duration overflow during preview,
	// before any asynchronous request or attempted write is created.
	d.graphPropertyOpen = false
	g.focus("n:" + root)
	s.openGraphPropertyCreation(d, g)
	d.graphPropertyDraft.name, d.graphPropertyDraft.kind, d.graphPropertyDraft.text = "rejected duration", "duration", `{"months":9223372036854775807,"days":0,"seconds":0,"nanoseconds":0}`
	if lists {
		d.graphPropertyDraft.kind = "list:duration"
		d.graphPropertyDraft.text = "[" + d.graphPropertyDraft.text + "]"
	}
	s.previewGraphProperty(d)
	if d.graphPropertyDraft.preview != nil || d.graphPropertyDraft.error == "" || d.graphPropertyDraft.attempted || d.graphPropertyPending != nil || len(d.graphPropertyRecords) != 24 {
		t.Fatal("provider duration overflow was dispatched instead of rejected in preview")
	}
	d.graphPropertyDraft = nil
	dateTime := samples[4]
	name := "concurrent zoned value"
	creation(dateTime.kind, dateTime.label, name, dateTime.first)
	apply("确认新增图属性", extendedNativeValue(t, dateTime.kind, dateTime.first))
	d.graphPropertyOpen = false
	click(name + " " + locales.Text("编辑图属性"))
	d.graphPropertyDraft.text = dateTime.after
	click(locales.Text("预览属性保存"))
	fixed := time.Date(2026, 7, 3, 1, 2, 3, 123456789, time.FixedZone("Offset", 7200))
	neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id SET n[$key]=$value", Parameters: map[string]any{"id": root, "key": name, "value": func() any {
		if lists {
			return []any{fixed, fixed}
		}
		return fixed
	}()}})
	click(locales.Text("确认保存图属性"))
	waitNativeNeighbors(t, s, d)
	if d.graphPropertyDraft == nil || len(d.graphPropertyRecords) != 26 || !d.graphPropertyRecords[25].Receipt.Conflict || d.graphPropertyRecords[25].Receipt.Applied {
		t.Fatal("same-clock fixed offset bypassed original named timezone comparison")
	}
	d.graphPropertyRecord = -1
	captureGraphProperty(t, s, d, stage+"-conflict")
	s.verifyGraphProperty(d, 25)
	waitNativeNeighbors(t, s, d)
	if len(d.graphPropertyRecords) != 27 || !d.graphPropertyRecords[26].Verification || d.graphPropertyRecords[26].Receipt.Applied {
		t.Fatal("independent temporal verification supplied false write confirmation")
	}
	d.graphPropertyDraft = nil
	d.graphPropertyOpen = false
	click(name + " " + locales.Text("编辑图属性"))
	d.graphPropertyDraft.text = dateTime.after
	click(locales.Text("预览属性保存"))
	apply("确认保存图属性", extendedNativeValue(t, dateTime.kind, dateTime.after))
	d.graphPropertyOpen = false
	s.expandGraph(d)
	waitNativeNeighbors(t, s, d)
	if g.node("n:"+root).Conflicting || g.edge("r:"+relationship).Conflicting {
		t.Fatal("continued extended edits/neighbor reads revived superseded observations")
	}
	g.focus("r:" + relationship)
	point := samples[7]
	creation(point.kind, point.label, "parked geographic point", point.after)
	click(locales.Text("确认新增图属性"))
	active = false
	deadline := time.Now().Add(20 * time.Second)
	for len(d.graphPropertyPending.results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(d.graphPropertyPending.results) == 0 {
		t.Fatal("parked extended completion was not buffered")
	}
	s.Consume()
	if len(d.graphPropertyRecords) != 28 || !d.Loading() {
		t.Fatal("parked spatial receipt was published for another profile")
	}
	fresh, err := m.ConnectSession(context.Background(), p, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	active = true
	waitNativeNeighbors(t, s, d)
	if !d.NeedsRecovery() || d.graphPropertyDraft == nil || !d.graphPropertyDraft.unresolved || len(d.graphPropertyRecords) != 29 || !d.graphPropertyRecords[28].Receipt.Applied || len(g.edge("r:"+relationship).Value.Properties) != 1 {
		t.Fatal("retired spatial completion lost evidence or replaced current graph")
	}
	d.graphPropertyRecord = -1
	captureGraphProperty(t, s, d, stage+"-retired")
	s.saveGraphProperty(d)
	if d.Loading() {
		t.Fatal("retired spatial write replayed")
	}
	var exported []byte
	complete := make(chan struct{})
	s.SetPersistence(nil, Files{SavePath: func(string) (string, error) { return "extended.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = append([]byte(nil), b.Bytes()...)
		close(complete)
		return err
	}}, nil)
	s.exportGraphPropertyRecords(d)
	<-complete
	var records []graphPropertyRecord
	if json.Unmarshal(exported, &records) != nil || len(records) != 29 {
		t.Fatal("extended export lost independent native receipts")
	}
	for index, sample := range samples {
		if !reflect.DeepEqual(records[index*3+1].Request.After, extendedNativeValue(t, sample.kind, sample.after)) || records[index*3+2].Request.After.Kind != "null" || !records[index*3+2].Receipt.Applied {
			t.Fatal("extended export flattened components or lost absence", sample.kind)
		}
	}
	after, _ := json.Marshal(d.result)
	snapshotAfter, _ := json.Marshal(snapshot.graphPropertyRecords)
	if !bytes.Equal(original, after) || !bytes.Equal(snapshotBytes, snapshotAfter) || d.Editor.Buffer.String() != source {
		t.Fatal("extended edits changed original query or independent snapshot")
	}
	read := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (a)-[r:NYA_EXTENDED]->() WHERE elementId(a)=$id RETURN a[$key],r[$point],a.name,a.exact,r.guard", Mode: "read", Parameters: map[string]any{"id": root, "key": name, "point": "parked geographic point"}})
	if !reflect.DeepEqual(read.Rows[0][0], extendedNativeValue(t, dateTime.kind, dateTime.after)) || !reflect.DeepEqual(read.Rows[0][1], extendedNativeValue(t, point.kind, point.after)) || read.Rows[0][2].Text != "Original 猫" || read.Rows[0][3].Text != "9223372036854775807" || read.Rows[0][4].Text != "unchanged" {
		t.Fatal("native extended writes changed unrelated values or lost exact components")
	}
	s.Close(d)
	if d.graphPropertyRecords != nil || d.graphPropertyDraft != nil || !snapshot.closed || !m.IsConnected(p.ID) || m.Generation(p.ID) != fresh.Generation() {
		t.Fatal("original extended close retained caches or closed replacement session")
	}
}
