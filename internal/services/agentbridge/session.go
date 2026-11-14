package agentbridge

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"sync"
	"sync/atomic"
)

// Session owns one opaque original agentSessionId. Its requests serialize;
// canceling a queued request never cancels the executor currently using it.
type Session struct {
	client       *Client
	id           string
	ctx          context.Context
	cancel       context.CancelFunc
	closed       atomic.Bool
	gate         chan struct{}
	closeOnce    sync.Once
	closeReceipt Receipt
	closeError   error
}

func (c *Client) OpenSession(ctx context.Context, params any) (*Session, Receipt, error) {
	if ctx == nil {
		return nil, Receipt{}, ErrRequest
	}
	s := &Session{client: c, id: rand.Text(), gate: make(chan struct{}, 1)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.gate <- struct{}{}
	raw, err := snapshotParams(params, s.id)
	if err != nil {
		s.revoke()
		return nil, Receipt{}, err
	}
	c.mu.Lock()
	if c.terminal != nil || c.closing.Load() {
		c.mu.Unlock()
		s.revoke()
		return nil, Receipt{}, ErrLost
	}
	c.sessions[s] = true
	c.mu.Unlock()
	request, cancel := s.request(ctx)
	r, err := c.call(request, "open_session", raw, s.id, false)
	cancel()
	if err != nil || ctx.Err() != nil || !s.Alive() {
		_, closeErr := s.Close()
		if err == nil {
			err = ctx.Err()
			if err == nil {
				err = ErrLost
			}
		}
		return nil, r, errorWithCancellation(err, closeErr)
	}
	return s, r, nil
}

func (s *Session) ID() string  { return s.id }
func (s *Session) Alive() bool { return !s.closed.Load() && s.client.Alive() }
func (s *Session) revoke()     { s.closed.Store(true); s.cancel() }
func (s *Session) request(ctx context.Context) (context.Context, context.CancelFunc) {
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel() }
}

func (s *Session) Call(ctx context.Context, method string, params any) (Receipt, error) {
	if ctx == nil || !validToken(method) || controlMethod(method) {
		return Receipt{}, ErrRequest
	}
	raw, err := snapshotParams(params, s.id)
	if err != nil {
		return Receipt{}, err
	}
	if !s.Alive() {
		return Receipt{}, ErrClosed
	}
	request, cancel := s.request(ctx)
	defer cancel()
	select {
	case <-request.Done():
		return Receipt{}, request.Err()
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()
	if !s.Alive() {
		return Receipt{}, ErrClosed
	}
	r, err := s.client.call(request, method, raw, s.id, false)
	if r.RemoteError != nil && r.RemoteError.SessionDisposition != "keep" {
		s.revoke()
	}
	return r, err
}

// Close confirms logical detachment only. DBX v2 close_session schedules native
// disposal asynchronously; its ok response is not proof of physical disposal.
// Closing Client waits for the captured process and releases its runtime pool.
func (s *Session) Close() (Receipt, error) {
	s.closeOnce.Do(func() {
		s.revoke()
		<-s.gate
		defer func() { s.gate <- struct{}{} }()
		ctx, cancel := context.WithTimeout(context.Background(), s.client.options.CancelWait)
		defer cancel()
		raw, _ := json.Marshal(map[string]string{"agentSessionId": s.id})
		s.closeReceipt, s.closeError = s.client.call(ctx, "close_session", raw, s.id, true)
		if s.closeError == nil && !receiptOK(s.closeReceipt.Result) {
			s.closeError = ErrProtocol
			s.client.stop(ErrProtocol)
			<-s.client.processDone
		}
		s.client.mu.Lock()
		delete(s.client.sessions, s)
		s.client.mu.Unlock()
	})
	return s.closeReceipt, s.closeError
}
