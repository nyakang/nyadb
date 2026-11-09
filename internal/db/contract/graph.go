package contract

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// Graph values retain native kinds, exact numeric text, temporal zone/offset
// metadata, duration components, spatial references and graph identities.
// Properties are ordered named values; maps and lists never flatten to text.
type GraphProperty struct {
	Name  string
	Value GraphValue
}
type GraphValue struct {
	Kind, Text   string
	Values       []GraphValue
	Properties   []GraphProperty
	Node         *GraphNode
	Relationship *GraphRelationship
	Path         *GraphPath
}
type GraphNode struct {
	ElementID  string
	LegacyID   int64
	Labels     []string
	Properties []GraphProperty
}
type GraphRelationship struct {
	ElementID, StartElementID, EndElementID, Type string
	LegacyID, StartLegacyID, EndLegacyID          int64
	Properties                                    []GraphProperty
}
type GraphPath struct {
	Nodes         []GraphNode
	Relationships []GraphRelationship
}
type GraphServer struct {
	Agent, Address               string
	ProtocolMajor, ProtocolMinor int
}
type GraphQueryRequest struct {
	Query, Database, Mode string
	// Adapters validate and deeply capture supported native parameter values.
	Parameters                                   map[string]any
	MaxRows, MaxBytes, FetchSize, TimeoutSeconds int
}
type GraphSummary struct {
	Server                        GraphServer
	Database, QueryType           string
	Counters                      map[string]int64
	AvailableAfter, ConsumedAfter time.Duration
	Bookmarks                     []string
}
type GraphResult struct {
	Columns                         []string
	Rows                            [][]GraphValue
	Summary                         GraphSummary
	FailureCode                     string
	Attempted, Completed, Truncated bool
	// Missing native completion cannot prove rollback of arbitrary procedures.
	Uncertain bool
}

func (r GraphResult) String() string {
	return fmt.Sprintf("Graph result: %d retained rows; attempted=%t completed=%t truncated=%t uncertain=%t", len(r.Rows), r.Attempted, r.Completed, r.Truncated, r.Uncertain)
}
func cloneGraphProperties(p []GraphProperty) []GraphProperty {
	p = slices.Clone(p)
	for i := range p {
		p[i].Value = p[i].Value.Clone()
	}
	return p
}
func (n GraphNode) Clone() GraphNode {
	n.Labels = slices.Clone(n.Labels)
	n.Properties = cloneGraphProperties(n.Properties)
	return n
}
func (r GraphRelationship) Clone() GraphRelationship {
	r.Properties = cloneGraphProperties(r.Properties)
	return r
}
func (v GraphValue) Clone() GraphValue {
	v.Values = slices.Clone(v.Values)
	for i := range v.Values {
		v.Values[i] = v.Values[i].Clone()
	}
	v.Properties = cloneGraphProperties(v.Properties)
	if v.Node != nil {
		n := v.Node.Clone()
		v.Node = &n
	}
	if v.Relationship != nil {
		r := v.Relationship.Clone()
		v.Relationship = &r
	}
	if v.Path != nil {
		p := &GraphPath{Nodes: slices.Clone(v.Path.Nodes), Relationships: slices.Clone(v.Path.Relationships)}
		for i := range p.Nodes {
			p.Nodes[i] = p.Nodes[i].Clone()
		}
		for i := range p.Relationships {
			p.Relationships[i] = p.Relationships[i].Clone()
		}
		v.Path = p
	}
	return v
}
func (r GraphResult) Clone() GraphResult {
	r.Columns = slices.Clone(r.Columns)
	r.Rows = slices.Clone(r.Rows)
	for i := range r.Rows {
		r.Rows[i] = slices.Clone(r.Rows[i])
		for j := range r.Rows[i] {
			r.Rows[i][j] = r.Rows[i][j].Clone()
		}
	}
	counters := make(map[string]int64, len(r.Summary.Counters))
	for k, v := range r.Summary.Counters {
		counters[k] = v
	}
	r.Summary.Counters = counters
	r.Summary.Bookmarks = slices.Clone(r.Summary.Bookmarks)
	return r
}

// GraphQueryLease complements the common result projection with its complete
// typed result and native receipt. Reservation/recovery retain QueryLease rules.
type GraphQueryLease interface {
	QueryLease
	ExecuteGraph(context.Context, GraphQueryRequest) (GraphResult, error)
}

// Schema observations retain the original native rows; property-type inference
// scans data and is not a relational schema or an atomic metadata snapshot.
type GraphSchemaSnapshot struct {
	Namespace                                   NamespaceRef
	Labels, RelationshipTypes                   GraphResult
	NodeProperties, RelationshipProperties      GraphResult
	Indexes, Constraints, Procedures, Functions GraphResult
}
type GraphMetadataProvider interface {
	GraphSchema(context.Context, NamespaceRef) (GraphSchemaSnapshot, error)
}
