package neo4j

import (
	"reflect"
	"testing"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func extendedPropertyTest(t *testing.T, kind, text string) contract.GraphValue {
	t.Helper()
	v, err := contract.ParseGraphPropertyExtended(kind, text)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestGraphPropertyExtendedParametersRoundtripCanonicalDurationAndRejectedDimensions(t *testing.T) {
	for _, sample := range []struct{ kind, text string }{
		{"date", "-0001-02-28"}, {"date", "10000-02-29"}, {"local_time", "23:59:59.999999999"},
		{"local_datetime", "10000-02-29T23:59:59.000000001"},
		{"time", `{"value":"23:59:59.999999999+00:20","offset_seconds":1234}`},
		{"datetime", `{"value":"1890-01-01T00:00:00+00:09","offset_seconds":561,"zone":"Europe/Paris"}`},
		{"duration", `{"months":-2,"days":3,"seconds":4,"nanoseconds":123456789}`},
		{"duration", `{"months":0,"days":0,"seconds":-60,"nanoseconds":500000000}`},
		{"duration", `{"months":0,"days":0,"seconds":-61,"nanoseconds":500000000}`},
		{"point2d", `{"srid":7203,"x":-0,"y":1.5}`}, {"point3d", `{"srid":4979,"x":113.5,"y":22.3,"z":10.5}`},
	} {
		v := extendedPropertyTest(t, sample.kind, sample.text)
		value, nativeType, err := propertyValue(v)
		b := valueBudget{limit: 1 << 20}
		captured, _, captureErr := b.capture(value, true, 0)
		if err != nil || captureErr != nil || nativeType == "" || !reflect.DeepEqual(captured, v) {
			t.Fatal("native parameter/canonical value mismatch", sample, captured, err, captureErr)
		}
		if duration, ok := value.(nd.Duration); ok && duration.String() != v.Text {
			t.Fatal("neutral duration formatter disagreed with native driver")
		}
		r := propertyRequestTest()
		r.Before, r.After = contract.GraphValue{Kind: "null"}, v.Clone()
		q, err := propertyWriteQuery(r)
		if err != nil || q.Parameters["after"] == nil {
			t.Fatal("native typed addition rejected", err)
		}
		if len(v.Properties) > 0 {
			v.Properties[0].Value.Text = "mutated after capture"
			b = valueBudget{limit: 1 << 20}
			copy, _, _ := b.capture(q.Parameters["after"], true, 0)
			if !reflect.DeepEqual(copy, r.After) {
				t.Fatal("native temporal/spatial parameters alias caller metadata")
			}
		}
	}
	r := propertyRequestTest()
	r.Before = extendedPropertyTest(t, "point2d", `{"srid":7203,"x":1,"y":2}`)
	r.After = extendedPropertyTest(t, "point3d", `{"srid":9157,"x":1,"y":2,"z":0}`)
	if _, err := propertyWriteQuery(r); err == nil {
		t.Fatal("same POINT native type silently admitted dimension changes")
	}
	for _, v := range []contract.GraphValue{r.Before, extendedPropertyTest(t, "date", "2026-01-01")} {
		if _, _, err := propertyValue(contract.GraphValue{Kind: "list", Values: []contract.GraphValue{v}}); err == nil {
			t.Fatal("extended scalar support silently admitted unproven extended lists")
		}
	}
}
