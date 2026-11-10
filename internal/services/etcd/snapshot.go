package etcd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// SnapshotLimits are captured finite output/stream bounds. Defaults support
// signed native file offsets; callers may reserve smaller explicit disk budgets.
type SnapshotLimits struct {
	BodyBytes    uint64 `json:"body_bytes,string"`
	JournalBytes uint64 `json:"journal_bytes,string"`
	Frames       uint64 `json:"frames,string"`
}

func DefaultSnapshotLimits() SnapshotLimits {
	return SnapshotLimits{BodyBytes: snapshotBodyLimit, JournalBytes: math.MaxInt64, Frames: snapshotFrameLimit}
}
func (l SnapshotLimits) valid() bool {
	return l.BodyBytes > 0 && l.BodyBytes <= snapshotBodyLimit && l.JournalBytes > 0 && l.JournalBytes <= math.MaxInt64 && l.Frames >= 2 && l.Frames <= snapshotFrameLimit
}

// SnapshotPlan captures the exact original empty request, original client and
// one-use flag. Native and Limits are inspection copies, never write authority.
type SnapshotPlan struct {
	owner   *Client
	request *pb.SnapshotRequest
	limits  SnapshotLimits
	used    *atomic.Bool
}

func (p SnapshotPlan) Native() *pb.SnapshotRequest {
	if p.request == nil {
		return nil
	}
	return proto.Clone(p.request).(*pb.SnapshotRequest)
}
func (p SnapshotPlan) Limits() SnapshotLimits { return p.limits }
func (c *Client) PrepareSnapshot(request *pb.SnapshotRequest, limits SnapshotLimits) (SnapshotPlan, error) {
	if !c.Alive() {
		return SnapshotPlan{}, ErrRetired
	}
	if request == nil || len(request.ProtoReflect().GetUnknown()) != 0 || !limits.valid() {
		return SnapshotPlan{}, ErrInvalid
	}
	return SnapshotPlan{owner: c, request: proto.Clone(request).(*pb.SnapshotRequest), limits: limits, used: &atomic.Bool{}}, nil
}

// SnapshotOutputs are captured by value before execution. Writers are distinct,
// caller-owned original resources and obey io.Writer's immutable-buffer rule.
// Execution/Close wait for their acknowledgement and never close a caller file.
// Snapshot receives the native body plus checksum; Journal retains full frames.
type SnapshotOutputs struct{ Snapshot, Journal io.Writer }

func snapshotWriterNil(w io.Writer) bool {
	if w == nil {
		return true
	}
	v := reflect.ValueOf(w)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}
func (o SnapshotOutputs) valid() bool {
	if snapshotWriterNil(o.Snapshot) || snapshotWriterNil(o.Journal) {
		return false
	}
	a, b := reflect.ValueOf(o.Snapshot), reflect.ValueOf(o.Journal)
	return a.Type() != b.Type() || !a.Comparable() || a.Interface() != b.Interface()
}

// SnapshotResult separates original source proof, acknowledged byte writes,
// native frame capture and cancellation. Completed does not publish/fsync a
// user file; that later boundary belongs to the original file request owner.
type SnapshotResult struct {
	Identity                                                                        Identity
	Limits                                                                          SnapshotLimits
	Attempted, Ended, Completed                                                     bool
	NativeTerminalObserved, EOF, SourceComplete, ChecksumMatched                    bool
	JournalComplete, OutputAcknowledged, Canceled                                   bool
	Frames, ExpectedBodyBytes, BodyBytes, SnapshotBytesWritten, JournalBytesWritten uint64
	StorageVersion                                                                  string
	BodySHA256, Checksum, JournalSHA256                                             []byte
	NativeStatus, RejectedNative                                                    []byte
	NativeCode                                                                      codes.Code
	LocalError, CaptureError                                                        error
	before                                                                          Response
}

func (r SnapshotResult) Before() Response { return r.before.clone() }
func (r SnapshotResult) clone() SnapshotResult {
	r.before = r.before.clone()
	r.BodySHA256, r.Checksum, r.JournalSHA256 = bytes.Clone(r.BodySHA256), bytes.Clone(r.Checksum), bytes.Clone(r.JournalSHA256)
	r.NativeStatus, r.RejectedNative = bytes.Clone(r.NativeStatus), bytes.Clone(r.RejectedNative)
	return r
}
func (c *Client) LastSnapshot() SnapshotResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSnapshot.clone()
}
func (c *Client) releaseLastSnapshot() { c.mu.Lock(); c.lastSnapshot = SnapshotResult{}; c.mu.Unlock() }

func (c *Client) snapshotBefore(parent context.Context) (r Response, nativeError error, validation error) {
	ctx, cancel := context.WithTimeout(parent, c.config.RequestTimeout)
	defer cancel()
	r.Attempted = true
	v, err := pb.NewMaintenanceClient(c.conn).Status(c.authContext(ctx), &pb.StatusRequest{})
	r.Code = status.Code(err)
	if v != nil {
		r.native, r.Header = proto.Clone(v), header(v.Header)
	}
	if err != nil {
		return r, err, err
	}
	if v == nil || !c.matches(v.Header) || v.Version != c.identity.Version {
		return r, nil, ErrProtocol
	}
	r.Completed = true
	return r, nil, nil
}

func nativeSnapshotStatus(err error) ([]byte, error) {
	if err == nil || err == io.EOF {
		return nil, nil
	}
	v := status.Convert(err).Proto()
	if proto.Size(v) > responseLimit {
		return nil, ErrBounds
	}
	return proto.Marshal(v)
}

// Official 3.5 Snapshot reports this definite maintenance rejection using
// the legacy Unknown code. Preserve that native status, and recognize only
// this original version/RPC/exact-message combination as a definite denial.
func (c *Client) definiteSnapshotRejection(err error) bool {
	s := status.Convert(err)
	return strings.HasPrefix(c.identity.Version, "3.5.") && s.Code() == codes.Unknown && s.Message() == "auth: permission denied"
}

// ExecuteSnapshot owns the captured transport across Status, every original
// receive and every output write. It uses a raw no-retry stream, never the
// clientv3 retry/concatenation helper, resumption or a different endpoint.
func (c *Client) ExecuteSnapshot(parent context.Context, plan SnapshotPlan, outputs SnapshotOutputs) (r SnapshotResult, err error) {
	if err = parent.Err(); err != nil {
		return r, err
	}
	if !outputs.valid() {
		return r, ErrInvalid
	}
	if !c.operation.TryLock() {
		return r, ErrBusy
	}
	defer c.operation.Unlock()
	if !c.Alive() {
		return r, ErrRetired
	}
	if plan.owner != c || plan.used == nil || !plan.used.CompareAndSwap(false, true) {
		return r, ErrUsed
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	r.Identity, r.Limits = c.identity, plan.limits
	r.OutputAcknowledged = true
	proof, _ := newSnapshotProof(c.identity, plan.limits.BodyBytes, plan.limits.Frames)
	journal := newSnapshotJournalWriter(outputs.Journal, plan.limits.JournalBytes)
	var nativeError error
	defer func() {
		r.Frames, r.ExpectedBodyBytes, r.BodyBytes = proof.frames, proof.expected, proof.body
		r.StorageVersion, r.ChecksumMatched, r.EOF = proof.version, proof.matched, proof.eof
		r.Checksum, r.BodySHA256 = bytes.Clone(proof.checksum), proof.digest.Sum(nil)
		r.SourceComplete = proof.started && proof.matched && proof.eof && proof.remaining == 0 && proof.body == proof.expected
		r.Canceled = ctx.Err() != nil
		terminal := snapshotJournalTerminal{Attempted: r.Attempted, NativeTerminalObserved: r.NativeTerminalObserved, EOF: r.EOF, SourceComplete: r.SourceComplete, ChecksumMatched: r.ChecksumMatched, OutputAcknowledged: r.OutputAcknowledged, Canceled: r.Canceled, NativeStatus: r.NativeStatus, NativeCode: r.NativeCode, Frames: r.Frames, ExpectedBodyBytes: r.ExpectedBodyBytes, BodyBytes: r.BodyBytes, SnapshotBytesWritten: r.SnapshotBytesWritten, BodySHA256: r.BodySHA256, Checksum: r.Checksum, StorageVersion: r.StorageVersion, LocalStop: snapshotErrorText(r.LocalError), OutputError: snapshotErrorText(r.CaptureError)}
		if e := journal.record(snapshotJournalEnd, terminal); e != nil {
			r.CaptureError = errors.Join(r.CaptureError, e)
			err = errors.Join(err, e)
		} else {
			r.JournalComplete = true
		}
		r.JournalBytesWritten, r.JournalSHA256 = journal.written, journal.digest.Sum(nil)
		r.Canceled = ctx.Err() != nil
		if r.Canceled {
			r.LocalError = errors.Join(r.LocalError, ctx.Err())
			err = errors.Join(err, ctx.Err())
		}
		r.Completed = err == nil && r.SourceComplete && r.OutputAcknowledged && r.JournalComplete && !r.Canceled && r.SnapshotBytesWritten == r.ExpectedBodyBytes+sha256.Size
		r.Ended = true
		code := status.Code(nativeError)
		definite := code == codes.PermissionDenied || code == codes.InvalidArgument || code == codes.NotFound || code == codes.FailedPrecondition || code == codes.OutOfRange || c.definiteSnapshotRejection(nativeError)
		if r.Canceled || errors.Is(err, ErrProtocol) || code == codes.Unauthenticated || code == codes.Unavailable || code == codes.Canceled || code == codes.DeadlineExceeded || code == codes.ResourceExhausted || r.Attempted && err != nil && !definite {
			c.mu.Lock()
			c.retired = true
			c.mu.Unlock()
			cancel()
			c.closeConnection()
			if r.Attempted {
				err = &Interrupted{Cause: err}
			}
		}
		c.mu.Lock()
		c.lastSnapshot = r.clone()
		c.mu.Unlock()
	}()
	var beforeNativeError error
	r.before, beforeNativeError, err = c.snapshotBefore(ctx)
	origin := snapshotOrigin(c.identity, plan.limits, r.before, beforeNativeError)
	if e := journal.start(origin); e != nil {
		r.CaptureError = e
		return r, errors.Join(err, e)
	}
	if err != nil {
		nativeError = beforeNativeError
		r.LocalError = err
		return r, err
	}
	if err = ctx.Err(); err != nil {
		r.LocalError = err
		return r, err
	}
	r.Attempted = true
	timer := time.AfterFunc(c.config.RequestTimeout, cancel)
	stream, e := pb.NewMaintenanceClient(c.conn).Snapshot(c.authContext(ctx), plan.Native())
	timer.Stop()
	if e != nil {
		r.NativeTerminalObserved, r.NativeCode, nativeError = true, status.Code(e), e
		r.NativeStatus, r.CaptureError = nativeSnapshotStatus(e)
		return r, errors.Join(e, r.CaptureError)
	}
	for {
		timer := time.AfterFunc(c.config.RequestTimeout, cancel)
		frame, receiveError := stream.Recv()
		timer.Stop()
		if receiveError != nil {
			r.NativeTerminalObserved, nativeError = true, receiveError
			if receiveError != io.EOF {
				r.NativeCode = status.Code(receiveError)
			}
			r.NativeStatus, r.CaptureError = nativeSnapshotStatus(receiveError)
			err = proof.finish(receiveError, ctx)
			if err != nil {
				r.LocalError = err
			}
			return r, errors.Join(err, r.CaptureError)
		}
		if frame == nil {
			r.LocalError = ErrProtocol
			return r, ErrProtocol
		}
		if proto.Size(frame) > responseLimit {
			r.LocalError = ErrBounds
			return r, ErrBounds
		}
		raw, marshalError := proto.Marshal(frame)
		if marshalError != nil {
			r.CaptureError = marshalError
			return r, marshalError
		}
		if e := journal.frame(raw); e != nil {
			r.RejectedNative, r.CaptureError = raw, e
			proof.frames++
			return r, e
		}
		if _, e := proof.accept(frame); e != nil {
			r.RejectedNative, r.LocalError = raw, e
			return r, e
		}
		if e := ctx.Err(); e != nil {
			r.RejectedNative, r.LocalError = raw, e
			return r, e
		}
		n, writeError := outputs.Snapshot.Write(frame.Blob)
		if n < 0 || n > len(frame.Blob) {
			n, writeError = 0, ErrProtocol
		}
		r.SnapshotBytesWritten += uint64(n)
		if writeError == nil && n != len(frame.Blob) {
			writeError = io.ErrShortWrite
		}
		if writeError != nil {
			r.RejectedNative, r.CaptureError, r.OutputAcknowledged = raw, writeError, false
			return r, writeError
		}
	}
}

func snapshotErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
