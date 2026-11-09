package contract

import (
	"reflect"
	"strings"
	"testing"
)

func TestGraphPropertyExtendedListsKeepCompleteCanonicalMembersAndEditingText(t *testing.T) {
	for _, sample := range []struct{ kind, text string }{
		{"date", `["10000-02-29","-0001-02-28"]`},
		{"local_time", `["23:59:59.999999999","01:02:03.000000001"]`},
		{"local_datetime", `["10000-02-29T23:59:59.999999999","-0001-02-28T00:00:00"]`},
		{"time", `[{"value":"01:02:03.000000001+00:20","offset_seconds":1234},{"value":"23:59:59.999999999-00:20","offset_seconds":-1234}]`},
		{"datetime", `[{"value":"2026-07-03T01:02:03.123456789+02:00","zone":"Europe/Stockholm","offset_seconds":7200},{"value":"1890-01-01T00:00:00+00:09","zone":"Europe/Paris","offset_seconds":561}]`},
		{"duration", `[{"months":0,"days":1,"seconds":0,"nanoseconds":0},{"months":-2,"days":3,"seconds":0,"nanoseconds":-1}]`},
		{"point2d", `[{"srid":4326,"x":181,"y":-90},{"srid":4326,"x":-180,"y":90}]`},
		{"point3d", `[{"srid":4979,"x":113.5,"y":22.3,"z":-0},{"srid":4979,"x":181,"y":-90,"z":10}]`},
	} {
		v, err := ParseGraphPropertyList(sample.kind, sample.text)
		kind, kindErr := GraphPropertyListElementKind(v)
		if err != nil || kindErr != nil || kind != sample.kind || len(v.Values) != 2 {
			t.Fatal("extended list lost selected native kind", sample.kind, err, kindErr)
		}
		text, err := GraphPropertyListJSON(v)
		copy, parseErr := ParseGraphPropertyList(sample.kind, text)
		if err != nil || parseErr != nil || !reflect.DeepEqual(v, copy) {
			t.Fatal("extended list editing text lost components", sample.kind, text, err, parseErr)
		}
		clone := v.Clone()
		if len(clone.Values[0].Properties) > 0 {
			clone.Values[0].Properties[0].Value.Text = "later"
		} else {
			clone.Values[0].Text = "later"
		}
		if reflect.DeepEqual(v, clone) {
			t.Fatal("extended list clone shared components")
		}
		empty, err := ParseGraphPropertyList(sample.kind, "[]")
		if err != nil || empty.Kind != "list" || len(empty.Values) != 0 {
			t.Fatal("empty extended list invented a subtype", err)
		}
		if sample.kind == "duration" && v.Values[1].Properties[2].Value.Text != "999999999" {
			t.Fatal("list duration failed visible canonical normalization")
		}
		if sample.kind == "point2d" && v.Values[0].Properties[1].Value.Text != "-179" {
			t.Fatal("list geographic longitude failed visible normalization")
		}
	}
}

func TestGraphPropertyExtendedListsRejectMixedSpatialIdentityMalformedMembersAndCompleteBudget(t *testing.T) {
	for _, sample := range []struct{ kind, text string }{
		{"date", `[null]`}, {"date", `[20260101]`}, {"date", `["2026-02-29"]`},
		{"time", `[{"value":"00:00:00Z","offset_seconds":1}]`},
		{"datetime", `[{"value":"2026-01-01T00:00:00Z","offset_seconds":0,"zone":"Local"}]`},
		{"duration", `[{"months":1,"months":2,"days":0,"seconds":0,"nanoseconds":0}]`},
		{"duration", `[{"months":0,"days":0,"seconds":9223372036854775807,"nanoseconds":1000000000}]`},
		{"point2d", `[{"srid":7203,"x":1,"y":2},{"srid":4326,"x":1,"y":2}]`},
		{"point2d", `[{"srid":7203,"x":1,"y":2},{"srid":9157,"x":1,"y":2,"z":0}]`},
		{"point3d", `[{"srid":9157,"x":1,"y":2,"z":0},{"srid":4979,"x":1,"y":2,"z":0}]`},
		{"point2d", `[{"srid":7203,"x":"1","y":2}]`},
		{"point2d", `[{"srid":7203,"x":1,"y":2,"extra":0}]`},
	} {
		if _, err := ParseGraphPropertyList(sample.kind, sample.text); err == nil {
			t.Fatal("unsupported extended list admitted", sample)
		}
	}
	member, err := ParseGraphPropertyExtended("duration", `{"months":0,"days":0,"seconds":0,"nanoseconds":1}`)
	if err != nil {
		t.Fatal(err)
	}
	v := GraphValue{Kind: "list", Values: make([]GraphValue, 5000)}
	for i := range v.Values {
		v.Values[i] = member.Clone()
	}
	if len(strings.Repeat(member.Text, len(v.Values))) >= 1<<20 {
		t.Fatal("fixture did not isolate hidden component budget")
	}
	if _, err := GraphPropertyListElementKind(v); err == nil {
		t.Fatal("complete duration components escaped the 1 MiB budget")
	}
}
