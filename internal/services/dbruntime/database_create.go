package dbruntime

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type databaseCreateDocumentLease struct {
	s               *session
	raw             contract.DatabaseCreateDocumentLease
	request         contract.DatabaseCreateDocumentRequest
	mu              sync.Mutex
	closed, blocked bool
	ctx             context.Context
	cancel          context.CancelFunc
	active          sync.WaitGroup
	serial          chan struct{}
	done            chan struct{}
	err             error
	sequence        uint64
	original        *contract.DatabaseCreateOutcome
}

func (s *session) CanCreateDatabase(t contract.DatabaseCreateTarget) bool {
	if t.Namespace.ProfileID != s.id || t.Generation != s.generation || !s.alive() {
		return false
	}
	p, err := contract.Require[contract.DatabaseCreateDocumentsProvider](s.raw)
	if err != nil {
		return false
	}
	t.Generation = s.raw.Generation()
	return p.CanCreateDatabase(t)
}

func (s *session) ReserveDatabaseCreateDocument(r contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error) {
	if !r.Valid() {
		return nil, connections.ErrInvalid
	}
	if !s.CanCreateDatabase(r.Target) {
		return nil, contract.ErrRevoked
	}
	p, err := contract.Require[contract.DatabaseCreateDocumentsProvider](s.raw)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	if _, exists := s.databaseCreateDocuments[r.DocumentID]; exists {
		s.mu.Unlock()
		return nil, connections.ErrInvalid
	}
	if s.databaseCreateDocuments == nil {
		s.databaseCreateDocuments = make(map[string]*databaseCreateDocumentLease)
	}
	s.databaseCreateDocuments[r.DocumentID] = nil
	s.snapshotStreams.Add(1)
	s.mu.Unlock()
	published := false
	defer func() {
		if !published {
			s.mu.Lock()
			delete(s.databaseCreateDocuments, r.DocumentID)
			s.mu.Unlock()
		}
		s.snapshotStreams.Done()
	}()
	native := r
	native.Target.Generation = s.raw.Generation()
	l, err := p.ReserveDatabaseCreateDocument(native)
	if err != nil || l == nil {
		if l != nil {
			_ = l.Close()
		}
		if err == nil {
			err = connections.ErrInvalid
		}
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	bound := &databaseCreateDocumentLease{s: s, raw: l, request: r, ctx: ctx, cancel: cancel, serial: make(chan struct{}, 1), done: make(chan struct{})}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		_ = l.Close()
		return nil, contract.ErrRevoked
	}
	s.databaseCreateDocuments[r.DocumentID] = bound
	published = true
	s.mu.Unlock()
	return bound, nil
}

func (l *databaseCreateDocumentLease) admission(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	l.active.Add(1)
	l.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	done := func() { stop(); cancel(); l.active.Done() }
	select {
	case <-child.Done():
		done()
		return nil, nil, child.Err()
	case l.serial <- struct{}{}:
	}
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed || child.Err() != nil || !l.s.CanCreateDatabase(l.request.Target) {
		<-l.serial
		done()
		return nil, nil, contract.ErrRevoked
	}
	return child, func() { <-l.serial; done() }, nil
}

func (l *databaseCreateDocumentLease) nativeTarget() contract.DatabaseCreateTarget {
	t := l.request.Target
	t.Generation = l.s.raw.Generation()
	return t
}

func (l *databaseCreateDocumentLease) Snapshot(ctx context.Context) (contract.DatabaseCreateSnapshot, error) {
	child, done, err := l.admission(ctx)
	if err != nil {
		return contract.DatabaseCreateSnapshot{}, err
	}
	defer done()
	out, err := l.raw.Snapshot(child)
	if err != nil {
		return contract.DatabaseCreateSnapshot{}, err
	}
	if out.Target != l.nativeTarget() || out.Revision == "" || out.Engine == "" || out.CreateSQL == "" {
		return contract.DatabaseCreateSnapshot{}, connections.ErrInvalid
	}
	if child.Err() != nil || !l.s.alive() {
		return contract.DatabaseCreateSnapshot{}, contract.ErrRevoked
	}
	out.Target = l.request.Target
	return out, nil
}

func (l *databaseCreateDocumentLease) Create(ctx context.Context, before contract.DatabaseCreateSnapshot) contract.DatabaseCreateOutcome {
	no := func(err error) contract.DatabaseCreateOutcome {
		return contract.DatabaseCreateOutcome{State: "not_executed", Error: err}
	}
	if before.Target != l.request.Target || before.Exists || before.Revision == "" || before.CreateSQL == "" {
		return no(connections.ErrInvalid)
	}
	child, done, err := l.admission(ctx)
	if err != nil {
		return no(err)
	}
	defer done()
	l.mu.Lock()
	blocked := l.blocked
	settled := l.original != nil && l.original.Verified
	l.mu.Unlock()
	if blocked {
		if settled {
			return no(contract.ErrDatabaseCreateConsumed)
		}
		return no(contract.ErrDatabaseCreateUnresolved)
	}
	native := before
	native.Target = l.nativeTarget()
	out := l.raw.Create(child, native).Clone()
	l.mu.Lock()
	defer l.mu.Unlock()
	valid := out.State == "not_executed" && !out.Verified && out.Actual == nil && out.Receipt == nil
	if r := out.Receipt; r != nil {
		valid = r.DocumentID == l.request.DocumentID && r.Sequence > l.sequence && r.Original == native && r.SQL == native.CreateSQL &&
			r.State == out.State && r.Verified == out.Verified && reflect.DeepEqual(r.Actual, out.Actual) &&
			(out.State == "not_executed" || out.State == "committed" || out.State == "uncertain")
		if r.Sequence > l.sequence {
			l.sequence = r.Sequence
		}
		if out.Verified {
			valid = valid && out.State == "committed" && out.Error == nil && out.Actual != nil && out.Actual.Target == native.Target &&
				out.Actual.Exists && out.Actual.NativeID != "" && out.Actual.DDL != "" && out.Actual.Revision != "" && out.Actual.Engine == native.Engine && out.Actual.CreateSQL == native.CreateSQL
		} else {
			valid = valid && out.Actual == nil
		}
	}
	// Only translate captured matching scopes. A foreign receipt stays complete
	// for diagnosis and can never be accepted as this document's operation.
	translate := func(v *contract.DatabaseCreateSnapshot) {
		if v != nil && v.Target == native.Target {
			v.Target = l.request.Target
		}
	}
	translate(out.Actual)
	if out.Receipt != nil {
		translate(&out.Receipt.Original)
		translate(out.Receipt.Actual)
	}
	if !valid {
		out.Verified, out.Actual = false, nil
		if out.State != "committed" {
			out.State = "uncertain"
		}
		out.Error = errors.Join(out.Error, contract.ErrDatabaseCreateUnresolved)
	}
	if child.Err() != nil || l.closed || !l.s.alive() {
		out.Verified, out.Actual = false, nil
		out.Error = errors.Join(out.Error, contract.ErrRevoked)
	}
	if out.Receipt != nil || out.State != "not_executed" {
		retained := out.Clone()
		l.original = &retained
	}
	l.blocked = !valid || out.State != "not_executed"
	return out
}

func (l *databaseCreateDocumentLease) OriginalOutcome() *contract.DatabaseCreateOutcome {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.original == nil {
		return nil
	}
	out := l.original.Clone()
	return &out
}

func (l *databaseCreateDocumentLease) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return l.err
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	err := l.raw.Close()
	l.active.Wait()
	l.mu.Lock()
	l.err = err
	l.original = nil
	close(l.done)
	l.mu.Unlock()
	l.s.mu.Lock()
	delete(l.s.databaseCreateDocuments, l.request.DocumentID)
	l.s.mu.Unlock()
	return err
}

func (m *Manager) DatabaseCreateDocuments(id int64) (contract.DatabaseCreateDocumentsProvider, error) {
	p, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	s := p.(*session)
	if _, err := contract.Require[contract.DatabaseCreateDocumentsProvider](s.raw); err != nil {
		return nil, err
	}
	return s, nil
}

var _ contract.DatabaseCreateDocumentsProvider = (*session)(nil)
