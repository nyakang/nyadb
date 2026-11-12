package contract

import (
	"context"
	"slices"
	"time"
)

// A bounded series of native HTTP PUT renewals for one original session UUID.
// Start first reads session/info and validates its TTL. Interval is measured
// after each acknowledged PUT; neither a run nor Stop promises an expiry time.
// This is write authorization, separate from a read Watch or an etcd lease.
type KVHTTPSessionRenewalRequest struct {
	SessionID   string
	Interval    time.Duration
	MaxRenewals int
	MaxBytes    int64
}
type KVHTTPSessionRenewalLimits struct {
	MinInterval, MaxInterval, ConnectTimeout, RequestTimeout time.Duration
	MaxRenewals                                              int
	MaxBytes, TerminalBytes                                  int64
}

type KVHTTPSessionRenewalPreparation interface {
	Snapshot() KVHTTPSessionRenewalRequest
}

// Session fields are an optional projection of the complete original body.
// Missing, invalid or changed fields stop further writes without replacing the
// native acknowledgement. Sequence zero is the initial GET, later values PUTs.
type KVHTTPSessionRenewalSession struct {
	ID, Name, Node, TTL, Behavior string
	LockDelay                     uint64
}
type KVHTTPSessionRenewalObservation struct {
	HTTP               KVHTTPResult
	Session            KVHTTPSessionRenewalSession
	Sequence           int
	ReceivedAt         time.Time
	ProjectionComplete bool
	ProjectionError    string
}

func (o KVHTTPSessionRenewalObservation) Clone() KVHTTPSessionRenewalObservation {
	o.HTTP = o.HTTP.Clone()
	return o
}

type KVHTTPSessionRenewalTerminal struct {
	Reason, Message        string
	Attempts, Acknowledged int
	WriteOutcomeUnknown    bool
}

// A final result that exceeds the authored byte limit is retained in the
// separately reserved terminal slot and ends the run. No evidence is evicted.
type KVHTTPSessionRenewalBatch struct {
	Observations     []KVHTTPSessionRenewalObservation
	Ended            bool
	TerminalOverflow bool
	RetainedBytes    int64
	Terminal         KVHTTPSessionRenewalTerminal
}

func (b KVHTTPSessionRenewalBatch) Clone() KVHTTPSessionRenewalBatch {
	b.Observations = slices.Clone(b.Observations)
	for i := range b.Observations {
		b.Observations[i] = b.Observations[i].Clone()
	}
	return b
}

type KVHTTPSessionRenewalSnapshot struct {
	Request KVHTTPSessionRenewalRequest
	KVHTTPSessionRenewalBatch
}

func (s KVHTTPSessionRenewalSnapshot) Clone() KVHTTPSessionRenewalSnapshot {
	s.KVHTTPSessionRenewalBatch = s.KVHTTPSessionRenewalBatch.Clone()
	return s
}

type KVHTTPSessionRenewalRun interface {
	Request() KVHTTPSessionRenewalRequest
	Poll() KVHTTPSessionRenewalBatch        // only the owning UI consumes this private buffer
	Snapshot() KVHTTPSessionRenewalSnapshot // complete immutable original evidence
	Stop(context.Context) error             // timeout does not imply executor acknowledgement
	Close() error                           // waits for ACK; unknown evidence can only be released by lease Close
}

// Optional indexed access keeps the archive on its original run. PollStatus
// consumes the same private pending buffer as Poll without copying response
// bodies. Ended requires original executor acknowledgement. Observation uses
// a zero-based ordinal, never a native event/version.
type KVHTTPSessionRenewalStatus struct {
	Observations            int
	Ended, TerminalOverflow bool
	RetainedBytes           int64
	Terminal                KVHTTPSessionRenewalTerminal
}

type KVHTTPSessionRenewalIndexedRun interface {
	PollStatus() KVHTTPSessionRenewalStatus
	Observation(int) (KVHTTPSessionRenewalObservation, bool)
}

// RetainedRenewal exposes the original settled archive after evidence-only
// recovery. It neither connects nor restarts the original request.
type KVHTTPSessionRenewalArchiveLease interface {
	RetainedRenewal() KVHTTPSessionRenewalRun
}

type KVHTTPSessionRenewalLease interface {
	Scope() KVScope
	CheckKVLease() error
	Limits() KVHTTPSessionRenewalLimits
	Prepare(KVHTTPSessionRenewalRequest) (KVHTTPSessionRenewalPreparation, error) // no I/O
	Start(context.Context, KVHTTPSessionRenewalPreparation) (KVHTTPSessionRenewalRun, error)
	LastRenewal() *KVHTTPSessionRenewalSnapshot
	ClearReceipts() error // explicit, only after known outcomes; cannot erase unknown writes
	Close() error
}

type KVHTTPSessionRenewalRecovery struct {
	Lease   KVHTTPSessionRenewalLease
	Renewal *KVHTTPSessionRenewalSnapshot
}

// Optional independent document lease. Hosts must share unknown-write barriers
// with other write tools of that same document. Recovery transfers original evidence
// and reserves a fresh lease, without connecting or resuming a write.
type KVHTTPSessionRenewalProvider interface {
	KVScope() KVScope
	ReserveKVHTTPSessionRenewal(uint64) (KVHTTPSessionRenewalLease, error)
	RecoverKVHTTPSessionRenewal(KVHTTPSessionRenewalLease) (KVHTTPSessionRenewalRecovery, error)
}
