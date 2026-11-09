package contract

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGraphPropertyExtendedExactComponentsCanonicalTextAndRoundtrip(t *testing.T) {
	for _, sample := range []struct{ kind, text string }{
		{"date", "-0001-02-28"}, {"date", "10000-02-29"}, {"date", "999999999-12-31"}, {"date", "-999999999-01-01"},
		{"local_time", "23:59:59.999999999"}, {"local_datetime", "10000-02-29T23:59:59.000000001"},
		{"time", `{"value":"23:59:59.999999999+00:20","offset_seconds":1234}`},
		{"datetime", `{"value":"1970-01-01T00:00:00.000000000+00:00","offset_seconds":0,"zone":"UTC"}`},
		{"datetime", `{"value":"2026-01-02T23:59:59.999999999-00:20","offset_seconds":-1234,"zone":"Offset"}`},
		{"datetime", `{"value":"2026-10-25T02:30:00+02:00","offset_seconds":7200,"zone":"Europe/Stockholm"}`},
		{"datetime", `{"value":"2026-10-25T02:30:00+01:00","offset_seconds":3600,"zone":"Europe/Stockholm"}`},
		{"datetime", `{"value":"1890-01-01T00:00:00+00:09","offset_seconds":561,"zone":"Europe/Paris"}`},
		{"datetime", `{"value":"1970-01-01T00:00:00Z","offset_seconds":0,"zone":"UTC"}`},
		{"duration", `{"months":-2,"days":3,"seconds":4,"nanoseconds":123456789}`},
		{"duration", `{"months":0,"days":0,"seconds":0,"nanoseconds":-1}`},
		{"duration", `{"months":0,"days":0,"seconds":-60,"nanoseconds":500000000}`},
		{"duration", `{"months":-9223372036854775808,"days":9223372036854775807,"seconds":-9223372036854775808,"nanoseconds":0}`},
		{"point2d", `{"srid":7203,"x":-0,"y":1.5}`},
		{"point2d", `{"srid":4326,"x":181,"y":-90}`},
		{"point3d", `{"srid":4979,"x":113.5,"y":22.3,"z":10.5}`},
		{"point3d", `{"srid":9157,"x":1,"y":2,"z":-3}`},
	} {
		v, err := ParseGraphPropertyExtended(sample.kind, sample.text)
		if err != nil || v.Kind != sample.kind || len(v.Values) != 0 {
			t.Fatal("native typed components failed", sample, err)
		}
		text, err := GraphPropertyExtendedText(v)
		if err != nil {
			t.Fatal(err)
		}
		copy, err := ParseGraphPropertyExtended(sample.kind, text)
		if err != nil || !reflect.DeepEqual(copy, v) {
			t.Fatal("editing/export roundtrip changed components", sample, text)
		}
		cloned := v.Clone()
		if len(cloned.Properties) > 0 {
			cloned.Properties[0].Value.Text = "different"
			if reflect.DeepEqual(cloned, v) {
				t.Fatal("component clones shared property storage")
			}
		}
		if sample.kind == "time" {
			value, _ := GraphPropertyExtendedValue(v)
			_, offset := value.(time.Time).Zone()
			if offset != 1234 || value.(time.Time).Nanosecond() != 999999999 {
				t.Fatal("display text lost exact native offset/nanoseconds")
			}
		}
	}
	for input, want := range map[string]string{
		`{"months":0,"days":0,"seconds":0,"nanoseconds":-1}`:          "PT-0.000000001S",
		`{"months":0,"days":0,"seconds":1,"nanoseconds":1000000001}`:  "PT2.000000001S",
		`{"months":0,"days":0,"seconds":-60,"nanoseconds":500000000}`: "PT-1M0.5S",
	} {
		v, err := ParseGraphPropertyExtended("duration", input)
		if err != nil || v.Text != want {
			t.Fatal("duration normalization lost canonical components", input, v.Text)
		}
	}
}

func TestGraphPropertyExtendedRejectsMalformedAmbiguousAndOutOfRangeComponents(t *testing.T) {
	for _, sample := range []struct{ kind, text string }{
		{"date", "2026-02-29"}, {"date", "-0000-01-01"}, {"date", "1000000000-01-01"}, {"date", "2026-01-01Z"},
		{"local_time", "24:00:00"}, {"local_time", "00:00:60"}, {"local_time", "12:34:56.1234567890"}, {"local_time", "12:00:00Z"},
		{"local_datetime", "2026-01-01T00:00:00Z"},
		{"time", `{"value":"00:00:00Z","offset_seconds":1}`}, {"time", `{"value":"00:00:00+18:00","offset_seconds":64801}`},
		{"time", `{"value":"00:00:00Z","offset_seconds":0,"offset_seconds":1}`},
		{"datetime", `{"value":"2026-03-29T02:30:00+01:00","offset_seconds":3600,"zone":"Europe/Stockholm"}`},
		{"datetime", `{"value":"2026-01-01T00:00:00Z","offset_seconds":0,"zone":"Local"}`},
		{"datetime", `{"value":"2026-01-01T00:00:00Z","offset_seconds":0,"zone":"Missing/Zone"}`},
		{"datetime", `{"value":"2026-07-03T01:02:03+02:00","offset_seconds":0,"zone":"Europe/Stockholm"}`},
		{"duration", `{"months":1e0,"days":0,"seconds":0,"nanoseconds":0}`},
		{"duration", `{"months":0,"days":0,"seconds":9223372036854775807,"nanoseconds":1000000000}`},
		{"point2d", `{"srid":4979,"x":1,"y":2}`}, {"point3d", `{"srid":7203,"x":1,"y":2,"z":3}`},
		{"point2d", `{"srid":4326,"x":1,"y":90.1}`}, {"point2d", `{"srid":7203,"x":1e1000,"y":2}`},
		{"point2d", `{"srid":7203,"x":1,"y":2,"z":0}`}, {"point2d", `{"srid":7203,"x":"1","y":2}`},
		{"duration", `{"months":0,"days":0,"seconds":0,"nanoseconds":0} {}`},
		{"duration", "null"}, {"point2d", "[]"}, {"unknown", "{}"}, {"date", strings.Repeat("x", 1<<20+1)},
	} {
		if _, err := ParseGraphPropertyExtended(sample.kind, sample.text); err == nil {
			t.Fatal("unsupported components were admitted", sample)
		}
	}
	v, _ := ParseGraphPropertyExtended("duration", `{"months":1,"days":2,"seconds":3,"nanoseconds":4}`)
	v.Text = "P100Y"
	if _, err := GraphPropertyExtendedValue(v); err == nil {
		t.Fatal("misleading duration display disagreed with authoritative parts")
	}
	v, _ = ParseGraphPropertyExtended("point3d", `{"srid":9157,"x":1,"y":2,"z":3}`)
	v.Properties = append(v.Properties, v.Properties[0])
	if _, err := GraphPropertyExtendedValue(v); err == nil {
		t.Fatal("duplicate native metadata admitted")
	}
}
