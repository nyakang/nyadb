package etcd

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"google.golang.org/protobuf/proto"
)

const (
	watchBatchLimit = 256
	watchEventLimit = 10000
	watchByteLimit  = 8 << 20
)

// WatchFrame owns the complete original protobuf receipt. Its projection never
// substitutes a session header for a zero-revision compaction terminal receipt.
type WatchFrame struct {
	Native                      []byte
	Header                      Header
	WatchID                     int64
	Created, Canceled, Progress bool
	CompactRevision             int64
	CancelReason                string
	Events                      []*mvccpb.Event
}

func (f WatchFrame) clone() WatchFrame {
	f.Native = bytes.Clone(f.Native)
	f.Events = slices.Clone(f.Events)
	for i, e := range f.Events {
		f.Events[i] = proto.Clone(e).(*mvccpb.Event)
	}
	return f
}

type bufferedWatchFrame struct {
	frame WatchFrame
	bytes int
}

type WatchTerminal struct {
	Reason string
	Cause  error
}

// WatchRun owns one stream on its captured Client. Poll transfers bounded
// complete frames; stop waits for that original receive executor. There is no
// subscription recovery or stream recreation.
type WatchRun struct {
	client                                 *Client
	request                                *pb.WatchCreateRequest
	stream                                 pb.Watch_WatchClient
	ctx                                    context.Context
	cancel                                 context.CancelFunc
	done                                   chan struct{}
	created                                chan error
	send, mu                               sync.Mutex
	frames                                 []bufferedWatchFrame
	bufferedBytes, bufferedEvents          int
	started, stopping, ended               bool
	pendingProgress                        int
	minimumRevision, lastEvent, lastHeader int64
	terminal                               WatchTerminal
	reserve                                func(int) bool
}

func validateWatchRequest(r *pb.WatchCreateRequest) error {
	if r == nil || !validKey(r.Key) || len(r.RangeEnd) > 0 && !validEnd(r.Key, r.RangeEnd) || r.StartRevision < 0 || r.WatchId <= 0 || r.Fragment || len(r.ProtoReflect().GetUnknown()) > 0 || len(r.Filters) > 2 {
		return ErrInvalid
	}
	seen := map[pb.WatchCreateRequest_FilterType]bool{}
	for _, f := range r.Filters {
		if f < pb.WatchCreateRequest_NOPUT || f > pb.WatchCreateRequest_NODELETE || seen[f] {
			return ErrInvalid
		}
		seen[f] = true
	}
	return nil
}
func (c *Client) StartWatch(parent context.Context, request *pb.WatchCreateRequest) (*WatchRun, error) {
	if err := validateWatchRequest(request); err != nil {
		return nil, err
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if !c.operation.TryLock() {
		return nil, ErrBusy
	}
	return c.startWatchOwned(parent, request)
}

// startWatchOwned consumes operation ownership on every path. A returned run
// releases it only after the original receive executor acknowledges termination.
func (c *Client) startWatchOwned(parent context.Context, request *pb.WatchCreateRequest) (*WatchRun, error) {
	return c.startWatchOwnedBudget(parent, request, nil)
}

func (c *Client) startWatchOwnedBudget(parent context.Context, request *pb.WatchCreateRequest, reserve func(int) bool) (*WatchRun, error) {
	if !c.Alive() {
		c.operation.Unlock()
		return nil, ErrRetired
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(c.ctx, cancel)
	stream, err := pb.NewWatchClient(c.conn).Watch(c.authContext(ctx))
	if err != nil {
		stop()
		cancel()
		c.operation.Unlock()
		return nil, err
	}
	w := &WatchRun{minimumRevision: request.StartRevision, client: c, request: proto.Clone(request).(*pb.WatchCreateRequest), stream: stream, ctx: ctx, cancel: cancel, done: make(chan struct{}), created: make(chan error, 1)}
	w.reserve = reserve
	timer := time.AfterFunc(c.config.RequestTimeout, cancel)
	go w.receive(stop, timer)
	w.send.Lock()
	err = stream.Send(&pb.WatchRequest{RequestUnion: &pb.WatchRequest_CreateRequest{CreateRequest: proto.Clone(w.request).(*pb.WatchCreateRequest)}})
	w.send.Unlock()
	if err != nil {
		cancel()
		<-w.done
		return w, err
	}
	// A failed creation still returns its owning run and native terminal receipt.
	// The caller may inspect/Poll it but must never treat it as an active Watch.
	err = <-w.created
	return w, err
}
func (w *WatchRun) Request() *pb.WatchCreateRequest {
	return proto.Clone(w.request).(*pb.WatchCreateRequest)
}
func (w *WatchRun) Terminal() (WatchTerminal, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.terminal, w.ended
}
func (w *WatchRun) Poll() []WatchFrame {
	w.mu.Lock()
	defer w.mu.Unlock()
	count := min(len(w.frames), 64)
	out := make([]WatchFrame, count)
	for i, b := range w.frames[:count] {
		out[i] = b.frame
		w.bufferedBytes -= b.bytes
		w.bufferedEvents -= len(b.frame.Events)
	}
	copy(w.frames, w.frames[count:])
	clear(w.frames[len(w.frames)-count:])
	w.frames = w.frames[:len(w.frames)-count]
	return out
}
func (w *WatchRun) Progress() error {
	w.send.Lock()
	defer w.send.Unlock()
	w.mu.Lock()
	if !w.started || w.stopping || w.ended || w.pendingProgress >= 64 {
		w.mu.Unlock()
		return ErrBusy
	}
	w.pendingProgress++
	w.mu.Unlock()
	timer := time.AfterFunc(w.client.config.RequestTimeout, w.cancel)
	defer timer.Stop()
	err := w.stream.Send(&pb.WatchRequest{RequestUnion: &pb.WatchRequest_ProgressRequest{ProgressRequest: &pb.WatchProgressRequest{}}})
	if err != nil {
		w.cancel()
	}
	return err
}
func (w *WatchRun) Stop(parent context.Context) error {
	w.send.Lock()
	w.mu.Lock()
	ended, stopping := w.ended, w.stopping
	if !ended && !stopping {
		w.stopping = true
	}
	w.mu.Unlock()
	if !ended && !stopping {
		// Bound both Send and acknowledgement by the original request timeout.
		timer := time.AfterFunc(w.client.config.RequestTimeout, w.cancel)
		stop := context.AfterFunc(parent, w.cancel)
		err := w.stream.Send(&pb.WatchRequest{RequestUnion: &pb.WatchRequest_CancelRequest{CancelRequest: &pb.WatchCancelRequest{WatchId: w.request.WatchId}}})
		if err != nil {
			w.cancel()
		}
		w.send.Unlock()
		<-w.done
		timer.Stop()
		stop()
	} else {
		w.send.Unlock()
		<-w.done
	}
	terminal, _ := w.Terminal()
	return terminal.Cause
}
func (w *WatchRun) Close() error {
	w.cancel()
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	clear(w.frames)
	w.frames = nil
	w.bufferedBytes = 0
	w.bufferedEvents = 0
	return w.terminal.Cause
}
func (w *WatchRun) receive(stop func() bool, timer *time.Timer) {
	creationSent := false
	publishCreation := func(err error) {
		if !creationSent {
			creationSent = true
			timer.Stop()
			w.created <- err
		}
	}
	reason := "interrupted"
	var terminalErr error
	defer func() {
		timer.Stop()
		stop()
		w.cancel()
		// Wait for the original driver to acknowledge stream cancellation before
		// releasing operation ownership. Queued frames after termination are revoked.
		for {
			if _, err := w.stream.Recv(); err != nil {
				break
			}
		}
		if terminalErr != nil && reason != "compacted" && reason != "server-canceled" && reason != "overflow" {
			w.client.mu.Lock()
			w.client.retired = true
			w.client.mu.Unlock()
			w.client.closeConnection()
		}
		publishCreation(terminalErr)
		w.mu.Lock()
		w.terminal = WatchTerminal{Reason: reason, Cause: terminalErr}
		w.ended = true
		w.mu.Unlock()
		w.client.operation.Unlock()
		close(w.done)
	}()
	for {
		reply, err := w.stream.Recv()
		if err != nil {
			terminalErr = err
			return
		}
		frame, err := w.observe(reply)
		if err != nil {
			reason = "protocol"
			if errors.Is(err, ErrBounds) {
				reason = "overflow"
			}
			terminalErr = err
			return
		}
		if err = w.append(frame); err != nil {
			reason = "overflow"
			terminalErr = err
			return
		}
		if reply.Created && !reply.Canceled {
			publishCreation(nil)
		}
		if reply.Canceled {
			if reply.CompactRevision > 0 {
				reason = "compacted"
				terminalErr = errors.New("original watch revision compacted")
			} else {
				w.mu.Lock()
				stopping := w.stopping
				w.mu.Unlock()
				if stopping {
					reason = "stopped"
				} else {
					reason = "server-canceled"
					terminalErr = errors.New("original watch canceled: " + reply.CancelReason)
				}
			}
			publishCreation(terminalErr)
			return
		}
	}
}
func (w *WatchRun) append(frame WatchFrame) error {
	charge := len(frame.Native)*4 + 512*len(frame.Events) + 128
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.frames) >= watchBatchLimit || w.bufferedEvents+len(frame.Events) > watchEventLimit || charge > watchByteLimit || w.bufferedBytes+charge > watchByteLimit {
		return ErrBounds
	}
	if w.reserve != nil && !w.reserve(charge) {
		return ErrBounds
	}
	w.frames = append(w.frames, bufferedWatchFrame{frame: frame, bytes: charge})
	w.bufferedBytes += charge
	w.bufferedEvents += len(frame.Events)
	return nil
}
func (w *WatchRun) observe(r *pb.WatchResponse) (WatchFrame, error) {
	if r == nil || r.Fragment {
		return WatchFrame{}, ErrProtocol
	}
	if len(r.Events) > watchEventLimit || proto.Size(r) > watchByteLimit {
		return WatchFrame{}, ErrBounds
	}
	// Serialize before projection: unknown fields remain in the original receipt.
	raw, err := proto.Marshal(r)
	if err != nil {
		return WatchFrame{}, err
	}
	if len(raw) > watchByteLimit {
		return WatchFrame{}, ErrBounds
	}
	f := WatchFrame{Native: raw, Header: header(r.Header), WatchID: r.WatchId, Created: r.Created, Canceled: r.Canceled, CompactRevision: r.CompactRevision, CancelReason: r.CancelReason}
	w.mu.Lock()
	defer w.mu.Unlock()
	if r.Canceled {
		failedCreation := !w.started && r.Created && r.WatchId == -1 && r.CompactRevision == 0
		compacted := w.started && !r.Created && r.WatchId == w.request.WatchId && r.CompactRevision > 0 && r.Header != nil && r.Header.Revision == 0 && r.Header.ClusterId == w.client.identity.ClusterID && r.Header.MemberId == w.client.identity.MemberID && r.Header.RaftTerm > 0
		owned := w.started && !r.Created && r.WatchId == w.request.WatchId
		if len(r.Events) > 0 || r.CompactRevision < 0 || (!failedCreation && !owned) || (!compacted && !w.client.matches(r.Header)) {
			return WatchFrame{}, ErrProtocol
		}
		return f, nil
	}
	if r.CompactRevision != 0 || r.CancelReason != "" || !w.client.matches(r.Header) || r.Header.Revision < w.lastHeader {
		return WatchFrame{}, ErrProtocol
	}
	w.lastHeader = r.Header.Revision
	if r.Created {
		if w.started || r.WatchId != w.request.WatchId || len(r.Events) > 0 {
			return WatchFrame{}, ErrProtocol
		}
		w.started = true
		if w.request.StartRevision == 0 {
			w.minimumRevision = r.Header.Revision + 1
			if w.minimumRevision <= 0 {
				return WatchFrame{}, ErrProtocol
			}
		}
		return f, nil
	}
	if !w.started {
		return WatchFrame{}, ErrProtocol
	}
	if r.WatchId == -1 {
		if len(r.Events) != 0 || w.pendingProgress == 0 {
			return WatchFrame{}, ErrProtocol
		}
		w.pendingProgress--
		f.Progress = true
		return f, nil
	}
	if r.WatchId != w.request.WatchId {
		return WatchFrame{}, ErrProtocol
	}
	if len(r.Events) == 0 {
		if !w.request.ProgressNotify {
			return WatchFrame{}, ErrProtocol
		}
		f.Progress = true
		return f, nil
	}
	scope := exactRange(w.request.Key)
	if len(w.request.RangeEnd) > 0 {
		scope = byteRange{w.request.Key, w.request.RangeEnd}
	}
	for _, event := range r.Events {
		if event == nil || event.Kv == nil || event.Type < mvccpb.PUT || event.Type > mvccpb.DELETE {
			return WatchFrame{}, ErrProtocol
		}
		kv := event.Kv
		if len(kv.Key) > 4096 {
			return WatchFrame{}, ErrBounds
		}
		if !validKey(kv.Key) || !scope.contains(kv.Key) || kv.ModRevision <= 0 || kv.ModRevision < w.minimumRevision || kv.ModRevision < w.lastEvent || kv.ModRevision > r.Header.Revision {
			return WatchFrame{}, ErrProtocol
		}
		if event.Type == mvccpb.PUT && (kv.CreateRevision <= 0 || kv.CreateRevision > kv.ModRevision || kv.Version <= 0) {
			return WatchFrame{}, ErrProtocol
		}
		if event.Type == mvccpb.DELETE && (len(kv.Value) != 0 || kv.CreateRevision != 0 || kv.Version != 0 || kv.Lease != 0) {
			return WatchFrame{}, ErrProtocol
		}
		for _, filter := range w.request.Filters {
			if filter == pb.WatchCreateRequest_NOPUT && event.Type == mvccpb.PUT || filter == pb.WatchCreateRequest_NODELETE && event.Type == mvccpb.DELETE {
				return WatchFrame{}, ErrProtocol
			}
		}
		if previous := event.PrevKv; previous != nil {
			if !w.request.PrevKv || !bytes.Equal(previous.Key, kv.Key) || previous.CreateRevision <= 0 || previous.ModRevision < previous.CreateRevision || previous.ModRevision >= kv.ModRevision || previous.Version <= 0 {
				return WatchFrame{}, ErrProtocol
			}
		}
		w.lastEvent = kv.ModRevision
		f.Events = append(f.Events, proto.Clone(event).(*mvccpb.Event))
	}
	return f, nil
}
