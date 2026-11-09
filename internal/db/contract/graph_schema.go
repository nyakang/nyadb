package contract

import (
	"context"
	"errors"
	"slices"
)

var (
	ErrGraphSchemaChanged      = errors.New("original graph schema observation changed; read it again")
	ErrGraphSchemaExists       = errors.New("graph index or constraint name already exists")
	ErrGraphSchemaBackingIndex = errors.New("constraint-owned index must be managed through its original constraint")
)

// A catalog retains complete native SHOW rows, including operational fields.
// The two reads are sequential observations, never an atomic catalog snapshot.
// Sequence is local to the original lease, not a server revision or incarnation.
type GraphSchemaCatalog struct {
	Object               ObjectRef
	Sequence             uint64
	Indexes, Constraints GraphResult
}

// Definition retains only native definition fields. Operational statistics and
// population state remain in the catalog; IDs and names are observations only.
type GraphSchemaDefinition struct {
	Kind, Name, Type, EntityType, Provider, CreateStatement string
	LabelsOrTypes, Properties                               []string
	LabelsOrTypesNull, PropertiesNull                       bool
	ID, OwningConstraint, OwnedIndex, PropertyType, Options GraphValue
}

type GraphSchemaDraft struct {
	Action, Name, Type, EntityType string
	LabelsOrTypes, Properties      []string
	Options                        GraphValue
}

type GraphSchemaRequest struct {
	Database, Query string
	Parameters      []GraphProperty
}

// These are preparations only. No native write is submitted through this
// feature. A DROP preview expressly records the absent atomic compare primitive;
// a later executor must not equate a preflight read with schema-lock protection.
type GraphSchemaPreview struct {
	Draft         GraphSchemaDraft
	Request       GraphSchemaRequest
	Before        []GraphSchemaDefinition
	NonAtomicDrop bool
}
type GraphSchemaObservation interface{ Snapshot() GraphSchemaCatalog }
type GraphSchemaPreparation interface{ Preview() GraphSchemaPreview }
type GraphSchemaPreparationLease interface {
	Object() ObjectRef
	Generation() uint64
	Read(context.Context) (GraphSchemaObservation, error)
	Prepare(GraphSchemaObservation, GraphSchemaDraft) (GraphSchemaPreparation, error)
	Close() error
}
type GraphSchemaPreparationProvider interface {
	SupportsGraphSchemaPreparation(ObjectRef) bool
	ReserveGraphSchemaPreparation(ObjectRef, uint64) (GraphSchemaPreparationLease, error)
}

func (c GraphSchemaCatalog) Clone() GraphSchemaCatalog {
	c.Indexes, c.Constraints = c.Indexes.Clone(), c.Constraints.Clone()
	return c
}
func (d GraphSchemaDefinition) Clone() GraphSchemaDefinition {
	d.LabelsOrTypes, d.Properties = slices.Clone(d.LabelsOrTypes), slices.Clone(d.Properties)
	d.ID, d.OwningConstraint, d.OwnedIndex = d.ID.Clone(), d.OwningConstraint.Clone(), d.OwnedIndex.Clone()
	d.PropertyType, d.Options = d.PropertyType.Clone(), d.Options.Clone()
	return d
}
func (d GraphSchemaDraft) Clone() GraphSchemaDraft {
	d.LabelsOrTypes, d.Properties = slices.Clone(d.LabelsOrTypes), slices.Clone(d.Properties)
	d.Options = d.Options.Clone()
	return d
}
func (r GraphSchemaRequest) Clone() GraphSchemaRequest {
	r.Parameters = cloneGraphProperties(r.Parameters)
	return r
}
func (p GraphSchemaPreview) Clone() GraphSchemaPreview {
	p.Draft, p.Request = p.Draft.Clone(), p.Request.Clone()
	p.Before = slices.Clone(p.Before)
	for i := range p.Before {
		p.Before[i] = p.Before[i].Clone()
	}
	return p
}
