package contract

import (
	"context"
	"errors"
	"slices"
)

var (
	ErrNamespaceChanged    = errors.New("original namespace observation changed; read it again before preparing an operation")
	ErrNamespaceInUse      = errors.New("close the original namespace's data, settings and task documents before changing it")
	ErrNamespaceBusy       = errors.New("original namespace operation is unresolved; retain evidence and observe it explicitly")
	ErrNamespaceExists     = errors.New("destination namespace already exists")
	ErrNamespaceNoChange   = errors.New("namespace operation has no changes")
	ErrNamespaceNotEmpty   = errors.New("primary key changes require an empty index")
	ErrNamespaceUnverified = errors.New("original namespace task succeeded but metadata verification is incomplete; retain the receipt without replay")
)

// NamespaceRecord captures one native document-index namespace and its count.
// Version is an adapter-local observation epoch, never a public generation,
// server revision, CAS token or immutable index incarnation. Token binds the
// complete immutable raw observations to their original task lease.
type NamespaceRecord struct {
	Ref                              NamespaceRef
	CreatedAt, UpdatedAt, PrimaryKey string
	Exists, HasPrimaryKey, Indexing  bool
	Documents, Version               uint64
	Raw, StatsRaw, Token             string
}

// PrimaryKeySet is optional for creation and required for a primary-key update.
// It does not express removal of an existing key. Native unsupported semantics
// must be rejected instead of being presented as a successful null update.
type NamespaceDraft struct {
	Ref, Destination NamespaceRef
	PrimaryKey       string
	PrimaryKeySet    bool
}
type NamespaceMutation struct {
	Draft    NamespaceDraft
	Before   []NamespaceRecord
	Versions []uint64
}

func CloneNamespaceMutation(n *NamespaceMutation) *NamespaceMutation {
	if n == nil {
		return nil
	}
	copy := *n
	copy.Before = slices.Clone(n.Before)
	copy.Versions = slices.Clone(n.Versions)
	return &copy
}
func CloneTaskMutationPlan(p TaskMutationPlan) TaskMutationPlan {
	p.Records = slices.Clone(p.Records)
	p.Namespace = CloneNamespaceMutation(p.Namespace)
	return p
}
func CloneTaskMutationReceipt(r TaskMutationReceipt) TaskMutationReceipt {
	r.Targets = slices.Clone(r.Targets)
	r.Namespace = CloneNamespaceMutation(r.Namespace)
	return r
}
func CloneTaskMutationResult(r TaskMutationResult) TaskMutationResult {
	r.Receipt = CloneTaskMutationReceipt(r.Receipt)
	r.Targets = slices.Clone(r.Targets)
	r.Namespace = CloneNamespaceMutation(r.Namespace)
	r.Namespaces = slices.Clone(r.Namespaces)
	return r
}

// This optional feature belongs to an original task document. Reads are explicit;
// Prepare is pure, signed and one-use. Creation/rename/swap/deletion require a
// global namespace connection. Stable-UID primary-key updates and document
// clearing may use an explicitly scoped original index. Native admission,
// task completion and metadata verification remain distinct.
type NamespaceManagementLease interface {
	SupportsNamespaceManagement(string) bool
	ReadNamespace(context.Context, NamespaceRef) (NamespaceRecord, error)
	PrepareNamespaceMutation(string, NamespaceDraft, []NamespaceRecord) (TaskMutationPlan, error)
}
type NamespaceManagementProvider interface {
	SupportsNamespaceManagement(ObjectRef, string) bool
}
