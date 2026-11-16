package agentbridge

import (
	"context"
	"sync"
	"sync/atomic"
)

// SingleSession owns one v1 agent and one connection attempt. It has no opaque
// v2 session IDs, rebinding, native cancel RPC or automatic restart. All requests
// share its gate; canceling a queued request never stops the active executor.
type SingleSession struct {
	client    *Client
	gate      chan struct{}
	bound     atomic.Bool
	connected atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// StartSingleSession explicitly admits v1, without relaxing Start's v2
// contract. Product adapters must disable native write retries in their captured
// connection configuration when the original driver otherwise retries writes.
func StartSingleSession(ctx context.Context, spec LaunchSpec, options Options) (*SingleSession, error) {
	c, err := start(ctx, spec, options, 1)
	if err != nil {
		return nil, err
	}
	s := &SingleSession{client: c, gate: make(chan struct{}, 1)}
	s.gate <- struct{}{}
	return s, nil
}

func (s *SingleSession) PID() int                { return s.client.PID() }
func (s *SingleSession) Exited() <-chan struct{} { return s.client.Exited() }
func (s *SingleSession) Alive() bool             { return s.client.Alive() }
func (s *SingleSession) Handshake() Handshake    { return s.client.Handshake() }

func (s *SingleSession) enter(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.client.done:
		return ErrClosed
	case <-s.gate:
	}
	if ctx.Err() != nil || !s.Alive() {
		s.leave()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrClosed
	}
	return nil
}

func (s *SingleSession) leave() { s.gate <- struct{}{} }

// Connect snapshots parameters before waiting and binds this process only
// once. A dispatched failure cannot be retried on this original process.
func (s *SingleSession) Connect(ctx context.Context, params any) (Receipt, error) {
	if ctx == nil {
		return Receipt{}, ErrRequest
	}
	raw, err := snapshotParams(params, "")
	if err != nil {
		return Receipt{}, err
	}
	if err = s.enter(ctx); err != nil {
		return Receipt{}, err
	}
	defer s.leave()
	if s.bound.Load() {
		return Receipt{}, ErrClosed
	}
	r, err := s.client.call(ctx, "connect", raw, "", false)
	if r.Dispatched {
		s.bound.Store(true)
	}
	if err == nil && !receiptOK(r.Result) {
		err = ErrProtocol
		s.client.stop(err)
		<-s.client.processDone
		r.ProcessExitConfirmed = true
	}
	if err == nil && s.Alive() {
		s.connected.Store(true)
	}
	return r, err
}

// TestConnection runs an isolated original-agent probe before binding. It
// neither publishes a session nor reconnects an existing one.
func (s *SingleSession) TestConnection(ctx context.Context, params any) (Receipt, error) {
	if ctx == nil || !s.Handshake().Supports("test_connection") {
		return Receipt{}, ErrRequest
	}
	raw, err := snapshotParams(params, "")
	if err != nil {
		return Receipt{}, err
	}
	if err = s.enter(ctx); err != nil {
		return Receipt{}, err
	}
	defer s.leave()
	if s.bound.Load() {
		return Receipt{}, ErrClosed
	}
	r, err := s.client.call(ctx, "test_connection", raw, "", false)
	if err == nil && !receiptOK(r.Result) {
		err = ErrProtocol
		s.client.stop(err)
		<-s.client.processDone
		r.ProcessExitConfirmed = true
	}
	return r, err
}

func (s *SingleSession) Call(ctx context.Context, method string, params any) (Receipt, error) {
	if ctx == nil || !validToken(method) || controlMethod(method) {
		return Receipt{}, ErrRequest
	}
	raw, err := snapshotParams(params, "")
	if err != nil {
		return Receipt{}, err
	}
	if err = s.enter(ctx); err != nil {
		return Receipt{}, err
	}
	defer s.leave()
	if !s.connected.Load() {
		return Receipt{}, ErrClosed
	}
	return s.client.call(ctx, method, raw, "", false)
}

// Close sends original shutdown when idle, retires an active captured process,
// and waits for the original executor and process before returning. Session
// closure is not proof that a remote write was rolled back.
func (s *SingleSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.client.Close()
		<-s.gate
		s.connected.Store(false)
		s.leave()
	})
	return s.closeErr
}
