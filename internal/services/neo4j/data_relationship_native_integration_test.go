package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func relationshipEndpointsTest(t *testing.T, l *dataLease, source, target string) *contract.DataGraphEndpoints {
	t.Helper()
	e, err := l.readGraphEndpoints(context.Background(), 42, "neo4j", l.object.Name, source, target)
	if err != nil {
		t.Fatal("original directed endpoint observation failed", err)
	}
	return e
}

func TestNeo4jNativeDataRelationshipCreationTypedDirectedParallelAndSelfLoop(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	label, rel := "NyaRelationshipNodes_"+f.Nonce+"_猫`", "NyaRelationshipType_"+f.Nonce+"_猫`"
	cleanupLabelTest(t, admin, label)
	seed := nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {exact:$exact,bytes:$bytes}),(b:$($label) {exact:2}) RETURN a,b", Parameters: map[string]any{"label": label, "exact": int64(9223372036854775807), "bytes": []byte{0, 255}}})
	first, second := seed.Rows[0][0].Node.ElementID, seed.Rows[0][1].Node.ElementID
	l := dataLeaseTest(t, s, "relationship", rel)
	e := relationshipEndpointsTest(t, l, first, second)
	if !reflect.DeepEqual(e.Source, *seed.Rows[0][0].Node) || !reflect.DeepEqual(e.Target, *seed.Rows[0][1].Node) || e.NativeJSON == "" || l.validateGraphEndpoints(e) != nil {
		t.Fatal("endpoint reader lost complete original typed nodes")
	}
	for _, scope := range [][3]string{{"41", "neo4j", rel}, {"42", "system", rel}, {"42", "neo4j", rel + "_other"}} {
		id := int64(42)
		if scope[0] == "41" {
			id = 41
		}
		if _, err := l.readGraphEndpoints(context.Background(), id, scope[1], scope[2], first, second); err == nil {
			t.Fatal("endpoint read crossed original profile/database/type")
		}
	}
	foreign := dataLeaseTest(t, s, "relationship", rel)
	if _, err := foreign.prepareEntityEdits([]contract.DataEdit{{Kind: "insert", GraphEndpoints: e}}); err == nil {
		t.Fatal("endpoint observation authorized another document")
	}
	values := map[string]contract.DataValue{
		"property.empty": {Kind: contract.Text, Text: ""}, "property.absent": {Null: true},
		"property.exact": {Kind: contract.Integer, Text: "-9223372036854775808"},
		"property.bytes": {Kind: contract.Binary, Bytes: []byte{0, 255}},
		"property.float": {Kind: contract.Floating, Text: "-0"},
		"property.flag":  {Kind: contract.Boolean, Text: "false"},
	}
	for _, sample := range []struct{ kind, text string }{
		{"date", "10000-02-29"}, {"local_time", "23:59:59.999999999"},
		{"local_datetime", "-0001-02-28T23:59:59.999999999"},
		{"time", `{"value":"23:59:59.999999999-00:20","offset_seconds":-1234}`},
		{"datetime", `{"value":"1890-01-01T00:00:00+00:09","offset_seconds":561,"zone":"Europe/Paris"}`},
		{"duration", `{"months":-2,"days":3,"seconds":0,"nanoseconds":-1}`},
		{"point2d", `{"srid":4326,"x":181,"y":-90}`},
		{"point3d", `{"srid":4979,"x":113.5,"y":22.3,"z":-0}`},
	} {
		values[graphPropertyPrefix+sample.kind] = graphDataValueTest(t, extendedPropertyTest(t, sample.kind, sample.text))
	}
	for kind, text := range map[string]string{"integer": `[-9223372036854775808,9223372036854775807]`, "string": `["","猫"]`, "boolean": `[false,true]`, "float": `[-0,1.5]`} {
		list, err := contract.ParseGraphPropertyList(kind, text)
		if err != nil {
			t.Fatal(err)
		}
		values[graphPropertyPrefix+"list_"+kind] = graphDataValueTest(t, list)
	}
	edits := []contract.DataEdit{
		{Kind: "insert", GraphEndpoints: e, Values: values},
		{Kind: "insert", GraphEndpoints: relationshipEndpointsTest(t, l, second, first)},
		{Kind: "insert", GraphEndpoints: e},
		{Kind: "insert", GraphEndpoints: relationshipEndpointsTest(t, l, first, first)},
	}
	plan, err := l.previewEntityEdits(context.Background(), 42, "neo4j", rel, edits)
	if err != nil {
		t.Fatal("native directed creation preview failed", err)
	}
	defer plan.Close()
	snapshot := plan.Snapshot()
	before, _ := json.Marshal(snapshot)
	e.Source.Labels[0] = "later caller label"
	values["property.bytes"].Bytes[0] = 9
	copy := plan.Snapshot()
	copy.Edits[0].GraphEndpoints.Target.Labels[0] = "later UI label"
	after, _ := json.Marshal(plan.Snapshot())
	if string(before) != string(after) {
		t.Fatal("preview adopted later caller/UI endpoint changes")
	}
	result, err := l.submitEntityPlan(context.Background(), 42, "neo4j", rel, plan)
	if err != nil || result == nil || !result.CommitConfirmed || result.Applied != 4 || len(result.Outcomes) != 4 {
		t.Fatal("native directed relationship batch not confirmed", err)
	}
	receipt := entityReceiptTest(t, result)
	var created, locked int64
	var lockedIDs []string
	for _, statement := range receipt.Statements {
		created += statement.Result.Summary.Counters["relationships_created"]
		if strings.Contains(statement.Query, "SET entity[$lock_key]") {
			locked++
			lockedIDs = append(lockedIDs, propertyTest(t, statement.Parameters, "element_id").Text)
		}
	}
	wantIDs := []string{first, second}
	slices.Sort(wantIDs)
	if !slices.Equal(lockedIDs, wantIDs) {
		t.Fatal("endpoint locks depended on relationship direction or map order")
	}
	if created != 4 || locked != 2 || !receipt.CommitConfirmed || len(receipt.Bookmarks) == 0 {
		t.Fatal("batch omitted unique endpoint locks or native creation/commit counts", created, locked)
	}
	for i, outcome := range result.Outcomes {
		if outcome.Saved == nil || outcome.Kind != "insert" {
			t.Fatal("confirmed relationship lost generated identity")
		}
		row, err := l.editRow(context.Background(), 42, "neo4j", rel, outcome.Saved.Ref)
		if err != nil {
			t.Fatal("generated relationship identity cannot be edited", err)
		}
		var entity Value
		if json.Unmarshal([]byte(row.Values["relationship"].Text), &entity) != nil || entity.Relationship == nil {
			t.Fatal("native relationship readback missing")
		}
		want := snapshot.Edits[i].GraphEndpoints
		if entity.Relationship.StartElementID != want.Source.ElementID || entity.Relationship.EndElementID != want.Target.ElementID || entity.Relationship.Type != rel {
			t.Fatal("source/target direction or original type changed")
		}
		if i == 0 {
			for field, value := range snapshot.Edits[0].Values {
				if value.Null {
					continue
				}
				want, _, err := dataPropertyValue(value)
				if err != nil || !reflect.DeepEqual(propertyTest(t, entity.Relationship.Properties, strings.TrimPrefix(field, graphPropertyPrefix)), want) {
					t.Fatal("relationship lost exact stored property kind", field, err)
				}
			}
			if len(entity.Relationship.Properties) != len(snapshot.Edits[0].Values)-1 {
				t.Fatal("absent property stored as NULL")
			}
		} else if len(entity.Relationship.Properties) != 0 {
			t.Fatal("propertyless relationship invented attributes")
		}
	}
	read := nativeRunTest(t, admin, Request{Query: "MATCH ()-[r]->() WHERE type(r)=$rel RETURN count(r)", Parameters: map[string]any{"rel": rel}})
	if read.Rows[0][0].Text != "4" {
		t.Fatal("parallel/reverse/self relationships merged or replayed")
	}
	if _, err := l.submitEntityPlan(context.Background(), 42, "neo4j", rel, plan); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("consumed relationship preview replayed", err)
	}
}

func TestNeo4jNativeDataRelationshipEndpointChangesBoundsAndAtomicRollback(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	ctx := context.Background()
	for _, change := range []string{"source_property", "source_label", "target_property", "target_delete"} {
		t.Run(change, func(t *testing.T) {
			label, rel := "NyaEndpointConflict_"+f.Nonce+"_"+change, "NyaEndpointType_"+f.Nonce+"_"+change
			cleanupLabelTest(t, admin, label)
			seed := nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}) RETURN a,b", Parameters: map[string]any{"label": label}})
			first, second := seed.Rows[0][0].Node.ElementID, seed.Rows[0][1].Node.ElementID
			l := dataLeaseTest(t, s, "relationship", rel)
			e := relationshipEndpointsTest(t, l, first, second)
			plan, err := l.previewEntityEdits(ctx, 42, "neo4j", rel, []contract.DataEdit{{Kind: "insert", GraphEndpoints: e}})
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Close()
			query := "MATCH (n) WHERE elementId(n)=$source SET n.guard=999"
			if change == "source_label" {
				query = "MATCH (n) WHERE elementId(n)=$source SET n:NYA_CHANGED_ENDPOINT_LABEL"
			}
			if change == "target_property" {
				query = "MATCH (n) WHERE elementId(n)=$target SET n.guard=999"
			}
			if change == "target_delete" {
				query = "MATCH (n) WHERE elementId(n)=$target DELETE n"
			}
			nativeRunTest(t, admin, Request{Query: query, Parameters: map[string]any{"source": first, "target": second}})
			result, err := l.submitEntityPlan(ctx, 42, "neo4j", rel, plan)
			if !errors.Is(err, contract.ErrDataConflict) || result == nil || result.CommitUncertain || !entityReceiptTest(t, result).RollbackConfirmed {
				t.Fatal("changed/missing endpoint did not reject and roll back original transaction", err)
			}
			read := nativeRunTest(t, admin, Request{Query: "MATCH ()-[r]->() WHERE type(r)=$rel RETURN count(r)", Parameters: map[string]any{"rel": rel}})
			if read.Rows[0][0].Text != "0" {
				t.Fatal("changed endpoint admitted relationship creation")
			}
			if _, err := l.previewEntityEdits(ctx, 42, "neo4j", rel, []contract.DataEdit{{Kind: "insert", GraphEndpoints: e}}); !errors.Is(err, contract.ErrDataConflict) {
				t.Fatal("stale endpoint reader evidence silently refreshed in preview", err)
			}
		})
	}
	label, rel := "NyaRelationshipAtomic_"+f.Nonce, "NyaRelationshipAtomicType_"+f.Nonce
	cleanupLabelTest(t, admin, label)
	seed := nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}) CREATE (a)-[r:$($rel) {guard:1}]->(b) RETURN a,b,r", Parameters: map[string]any{"label": label, "rel": rel}})
	l := dataLeaseTest(t, s, "relationship", rel)
	first, second, old := seed.Rows[0][0].Node.ElementID, seed.Rows[0][1].Node.ElementID, seed.Rows[0][2].Relationship.ElementID
	edits := []contract.DataEdit{{Kind: "insert", GraphEndpoints: relationshipEndpointsTest(t, l, first, second), Values: map[string]contract.DataValue{"property.guard": {Kind: contract.Integer, Text: "111"}}}, {Kind: "update", Original: entitySnapshotTest(t, l, old), Values: map[string]contract.DataValue{"property.guard": {Kind: contract.Integer, Text: "222"}}}}
	plan, err := l.previewEntityEdits(ctx, 42, "neo4j", rel, edits)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	nativeRunTest(t, admin, Request{Query: "MATCH ()-[r]->() WHERE elementId(r)=$id SET r.guard=-999", Parameters: map[string]any{"id": old}})
	result, err := l.submitEntityPlan(ctx, 42, "neo4j", rel, plan)
	if !errors.Is(err, contract.ErrDataConflict) || result == nil || result.CommitUncertain || !entityReceiptTest(t, result).RollbackConfirmed {
		t.Fatal("later row conflict failed to roll back provisional creation", err)
	}
	var created int64
	for _, statement := range entityReceiptTest(t, result).Statements {
		created += statement.Result.Summary.Counters["relationships_created"]
	}
	if created != 1 {
		t.Fatal("rollback test did not provisionally create the preceding relationship")
	}
	read := nativeRunTest(t, admin, Request{Query: "MATCH ()-[r]->() WHERE type(r)=$rel RETURN r.guard", Parameters: map[string]any{"rel": rel}})
	if len(read.Rows) != 1 || read.Rows[0][0].Text != "-999" {
		t.Fatal("atomic rollback left provisional edge or overwrote later row")
	}
	// Build the oversized stored value in Neo4j so the fixture request itself
	// stays below the native parameter budget and reaches the endpoint reader.
	nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE elementId(n)=$id SET n.large=reduce(value=$chunk, i IN range(1,10) | value+value)", Parameters: map[string]any{"id": first, "chunk": strings.Repeat("x", contract.DataEditMaxBytes>>10)}})
	if _, err := l.readGraphEndpoints(ctx, 42, "neo4j", rel, first, second); !errors.Is(err, ErrBounds) {
		t.Fatal("complete endpoint observation returned partial/oversized request evidence", err)
	}
}
