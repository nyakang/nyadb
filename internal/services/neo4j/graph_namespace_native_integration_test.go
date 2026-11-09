package neo4j

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
)

// The namespace verifier runs this parent first in its fresh owned database.
func TestNeo4jNativeGraphNamespaceEmptyExchangeImportAndOriginalRuntime(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	count := nativeRunTest(t, admin, Request{Query: "MATCH (n) RETURN count(n)"})
	if count.Rows[0][0].Text != "0" {
		t.Fatal("namespace acceptance requires its original empty owned database")
	}
	l := dataLeaseTest(t, s, contract.GraphNamespaceKind, "")
	if !contract.BoundDataCapabilities(l.Runtime()).GraphExchange {
		t.Fatal("namespace transfer not bound")
	}
	empty, e := l.exportGraph(context.Background(), l.object)
	if e != nil || empty.Source != s.Namespace("neo4j") || len(empty.Nodes) != 0 || len(empty.Relationships) != 0 {
		t.Fatal("empty namespace exchange failed", e)
	}
	for _, format := range []string{"json", "jsonl"} {
		var raw bytes.Buffer
		if e = graphimport.Write(&raw, empty, format); e != nil {
			t.Fatal(e)
		}
		b, e := graphimport.Parse(&raw, format)
		if e != nil || b.Source != empty.Source {
			t.Fatal("empty format lost namespace", e)
		}
	}
	props := graphImportPropertiesTest(t)
	request := contract.GraphImportRequest{Bundle: contract.GraphImportBundle{Format: graphimport.Format, Nodes: []Node{{ElementID: "first", Properties: props}, {ElementID: "second", Labels: []string{"NyaNamespace_" + f.Nonce}, Properties: props}}}}
	for _, edge := range []struct{ id, start, end string }{{"forward", "first", "second"}, {"parallel", "first", "second"}, {"reverse", "second", "first"}, {"self", "first", "first"}} {
		request.Bundle.Relationships = append(request.Bundle.Relationships, Relationship{ElementID: edge.id, StartElementID: edge.start, EndElementID: edge.end, Type: "NyaNamespaceEdges_" + f.Nonce, Properties: props})
	}
	plan := graphImportPreviewTest(t, l, request)
	expectedProperties := plan.Snapshot().Request.Bundle.Nodes[0].Properties
	if plan.Snapshot().Object != l.object || plan.Snapshot().Nodes != 2 || !strings.Contains(plan.Snapshot().NativeJSON, contract.GraphNamespaceKind) {
		t.Fatal("preview used a fabricated label")
	}
	if count = nativeRunTest(t, admin, Request{Query: "MATCH (n) RETURN count(n)"}); count.Rows[0][0].Text != "0" {
		t.Fatal("namespace preview created graph")
	}
	out, e := l.submitGraphImport(context.Background(), s.ProfileID(), "neo4j", "", plan)
	if e != nil || out == nil || !out.CommitConfirmed || out.OutcomeUnknown || out.NodesCreated != 2 || out.RelationshipsCreated != 4 || len(out.Identities) != 6 {
		t.Fatal("empty namespace import lacks full commit", e)
	}
	ids := []string{}
	for _, id := range out.Identities {
		if id.Kind == "node" {
			ids = append(ids, id.DestinationID)
		}
	}
	defer nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE elementId(n) IN $ids DETACH DELETE n", Parameters: map[string]any{"ids": ids}})
	exported, e := l.exportGraph(context.Background(), l.object)
	if e != nil || len(exported.Nodes) != 2 || len(exported.Relationships) != 4 || len(graphimport.ExternalEndpoints(exported)) != 0 {
		t.Fatal("whole namespace exchange lost topology", e)
	}
	for _, n := range exported.Nodes {
		if !reflect.DeepEqual(n.Properties, expectedProperties) {
			t.Fatal("whole namespace exchange lost complete types")
		}
	}
	for _, r := range exported.Relationships {
		if !reflect.DeepEqual(r.Properties, expectedProperties) {
			t.Fatal("whole namespace exchange lost complete relationship types")
		}
	}
	if _, e = l.submitGraphImport(context.Background(), s.ProfileID(), "neo4j", "", plan); e == nil {
		t.Fatal("namespace graph plan replayed")
	}
	next := graphImportPreviewTest(t, l, request)
	if e = l.Close(); e != nil {
		t.Fatal(e)
	}
	if next.Snapshot().Object.ProfileID != 0 {
		t.Fatal("namespace close retained original plan")
	}
}
