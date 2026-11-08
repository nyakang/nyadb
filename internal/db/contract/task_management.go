package contract

import (
	"context"
	"errors"
	"time"
)

var (
	ErrTaskMutationUsed        = errors.New("task operation preview changed, replaced, or already submitted")
	ErrTaskMutationState       = errors.New("selected original tasks are not eligible for this operation")
	ErrTaskMutationUnavailable = errors.New("original management task unavailable; retain admission and confirm on original server")
)

// Plans capture a finite set of original observed task records, or one global
// server backup or namespace operation. Request preserves the complete native operation;
// backup plans have no target task records or local output path.
type TaskMutationPlan struct {
	Object                 ObjectRef
	Action, Request, Token string
	Records                []TaskRecord
	Sequence               uint64
	Namespace              *NamespaceMutation
}

// Receipt certifies admission only and binds the original response to its lease.
type TaskMutationReceipt struct {
	Object                                    ObjectRef
	Action, UID, Type, EnqueuedAt, Raw, Token string
	Targets                                   []string
	Namespace                                 *NamespaceMutation
}

// TaskServerArtifact identifies a server-managed artifact reported by a completed
// task. It certifies neither file availability, download nor restore. UID may be
// absent for native snapshots. Server directories are outside this contract.
type TaskServerArtifact struct {
	Kind, UID string
}

type TaskMutationResult struct {
	State             string
	Receipt           TaskMutationReceipt
	TaskRaw, Request  string
	Targets           []string
	Matched, Changed  uint64
	CountsKnown       bool
	Artifact          TaskServerArtifact
	Namespace         *NamespaceMutation
	Namespaces        []NamespaceRecord
	NamespaceVerified bool
}
type TaskRecovery struct {
	Lease     TaskDocumentLease
	Receipt   TaskMutationReceipt
	Uncertain bool
	Namespace *NamespaceMutation
}

// This optional feature keeps task browsing providers read-only unless their
// captured native lease explicitly supports task mutation. Supports is pure,
// and does not imply server permission. Prepare performs no I/O. Mutate consumes
// a one-use plan, rechecks original observations, and never replays a request.
type TaskManagementLease interface {
	SupportsTaskManagement() bool
	PrepareTaskMutation(string, []TaskRecord) (TaskMutationPlan, error)
	MutateTasks(context.Context, TaskMutationPlan) (TaskMutationResult, error)
	ObserveTaskMutation(context.Context, TaskMutationReceipt, time.Duration) (TaskMutationResult, error)
}

// Recovery transfers original proven admission evidence without I/O. It does
// not transfer previews/cursors or turn an unknown submission into a retry.
type TaskRecoveryProvider interface {
	RecoverTaskDocument(ObjectRef, TaskDocumentLease) (TaskRecovery, error)
}

// ServerBackupLease is optional on the original task lease. Prepare is pure and
// shares the one-use management plan/receipt lifecycle. Only an unscoped global
// document may create a server artifact. Permission remains server-enforced.
// Submit and observe use TaskManagementLease; recovery never repeats creation.
type ServerBackupLease interface {
	SupportsServerBackup(string) bool
	PrepareServerBackup(string) (TaskMutationPlan, error)
}
