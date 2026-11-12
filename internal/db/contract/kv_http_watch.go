package contract

import (
	"context"
	"slices"
	"time"
)

// HTTP Watch observes current ranges through native blocking reads. It does not
// provide an event stream, historical revisions, or confirmation of any write.
// ObservationPrefix is the explicitly authored actual read range; Target and
// Prefix only select original keys within that range, without path cleaning.
type KVHTTPWatchRequest struct {
	Target, ObservationPrefix string
	Prefix                    bool
	StartIndex                uint64
	Wait                      time.Duration
	MaxObservations           int
	MaxBytes                  int64
}

type KVHTTPWatchLimits struct {
	MinWait, MaxWait, ConnectTimeout, RequestTimeout time.Duration
	MaxObservations                                  int
	MaxBytes, TerminalBytes                          int64
}

type KVHTTPWatchPreparation interface {
	Snapshot() KVHTTPWatchRequest
}

// Matches contains indices into the complete HTTP entries, avoiding a second
// value copy. ProjectionComplete proves projection/capture only. ACLFiltered
// remains independent; absence of a matched key is not a deletion event.
// ReceivedAt is local receipt time, never a native version or incarnation.
type KVHTTPWatchObservation struct {
	HTTP               KVHTTPResult
	Matches            []int
	ReceivedAt         time.Time
	IndexRelation      string
	ProjectionComplete bool
}

func (o KVHTTPWatchObservation) Clone() KVHTTPWatchObservation {
	o.HTTP = o.HTTP.Clone()
	o.Matches = slices.Clone(o.Matches)
	return o
}

type KVHTTPWatchTerminal struct{ Reason, Message string }

// A final result that exceeds the authored byte limit is retained in the
// separately reserved terminal slot and ends the run. No evidence is evicted.
type KVHTTPWatchBatch struct {
	Observations     []KVHTTPWatchObservation
	Ended            bool
	TerminalOverflow bool
	RetainedBytes    int64
	Terminal         KVHTTPWatchTerminal
}

func (b KVHTTPWatchBatch) Clone() KVHTTPWatchBatch {
	b.Observations = slices.Clone(b.Observations)
	for i := range b.Observations {
		b.Observations[i] = b.Observations[i].Clone()
	}
	return b
}

type KVHTTPWatchSnapshot struct {
	Request KVHTTPWatchRequest
	KVHTTPWatchBatch
}

func (s KVHTTPWatchSnapshot) Clone() KVHTTPWatchSnapshot {
	s.KVHTTPWatchBatch = s.KVHTTPWatchBatch.Clone()
	return s
}

type KVHTTPWatchRun interface {
	Request() KVHTTPWatchRequest
	Poll() KVHTTPWatchBatch        // only the owning UI consumes this private buffer
	Snapshot() KVHTTPWatchSnapshot // complete immutable original evidence
	Stop(context.Context) error    // timeout does not imply executor acknowledgement
	Close() error                  // waits for acknowledgement, then releases this run's evidence
}

// Optional indexed access keeps the archive on its original run. PollStatus
// consumes the same private pending buffer as Poll without copying response
// bodies. Ended requires original executor acknowledgement. Observation uses
// a zero-based ordinal, never a native event/version.
type KVHTTPWatchStatus struct {
	Observations            int
	Ended, TerminalOverflow bool
	RetainedBytes           int64
	Terminal                KVHTTPWatchTerminal
}

type KVHTTPWatchIndexedRun interface {
	PollStatus() KVHTTPWatchStatus
	Observation(int) (KVHTTPWatchObservation, bool)
}

// RetainedWatch exposes the original settled archive after evidence-only
// recovery. It neither connects nor restarts the original request.
type KVHTTPWatchArchiveLease interface {
	RetainedWatch() KVHTTPWatchRun
}

type KVHTTPWatchLease interface {
	Scope() KVScope
	CheckKVLease() error
	Limits() KVHTTPWatchLimits
	Prepare(KVHTTPWatchRequest) (KVHTTPWatchPreparation, error) // no I/O
	Start(context.Context, KVHTTPWatchPreparation) (KVHTTPWatchRun, error)
	LastWatch() *KVHTTPWatchSnapshot
	ClearObservations() error // explicit, only after original executor has ended
	Close() error
}

type KVHTTPWatchRecovery struct {
	Lease KVHTTPWatchLease
	Watch *KVHTTPWatchSnapshot
}

// Optional independent document lease. Recovery transfers original evidence
// and reserves a fresh lease, without connecting or resuming a read.
type KVHTTPWatchProvider interface {
	KVScope() KVScope
	ReserveKVHTTPWatch(uint64) (KVHTTPWatchLease, error)
	RecoverKVHTTPWatch(KVHTTPWatchLease) (KVHTTPWatchRecovery, error)
}
