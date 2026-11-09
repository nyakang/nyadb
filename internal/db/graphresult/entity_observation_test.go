package graphresult

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestEntityObservationOrderPreservesHistorySourcesAndIndependentLaterConflicts(t *testing.T) {
	a, b := topologyNode("a", "original"), topologyNode("b", "other original")
	relation := contract.GraphRelationship{ElementID: "r", StartElementID: "a", EndElementID: "b", Type: "DIRECTED", Properties: []contract.GraphProperty{{Name: "name", Value: contract.GraphValue{Kind: "string", Text: "original relation"}}}}
	original := contract.GraphResult{Columns: []string{"a", "r", "b"}, Rows: [][]contract.GraphValue{{{Kind: "node", Node: &a}, {Kind: "relationship", Relationship: &relation}, {Kind: "node", Node: &b}}}}
	past := original.Clone()
	past.Rows[0][0].Node.Properties[0].Value.Text = "historical external"
	saved := original.Clone()
	saved.Rows[0][0].Node.Properties[0].Value.Text = "saved"
	saved.Rows[0][1].Relationship.Properties[0].Value.Text = "saved relation"
	later := saved.Clone()
	later.Rows[0][0].Node.Properties[0].Value.Text = "later external"
	later.Rows[0][1].Relationship.Properties[0].Value.Text = "later external relation"
	later.Rows[0][2].Node.Labels = []string{"ChangedOther"}
	entities := map[string]EntityObservation{
		"n:a": {AfterObservations: 1, Value: saved.Rows[0][0].Clone()},
		"r:r": {AfterObservations: 1, Value: saved.Rows[0][1].Clone()},
	}
	before, _ := json.Marshal([]contract.GraphResult{original, past, saved, later})
	topology := ExtractTopologyWithEntityObservations(original, []contract.GraphResult{past, saved}, entities)
	if topology.Nodes[0].Conflicting || topology.Edges[0].Conflicting || !topology.Nodes[0].EntityObserved || !topology.Edges[0].EntityObserved || topology.Nodes[0].Value.Properties[0].Value.Text != "saved" {
		t.Fatal("explicit save baseline included prior history in its subsequent conflicts")
	}
	if !reflect.DeepEqual(topology.Nodes[0].Cells, []CellRef{{0, 0}, {0, 1}}) || !reflect.DeepEqual(topology.Edges[0].Observations, []ObservationRef{{0, 0, 1}, {1, 0, 1}}) {
		t.Fatal("baseline manufactured source links or lost original and neighbor provenance")
	}
	topology = ExtractTopologyWithEntityObservations(original, []contract.GraphResult{past, saved, later}, entities)
	if !topology.Nodes[0].Conflicting || !topology.Nodes[1].Conflicting || !topology.Edges[0].Conflicting || topology.Nodes[0].Value.Properties[0].Value.Text != "saved" || topology.Nodes[1].EntityObserved {
		t.Fatal("later same-identity observations or an unrelated entity conflict were hidden")
	}
	entities["n:a"] = EntityObservation{AfterObservations: 3, Value: later.Rows[0][0].Clone()}
	topology = ExtractTopologyWithEntityObservations(original, []contract.GraphResult{past, saved, later}, entities)
	if topology.Nodes[0].Conflicting || !topology.Nodes[1].Conflicting || !topology.Edges[0].Conflicting || topology.Nodes[0].Value.Properties[0].Value.Text != "later external" || topology.Nodes[0].Value.Properties[1].Value.Text != "9223372036854775807" {
		t.Fatal("node verification cleared another entity or lost complete native types")
	}
	after, _ := json.Marshal([]contract.GraphResult{original, past, saved, later})
	if !bytes.Equal(before, after) {
		t.Fatal("reconciliation rewrote immutable source observations")
	}
	entities["n:a"].Value.Node.Properties[0].Value.Text = "mutated input"
	entities["r:r"].Value.Relationship.Properties[0].Value.Text = "mutated relation input"
	if topology.Nodes[0].Value.Properties[0].Value.Text != "later external" || topology.Edges[0].Value.Properties[0].Value.Text != "saved relation" {
		t.Fatal("displayed baseline borrowed the retained full entity")
	}
}

func TestEntityObservationsRejectInvalidOrderIdentityDirectionAndBudgetWithoutErasingConflicts(t *testing.T) {
	a := topologyNode("a", "original")
	relation := contract.GraphRelationship{ElementID: "r", StartElementID: "a", EndElementID: "b", Type: "R"}
	original := contract.GraphResult{Rows: [][]contract.GraphValue{{{Kind: "node", Node: &a}, {Kind: "relationship", Relationship: &relation}}}}
	later := original.Clone()
	later.Rows[0][0].Node.Properties[0].Value.Text = "later"
	for _, kind := range []string{"future", "negative", "identity", "direction", "bytes", "count", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			entities := map[string]EntityObservation{"n:a": {AfterObservations: 1, Value: later.Rows[0][0].Clone()}}
			switch kind {
			case "future":
				entities["n:a"] = EntityObservation{AfterObservations: 2, Value: later.Rows[0][0].Clone()}
			case "negative":
				entities["n:a"] = EntityObservation{AfterObservations: -1, Value: later.Rows[0][0].Clone()}
			case "identity":
				entities["n:a"].Value.Node.ElementID = "b"
			case "direction":
				entities = map[string]EntityObservation{"r:r": {AfterObservations: 1, Value: original.Rows[0][1].Clone()}}
				entities["r:r"].Value.Relationship.StartElementID = "b"
				entities["r:r"].Value.Relationship.EndElementID = "a"
			case "bytes":
				entities["n:a"].Value.Node.Properties[0].Value.Text = strings.Repeat("x", topologyMaxBytes)
			case "count":
				for i := 0; i <= TopologyMaxNodes+TopologyMaxEdges; i++ {
					entities[strings.Repeat("x", i+1)] = EntityObservation{}
				}
			case "unknown":
				entities = map[string]EntityObservation{"n:new": {AfterObservations: 1, Value: contract.GraphValue{Kind: "node", Node: &contract.GraphNode{ElementID: "new"}}}}
			}
			topology := ExtractTopologyWithEntityObservations(original, []contract.GraphResult{later}, entities)
			if !topology.Nodes[0].Conflicting || topology.Nodes[0].EntityObserved || topology.Nodes[0].Value.Properties[0].Value.Text != "original" || len(topology.Nodes) != 2 || len(topology.Edges) != 1 || topology.Edges[0].Source != "n:a" || topology.Edges[0].Target != "n:b" {
				t.Fatal("unadmitted explicit observation erased conflict or changed topology identity/direction")
			}
			if kind == "direction" && (!topology.Edges[0].Conflicting || topology.Edges[0].EntityObserved) {
				t.Fatal("different directed endpoints were admitted as the original entity")
			}
			if (kind == "count" || kind == "bytes") && !topology.Bounded {
				t.Fatal("entity baseline bypassed topology retention limits")
			}
		})
	}
}
