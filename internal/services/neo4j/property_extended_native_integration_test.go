package neo4j

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNeo4jNativePropertyExtendedStorageTypesAndNativeComparison(t *testing.T) {
	f := fixtureTest(t)
	c := nativeOpenTest(t, f)
	marker := markerTest(f, "extended_type")
	t.Cleanup(func() { cleanupMarkerTest(t, c, marker) })
	zone, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 7, 3, 1, 2, 3, 123456789, zone)
	created := nativeRunTest(t, c, Request{Query: "CREATE (a:NyaFoundation {marker:$marker})-[r:NYA_EXTENDED]->(b:NyaFoundation {marker:$marker}) RETURN a,r", Parameters: map[string]any{"marker": marker}})
	for _, target := range []struct{ pattern, id string }{{"(e)", created.Rows[0][0].Node.ElementID}, {"()-[e]->()", created.Rows[0][1].Relationship.ElementID}} {
		for _, sample := range []struct {
			name, kind, nativeType string
			value                  any
		}{
			{"date", "date", "DATE NOT NULL", nd.Date(stamp)},
			{"date_negative", "date", "DATE NOT NULL", nd.Date(time.Date(-1, 2, 28, 0, 0, 0, 0, time.UTC))},
			{"date_extended", "date", "DATE NOT NULL", nd.Date(time.Date(10000, 2, 29, 0, 0, 0, 0, time.UTC))},
			{"date_max", "date", "DATE NOT NULL", nd.Date(time.Date(999999999, 12, 31, 0, 0, 0, 0, time.UTC))},
			{"date_min", "date", "DATE NOT NULL", nd.Date(time.Date(-999999999, 1, 1, 0, 0, 0, 0, time.UTC))},
			{"local_datetime", "local_datetime", "LOCAL DATETIME NOT NULL", nd.LocalDateTime(stamp)},
			{"local_time", "local_time", "LOCAL TIME NOT NULL", nd.LocalTime(stamp)},
			{"time", "time", "ZONED TIME NOT NULL", nd.Time(stamp)},
			{"time_seconds_offset", "time", "ZONED TIME NOT NULL", nd.Time(time.Date(0, 1, 1, 23, 59, 59, 999999999, time.FixedZone("Offset", 1234)))},
			{"datetime", "datetime", "ZONED DATETIME NOT NULL", stamp},
			{"datetime_seconds_offset", "datetime", "ZONED DATETIME NOT NULL", time.Date(2026, 1, 2, 23, 59, 59, 999999999, time.FixedZone("Offset", -1234))},
			{"duration", "duration", "DURATION NOT NULL", nd.Duration{Months: -2, Days: 3, Seconds: 4, Nanos: 123456789}},
			{"duration_negative_nanos", "duration", "DURATION NOT NULL", nd.Duration{Seconds: 0, Nanos: -1}},
			{"duration_carry_nanos", "duration", "DURATION NOT NULL", nd.Duration{Seconds: 1, Nanos: 1000000001}},
			{"point2d", "point2d", "POINT NOT NULL", nd.Point2D{SpatialRefId: 7203, X: math.Copysign(0, -1), Y: 1.5}},
			{"point3d", "point3d", "POINT NOT NULL", nd.Point3D{SpatialRefId: 9157, X: 1.5, Y: 2.5, Z: -3.5}},
			{"geographic2d", "point2d", "POINT NOT NULL", nd.Point2D{SpatialRefId: 4326, X: 180, Y: -90}},
			{"geographic3d", "point3d", "POINT NOT NULL", nd.Point3D{SpatialRefId: 4979, X: 113.5, Y: 22.3, Z: 10.5}},
			{"geographic_wrap", "point2d", "POINT NOT NULL", nd.Point2D{SpatialRefId: 4326, X: 181, Y: 2}},
		} {
			r := nativeRunTest(t, c, Request{Query: "MATCH " + target.pattern + " WHERE elementId(e)=$id SET e[$key]=$value RETURN valueType($value),valueType(e[$key]),e[$key],e[$key]=$value", Parameters: map[string]any{"id": target.id, "key": sample.name, "value": sample.value}})
			if len(r.Rows) != 1 || r.Rows[0][0].Text != sample.nativeType || r.Rows[0][1].Text != sample.nativeType || r.Rows[0][2].Kind != sample.kind || r.Rows[0][3].Text != "true" {
				t.Fatal("native temporal/spatial storage type proof failed", sample.name)
			}
			wantNative := sample.value
			switch sample.name {
			case "duration_negative_nanos":
				wantNative = nd.Duration{Seconds: -1, Nanos: 999999999}
			case "duration_carry_nanos":
				wantNative = nd.Duration{Seconds: 2, Nanos: 1}
			case "geographic_wrap":
				wantNative = nd.Point2D{SpatialRefId: 4326, X: -179, Y: 2}
			}
			b := valueBudget{limit: 1 << 20}
			want, _, err := b.capture(wantNative, true, 0)
			if err != nil || !reflect.DeepEqual(want, r.Rows[0][2]) {
				t.Fatal("native storage changed precise temporal/spatial components", sample.name)
			}
			raw, _ := json.Marshal(r.Rows[0][2])
			t.Log(target.pattern, sample.name, string(raw))
		}
	}
	for index, query := range []string{
		"RETURN duration('P1D')=duration('PT24H'),duration('P1M')=duration('P30D')",
		"RETURN datetime('2026-01-01T00:00:00Z')=datetime('2026-01-01T00:00:00[Europe/London]'),datetime('2026-07-01T00:00:00+02:00')=datetime('2026-07-01T00:00:00[Europe/Stockholm]')",
		"RETURN point({x:1.0,y:2.0,crs:'cartesian'})=point({longitude:1.0,latitude:2.0}),point({x:1.0,y:2.0,crs:'cartesian'})=point({x:1.0,y:2.0,z:0.0,crs:'cartesian-3d'})",
		"RETURN datetime('2026-07-01T00:00:00+02:00').timezone,datetime('2026-07-01T00:00:00[Europe/Stockholm]').timezone,$offset_time.offsetSeconds",
	} {
		r := nativeRunTest(t, c, Request{Query: query, Mode: "read", Parameters: map[string]any{"offset_time": nd.Time(time.Date(0, 1, 1, 0, 0, 0, 0, time.FixedZone("Offset", 1234)))}})
		if index < 3 {
			for _, value := range r.Rows[0] {
				if value.Kind != "boolean" || value.Text != "false" {
					t.Fatal("native comparison discarded component identity", index)
				}
			}
		} else if r.Rows[0][0].Text != "+02:00" || r.Rows[0][1].Text != "Europe/Stockholm" || r.Rows[0][2].Text != "1234" {
			t.Fatal("native timezone/offset metadata changed")
		}
		raw, _ := json.Marshal(r.Rows)
		t.Log("comparison", query, string(raw))
	}
	for _, sample := range []struct {
		name  string
		value any
	}{
		{"invalid_date_year", nd.Date(time.Date(1000000000, 1, 1, 0, 0, 0, 0, time.UTC))},
		{"invalid_time_offset", nd.Time(time.Date(0, 1, 1, 0, 0, 0, 0, time.FixedZone("Offset", 64801)))},
		{"invalid_duration_months", nd.Duration{Months: math.MaxInt64}},
		{"invalid_duration_days", nd.Duration{Days: math.MaxInt64}},
		{"invalid_spatial_ref", nd.Point2D{SpatialRefId: 999, X: 1, Y: 2}},
		{"invalid_spatial_dimension", nd.Point2D{SpatialRefId: 4979, X: 1, Y: 2}},
		{"invalid_latitude", nd.Point2D{SpatialRefId: 4326, X: 1, Y: 91}},
	} {
		client := nativeOpenTest(t, f)
		plan := prepareTest(t, client, Request{Query: "MATCH (e) WHERE elementId(e)=$id SET e[$key]=$value RETURN e[$key]", Parameters: map[string]any{"id": created.Rows[0][0].Node.ElementID, "key": sample.name, "value": sample.value}})
		r, err := client.Execute(context.Background(), plan)
		if err == nil {
			t.Fatal("server admitted out-of-range native property", sample.name)
		}
		t.Log("native rejection", sample.name, r.FailureCode)
		read := nativeRunTest(t, c, Request{Query: "MATCH (e) WHERE elementId(e)=$id RETURN e[$key]", Mode: "read", Parameters: map[string]any{"id": created.Rows[0][0].Node.ElementID, "key": sample.name}})
		if read.Rows[0][0].Kind != "null" {
			t.Fatal("rejected native value retained an invalid property", sample.name)
		}
	}
	for _, sample := range []struct {
		name  string
		value nd.Duration
	}{
		{"seconds_max", nd.Duration{Seconds: math.MaxInt64, Nanos: 999999999}},
		{"months_product_canceled", nd.Duration{Months: math.MaxInt64/2629746 + 1, Days: -31}},
		{"days_product_canceled", nd.Duration{Days: math.MaxInt64/86400 + 1, Seconds: -86400}},
		{"intermediate_sum_canceled", nd.Duration{Months: math.MaxInt64 / 2629746, Days: 31, Seconds: -31 * 86400}},
		{"months_product_limit", nd.Duration{Months: math.MaxInt64 / 2629746}},
		{"sum_overflow", nd.Duration{Months: math.MaxInt64 / 2629746, Seconds: 2629746}},
	} {
		client := nativeOpenTest(t, f)
		plan := prepareTest(t, client, Request{Query: "MATCH (e) WHERE elementId(e)=$id SET e[$key]=$value RETURN e[$key]", Parameters: map[string]any{"id": created.Rows[0][0].Node.ElementID, "key": sample.name, "value": sample.value}})
		r, err := client.Execute(context.Background(), plan)
		wantAccepted := sample.name == "seconds_max" || sample.name == "months_product_limit"
		if (err == nil) != wantAccepted {
			t.Fatal("native duration overflow boundary changed", sample.name, err)
		}
		b := valueBudget{limit: 1 << 20}
		v, _, captureErr := b.capture(sample.value, true, 0)
		_, _, validationErr := propertyValue(v)
		if captureErr != nil || (validationErr == nil) != wantAccepted {
			t.Fatal("preview duration bounds disagree with native acceptance", sample.name)
		}
		t.Log("duration boundary", sample.name, "accepted", err == nil, r.FailureCode)
	}
}

func TestNeo4jNativePropertyExtendedTypedLocksReceiptsAndComponents(t *testing.T) {
	testNativeExtendedPropertyLocks(t, false)
}

func TestNeo4jNativePropertyExtendedListsTypedLocksReceiptsAndComponents(t *testing.T) {
	testNativeExtendedPropertyLocks(t, true)
}

func testNativeExtendedPropertyLocks(t *testing.T, lists bool) {
	f := sessionFixtureTest(t)
	admin := nativeOpenTest(t, f)
	s := nativeSessionTest(t, f)
	lease := nativeGraphLeaseTest(t, s, 31).(contract.GraphPropertyWriteLease)
	marker := markerTest(f, "extended_write")
	t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
	created := nativeRunTest(t, admin, Request{Query: "CREATE (a:NyaFoundation {marker:$marker,guard:'unchanged'})-[r:NYA_EXTENDED_WRITE {guard:'unchanged'}]->(b:NyaFoundation {marker:$marker}) RETURN a,r", Parameters: map[string]any{"marker": marker}})
	n, e := created.Rows[0][0].Node, created.Rows[0][1].Relationship
	for _, target := range []contract.GraphPropertyTarget{{Kind: "node", ElementID: n.ElementID, Labels: n.Labels}, {Kind: "relationship", ElementID: e.ElementID, SourceID: e.StartElementID, TargetID: e.EndElementID, RelationshipType: e.Type}} {
		pattern := "(e)"
		if target.Kind == "relationship" {
			pattern = "()-[e]->()"
		}
		for _, sample := range []struct{ kind, first, changed, after string }{
			{"date", "10000-02-29", "-0001-02-28", "2028-02-29"},
			{"local_time", "23:59:59.999999999", "01:02:03.000000001", "00:00:00"},
			{"local_datetime", "10000-02-29T23:59:59.999999999", "2026-01-01T01:02:03.000000001", "-0001-02-28T00:00:00"},
			{"time", `{"value":"01:02:03.000000001+00:20","offset_seconds":1234}`, `{"value":"01:02:03.000000001+00:20","offset_seconds":1235}`, `{"value":"23:59:59.999999999-00:20","offset_seconds":-1234}`},
			{"datetime", `{"value":"2026-07-03T01:02:03.123456789+02:00","zone":"Europe/Stockholm","offset_seconds":7200}`, `{"value":"2026-07-03T01:02:03.123456789+02:00","zone":"Offset","offset_seconds":7200}`, `{"value":"1890-01-01T00:00:00+00:09","zone":"Europe/Paris","offset_seconds":561}`},
			{"duration", `{"months":0,"days":1,"seconds":0,"nanoseconds":0}`, `{"months":0,"days":0,"seconds":86400,"nanoseconds":0}`, `{"months":-2,"days":3,"seconds":0,"nanoseconds":-1}`},
			{"point2d", `{"srid":7203,"x":1,"y":2}`, `{"srid":4326,"x":1,"y":2}`, `{"srid":4326,"x":181,"y":-90}`},
			{"point3d", `{"srid":9157,"x":1,"y":2,"z":3}`, `{"srid":4979,"x":1,"y":2,"z":3}`, `{"srid":4979,"x":113.5,"y":22.3,"z":-0}`},
		} {
			t.Run(target.Kind+"/"+sample.kind, func(t *testing.T) {
				first, changed, after := extendedPropertyTest(t, sample.kind, sample.first), extendedPropertyTest(t, sample.kind, sample.changed), extendedPropertyTest(t, sample.kind, sample.after)
				if lists {
					first = contract.GraphValue{Kind: "list", Values: []contract.GraphValue{first}}
					changed = contract.GraphValue{Kind: "list", Values: []contract.GraphValue{changed}}
					after = contract.GraphValue{Kind: "list", Values: []contract.GraphValue{after}}
				}
				r := contract.GraphPropertyWriteRequest{Namespace: s.Namespace("neo4j"), Target: target, Property: "native ` 猫 " + sample.kind, Before: contract.GraphValue{Kind: "null"}, After: first}
				receipt, err := lease.WriteGraphProperty(context.Background(), r)
				if err != nil || !receipt.Applied || !reflect.DeepEqual(receipt.Result.Rows[0][2], first) {
					t.Fatal("native extended creation lacks precise receipt", err)
				}
				duplicate, err := lease.WriteGraphProperty(context.Background(), r)
				if err != nil || !duplicate.Conflict || duplicate.Applied {
					t.Fatal("extended creation overwrote an existing property", err)
				}
				r.Before, r.After = first.Clone(), after.Clone()
				holder := nativeOpenTest(t, f).driver.NewSession(context.Background(), nd.SessionConfig{DatabaseName: "neo4j"})
				defer holder.Close(context.Background())
				tx, err := holder.BeginTransaction(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Close(context.Background())
				parameter, _, _ := propertyValue(changed)
				result, err := tx.Run(context.Background(), "MATCH "+pattern+" WHERE elementId(e)=$id SET e[$key]=$value", map[string]any{"id": target.ElementID, "key": r.Property, "value": parameter})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = result.Consume(context.Background()); err != nil {
					t.Fatal(err)
				}
				type outcome struct {
					receipt contract.GraphPropertyWriteReceipt
					err     error
				}
				done := make(chan outcome, 1)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				go func() { receipt, err := lease.WriteGraphProperty(ctx, r); done <- outcome{receipt, err} }()
				query, _ := propertyWriteQuery(r)
				blocked := false
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					show := nativeRunTest(t, admin, Request{Query: "SHOW TRANSACTIONS YIELD currentQuery,status WHERE currentQuery=$query RETURN status", Database: "system", Mode: "read", Parameters: map[string]any{"query": query.Query}})
					if len(show.Rows) > 0 && strings.Contains(show.Rows[0][0].Text, "Blocked") {
						blocked = true
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !blocked {
					cancel()
					<-done
					t.Fatal("extended old-component comparison did not wait for the native lock")
				}
				if err = tx.Commit(context.Background()); err != nil {
					t.Fatal(err)
				}
				completed := <-done
				if completed.err != nil || !completed.receipt.Conflict || completed.receipt.Applied || !completed.receipt.Result.Completed {
					t.Fatal("stale component comparison overwrote concurrent native value", completed.err)
				}
				r.Before = changed.Clone()
				receipt, err = lease.WriteGraphProperty(context.Background(), r)
				if err != nil || !receipt.Applied || !reflect.DeepEqual(receipt.Result.Rows[0][2], after) || !propertyReceiptEntity(receipt.Result.Rows[0][3], target, r.Property, after) {
					t.Fatal("fresh native extended edit lacks complete precise entity receipt", err)
				}
				r.Before, r.After = after.Clone(), contract.GraphValue{Kind: "null"}
				receipt, err = lease.WriteGraphProperty(context.Background(), r)
				if err != nil || !receipt.Applied || !propertyReceiptEntity(receipt.Result.Rows[0][3], target, r.Property, r.After) {
					t.Fatal("native extended removal lacks absence receipt", err)
				}
				read := nativeRunTest(t, admin, Request{Query: "MATCH " + pattern + " WHERE elementId(e)=$id RETURN e[$key],e.guard", Mode: "read", Parameters: map[string]any{"id": target.ElementID, "key": r.Property}})
				if read.Rows[0][0].Kind != "null" || read.Rows[0][1].Text != "unchanged" {
					t.Fatal("native extended writes changed unrelated properties")
				}
			})
		}
	}
}
