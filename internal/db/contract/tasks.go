package contract

import (
	"context"
	"errors"
	"time"
)

// TaskRequestError retains complete native diagnostic evidence separately from
// the cached successful task. Error() never logs that potentially private body.
type TaskRequestError struct {
	Raw   string
	Cause error
}

func (e *TaskRequestError) Error() string { return e.Cause.Error() }
func (e *TaskRequestError) Unwrap() error { return e.Cause }

var ErrTaskLeaseLost = errors.New("original task connection lost; recover explicitly without replay")

// TaskQuery uses adapter-owned opaque cursors. Filters and ordering apply to a
// live view; a total or previous page is not an atomic server snapshot.
type TaskQuery struct {
	Limit                                                                                                 int
	From                                                                                                  string
	Reverse                                                                                               bool
	UIDs, Indexes, Statuses, Types, BatchUIDs, CanceledBy                                                 []string
	BeforeEnqueuedAt, AfterEnqueuedAt, BeforeStartedAt, AfterStartedAt, BeforeFinishedAt, AfterFinishedAt string
}

// TaskRecord preserves original native JSON and exact ID/timestamp text. Token
// binds that evidence to its owning physical lease; consumers must not edit it.
type TaskRecord struct {
	UID, Index, Type, Status                    string
	EnqueuedAt, StartedAt, FinishedAt, Duration string
	Details, Error, Raw, Token                  string
}
type TaskPage struct {
	Results            []TaskRecord
	Total              uint64
	Next, Request, Raw string
}

// TaskDocumentLease observes tasks only. Reservation performs no I/O. Close
// revokes requests and waits for the original executor, including unpublished
// results. Observe runs only at an explicit caller request with a bounded wait.
type TaskDocumentLease interface {
	List(context.Context, TaskQuery) (TaskPage, error)
	Read(context.Context, TaskRecord) (TaskRecord, error)
	Observe(context.Context, TaskRecord, time.Duration) (TaskRecord, error)
	Close() error
}
type TaskDocumentsProvider interface {
	SupportsTaskDocument(ObjectRef) bool // pure scope check; no permission claim.
	ReserveTaskDocument(ObjectRef) (TaskDocumentLease, error)
}
