// Package tdengine implements original native WebSocket sessions and feature
// contracts. It never reconnects or replays an operation implicitly.
package tdengine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type Session struct {
	mu                      sync.Mutex
	profile                 connections.Profile
	password, version       string
	ctx                     context.Context
	cancel                  context.CancelFunc
	closed                  bool
	leases                  map[*queryLease]bool
	dataLeases              map[*dataLease]bool
	metadataSlots, sqlSlots chan struct{}
	active                  sync.WaitGroup
	closeOnce               sync.Once
	closedDone              chan struct{}
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	p, err := connections.NormalizeProfile(p)
	if err != nil || p.Kind != connections.TDengine || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	root, cancel := context.WithCancel(context.Background())
	s := &Session{profile: p, password: password, ctx: root, cancel: cancel, leases: map[*queryLease]bool{}, metadataSlots: make(chan struct{}, p.TDengineOptions.MaxConnections), sqlSlots: make(chan struct{}, p.TDengineOptions.SQLMaxConnections), closedDone: make(chan struct{})}
	err = s.withConnection(ctx, func(child context.Context, w *wire) error {
		r, _, e := w.query(child, "SELECT server_version()", nil, nil)
		if e != nil {
			return e
		}
		if len(r.Rows) != 1 || len(r.Rows[0]) != 1 || !strings.HasPrefix(r.Rows[0][0].Text, "3.") {
			return contract.ErrUnsupported
		}
		s.version = r.Rows[0][0].Text
		return nil
	})
	if err != nil || ctx.Err() != nil {
		_ = s.Close()
		if err == nil {
			err = ctx.Err()
		}
		return nil, safeError(err, password)
	}
	return s, nil
}

func (s *Session) request(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	s.active.Add(1)
	s.mu.Unlock()
	child, cancel := context.WithTimeout(ctx, time.Duration(s.profile.TDengineOptions.QueryTimeoutSeconds)*time.Second)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel(); s.active.Done() }, nil
}
func (s *Session) connect(ctx context.Context) (*wire, error) {
	s.mu.Lock()
	closed, password := s.closed, s.password
	s.mu.Unlock()
	if closed {
		return nil, contract.ErrRevoked
	}
	child, cancel := context.WithTimeout(ctx, time.Duration(s.profile.TDengineOptions.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	return openWire(child, s.profile, password)
}
func (s *Session) withConnection(ctx context.Context, work func(context.Context, *wire) error) error {
	child, done, err := s.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	select {
	case s.metadataSlots <- struct{}{}:
	case <-child.Done():
		return child.Err()
	}
	defer func() { <-s.metadataSlots }()
	if err = child.Err(); err != nil {
		return err
	}
	w, err := s.connect(child)
	if err != nil {
		return safeError(err, s.secret())
	}
	defer w.close()
	return safeError(work(child, w), s.secret())
}
func (s *Session) secret() string     { s.mu.Lock(); defer s.mu.Unlock(); return s.password }
func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Catalog() string    { return s.profile.Database }
func (s *Session) Alive() bool        { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *Session) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(connections.TDengine)
	return d.Capabilities
}
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		all := make([]*queryLease, 0, len(s.leases))
		for l := range s.leases {
			all = append(all, l)
		}
		data := make([]*dataLease, 0, len(s.dataLeases))
		for l := range s.dataLeases {
			data = append(data, l)
		}
		s.mu.Unlock()
		for _, l := range all {
			l.Release()
		}
		for _, l := range data {
			_ = l.Close()
		}
		s.active.Wait()
		s.mu.Lock()
		s.password = ""
		s.mu.Unlock()
		close(s.closedDone)
	})
	<-s.closedDone
	return nil
}

func isServerError(err error) bool { var e *serverError; return errors.As(err, &e) }
