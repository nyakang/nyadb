package qdrant

import (
	"context"
	"errors"
	"io"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (l *SnapshotLease) originalResult() SnapshotResult {
	r := SnapshotResult{ProfileID: l.session.ProfileID(), State: "indeterminate"}
	if l.pending != nil {
		r = cloneSnapshotResult(l.pending.last)
		r.Receipt = cloneSnapshotReceipt(l.pending.receipt)
		r.State = "indeterminate"
	}
	return r
}
func (l *SnapshotLease) keepResult(r SnapshotResult) {
	if l.pending != nil {
		r.Receipt = SnapshotReceipt{}
		l.pending.last = cloneSnapshotResult(r)
	}
}

type snapshotReadCloser struct {
	io.Reader
	close func() error
}

func (r snapshotReadCloser) Close() error { return r.close() }

type snapshotWriteCloser struct {
	io.Writer
	close func() error
}

func (w snapshotWriteCloser) Close() error { return w.close() }

func (l *SnapshotLease) Submit(ctx context.Context, p SessionSnapshotPlan) (SnapshotResult, error) {
	if p.Action() == "upload" || p.Action() == "download" || p.Action() == "stream" {
		return SnapshotResult{State: "not_submitted"}, ErrInvalid
	}
	return l.execute(ctx, p, nil, nil, SnapshotTransferOptions{})
}
func (l *SnapshotLease) Upload(ctx context.Context, p SessionSnapshotPlan, source io.ReadCloser, options SnapshotTransferOptions) (SnapshotResult, error) {
	if source == nil {
		return SnapshotResult{State: "not_submitted"}, ErrInvalid
	}
	return l.execute(ctx, p, source, nil, options)
}
func (l *SnapshotLease) Download(ctx context.Context, p SessionSnapshotPlan, sink io.WriteCloser, options SnapshotTransferOptions) (SnapshotResult, error) {
	if sink == nil {
		return SnapshotResult{State: "not_submitted"}, ErrInvalid
	}
	return l.execute(ctx, p, nil, sink, options)
}

// Streams are owned from admission, including during handshake and preflight.
// Closing the lease/session waits for their original Close acknowledgement.
func (l *SnapshotLease) execute(ctx context.Context, p SessionSnapshotPlan, source io.ReadCloser, sink io.WriteCloser, options SnapshotTransferOptions) (result SnapshotResult, err error) {
	result = SnapshotResult{ProfileID: l.session.ProfileID(), Action: p.Action(), Target: p.Target(), State: "not_submitted"}
	closeOwned := func() error { return nil }
	if source != nil {
		closeOwned = onceClose(source)
		source = snapshotReadCloser{Reader: source, close: closeOwned}
	} else if sink != nil {
		closeOwned = onceClose(sink)
		sink = snapshotWriteCloser{Writer: sink, close: closeOwned}
	}
	if !l.operation.TryLock() {
		_ = closeOwned()
		return result, ErrBusy
	}
	child, done, err := l.begin(ctx)
	if err != nil {
		_ = closeOwned()
		l.operation.Unlock()
		return result, err
	}
	stopIO := context.AfterFunc(child, func() { _ = closeOwned() })
	defer func() {
		stopIO()
		_ = closeOwned() // onceClose also joins an already-running callback.
		done()
		l.operation.Unlock()
	}()
	ctx = child
	if l.pending != nil {
		return l.originalResult(), contract.ErrNamespaceBusy
	}
	if p.owner != l || p.before == nil || p.before != l.observation || p.sequence != l.sequence || p.attempt == nil || p.attempt.Load() {
		return result, ErrSnapshotUsed
	}
	if p.Action() == "upload" && source == nil || p.Action() != "upload" && source != nil || (p.Action() == "download" || p.Action() == "stream") != (sink != nil) {
		return result, ErrInvalid
	}
	if source != nil || sink != nil {
		if err = options.validate(); err != nil {
			return result, err
		}
		if source != nil && p.native.upload.size > options.MaxBytes || p.selected != nil && sink != nil && p.selected.Size > options.MaxBytes {
			return result, ErrBounds
		}
	}
	read := sink != nil
	if !read {
		if err = l.claim(p); err != nil {
			return result, err
		}
	}
	defer func() {
		if l.pending == nil {
			l.releaseClaim()
		}
	}()
	// Invalidate even a preview whose preflight fails; it never dispatches later.
	l.sequence++
	l.observation = nil
	c, err := l.session.open(ctx)
	if err != nil {
		return result, err
	}
	defer c.Close()
	result.Metadata, err = l.read(ctx, c, p.Target())
	if err != nil {
		return result, err
	}
	if !snapshotPreflightMatches(p, result.Metadata) {
		return result, contract.ErrNamespaceChanged
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if !p.attempt.CompareAndSwap(false, true) {
		return result, ErrSnapshotUsed
	}
	if read {
		if p.Action() == "stream" {
			result.Transfer, err = c.StreamShardSnapshot(ctx, p.Target(), sink, options)
		} else {
			result.Transfer, err = c.DownloadSnapshot(ctx, p.Target(), *p.selected, sink, options)
		}
		result.State = "transfer_incomplete"
		if result.Transfer.Complete {
			result.State = "transferred"
		}
		return cloneSnapshotResult(result), err
	}
	l.pending = &snapshotPending{plan: p}
	defer func() { l.keepResult(result) }()
	if source != nil {
		result.Receipt, err = c.SubmitSnapshotUpload(ctx, p.native, source, options)
	} else {
		result.Receipt, err = c.SubmitSnapshot(ctx, p.native)
	}
	l.pending.receipt = cloneSnapshotReceipt(result.Receipt)
	var interrupted *Interrupted
	if err != nil && !errors.As(err, &interrupted) && result.Receipt.Response.Status == 0 {
		l.pending = nil
		return result, err
	}
	result.State = "uncertain"
	if result.Receipt.State == "enqueued" {
		result.State = "enqueued"
	}
	if result.Receipt.State == "completed" {
		result.State = "response"
	}
	return cloneSnapshotResult(result), err
}

// Verify performs original-session reads only. Recovery readback displays live
// collection metadata/count; it cannot prove complete archive/data equivalence.
// Unknown/admitted writes retain their original pending claim and cannot replay.
func (l *SnapshotLease) Verify(ctx context.Context) (result SnapshotResult, err error) {
	if !l.operation.TryLock() {
		return SnapshotResult{}, ErrBusy
	}
	defer l.operation.Unlock()
	result = l.originalResult()
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	if l.pending == nil {
		return result, ErrInvalid
	}
	defer func() { l.keepResult(result) }()
	c, err := l.session.open(ctx)
	if err != nil {
		return result, err
	}
	defer c.Close()
	p := l.pending.plan
	result.Matched, result.HasCount = false, false
	result.Metadata, err = l.read(ctx, c, p.Target())
	if err != nil {
		return result, err
	}
	if p.RestoresData() && snapshotPhysical(result.Metadata) {
		var response Response
		result.Count, response, err = c.Count(ctx, p.Target().Collection, nil)
		if bound := retainSnapshotRead(&result.Metadata, response); bound != nil {
			return result, bound
		}
		if err != nil {
			return result, err
		}
		result.HasCount = true
		if err = l.session.validateObservation(ctx, c); err != nil {
			return result, err
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	result.State = "uncertain"
	if result.Receipt.State == "enqueued" {
		result.State = "enqueued"
	}
	if result.Receipt.State != "completed" {
		return cloneSnapshotResult(result), nil
	}
	if p.RestoresData() {
		result.State = "observed"
		if !result.HasCount {
			result.State = "diverged"
			return cloneSnapshotResult(result), nil
		}
	} else {
		present, same := false, false
		name := ""
		if p.Action() == "create" && result.Receipt.Description != nil {
			name = result.Receipt.Description.Name
		}
		if p.Action() == "delete" && p.selected != nil {
			name = p.selected.Name
		}
		for _, d := range result.Metadata.Snapshots {
			if d.Name == name {
				present = true
				if p.Action() == "create" {
					same = sameSnapshotDescription(d, *result.Receipt.Description)
				} else {
					same = sameSnapshotDescription(d, *p.selected)
				}
			}
		}
		result.Matched = snapshotPhysical(result.Metadata)
		if p.Action() == "create" {
			result.Matched = result.Matched && same
		} else if result.Receipt.Result {
			result.Matched = result.Matched && !present
		} else {
			result.Matched = result.Matched && same
		}
		result.State = "diverged"
		if !result.Matched {
			return cloneSnapshotResult(result), nil
		}
		result.State = "applied"
		if result.Receipt.HasResult && !result.Receipt.Result {
			result.State = "unchanged"
		}
	}
	l.pending, l.observation = nil, nil
	l.releaseClaim()
	return cloneSnapshotResult(result), nil
}
