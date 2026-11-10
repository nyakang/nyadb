package contract

import (
	"context"
	"slices"
)

// Watch owns an absolute byte range. A missing RangeEnd means an exact key;
// the adapter never reconstructs keys from a display string or fills ACL gaps.
type KVWatchRequest struct {
	Key, RangeEnd          []byte
	StartRevision, WatchID int64
	PrevKV, ProgressNotify bool
	NoPut, NoDelete        bool
}

func (r KVWatchRequest) Clone() KVWatchRequest {
	r.Key, r.RangeEnd = slices.Clone(r.Key), slices.Clone(r.RangeEnd)
	return r
}

type KVHistoryRequest struct {
	Key                        []byte
	StartRevision, EndRevision int64
	Limit                      int
}

func (r KVHistoryRequest) Clone() KVHistoryRequest { r.Key = slices.Clone(r.Key); return r }

type KVEvent struct {
	Type     string
	Entry    KVEntry
	Previous *KVEntry
}

func (e KVEvent) Clone() KVEvent {
	e.Entry = e.Entry.Clone()
	if e.Previous != nil {
		p := e.Previous.Clone()
		e.Previous = &p
	}
	return e
}

// Native is the complete original protobuf frame, including unknown fields.
// JSON is an optional bounded presentation; Events retain exact binary values.
type KVEventFrame struct {
	Native                      []byte
	JSON, JSONCaptureError      string
	Header                      KVHeader
	WatchID                     int64
	Created, Canceled, Progress bool
	CompactRevision             int64
	CancelReason                string
	Events                      []KVEvent
}

func (f KVEventFrame) Clone() KVEventFrame {
	f.Native = slices.Clone(f.Native)
	f.Events = slices.Clone(f.Events)
	for i := range f.Events {
		f.Events[i] = f.Events[i].Clone()
	}
	return f
}

type KVEventTerminal struct{ Reason, Code, Message string }

type KVWatchBatch struct {
	Frames            []KVEventFrame
	Ended, Incomplete bool
	Terminal          KVEventTerminal
}

func (b KVWatchBatch) Clone() KVWatchBatch {
	b.Frames = slices.Clone(b.Frames)
	for i := range b.Frames {
		b.Frames[i] = b.Frames[i].Clone()
	}
	return b
}

// Completion proves only Window, independently of display truncation and
// incomplete native capture. Missing progress never implies an empty history.
type KVHistoryResult struct {
	Authored, Window                       KVHistoryRequest
	Initial                                KVResult
	Frames                                 []KVEventFrame
	Events                                 []KVEvent
	Complete, Truncated, CaptureIncomplete bool
	ProgressHeader                         KVHeader
	ProgressRequests                       int
	Terminal                               KVEventTerminal
}

func (r KVHistoryResult) Clone() KVHistoryResult {
	r.Authored, r.Window = r.Authored.Clone(), r.Window.Clone()
	r.Initial = r.Initial.Clone()
	r.Frames = slices.Clone(r.Frames)
	for i := range r.Frames {
		r.Frames[i] = r.Frames[i].Clone()
	}
	r.Events = slices.Clone(r.Events)
	for i := range r.Events {
		r.Events[i] = r.Events[i].Clone()
	}
	return r
}

type KVWatchRun interface {
	Request() KVWatchRequest
	Poll() KVWatchBatch // bounded transfer from the captured original receiver
	Progress() error
	Stop(context.Context) error // waits for the original executor's acknowledgement
	Close() error
}

type KVEventsLease interface {
	Scope() KVScope
	CheckKVLease() error
	Preconnect(context.Context) error
	StartWatch(context.Context, KVWatchRequest) (KVWatchRun, error)
	History(context.Context, KVHistoryRequest) (KVHistoryResult, error)
	LastHistory() *KVHistoryResult
	ClearReceipts() error // explicit, only after the original executor has settled
	Close() error
}

type KVEventsRecovery struct {
	Lease   KVEventsLease
	History *KVHistoryResult
	Watch   KVWatchBatch
}

// This feature has its own document lease; native one-use write plans and
// unknown write receipts remain owned by KVRequestsProvider and its document.
type KVEventsProvider interface {
	KVScope() KVScope
	ReserveKVEvents(uint64) (KVEventsLease, error)           // no I/O
	RecoverKVEvents(KVEventsLease) (KVEventsRecovery, error) // never resubscribes
}
