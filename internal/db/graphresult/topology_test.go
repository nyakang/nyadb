package graphresult

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func topologyNode(id, name string) contract.GraphNode {
	return contract.GraphNode{ElementID: id, LegacyID: 9223372036854775807, Labels: []string{"Person"}, Properties: []contract.GraphProperty{{Name: "name", Value: contract.GraphValue{Kind: "string", Text: name}}, {Name: "exact", Value: contract.GraphValue{Kind: "integer", Text: "9223372036854775807"}}}}
}
func TestTopologyRetainedNestedPathsMissingEndpointsConflictsAndOwnedExactValues(t *testing.T) {
	a, b := topologyNode("a", "Alice"), topologyNode("b", "Bob")
	e := contract.GraphRelationship{ElementID: "e", StartElementID: "a", EndElementID: "b", Type: "KNOWS", Properties: []contract.GraphProperty{{Name: "exact", Value: contract.GraphValue{Kind: "integer", Text: "-9223372036854775808"}}}}
	unknown := contract.GraphRelationship{ElementID: "unknown", StartElementID: "b", EndElementID: "c", Type: "MISSING"}
	changed := a.Clone()
	changed.Properties[0].Value.Text = "later observation"
	graph := contract.GraphResult{Columns: []string{"native", "nested"}, Completed: true, Rows: [][]contract.GraphValue{
		{{Kind: "relationship", Relationship: &e}, {Kind: "map", Properties: []contract.GraphProperty{{Name: "nested", Value: contract.GraphValue{Kind: "list", Values: []contract.GraphValue{{Kind: "path", Path: &contract.GraphPath{Nodes: []contract.GraphNode{a, b}, Relationships: []contract.GraphRelationship{e}}}}}}}}},
		{{Kind: "relationship", Relationship: &unknown}, {Kind: "node", Node: &changed}},
	}}
	before, _ := json.Marshal(graph)
	topology := ExtractTopology(graph)
	if topology.Bounded || topology.Unidentified || len(topology.Nodes) != 3 || len(topology.Edges) != 2 {
		t.Fatal("native path/map/list entities lost", topology)
	}
	if topology.Nodes[0].Key != "n:a" || topology.Nodes[0].Missing || !topology.Nodes[0].Conflicting || topology.Nodes[0].Value.Properties[0].Value.Text != "Alice" || topology.Nodes[0].Value.LegacyID != 9223372036854775807 || topology.Nodes[0].Value.Properties[1].Value.Text != "9223372036854775807" {
		t.Fatal("original identity, exact types or first conflicting observation changed")
	}
	if topology.Nodes[1].Missing || !topology.Nodes[2].Missing || len(topology.Nodes[2].Value.Properties) != 0 || topology.Nodes[2].Value.ElementID != "c" {
		t.Fatal("missing endpoint was fabricated or returned node remained missing")
	}
	if !reflect.DeepEqual(topology.Nodes[0].Cells, []CellRef{{0, 0}, {0, 1}, {1, 1}}) || !reflect.DeepEqual(topology.Edges[0].Cells, []CellRef{{0, 0}, {0, 1}}) || topology.Edges[0].Source != "n:a" || topology.Edges[0].Target != "n:b" {
		t.Fatal("original cell references or relationship direction changed")
	}
	after, _ := json.Marshal(graph)
	if string(before) != string(after) || !graph.Completed {
		t.Fatal("visual extraction changed original graph or receipt")
	}
	topology.Nodes[0].Value.Properties[0].Value.Text = "mutated visualization"
	topology.Edges[0].Value.Properties[0].Value.Text = "mutated edge"
	after, _ = json.Marshal(graph)
	if string(before) != string(after) {
		t.Fatal("topology borrowed original node/edge properties")
	}
}
func TestTopologyDistinctRelationshipDirectionParallelSelfLoopsAndIdentityKinds(t *testing.T) {
	graph := contract.GraphResult{Columns: []string{"r"}}
	for i, pair := range [][2]string{{"a", "b"}, {"a", "b"}, {"b", "a"}, {"a", "a"}} {
		e := contract.GraphRelationship{ElementID: fmt.Sprint(i), StartElementID: pair[0], EndElementID: pair[1], Type: "R"}
		graph.Rows = append(graph.Rows, []contract.GraphValue{{Kind: "relationship", Relationship: &e}})
	}
	n := topologyNode("0", "same ID as relationship")
	graph.Rows = append(graph.Rows, []contract.GraphValue{{Kind: "node", Node: &n}})
	topology := ExtractTopology(graph)
	if len(topology.Nodes) != 3 || len(topology.Edges) != 4 || topology.Edges[2].Source != "n:b" || topology.Edges[2].Target != "n:a" || topology.Edges[3].Source != topology.Edges[3].Target || topology.Nodes[0].Key == topology.Edges[0].Key {
		t.Fatal("parallel edges, self-loop, direction or node/edge key spaces merged")
	}
	graph.Rows = append(graph.Rows, []contract.GraphValue{{Kind: "node", Node: &contract.GraphNode{LegacyID: 0}}})
	if !ExtractTopology(graph).Unidentified {
		t.Fatal("an absent element ID inferred a shared zero legacy identity")
	}
}

func TestTopologyNeighborObservationsKeepOriginalCellsSeparateAndFirstValues(t *testing.T) {
	a, b := topologyNode("a", "original"), topologyNode("b", "neighbor")
	later := a.Clone()
	later.Properties[0].Value.Text = "later"
	e := contract.GraphRelationship{ElementID: "r", StartElementID: "a", EndElementID: "b", Type: "NEIGHBOR"}
	original := contract.GraphResult{Columns: []string{"a"}, Rows: [][]contract.GraphValue{{{Kind: "node", Node: &a}}}}
	observed := contract.GraphResult{Columns: []string{"n", "r", "m"}, Rows: [][]contract.GraphValue{{{Kind: "node", Node: &later}, {Kind: "relationship", Relationship: &e}, {Kind: "node", Node: &b}}}}
	before, _ := json.Marshal(observed)
	topology := ExtractTopologyObservations(original, []contract.GraphResult{observed})
	if len(topology.Nodes) != 2 || len(topology.Edges) != 1 || topology.Bounded || topology.Nodes[0].Value.Properties[0].Value.Text != "original" || !topology.Nodes[0].Conflicting {
		t.Fatal("neighbor observations replaced or dropped original values")
	}
	if !reflect.DeepEqual(topology.Nodes[0].Cells, []CellRef{{0, 0}}) || len(topology.Nodes[1].Cells) != 0 || len(topology.Edges[0].Cells) != 0 || !reflect.DeepEqual(topology.Nodes[1].Observations, []ObservationRef{{0, 0, 1}, {0, 0, 2}}) {
		t.Fatal("neighbor links were falsely assigned to original result cells")
	}
	topology.Nodes[1].Value.Properties[0].Value.Text = "visual mutation"
	after, _ := json.Marshal(observed)
	if string(before) != string(after) {
		t.Fatal("merged topology borrowed the neighbor receipt values")
	}
}
func TestTopologyBoundsPreserveOriginalRowsAndAllPublishedEdgeEndpoints(t *testing.T) {
	for _, kind := range []string{"nodes", "edges", "bytes", "depth", "visits", "refs"} {
		t.Run(kind, func(t *testing.T) {
			graph := contract.GraphResult{Columns: []string{"value"}, Completed: true}
			switch kind {
			case "nodes":
				for i := 0; i < 1001; i++ {
					n := topologyNode(fmt.Sprint(i), "n")
					graph.Rows = append(graph.Rows, []contract.GraphValue{{Kind: "node", Node: &n}})
				}
			case "edges":
				for i := 0; i < 2001; i++ {
					e := contract.GraphRelationship{ElementID: fmt.Sprint(i), StartElementID: "a", EndElementID: "b"}
					graph.Rows = append(graph.Rows, []contract.GraphValue{{Kind: "relationship", Relationship: &e}})
				}
			case "bytes":
				for i := 0; i < 10; i++ {
					n := topologyNode(fmt.Sprint(i), strings.Repeat("x", 1<<20))
					graph.Rows = append(graph.Rows, []contract.GraphValue{{Kind: "node", Node: &n}})
				}
			case "depth":
				n := topologyNode("a", "n")
				v := contract.GraphValue{Kind: "node", Node: &n}
				for range 34 {
					v = contract.GraphValue{Kind: "list", Values: []contract.GraphValue{v}}
				}
				graph.Rows = [][]contract.GraphValue{{v}}
			case "visits":
				for range 65537 {
					graph.Rows = append(graph.Rows, []contract.GraphValue{{Kind: "null"}})
				}
			case "refs":
				for range 40000 {
					e := contract.GraphRelationship{ElementID: "e", StartElementID: "a", EndElementID: "b"}
					graph.Rows = append(graph.Rows, []contract.GraphValue{{Kind: "relationship", Relationship: &e}})
				}
			}
			before, _ := json.Marshal(graph)
			topology := ExtractTopology(graph)
			if !topology.Bounded || len(topology.Nodes) > TopologyMaxNodes || len(topology.Edges) > TopologyMaxEdges {
				t.Fatal("topology bound missing", kind)
			}
			ids := map[string]bool{}
			for _, node := range topology.Nodes {
				ids[node.Key] = true
			}
			for _, edge := range topology.Edges {
				if !ids[edge.Source] || !ids[edge.Target] {
					t.Fatal("edge has unpublished endpoint")
				}
			}
			after, _ := json.Marshal(graph)
			if string(before) != string(after) || !graph.Completed {
				t.Fatal("bounded visualization modified retained native result")
			}
		})
	}
}
