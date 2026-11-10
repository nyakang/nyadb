package etcd

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	renewalFrameLimit = 256
	renewalByteLimit  = 1 << 20
	renewalFrameBytes = 64 << 10
)

var ErrLeaseExpired = errors.New("original etcd lease expired or does not exist")

// RenewalPlan represents explicit authorization for one continuous run on one
// original connection/lease ID. It is separate from one-shot KV write plans.
type RenewalPlan struct {
	owner   *Client
	request *pb.LeaseKeepAliveRequest
	used    *atomic.Bool
}

func (p RenewalPlan) Request() *pb.LeaseKeepAliveRequest {
	if p.request == nil {
		return nil
	}
	return proto.Clone(p.request).(*pb.LeaseKeepAliveRequest)
}
func (c *Client) PrepareRenewal(request *pb.LeaseKeepAliveRequest) (RenewalPlan, error) {
	if request == nil || request.ID == 0 || len(request.ProtoReflect().GetUnknown()) != 0 {
		return RenewalPlan{}, ErrInvalid
	}
	if !c.Alive() {
		return RenewalPlan{}, ErrRetired
	}
	return RenewalPlan{owner: c, request: proto.Clone(request).(*pb.LeaseKeepAliveRequest), used: &atomic.Bool{}}, nil
}

// RenewalReceipt preserves each original attempt independently. A later TTL
// read cannot turn an unacknowledged renewal into a confirmed one.
type RenewalReceipt struct {
	Sequence                                  uint64
	Attempted, Completed, WriteOutcomeUnknown bool
	Native                                    []byte
	Header                                    Header
	LeaseID, TTL                              int64
	Code                                      codes.Code
}

func (r RenewalReceipt) clone() RenewalReceipt { r.Native = bytes.Clone(r.Native); return r }

type RenewalTerminal struct {
	Reason                                 string
	Cause                                  error
	Attempts, Acknowledged                 uint64
	WriteOutcomeUnknown, CaptureIncomplete bool
}
type bufferedRenewalReceipt struct {
	receipt RenewalReceipt
	charge  int
}

// RenewalRun never reopens its captured stream or sends an unconfirmed request
// again. Poll transfers complete receipts; Stop/Close await that executor.
type RenewalRun struct {
	client          *Client
	request         *pb.LeaseKeepAliveRequest
	stream          pb.Lease_LeaseKeepAliveClient
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	first           chan error
	mu              sync.Mutex
	receipts        []bufferedRenewalReceipt
	bytes           int
	stopping, ended bool
	terminal        RenewalTerminal
	reserve         func(int) bool
}

func (c *Client) StartRenewal(parent context.Context, plan RenewalPlan) (*RenewalRun, error) {
	return c.startRenewal(parent, plan, nil)
}
func (c *Client) startRenewal(parent context.Context, plan RenewalPlan, reserve func(int) bool) (*RenewalRun, error) {
	if plan.owner != c || plan.request == nil || plan.used == nil {
		return nil, ErrUsed
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if !c.operation.TryLock() {
		return nil, ErrBusy
	}
	if !c.Alive() {
		c.operation.Unlock()
		return nil, ErrRetired
	}
	if !plan.used.CompareAndSwap(false, true) {
		c.operation.Unlock()
		return nil, ErrUsed
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(c.ctx, cancel)
	stream, err := pb.NewLeaseClient(c.conn).LeaseKeepAlive(c.authContext(ctx))
	if err != nil {
		stop()
		cancel()
		c.operation.Unlock()
		return nil, err
	}
	r := &RenewalRun{client: c, request: plan.Request(), stream: stream, ctx: ctx, cancel: cancel, done: make(chan struct{}), first: make(chan error, 1), reserve: reserve}
	go r.execute(stop)
	// The run survives its first acknowledgement. Failure still returns its
	// owning receipt, including an original processed-but-unconfirmed attempt.
	err = <-r.first
	return r, err
}
func (r *RenewalRun) Request() *pb.LeaseKeepAliveRequest {
	return proto.Clone(r.request).(*pb.LeaseKeepAliveRequest)
}
func (r *RenewalRun) Terminal() (RenewalTerminal, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminal, r.ended
}
func (r *RenewalRun) Poll() []RenewalReceipt {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := min(len(r.receipts), 64)
	result := make([]RenewalReceipt, count)
	for i, v := range r.receipts[:count] {
		result[i] = v.receipt
		r.bytes -= v.charge
	}
	copy(r.receipts, r.receipts[count:])
	clear(r.receipts[len(r.receipts)-count:])
	r.receipts = r.receipts[:len(r.receipts)-count]
	return result
}
func (r *RenewalRun) Stop(_ context.Context) error {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
	r.cancel()
	// Even a canceled caller waits for the original driver acknowledgement.
	<-r.done
	t, _ := r.Terminal()
	return t.Cause
}
func (r *RenewalRun) Close() error {
	err := r.Stop(context.Background())
	r.mu.Lock()
	clear(r.receipts)
	r.receipts = nil
	r.bytes = 0
	r.mu.Unlock()
	return err
}
func renewalDelay(ttl int64) time.Duration {
	// Bound duration arithmetic while keeping even short leases below TTL.
	return max(100*time.Millisecond, time.Duration(min(max(ttl, 1), 86400))*time.Second/3)
}
func definiteRenewalError(err error) bool {
	switch status.Code(err) {
	case codes.InvalidArgument, codes.PermissionDenied, codes.Unauthenticated, codes.NotFound, codes.AlreadyExists, codes.OutOfRange, codes.FailedPrecondition:
		return true
	}
	return false
}
func (r *RenewalRun) append(receipt RenewalReceipt) error {
	charge := 128 + len(receipt.Native)*4
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.receipts) >= renewalFrameLimit || charge > renewalByteLimit-r.bytes || r.reserve != nil && !r.reserve(charge) {
		return ErrBounds
	}
	r.receipts = append(r.receipts, bufferedRenewalReceipt{receipt, charge})
	r.bytes += charge
	return nil
}
func (r *RenewalRun) observe(receipt *RenewalReceipt, response *pb.LeaseKeepAliveResponse) error {
	if response == nil {
		return ErrProtocol
	}
	if proto.Size(response) > renewalFrameBytes {
		return ErrBounds
	}
	raw, err := proto.Marshal(response)
	if err != nil {
		return err
	}
	receipt.Native = raw
	receipt.Header = header(response.Header)
	receipt.LeaseID = response.ID
	receipt.TTL = response.TTL
	h := response.Header
	// TTL=0 is an original terminal response, including servers that report a
	// zero revision. It never substitutes the current connection's header.
	ownedTerminal := response.TTL <= 0 && h != nil && h.Revision >= 0 && h.ClusterId == r.client.identity.ClusterID && h.MemberId == r.client.identity.MemberID && h.RaftTerm > 0
	if response.ID != r.request.ID || (!r.client.matches(h) && !ownedTerminal) {
		return ErrProtocol
	}
	receipt.Completed = true
	if response.TTL <= 0 {
		return ErrLeaseExpired
	}
	return nil
}
func (r *RenewalRun) execute(stop func() bool) {
	terminal := RenewalTerminal{Reason: "interrupted"}
	firstSent := false
	publishFirst := func(err error) {
		if !firstSent {
			firstSent = true
			r.first <- err
		}
	}
	defer func() {
		stop()
		r.cancel()
		// Cancellation must settle the original stream before another operation
		// can acquire this channel. No response here authorizes another send.
		for {
			if _, err := r.stream.Recv(); err != nil {
				break
			}
		}
		if terminal.WriteOutcomeUnknown || terminal.Reason == "protocol" || status.Code(terminal.Cause) == codes.Unavailable || status.Code(terminal.Cause) == codes.Unauthenticated {
			r.client.mu.Lock()
			r.client.retired = true
			r.client.mu.Unlock()
			r.client.closeConnection()
		}
		publishFirst(terminal.Cause)
		r.mu.Lock()
		r.terminal = terminal
		r.ended = true
		r.mu.Unlock()
		r.client.operation.Unlock()
		close(r.done)
	}()
	for {
		if err := r.ctx.Err(); err != nil {
			r.mu.Lock()
			stopped := r.stopping
			r.mu.Unlock()
			if stopped {
				terminal.Reason = "stopped"
			} else {
				terminal.Cause = err
			}
			return
		}
		terminal.Attempts++
		receipt := RenewalReceipt{Sequence: terminal.Attempts, Attempted: true, LeaseID: r.request.ID}
		timer := time.AfterFunc(r.client.config.RequestTimeout, r.cancel)
		err := r.stream.Send(proto.Clone(r.request).(*pb.LeaseKeepAliveRequest))
		if err == nil {
			var response *pb.LeaseKeepAliveResponse
			response, err = r.stream.Recv()
			if err == nil {
				err = r.observe(&receipt, response)
			}
		}
		timer.Stop()
		if receipt.Completed {
			terminal.Acknowledged++
		}
		if err != nil {
			receipt.Code = status.Code(err)
			receipt.WriteOutcomeUnknown = !receipt.Completed && !definiteRenewalError(err)
			terminal.Cause = err
			terminal.WriteOutcomeUnknown = receipt.WriteOutcomeUnknown
			switch {
			case errors.Is(err, ErrLeaseExpired):
				terminal.Reason = "expired"
			case errors.Is(err, ErrBounds):
				terminal.Reason = "overflow"
				terminal.CaptureIncomplete = true
			case errors.Is(err, ErrProtocol):
				terminal.Reason = "protocol"
			case definiteRenewalError(err):
				terminal.Reason = "rejected"
			}
		}
		if captureErr := r.append(receipt); captureErr != nil {
			terminal.Reason = "overflow"
			terminal.CaptureIncomplete = true
			terminal.Cause = errors.Join(err, captureErr)
			publishFirst(terminal.Cause)
			return
		}
		publishFirst(err)
		if err != nil {
			return
		}
		wait := time.NewTimer(renewalDelay(receipt.TTL))
		select {
		case <-wait.C:
		case <-r.ctx.Done():
			wait.Stop()
		}
	}
}
