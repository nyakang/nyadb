package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func entityReceiptTest(t *testing.T, result *contract.DataEditResult) dataWriteReceipt {
	t.Helper()
	var receipt dataWriteReceipt
	if result == nil || json.Unmarshal([]byte(result.NativeJSON), &receipt) != nil {
		t.Fatal("missing original entity transaction receipt")
	}
	return receipt
}

func TestNeo4jNativeDataEntityCreationDeletionAndDetachEvidence(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	label, rel := "NyaEntity_"+f.Nonce+"_猫`", "NyaEntityEdge_"+f.Nonce+"_猫`"
	cleanupLabelTest(t, admin, label)
	ctx := context.Background()
	l := dataLeaseTest(t, s, "label", label)
	values := map[string]contract.DataValue{
		"property.empty": {Kind: contract.Text, Text: ""}, "property.missing": {Null: true},
		"property.int":   {Kind: contract.Integer, Text: "9223372036854775807"},
		"property.bytes": {Kind: contract.Binary, Binary: true, Bytes: []byte{0, 255}},
		"property.list":  {Kind: contract.JSON, Text: `{"Kind":"list","Values":[{"Kind":"integer","Text":"-9223372036854775808"}]}`},
		"property.date":  graphDataValueTest(t, extendedPropertyTest(t, "date", "10000-02-29")),
		"property.point": graphDataValueTest(t, extendedPropertyTest(t, "point3d", `{"srid":4979,"x":113.5,"y":22.3,"z":-0}`)),
	}
	values["property.float"] = contract.DataValue{Kind: contract.Floating, Text: "-0"}
	values["property.boolean"] = contract.DataValue{Kind: contract.Boolean, Text: "false"}
	for _, sample := range []struct{ kind, text string }{
		{"local_time", "23:59:59.999999999"},
		{"local_datetime", "-0001-02-28T23:59:59.999999999"},
		{"time", `{"value":"23:59:59.999999999-00:20","offset_seconds":-1234}`},
		{"datetime", `{"value":"1890-01-01T00:00:00+00:09","offset_seconds":561,"zone":"Europe/Paris"}`},
		{"duration", `{"months":-2,"days":3,"seconds":0,"nanoseconds":-1}`},
		{"point2d", `{"srid":4326,"x":181,"y":-90}`},
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
	created, err := l.saveEntityEdits(ctx, 42, "neo4j", label, []contract.DataEdit{{Kind: "insert", Values: values}, {Kind: "insert"}})
	if err != nil || !created.CommitConfirmed || created.Applied != 2 {
		t.Fatal("native typed and propertyless node creation failed", err)
	}
	receipt := entityReceiptTest(t, created)
	if len(receipt.Statements) != 2 || !receipt.CommitConfirmed || len(receipt.Bookmarks) == 0 {
		t.Fatal("node creation lost actual COMMIT evidence")
	}
	for index, outcome := range created.Outcomes {
		row, err := l.editRow(ctx, 42, "neo4j", label, outcome.Saved.Ref)
		if err != nil || !reflect.DeepEqual(row.Snapshot, outcome.Saved) {
			t.Fatal("created identity did not retain complete signed native snapshot", err)
		}
		var entity Value
		if json.Unmarshal([]byte(row.Values["node"].Text), &entity) != nil || len(entity.Node.Labels) != 1 || entity.Node.Labels[0] != label {
			t.Fatal("created node lost exact qualified label")
		}
		if index == 0 {
			if len(entity.Node.Properties) != len(values)-1 {
				t.Fatal("native creation lost properties or stored NULL")
			}
			for field, input := range values {
				if input.Null {
					continue
				}
				want, _, err := dataPropertyValue(input)
				if err != nil || !reflect.DeepEqual(propertyTest(t, entity.Node.Properties, strings.TrimPrefix(field, graphPropertyPrefix)), want) {
					t.Fatal("native creation lost exact scalar/list/temporal/spatial kind", field, err)
				}
			}
		} else if len(entity.Node.Properties) != 0 {
			t.Fatal("empty creation invented a lock/identity property")
		}
	}
	id := created.Outcomes[0].Saved.Ref.TypedValues[0].Text
	other := created.Outcomes[1].Saved.Ref.TypedValues[0].Text
	dropped, err := l.saveEntityEdits(ctx, 42, "neo4j", label, []contract.DataEdit{{Kind: "delete", Original: created.Outcomes[1].Saved}})
	if err != nil || !dropped.CommitConfirmed || dropped.Outcomes[0].Saved != nil || entityReceiptTest(t, dropped).Statements[1].Result.Summary.Counters["nodes_deleted"] != 1 {
		t.Fatal("propertyless node deletion lacks native confirmation", err)
	}
	props := map[string]any{graphEntityLockKey: false}
	for field, value := range values {
		if !value.Null {
			props[strings.TrimPrefix(field, graphPropertyPrefix)] = mustEntityParameterTest(t, value)
		}
	}
	nativeRunTest(t, admin, Request{Query: "MATCH (a) WHERE elementId(a)=$id CREATE (b:$($label)) CREATE (a)-[r:$($rel)]->(b),(b)-[s:$($rel)]->(a),(a)-[loop:$($rel)]->(a) SET r=$properties,s=$properties,loop=$properties RETURN r,s,loop,b", Parameters: map[string]any{"id": id, "label": label, "rel": rel, "properties": props}})
	edgeLease := dataLeaseTest(t, s, "relationship", rel)
	edges, err := edgeLease.page(ctx, 42, "neo4j", rel, contract.DataOptions{Limit: 10})
	if err != nil || len(edges.Rows) != 3 {
		t.Fatal("native graph fixture lost directed/self-loop edges", err)
	}
	// Relationship deletion is independent of both endpoint node documents.
	edgeID := edges.RowSnapshots[0].Ref.TypedValues[0].Text
	edgeDeleted, err := edgeLease.saveEntityEdits(ctx, 42, "neo4j", rel, []contract.DataEdit{{Kind: "delete", Original: edges.RowSnapshots[0]}})
	if err != nil || !edgeDeleted.CommitConfirmed || entityReceiptTest(t, edgeDeleted).Statements[1].Result.Summary.Counters["relationships_deleted"] != 1 {
		t.Fatal("native relationship deletion failed", err)
	}
	remaining := nativeRunTest(t, admin, Request{Query: "MATCH (a) WHERE elementId(a)=$id MATCH (a)-[r]-() RETURN DISTINCT elementId(r),properties(r) ORDER BY elementId(r)", Parameters: map[string]any{"id": id}})
	if len(remaining.Rows) != 2 {
		t.Fatal("relationship deletion modified unrelated edges")
	}
	// A plain delete rejects a connected node. It must not implicitly detach.
	plain := dataLeaseTest(t, s, "label", label)
	plainSnapshot := entitySnapshotTest(t, plain, id)
	failed, err := plain.saveEntityEdits(ctx, 42, "neo4j", label, []contract.DataEdit{{Kind: "delete", Original: plainSnapshot}})
	if err == nil || failed == nil || failed.CommitConfirmed || !failed.CommitUncertain || entityReceiptTest(t, failed).RollbackConfirmed {
		t.Fatal("connected plain DELETE supplied false deletion/rollback acknowledgement", err)
	}
	detach := dataLeaseTest(t, s, "label", label)
	detachSnapshot := entitySnapshotTest(t, detach, id)
	deleted, err := detach.saveEntityEdits(ctx, 42, "neo4j", label, []contract.DataEdit{{Kind: "detach_delete", Original: detachSnapshot}})
	if err != nil || !deleted.CommitConfirmed {
		if deleted != nil {
			t.Log(deleted.NativeJSON)
		}
		t.Fatal("native detached deletion failed", err)
	}
	r := entityReceiptTest(t, deleted)
	result := r.Statements[1].Result
	if !result.Completed || result.Summary.Counters["nodes_deleted"] != 1 || result.Summary.Counters["relationships_deleted"] != 2 || len(result.Rows[0][1].Values) != 2 {
		t.Fatal("detached deletion lost affected relationship evidence")
	}
	for i, edge := range result.Rows[0][1].Values {
		if propertyTest(t, edge.Properties, "element_id").Text != remaining.Rows[i][0].Text || !reflect.DeepEqual(propertyTest(t, edge.Properties, "properties"), remaining.Rows[i][1]) {
			t.Fatal("deleted edge receipt lost original identity/typed properties")
		}
	}
	read := nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) RETURN count(n)", Parameters: map[string]any{"label": label}})
	if read.Rows[0][0].Text != "1" || other == id || edgeID == "" {
		t.Fatal("detach deletion changed an unselected endpoint or generated identity")
	}
	t.Log("Native typed node creation, propertyless/relationship deletion, connected plain DELETE rejection and bounded directed/self-loop DETACH evidence confirmed")
}

func mustEntityParameterTest(t *testing.T, value contract.DataValue) any {
	t.Helper()
	_, parameter, err := dataPropertyValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return parameter
}

func entitySnapshotTest(t *testing.T, l *dataLease, id string) *contract.RowSnapshot {
	t.Helper()
	page, err := l.page(context.Background(), 42, "neo4j", l.object.Name, contract.DataOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range page.RowSnapshots {
		if snapshot.Ref.TypedValues[0].Text == id {
			return snapshot
		}
	}
	t.Fatal("missing original entity snapshot")
	return nil
}

func TestNeo4jNativeDataEntityAtomicRollbackAndBoundedDetach(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	label := "NyaEntityRollback_" + f.Nonce
	cleanupLabelTest(t, admin, label)
	ctx := context.Background()
	nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}) RETURN a,b", Parameters: map[string]any{"label": label}})
	l := dataLeaseTest(t, s, "label", label)
	page, err := l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE elementId(n)=$id SET n.guard=77", Parameters: map[string]any{"id": page.RowSnapshots[1].Ref.TypedValues[0].Text}})
	rolled, err := l.saveEntityEdits(ctx, 42, "neo4j", label, []contract.DataEdit{
		{Kind: "insert", Values: map[string]contract.DataValue{"property.guard": {Kind: contract.Integer, Text: "99"}}},
		{Kind: "delete", Original: page.RowSnapshots[0]}, {Kind: "delete", Original: page.RowSnapshots[1]},
	})
	if !errors.Is(err, contract.ErrDataConflict) || rolled.CommitConfirmed || rolled.CommitUncertain || !entityReceiptTest(t, rolled).RollbackConfirmed {
		t.Fatal("later delete conflict did not atomically roll back earlier creation/deletion", err)
	}
	read := nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) RETURN elementId(n),n.guard ORDER BY elementId(n)", Parameters: map[string]any{"label": label}})
	if len(read.Rows) != 2 || read.Rows[0][0].Text != page.RowSnapshots[0].Ref.TypedValues[0].Text || read.Rows[1][1].Text != "77" {
		t.Fatal("rolled-back deletion recreated identity or retained inserted node")
	}
	// More than the explicit native edge limit must settle without any deletion.
	wide := "NyaEntityWide_" + f.Nonce
	cleanupLabelTest(t, admin, wide)
	nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label)),(b:$($label)) WITH a,b UNWIND range(1,$count) AS i CREATE (a)-[:NYA_ENTITY_WIDE]->(b)", Parameters: map[string]any{"label": wide, "count": int64(graphDetachMaxRelationships + 1)}})
	w := dataLeaseTest(t, s, "label", wide)
	widePage, err := w.page(ctx, 42, "neo4j", wide, contract.DataOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := w.saveEntityEdits(ctx, 42, "neo4j", wide, []contract.DataEdit{{Kind: "detach_delete", Original: widePage.RowSnapshots[0]}})
	if !errors.Is(err, ErrBounds) || bounded.CommitConfirmed || bounded.CommitUncertain || !entityReceiptTest(t, bounded).RollbackConfirmed {
		t.Fatal("wide detach did not reject with actual rollback", err)
	}
	read = nativeRunTest(t, admin, Request{Query: "MATCH (a)-[r:NYA_ENTITY_WIDE]->(b) WHERE $label IN labels(a) RETURN count(r)", Parameters: map[string]any{"label": wide}})
	if read.Rows[0][0].Text != "2001" {
		t.Fatal("bounded detach deleted a partial adjacency")
	}
	// Capture overflow after a provisional DELETE also requires full rollback.
	large := "NyaEntityLarge_" + f.Nonce
	cleanupLabelTest(t, admin, large)
	nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label)),(b:$($label)) CREATE (a)-[:NYA_ENTITY_LARGE {text:left(reduce(s='x',i IN range(1,22)|s+s),2097252)}]->(b)", Parameters: map[string]any{"label": large}})
	big := dataLeaseTest(t, s, "label", large)
	bigPage, err := big.page(ctx, 42, "neo4j", large, contract.DataOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	tooLarge, err := big.saveEntityEdits(ctx, 42, "neo4j", large, []contract.DataEdit{{Kind: "detach_delete", Original: bigPage.RowSnapshots[0]}})
	if !errors.Is(err, ErrBounds) || tooLarge.CommitConfirmed || tooLarge.CommitUncertain || !entityReceiptTest(t, tooLarge).RollbackConfirmed {
		t.Fatal("large deleted-edge capture overflow did not preserve rollback", err)
	}
	read = nativeRunTest(t, admin, Request{Query: "MATCH (a)-[r:NYA_ENTITY_LARGE]->(b) WHERE $label IN labels(a) RETURN count(r)", Parameters: map[string]any{"label": large}})
	if read.Rows[0][0].Text != "1" {
		t.Fatal("failed receipt capture retained a provisional deletion")
	}
}
