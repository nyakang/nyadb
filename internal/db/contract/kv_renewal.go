package contract

import (
	"context"
	"errors"
	"slices"
)

var ErrKVRenewalUncertain = errors.New("original renewal outcome is unknown; this document cannot authorize further writes")

// A preparation authorizes one continuous run on this exact native lease ID.
// It is distinct from a one-shot KV request and never authorizes LeaseGrant.
type KVRenewalRequest struct{ LeaseID int64 }
type KVRenewalPreparation interface {
	Snapshot() KVRenewalRequest
}

type KVRenewalReceipt struct {
	Sequence                                  uint64
	Attempted, Completed, WriteOutcomeUnknown bool
	Native                                    []byte
	JSON, JSONCaptureError, Code              string
	Header                                    KVHeader
	LeaseID, TTL                              int64
}

func (r KVRenewalReceipt) Clone() KVRenewalReceipt {
	r.Native = slices.Clone(r.Native)
	return r
}

type KVRenewalTerminal struct {
	Reason, Code, Message                  string
	Attempts, Acknowledged                 uint64
	WriteOutcomeUnknown, CaptureIncomplete bool
}
type KVRenewalBatch struct {
	Request  KVRenewalRequest
	Receipts []KVRenewalReceipt
	Ended    bool
	Terminal KVRenewalTerminal
}

func (b KVRenewalBatch) Clone() KVRenewalBatch {
	b.Receipts = slices.Clone(b.Receipts)
	for i := range b.Receipts {
		b.Receipts[i] = b.Receipts[i].Clone()
	}
	return b
}

type KVRenewalRun interface {
	Request() KVRenewalRequest
	Poll() KVRenewalBatch       // bounded transfer; retained receipts still charge their document
	Stop(context.Context) error // waits for the captured original executor
	Close() error
}
type KVRenewalLease interface {
	Scope() KVScope
	CheckKVLease() error
	Prepare(KVRenewalRequest) (KVRenewalPreparation, error) // pure, immutable and one-use
	Preconnect(context.Context) error
	Start(context.Context, KVRenewalPreparation) (KVRenewalRun, error)
	ClearReceipts() error // settled, known outcomes only; cannot erase an unknown write barrier
	Close() error
}
type KVRenewalRecovery struct {
	Lease KVRenewalLease
	Batch KVRenewalBatch // only receipts not already transferred to the original document
}
type KVRenewalProvider interface {
	KVScope() KVScope
	ReserveKVRenewal(uint64) (KVRenewalLease, error)            // no I/O
	RecoverKVRenewal(KVRenewalLease) (KVRenewalRecovery, error) // no renewal or reconnection
}
