package contract

import (
	"bytes"
	"context"
	"slices"
)

// Native configuration, boolean receipts and desired-state readback remain
// distinct. These endpoints have no task UID or remote incarnation/CAS token.
type CollectionManagementRequest struct {
	Method, Path, ContentType string
	Body                      []byte
}
type CollectionManagementResponse struct {
	Status      int
	ContentType string
	Body        []byte
	Incomplete  bool
}
type CollectionManagementRead struct {
	Response          CollectionManagementResponse
	BodyTruncated     bool
	OriginalBodyBytes int
}
type CollectionAlias struct {
	Alias, Collection NamespaceRef
}
type CollectionConfiguration struct {
	Collection NamespaceRef
	JSON       string
}
type CollectionManagementSnapshot struct {
	Object               ObjectRef
	Sequence             uint64 // Original lease observation sequence, never a public generation.
	Targets, Collections []NamespaceRef
	Aliases              []CollectionAlias
	Configurations       []CollectionConfiguration
	Reads                []CollectionManagementRead
}
type CollectionAliasChange struct {
	Action                         string
	Alias, Collection, Destination NamespaceRef
}
type CollectionManagementDraft struct {
	Action         string
	Collection     NamespaceRef
	Configuration  string
	Aliases        []CollectionAliasChange
	TimeoutSeconds int
}
type CollectionManagementReceipt struct {
	Action            string
	Collection        NamespaceRef
	HasResult, Result bool
	Response          CollectionManagementResponse
}
type CollectionManagementResult struct {
	State    string
	Receipt  CollectionManagementReceipt
	Snapshot CollectionManagementSnapshot
	Matched  bool
	States   map[string]string
}

// Handles retain adapter-private signed observations and one-attempt state.
// Accessors return deep copies; caller implementations are never authorization.
type CollectionManagementObservation interface {
	Snapshot() CollectionManagementSnapshot
}
type CollectionManagementPlan interface {
	Action() string
	Preview() CollectionManagementRequest
	Affected() []NamespaceRef
}
type CollectionManagementLease interface {
	Object() ObjectRef
	Generation() uint64 // Translated only by the runtime/adapter boundary.
	Read(context.Context, []NamespaceRef) (CollectionManagementObservation, error)
	Prepare(CollectionManagementObservation, CollectionManagementDraft) (CollectionManagementPlan, error)
	Submit(context.Context, CollectionManagementPlan) (CollectionManagementResult, error)
	Verify(context.Context) (CollectionManagementResult, error)
	Close() error
}
type CollectionManagementProvider interface {
	SupportsCollectionManagement(ObjectRef, string) bool
	ReserveCollectionManagementDocument(ObjectRef, uint64) (CollectionManagementLease, error)
}

func CloneCollectionManagementRequest(r CollectionManagementRequest) CollectionManagementRequest {
	r.Body = bytes.Clone(r.Body)
	return r
}
func CloneCollectionManagementSnapshot(s CollectionManagementSnapshot) CollectionManagementSnapshot {
	s.Targets, s.Collections = slices.Clone(s.Targets), slices.Clone(s.Collections)
	s.Aliases, s.Configurations, s.Reads = slices.Clone(s.Aliases), slices.Clone(s.Configurations), slices.Clone(s.Reads)
	for i := range s.Reads {
		s.Reads[i].Response.Body = bytes.Clone(s.Reads[i].Response.Body)
	}
	return s
}
func CloneCollectionManagementDraft(d CollectionManagementDraft) CollectionManagementDraft {
	d.Aliases = slices.Clone(d.Aliases)
	return d
}
func CloneCollectionManagementResult(r CollectionManagementResult) CollectionManagementResult {
	r.Receipt.Response.Body = bytes.Clone(r.Receipt.Response.Body)
	r.Snapshot = CloneCollectionManagementSnapshot(r.Snapshot)
	states := make(map[string]string, len(r.States))
	for key, value := range r.States {
		states[key] = value
	}
	r.States = states
	return r
}
