package etcd

import (
	"bytes"
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (l *kvLease) permissions(parent context.Context) (*keyAccess, error) {
	if !l.ops.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	if err = l.connect(ctx); err != nil {
		return nil, err
	}
	a, err := l.client.permissions(ctx)
	if !l.client.Alive() {
		return nil, l.markLost(err)
	}
	return a, err
}
func (l *dataLease) captureAccess(ctx context.Context, scope int) (*keyAccess, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	if l.rangeLost {
		l.mu.Unlock()
		return nil, errDataRangeLost
	}
	if l.accessScopes[scope] {
		a := l.access
		l.mu.Unlock()
		return a, nil
	}
	l.mu.Unlock()
	a, err := l.scopes[scope].permissions(ctx)
	if err != nil {
		return nil, err
	}
	fingerprint := a.fingerprint()
	scoped := a.scoped(l.session.prefix)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, contract.ErrRevoked
	}
	if l.access == nil {
		l.access = scoped
		l.accessFingerprint = fingerprint
	}
	if l.accessFingerprint != fingerprint {
		l.rangeLost = true
		return nil, errAccessChanged
	}
	l.accessScopes[scope] = true
	return l.access, nil
}
func (l *dataLease) checkWriteAccess(ctx context.Context, prepared []preparedDataEdit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	a := l.access
	l.mu.Unlock()
	if a == nil {
		// Inserts retain a pure preview: use the original handshake's captured
		// grants. The actual owning write channel verifies them before dispatch.
		l.session.client.mu.Lock()
		original := l.session.client.access.clone()
		l.session.client.mu.Unlock()
		if original == nil {
			return ErrProtocol
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return contract.ErrRevoked
		}
		if l.access == nil {
			l.access = original.scoped(l.session.prefix)
			l.accessFingerprint = original.fingerprint()
		}
		a = l.access
		l.mu.Unlock()
	}
	for _, p := range prepared {
		if p.edit.Kind != "insert" && !allowsKey(a.Write, p.before.Key) {
			return status.Error(codes.PermissionDenied, "original key is read-only")
		}
		if p.edit.Kind != "delete" && !allowsKey(a.Write, p.after.Key) {
			return status.Error(codes.PermissionDenied, "destination key is outside writable read ranges")
		}
	}
	return nil
}

type keyIterator struct {
	ranges          []byteRange
	index           int
	start, previous []byte
	revision        int64
}

func newKeyIterator(ranges []byteRange, index int, start []byte, revision int64) *keyIterator {
	it := &keyIterator{ranges: normalizeRanges(ranges), index: index, start: bytes.Clone(start), revision: revision}
	if it.start == nil && index < len(it.ranges) {
		it.start = bytes.Clone(it.ranges[index].Start)
	}
	return it
}
func (it *keyIterator) next(ctx context.Context, l *dataLease, scope int, keysOnly bool) (*contract.KVEntry, int, error) {
	for it.index < len(it.ranges) {
		current := it.index
		r := it.ranges[current]
		request := r.request(it.start, 1, it.revision)
		request.KeysOnly = keysOnly
		result, err := l.native(ctx, scope, request)
		if err != nil {
			return nil, 0, err
		}
		if !result.Completed || result.Reply == nil || result.ProjectionError != "" || result.Reply.Kind != "range" || len(result.Reply.Entries) > 1 {
			return nil, 0, ErrProtocol
		}
		reply := result.Reply
		if len(reply.Entries) == 0 {
			if reply.More {
				return nil, 0, ErrProtocol
			}
			it.index++
			if it.index < len(it.ranges) {
				it.start = bytes.Clone(it.ranges[it.index].Start)
			}
			continue
		}
		entry := reply.Entries[0].Clone()
		if len(entry.Key) > 4096 {
			return nil, 0, ErrBounds
		}
		if !validDataEntry(entry) || !r.contains(entry.Key) || bytes.Compare(entry.Key, it.start) < 0 || it.previous != nil && bytes.Compare(entry.Key, it.previous) <= 0 || entry.ModRevision > it.revision {
			return nil, 0, ErrProtocol
		}
		it.previous = bytes.Clone(entry.Key)
		if reply.More {
			if len(entry.Key) == 4096 {
				it.start, err = it.nextAfterMaximum(ctx, l, scope, r, entry)
				if err != nil {
					return nil, 0, err
				}
			} else {
				it.start = AfterKey(entry.Key)
			}
			if it.start == nil || !r.contains(it.start) {
				return nil, 0, ErrProtocol
			}
		} else {
			it.index++
			if it.index < len(it.ranges) {
				it.start = bytes.Clone(it.ranges[it.index].Start)
			}
		}
		return &entry, current, nil
	}
	return nil, 0, ctx.Err()
}

// A bounded carry can skip legal server keys longer than our supported limit.
// Observe the next actual key with a bounded keys-only RPC before advancing.
// Oversized keys fail explicitly; they are never omitted from a completed scan.
func (it *keyIterator) nextAfterMaximum(ctx context.Context, l *dataLease, scope int, r byteRange, original contract.KVEntry) ([]byte, error) {
	request := r.request(original.Key, 2, it.revision)
	request.KeysOnly = true
	result, err := l.native(ctx, scope, request)
	if err != nil {
		return nil, err
	}
	if !result.Completed || result.Reply == nil || result.ProjectionError != "" || result.Reply.Kind != "range" || len(result.Reply.Entries) != 2 {
		return nil, ErrProtocol
	}
	first, next := result.Reply.Entries[0], result.Reply.Entries[1]
	if !bytes.Equal(first.Key, original.Key) || first.CreateRevision != original.CreateRevision || first.ModRevision != original.ModRevision || first.Version != original.Version || first.Lease != original.Lease || bytes.Compare(next.Key, original.Key) <= 0 || !r.contains(next.Key) {
		return nil, ErrProtocol
	}
	if len(next.Key) > 4096 {
		return nil, ErrBounds
	}
	if !validDataEntry(next) || next.ModRevision > it.revision {
		return nil, ErrProtocol
	}
	return bytes.Clone(next.Key), nil
}
