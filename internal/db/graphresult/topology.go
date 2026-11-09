package graphresult

import (
	"crypto/sha256"
	"encoding/json"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Topology is an owned, bounded view of retained observations, not a new graph
// read or a claim that missing endpoints/duplicate observations are current.
type Topology struct {
	Nodes                 []TopologyNode
	Edges                 []TopologyEdge
	Bounded, Unidentified bool
}
type CellRef struct{ Row, Column int }

// ObservationRef belongs to a separate neighbor query, never the original grid.
type ObservationRef struct{ Observation, Row, Column int }
type topologyCell struct{ observation, row, column int }
type TopologyNode struct {
	Key                  string
	Value                contract.GraphNode
	Missing, Conflicting bool
	Cells                []CellRef
	Observations         []ObservationRef
}
type TopologyEdge struct {
	Key, Source, Target string
	Value               contract.GraphRelationship
	Conflicting         bool
	Cells               []CellRef
	Observations        []ObservationRef
}

const TopologyMaxNodes = 1000
const TopologyMaxEdges = 2000
const topologyMaxBytes = 8 << 20
const topologyMaxRefs = 65536
const topologyMaxVisits = 65536

func ExtractTopology(r contract.GraphResult) Topology {
	return ExtractTopologyObservations(r, nil)
}

// ExtractTopologyObservations admits original values first, with separate links
// for later live reads. All sources share one topology/traversal/link budget.
func ExtractTopologyObservations(r contract.GraphResult, observations []contract.GraphResult) Topology {
	t := Topology{}
	nodes, edges := map[string]int{}, map[string]int{}
	nodeHashes, edgeHashes := map[string][32]byte{}, map[string][32]byte{}
	refs, visits, bytes := 0, 0, 0
	charge := func(n int) bool {
		if bytes+n > topologyMaxBytes {
			t.Bounded = true
			return false
		}
		bytes += n
		return true
	}
	addRef := func(cells *[]CellRef, observed *[]ObservationRef, cell topologyCell) {
		original := CellRef{cell.row, cell.column}
		observation := ObservationRef{cell.observation, cell.row, cell.column}
		if cell.observation < 0 && len(*cells) > 0 && (*cells)[len(*cells)-1] == original || cell.observation >= 0 && len(*observed) > 0 && (*observed)[len(*observed)-1] == observation {
			return
		}
		cost := 16
		if cell.observation >= 0 {
			cost = 24
		}
		if refs >= topologyMaxRefs || !charge(cost) {
			t.Bounded = true
			return
		}
		refs++
		if cell.observation < 0 {
			*cells = append(*cells, original)
		} else {
			*observed = append(*observed, observation)
		}
	}
	var node func(contract.GraphNode, topologyCell, bool) bool
	node = func(n contract.GraphNode, cell topologyCell, missing bool) bool {
		if n.ElementID == "" {
			t.Unidentified = true
			return false
		}
		key := "n:" + n.ElementID
		if index, ok := nodes[key]; ok {
			item := &t.Nodes[index]
			if !missing {
				raw, err := json.Marshal(n)
				if err != nil {
					t.Unidentified = true
					return false
				}
				hash := sha256.Sum256(raw)
				if item.Missing {
					if !charge(len(raw)) {
						return false
					}
					item.Value, item.Missing = n.Clone(), false
					nodeHashes[key] = hash
				} else if nodeHashes[key] != hash {
					item.Conflicting = true
				}
			}
			addRef(&item.Cells, &item.Observations, cell)
			return true
		}
		if len(t.Nodes) >= TopologyMaxNodes {
			t.Bounded = true
			return false
		}
		raw, err := json.Marshal(n)
		if err != nil {
			t.Unidentified = true
			return false
		}
		if !charge(len(raw) + len(key) + 128) {
			return false
		}
		nodes[key] = len(t.Nodes)
		nodeHashes[key] = sha256.Sum256(raw)
		t.Nodes = append(t.Nodes, TopologyNode{Key: key, Value: n.Clone(), Missing: missing})
		item := &t.Nodes[len(t.Nodes)-1]
		addRef(&item.Cells, &item.Observations, cell)
		return true
	}
	edge := func(e contract.GraphRelationship, cell topologyCell) {
		if e.ElementID == "" || e.StartElementID == "" || e.EndElementID == "" {
			t.Unidentified = true
			return
		}
		key := "r:" + e.ElementID
		raw, err := json.Marshal(e)
		if err != nil {
			t.Unidentified = true
			return
		}
		if index, ok := edges[key]; ok {
			item := &t.Edges[index]
			if edgeHashes[key] != sha256.Sum256(raw) {
				item.Conflicting = true
			}
			addRef(&item.Cells, &item.Observations, cell)
			if item.Value.StartElementID == e.StartElementID && item.Value.EndElementID == e.EndElementID {
				start, end := &t.Nodes[nodes[item.Source]], &t.Nodes[nodes[item.Target]]
				addRef(&start.Cells, &start.Observations, cell)
				addRef(&end.Cells, &end.Observations, cell)
			}
			return
		}
		if len(t.Edges) >= TopologyMaxEdges {
			t.Bounded = true
			return
		}
		if !charge(len(raw) + len(key) + 128) {
			return
		}
		// Missing endpoint nodes are explicit identity-only placeholders. A later
		// retained node observation can fill them, without inventing properties.
		if !node(contract.GraphNode{ElementID: e.StartElementID}, cell, true) || !node(contract.GraphNode{ElementID: e.EndElementID}, cell, true) {
			return
		}
		edges[key] = len(t.Edges)
		edgeHashes[key] = sha256.Sum256(raw)
		t.Edges = append(t.Edges, TopologyEdge{Key: key, Source: "n:" + e.StartElementID, Target: "n:" + e.EndElementID, Value: e.Clone()})
		item := &t.Edges[len(t.Edges)-1]
		addRef(&item.Cells, &item.Observations, cell)
	}
	var visit func(contract.GraphValue, topologyCell, int)
	visit = func(v contract.GraphValue, cell topologyCell, depth int) {
		if visits >= topologyMaxVisits || depth > 32 {
			t.Bounded = true
			return
		}
		visits++
		switch v.Kind {
		case "node":
			if v.Node != nil {
				node(*v.Node, cell, false)
			}
		case "relationship":
			if v.Relationship != nil {
				edge(*v.Relationship, cell)
			}
		case "path":
			if v.Path != nil {
				for _, n := range v.Path.Nodes {
					if visits >= topologyMaxVisits {
						t.Bounded = true
						break
					}
					visits++
					node(n, cell, false)
				}
				for _, e := range v.Path.Relationships {
					if visits >= topologyMaxVisits {
						t.Bounded = true
						break
					}
					visits++
					edge(e, cell)
				}
			}
		}
		for _, value := range v.Values {
			if visits >= topologyMaxVisits {
				t.Bounded = true
				break
			}
			visit(value, cell, depth+1)
		}
		for _, property := range v.Properties {
			if visits >= topologyMaxVisits {
				t.Bounded = true
				break
			}
			visit(property.Value, cell, depth+1)
		}
	}
	sources := append([]contract.GraphResult{r}, observations...)
	for observation, source := range sources {
		for row, values := range source.Rows {
			for column, value := range values {
				if visits >= topologyMaxVisits {
					t.Bounded = true
					break
				}
				visit(value, topologyCell{observation - 1, row, column}, 0)
			}
			if visits >= topologyMaxVisits {
				if row+1 < len(source.Rows) || observation+1 < len(sources) {
					t.Bounded = true
				}
				break
			}
		}
		if visits >= topologyMaxVisits {
			break
		}
	}
	slices.SortFunc(t.Nodes, func(a, b TopologyNode) int {
		if a.Key < b.Key {
			return -1
		}
		if a.Key > b.Key {
			return 1
		}
		return 0
	})
	slices.SortFunc(t.Edges, func(a, b TopologyEdge) int {
		if a.Key < b.Key {
			return -1
		}
		if a.Key > b.Key {
			return 1
		}
		return 0
	})
	return t
}
