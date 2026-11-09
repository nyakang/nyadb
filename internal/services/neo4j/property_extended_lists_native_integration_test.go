package neo4j

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type extendedListSample struct {
	name, kind, nativeType string
	first, after           []string
}

func extendedListSamples() []extendedListSample {
	return []extendedListSample{
		{"date", "date", "DATE", []string{"10000-02-29", "-0001-02-28"}, []string{"2028-02-29", "999999999-12-31"}},
		{"local_time", "local_time", "LOCAL TIME", []string{"23:59:59.999999999", "00:00:00"}, []string{"01:02:03.000000001", "23:59:59"}},
		{"local_datetime", "local_datetime", "LOCAL DATETIME", []string{"10000-02-29T23:59:59.999999999", "-0001-02-28T00:00:00"}, []string{"2026-01-01T01:02:03.000000001", "2028-02-29T00:00:00"}},
		{"time", "time", "ZONED TIME", []string{`{"value":"01:02:03.000000001+00:20","offset_seconds":1234}`, `{"value":"23:59:59.999999999-00:20","offset_seconds":-1234}`}, []string{`{"value":"01:02:03.000000001+00:20","offset_seconds":1235}`, `{"value":"00:00:00Z","offset_seconds":0}`}},
		{"datetime", "datetime", "ZONED DATETIME", []string{`{"value":"2026-07-03T01:02:03.123456789+02:00","zone":"Europe/Stockholm","offset_seconds":7200}`, `{"value":"1890-01-01T00:00:00+00:09","zone":"Europe/Paris","offset_seconds":561}`}, []string{`{"value":"2026-07-03T01:02:03.123456789+02:00","zone":"Offset","offset_seconds":7200}`, `{"value":"2026-01-01T00:00:00Z","zone":"UTC","offset_seconds":0}`}},
		{"duration", "duration", "DURATION", []string{`{"months":0,"days":1,"seconds":0,"nanoseconds":0}`, `{"months":-2,"days":3,"seconds":0,"nanoseconds":-1}`}, []string{`{"months":0,"days":0,"seconds":86400,"nanoseconds":0}`, `{"months":1,"days":0,"seconds":1,"nanoseconds":1000000001}`}},
		{"point2d", "point2d", "POINT", []string{`{"srid":7203,"x":-0,"y":2}`, `{"srid":7203,"x":1.5,"y":-2.5}`}, []string{`{"srid":7203,"x":0,"y":3}`, `{"srid":7203,"x":-1,"y":2}`}},
		{"point3d", "point3d", "POINT", []string{`{"srid":9157,"x":1,"y":2,"z":-0}`, `{"srid":9157,"x":-1,"y":2,"z":3}`}, []string{`{"srid":9157,"x":1,"y":-2,"z":0}`, `{"srid":9157,"x":-3,"y":4,"z":5}`}},
		{"geographic2d", "point2d", "POINT", []string{`{"srid":4326,"x":181,"y":-90}`, `{"srid":4326,"x":-180,"y":90}`}, []string{`{"srid":4326,"x":179,"y":-90}`, `{"srid":4326,"x":180,"y":0}`}},
		{"geographic3d", "point3d", "POINT", []string{`{"srid":4979,"x":113.5,"y":22.3,"z":-0}`, `{"srid":4979,"x":181,"y":-90,"z":10}`}, []string{`{"srid":4979,"x":113.5,"y":22.3,"z":0}`, `{"srid":4979,"x":180,"y":90,"z":-10}`}},
	}
}

func nativeExtendedListSample(t *testing.T, kind string, items []string) (contract.GraphValue, []any) {
	t.Helper()
	v := contract.GraphValue{Kind: "list", Values: make([]contract.GraphValue, len(items))}
	params := make([]any, len(items))
	for i, text := range items {
		item, err := contract.ParseGraphPropertyExtended(kind, text)
		if err != nil {
			t.Fatal(err)
		}
		param, _, err := propertyExtended(item)
		if err != nil {
			t.Fatal(err)
		}
		v.Values[i], params[i] = item, param
	}
	return v, params
}

func TestNeo4jNativePropertyExtendedListsStorageTypesAndNativeComparison(t *testing.T) {
	f := fixtureTest(t)
	admin := nativeOpenTest(t, f)
	marker := markerTest(f, "extended_list_types")
	t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
	created := nativeRunTest(t, admin, Request{Query: "CREATE (a:NyaFoundation {marker:$marker})-[r:NYA_EXTENDED_LIST]->(b:NyaFoundation {marker:$marker}) RETURN a,r", Parameters: map[string]any{"marker": marker}})
	for _, target := range []struct{ pattern, id string }{{"(e)", created.Rows[0][0].Node.ElementID}, {"()-[e]->()", created.Rows[0][1].Relationship.ElementID}} {
		for _, sample := range extendedListSamples() {
			want, params := nativeExtendedListSample(t, sample.kind, sample.first)
			r := nativeRunTest(t, admin, Request{Query: "MATCH " + target.pattern + " WHERE elementId(e)=$id SET e[$key]=$value RETURN valueType($value),valueType(e[$key]),e[$key],e[$key]=$value", Parameters: map[string]any{"id": target.id, "key": sample.name, "value": params}})
			typeName := "LIST<" + sample.nativeType + " NOT NULL> NOT NULL"
			if len(r.Rows) != 1 || r.Rows[0][0].Text != typeName || r.Rows[0][1].Text != typeName || !reflect.DeepEqual(r.Rows[0][2], want) || r.Rows[0][3].Text != "true" {
				t.Fatal("native extended list storage changed type/components", sample.name, r.Rows)
			}
			raw, _ := json.Marshal(r.Rows[0])
			t.Log(target.pattern, sample.name, string(raw))
		}
		_, points2d := nativeExtendedListSample(t, "point2d", []string{`{"srid":7203,"x":1,"y":2}`})
		_, geographic := nativeExtendedListSample(t, "point2d", []string{`{"srid":4326,"x":1,"y":2}`})
		_, points3d := nativeExtendedListSample(t, "point3d", []string{`{"srid":9157,"x":1,"y":2,"z":0}`})
		_, dates := nativeExtendedListSample(t, "date", []string{"2026-11-10"})
		for _, invalid := range []struct {
			name string
			list []any
		}{{"mixed_crs", []any{points2d[0], geographic[0]}}, {"mixed_dimension", []any{points2d[0], points3d[0]}}, {"mixed_temporal", []any{points2d[0], dates[0]}}, {"null_member", []any{dates[0], nil}}, {"nested", []any{dates}}} {
			client := nativeOpenTest(t, f)
			plan := prepareTest(t, client, Request{Query: "MATCH " + target.pattern + " WHERE elementId(e)=$id SET e[$key]=$value RETURN e[$key]", Parameters: map[string]any{"id": target.id, "key": invalid.name, "value": invalid.list}})
			r, err := client.Execute(context.Background(), plan)
			if err == nil {
				t.Fatal("server admitted unsupported extended list", invalid.name)
			}
			t.Log("native rejection", invalid.name, r.FailureCode)
			read := nativeRunTest(t, admin, Request{Query: "MATCH " + target.pattern + " WHERE elementId(e)=$id RETURN e[$key]", Mode: "read", Parameters: map[string]any{"id": target.id, "key": invalid.name}})
			if read.Rows[0][0].Kind != "null" {
				t.Fatal("rejected extended list retained a property", invalid.name)
			}
		}
	}
}
