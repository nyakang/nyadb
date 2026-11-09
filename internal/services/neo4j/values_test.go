package neo4j

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

func captureTest(t *testing.T, raw any, graph bool) (Value, any) {
	t.Helper()
	b := valueBudget{limit: 1 << 20}
	v, native, err := b.capture(raw, graph, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v, native
}

func propertyTest(t *testing.T, props []Property, key string) Value {
	t.Helper()
	for _, p := range props {
		if p.Name == key {
			return p.Value
		}
	}
	t.Fatalf("missing property %s", key)
	return Value{}
}

func TestNeo4jExactNumbersAndImmutableParameterCapture(t *testing.T) {
	for _, tc := range []struct {
		raw        any
		kind, text string
	}{
		{int64(math.MaxInt64), "integer", "9223372036854775807"},
		{int64(math.MinInt64), "integer", "-9223372036854775808"},
		{uint32(math.MaxUint32), "integer", "4294967295"},
		{math.Copysign(0, -1), "float", "-0"},
		{math.Inf(1), "float", "+Inf"}, {math.Inf(-1), "float", "-Inf"},
		{math.NaN(), "float", "NaN"}, {nil, "null", ""},
	} {
		v, _ := captureTest(t, tc.raw, false)
		if v.Kind != tc.kind || v.Text != tc.text {
			t.Fatalf("lost native scalar: %#v", v)
		}
	}
	bytes := []byte{0, 255, 3}
	list := []int64{math.MaxInt64, math.MinInt64}
	nested := map[string]any{"bytes": bytes, "numbers": list}
	v, native := captureTest(t, map[string]any{"nested": nested, "array": [2]string{"猫", ""}}, false)
	bytes[0], list[0], nested["other"] = 9, 7, true
	n := native.(map[string]any)["nested"].(map[string]any)
	if !reflect.DeepEqual(n["bytes"], []byte{0, 255, 3}) || n["numbers"].([]any)[0] != int64(math.MaxInt64) {
		t.Fatal("captured parameters alias caller data")
	}
	if propertyTest(t, propertyTest(t, v.Properties, "nested").Properties, "bytes").Text != "AP8D" {
		t.Fatal("lost bytes")
	}
}

func TestNeo4jTemporalSpatialAndGraphIdentity(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := time.Date(2026, 7, 3, 1, 2, 3, 123456789, zone)
	v, native := captureTest(t, timestamp, false)
	if native.(time.Time).Location() != zone || propertyTest(t, v.Properties, "zone").Text != "Europe/Stockholm" || propertyTest(t, v.Properties, "offset_seconds").Text != "7200" {
		t.Fatal("lost zone")
	}
	for _, tc := range []struct {
		raw  any
		kind string
	}{
		{nd.Date(timestamp), "date"}, {nd.LocalDateTime(timestamp), "local_datetime"},
		{nd.Time(timestamp), "time"}, {nd.LocalTime(timestamp), "local_time"},
		{nd.Duration{Months: -2, Days: 3, Seconds: 4, Nanos: 123}, "duration"},
		{nd.Point2D{X: 1.5, Y: 2.5, SpatialRefId: 7203}, "point2d"},
		{nd.Point3D{X: 1.5, Y: 2.5, Z: 3.5, SpatialRefId: 9157}, "point3d"},
	} {
		v, n := captureTest(t, tc.raw, false)
		if v.Kind != tc.kind || !reflect.DeepEqual(tc.raw, n) {
			t.Fatalf("lost native temporal/spatial kind: %#v", v)
		}
		if v.Kind == "duration" && propertyTest(t, v.Properties, "months").Text != "-2" {
			t.Fatal("lost duration months")
		}
	}
	props := map[string]any{"exact": int64(math.MaxInt64)}
	n := nd.Node{Id: 42, ElementId: "4:database:42", Labels: []string{"Person", "猫"}, Props: props}
	m := nd.Node{Id: 8, ElementId: "4:database:8", Labels: []string{"Person"}, Props: map[string]any{}}
	r := nd.Relationship{Id: 9, ElementId: "5:database:9", StartId: 42, EndId: 8, StartElementId: n.ElementId, EndElementId: m.ElementId, Type: "KNOWS", Props: map[string]any{"weight": 1.5}}
	v, _ = captureTest(t, nd.Path{Nodes: []nd.Node{m, n}, Relationships: []nd.Relationship{r}}, true)
	n.Labels[0], props["exact"] = "Changed", int64(0)
	if v.Path.Nodes[1].LegacyID != 42 || v.Path.Nodes[1].ElementID != n.ElementId || v.Path.Nodes[1].Labels[0] != "Person" || propertyTest(t, v.Path.Nodes[1].Properties, "exact").Text != "9223372036854775807" {
		t.Fatal("lost ordered graph identity or aliased properties")
	}
	if v.Path.Relationships[0].StartElementID != n.ElementId || v.Path.Relationships[0].EndLegacyID != 8 {
		t.Fatal("lost native direction")
	}
	copy := cloneValue(v)
	copy.Path.Nodes[1].Properties[0].Value.Text = "mutated"
	copy.Path.Relationships[0].Properties[0].Value.Text = "mutated"
	if propertyTest(t, v.Path.Nodes[1].Properties, "exact").Text == "mutated" || v.Path.Relationships[0].Properties[0].Value.Text == "mutated" {
		t.Fatal("graph copy aliases original")
	}
}

func TestNeo4jRejectsUnboundedCyclesAndUnsupportedParameters(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, tc := range []struct {
		raw   any
		limit int
		want  error
	}{
		{cycle, 1 << 20, ErrBounds}, {make([]int, 100001), 1 << 20, ErrBounds},
		{uint64(math.MaxUint64), 1 << 20, ErrBounds}, {"too large", 20, ErrBounds},
		{nd.Node{}, 1 << 20, ErrInvalid}, {struct{ X int }{1}, 1 << 20, ErrInvalid},
		{map[string]any{"\xff": 1}, 1 << 20, ErrProtocol},
	} {
		b := valueBudget{limit: tc.limit}
		_, _, err := b.capture(tc.raw, false, 0)
		if !errors.Is(err, tc.want) {
			t.Fatalf("expected %v, got %v", tc.want, err)
		}
	}
}
