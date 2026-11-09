package contract

import "slices"

// GraphNamespaceKind identifies a complete graph transfer document. It has an
// original namespace and no fabricated label, relationship type or object name.
const GraphNamespaceKind = "graph_namespace"

// Graph exchange IDs are source references. They never authorize destination
// reads or writes, and generated IDs become authoritative only after COMMIT.
type GraphImportBundle struct {
	Format        string
	Source        NamespaceRef
	Nodes         []GraphNode
	Relationships []GraphRelationship
}

type GraphImportBinding struct {
	SourceID, DestinationID string
}

type GraphImportRequest struct {
	Bundle   GraphImportBundle
	Bindings []GraphImportBinding
}

type GraphImportAnchor struct {
	SourceID string
	Node     GraphNode
}

type GraphImportSnapshot struct {
	Object     ObjectRef
	Request    GraphImportRequest
	Anchors    []GraphImportAnchor
	Nodes      int
	NativeJSON string
}

type GraphImportPlan interface {
	Snapshot() GraphImportSnapshot
	Close() error
}

type GraphImportIdentity struct {
	Kind, SourceID, DestinationID string
}

type GraphImportResult struct {
	Object                                               ObjectRef
	Identities                                           []GraphImportIdentity
	NodesCreated, RelationshipsCreated                   int
	BeginConfirmed, CommitAttempted, CommitConfirmed     bool
	RollbackAttempted, RollbackConfirmed, OutcomeUnknown bool
	NativeJSON                                           string
}

func CloneGraphImportBundle(b GraphImportBundle) GraphImportBundle {
	b.Nodes = slices.Clone(b.Nodes)
	for i := range b.Nodes {
		b.Nodes[i] = *GraphValue{Kind: "node", Node: &b.Nodes[i]}.Clone().Node
	}
	b.Relationships = slices.Clone(b.Relationships)
	for i := range b.Relationships {
		b.Relationships[i] = *GraphValue{Kind: "relationship", Relationship: &b.Relationships[i]}.Clone().Relationship
	}
	return b
}

func CloneGraphImportRequest(r GraphImportRequest) GraphImportRequest {
	r.Bundle = CloneGraphImportBundle(r.Bundle)
	r.Bindings = slices.Clone(r.Bindings)
	return r
}

func CloneGraphImportSnapshot(s GraphImportSnapshot) GraphImportSnapshot {
	s.Request = CloneGraphImportRequest(s.Request)
	s.Anchors = slices.Clone(s.Anchors)
	for i := range s.Anchors {
		s.Anchors[i].Node = *GraphValue{Kind: "node", Node: &s.Anchors[i].Node}.Clone().Node
	}
	return s
}

func CloneGraphImportResult(r *GraphImportResult) *GraphImportResult {
	if r == nil {
		return nil
	}
	copy := *r
	copy.Identities = slices.Clone(r.Identities)
	return &copy
}
