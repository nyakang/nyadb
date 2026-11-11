package etcd

import (
	"context"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

const snapshotMetadataReservation = 200 << 20
const snapshotMetadataSessionBytes = 512 << 20

type kvSnapshotPreparation struct {
	authority *kvSnapshotPreparation
	owner     *kvLease
	request   contract.KVSnapshotRequest
	scope     contract.KVSnapshotScope
	used      *atomic.Bool
}

func (p *kvSnapshotPreparation) Snapshot() contract.KVSnapshotRequest { return p.request }
func (p *kvSnapshotPreparation) Scope() contract.KVSnapshotScope      { return p.scope.Clone() }
func snapshotLimits(r contract.KVSnapshotRequest) SnapshotLimits {
	return SnapshotLimits{BodyBytes: r.BodyBytes, JournalBytes: r.JournalBytes, Frames: r.Frames}
}
func (l *kvLease) PrepareSnapshot(r contract.KVSnapshotRequest) (contract.KVSnapshotPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	if !snapshotLimits(r).valid() {
		return nil, ErrInvalid
	}
	p := &kvSnapshotPreparation{owner: l, request: r, scope: contract.KVSnapshotScope{Original: l.Scope(), DocumentID: l.document}, used: &atomic.Bool{}}
	p.authority = p
	return p, nil
}
func snapshotLeaseErrorText(err error) string {
	text := snapshotErrorText(err)
	if len(text) > 65536 {
		return text[:65536] + " [local diagnostic truncated]"
	}
	return text
}
func snapshotReceiptBytes(r contract.KVSnapshotResult) int {
	return 2 * (4096 + len(r.Scope.Original.Prefix) + len(r.NativeStatus) + len(r.RejectedNative) + len(r.BodySHA256) + len(r.Checksum) + len(r.JournalSHA256) + len(r.NativeCode) + len(r.StorageVersion) + len(r.LocalError) + len(r.CaptureError) + resultBytes(r.Before))
}
func (l *kvLease) reserveSnapshotMetadata() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	if l.closed || l.lost || l.s.closed || l.s.revoked {
		return contract.ErrRevoked
	}
	if snapshotMetadataReservation > snapshotMetadataSessionBytes-l.s.snapshotRetainedBytes {
		return ErrBounds
	}
	l.s.snapshotRetainedBytes += snapshotMetadataReservation
	return nil
}
func (l *kvLease) finishSnapshotReservation(result *contract.KVSnapshotResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	l.s.snapshotRetainedBytes -= snapshotMetadataReservation
	if result != nil && result.Ended {
		l.s.snapshotRetainedBytes -= l.snapshotCharged
		l.lastSnapshot = result.Clone()
		l.snapshotCharged = snapshotReceiptBytes(l.lastSnapshot)
		l.s.snapshotRetainedBytes += l.snapshotCharged
	}
}
func (l *kvLease) releaseSnapshotMetadataOwned() {
	l.mu.Lock()
	l.s.mu.Lock()
	l.s.snapshotRetainedBytes -= l.snapshotCharged
	l.snapshotCharged = 0
	l.lastSnapshot = contract.KVSnapshotResult{}
	l.s.mu.Unlock()
	l.mu.Unlock()
}
func (l *kvLease) LastSnapshotResult() contract.KVSnapshotResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastSnapshot.Clone()
}
func (l *kvLease) ExecuteSnapshot(parent context.Context, prepared contract.KVSnapshotPreparation, outputs contract.KVSnapshotOutputs) (out contract.KVSnapshotResult, err error) {
	p, ok := prepared.(*kvSnapshotPreparation)
	if !ok || p == nil || p.authority != p || p.owner != l || p.used == nil {
		return out, contract.ErrKVPlanUsed
	}
	out.Scope, out.Request = p.Scope(), p.request
	if err = parent.Err(); err != nil {
		return out, err
	}
	nativeOutputs := SnapshotOutputs{Snapshot: outputs.Snapshot, Journal: outputs.Journal}
	if !nativeOutputs.valid() {
		return out, ErrInvalid
	}
	if !l.ops.TryLock() {
		return out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	if p.used.Load() {
		return out, contract.ErrKVPlanUsed
	}
	ctx, done, err := l.request(parent)
	if err != nil {
		return out, err
	}
	defer done()
	if err = l.reserveSnapshotMetadata(); err != nil {
		return out, err
	}
	defer func() { l.finishSnapshotReservation(&out) }()
	if err = l.connect(ctx); err != nil {
		return out, err
	}
	c := l.client
	native, err := c.PrepareSnapshot(&pb.SnapshotRequest{}, snapshotLimits(p.request))
	if err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if !p.used.CompareAndSwap(false, true) {
		return out, contract.ErrKVPlanUsed
	}
	r, err := c.ExecuteSnapshot(ctx, native, nativeOutputs)
	// Do not overwrite LastResult or any unknown-write state. This independent
	// native source receipt cannot settle an earlier KV/member/Alarm write.
	out = contract.KVSnapshotResult{Scope: p.Scope(), Request: p.request, Attempted: r.Attempted, Ended: r.Ended, Completed: r.Completed, NativeTerminalObserved: r.NativeTerminalObserved, EOF: r.EOF, SourceComplete: r.SourceComplete, ChecksumMatched: r.ChecksumMatched, JournalComplete: r.JournalComplete, OutputAcknowledged: r.OutputAcknowledged, Canceled: r.Canceled, Frames: r.Frames, ExpectedBodyBytes: r.ExpectedBodyBytes, BodyBytes: r.BodyBytes, SnapshotBytesWritten: r.SnapshotBytesWritten, JournalBytesWritten: r.JournalBytesWritten, StorageVersion: r.StorageVersion, BodySHA256: r.BodySHA256, Checksum: r.Checksum, JournalSHA256: r.JournalSHA256, NativeStatus: r.NativeStatus, RejectedNative: r.RejectedNative, NativeCode: r.NativeCode.String(), LocalError: snapshotLeaseErrorText(r.LocalError), CaptureError: snapshotLeaseErrorText(r.CaptureError), Before: projectKVResult(memberStatusRequest, r.Before())}.Clone()
	c.releaseLastSnapshot()
	if !r.Ended {
		p.used.Store(false)
	}
	if !c.Alive() {
		err = l.markLost(err)
	}
	return out, err
}

var _ contract.KVSnapshots = (*kvLease)(nil)
