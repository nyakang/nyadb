package dbruntime

import (
	"context"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type schemaDesignDocumentLease struct {
	s        *session
	raw      contract.SchemaDesignDocumentLease
	request  contract.SchemaDesignDocumentRequest
	mu       sync.Mutex
	closed   bool
	done     chan struct{}
	err      error
	ctx      context.Context
	cancel   context.CancelFunc
	active   sync.WaitGroup
	writes   chan struct{}
	original *contract.SchemaDesignOutcome
	blocked  bool
	sequence uint64
}

func (s *session) ReserveSchemaDesignDocument(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
	if !r.Valid() {
		return nil, connections.ErrInvalid
	}
	if err := s.schemaDesignTarget(r.Target); err != nil {
		return nil, err
	}
	p, err := contract.Require[contract.SchemaDesignDocumentsProvider](s.raw)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	if _, exists := s.schemaDocuments[r.DocumentID]; exists {
		s.mu.Unlock()
		return nil, connections.ErrInvalid
	}
	if s.schemaDocuments == nil {
		s.schemaDocuments = make(map[string]*schemaDesignDocumentLease)
	}
	s.schemaDocuments[r.DocumentID] = nil // Reserve before calling the provider.
	s.snapshotStreams.Add(1)
	s.mu.Unlock()
	published := false
	defer func() {
		if !published {
			s.mu.Lock()
			delete(s.schemaDocuments, r.DocumentID)
			s.mu.Unlock()
		}
		s.snapshotStreams.Done()
	}()
	native := r
	native.Target.Generation = s.raw.Generation()
	l, err := p.ReserveSchemaDesignDocument(native)
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
	bound := &schemaDesignDocumentLease{s: s, raw: l, request: r, done: make(chan struct{}), ctx: ctx, cancel: cancel, writes: make(chan struct{}, 1)}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		_ = l.Close() // Close only the captured original lease.
		return nil, contract.ErrRevoked
	}
	s.schemaDocuments[r.DocumentID] = bound
	published = true
	s.mu.Unlock()
	return bound, nil
}

func (l *schemaDesignDocumentLease) target(t contract.SchemaDesignTarget) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return contract.ErrRevoked
	}
	if t != l.request.Target {
		return connections.ErrInvalid
	}
	return nil
}

func (l *schemaDesignDocumentLease) Operations() contract.SchemaDesignOperations {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return contract.SchemaDesignOperations{}
	}
	ops := l.s.bindSchemaDesign(l.raw.Operations(), l.request.DocumentID)
	if f := ops.Snapshot; f != nil {
		ops.Snapshot = func(ctx context.Context, t contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
			child, done, err := l.admission(ctx, t)
			if err != nil {
				return contract.SchemaDesignSnapshot{}, err
			}
			defer done()
			out, err := f(child, t)
			if scopeErr := l.target(t); scopeErr != nil {
				return contract.SchemaDesignSnapshot{}, scopeErr
			}
			if child.Err() != nil {
				return contract.SchemaDesignSnapshot{}, child.Err()
			}
			return out, err
		}
	}
	if f := ops.CanApply; f != nil {
		ops.CanApply = func(t contract.SchemaDesignTarget) bool { return l.writable(t) && f(t) }
	}
	if f := ops.CanDrop; f != nil {
		ops.CanDrop = func(t contract.SchemaDesignTarget) bool { return l.writable(t) && f(t) }
	}
	if f := ops.CanCopy; f != nil {
		ops.CanCopy = func(t contract.SchemaDesignTarget) bool { return l.writable(t) && f(t) }
	}
	if f := ops.CanRename; f != nil {
		ops.CanRename = func(t contract.SchemaDesignTarget) bool { return l.writable(t) && f(t) }
	}
	if f := ops.Apply; f != nil {
		ops.Apply = func(ctx context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
			r = r.Clone() // Capture every original value before admission can wait.
			return l.write(ctx, r.Original.Target, true, func(child context.Context) contract.SchemaDesignOutcome { return f(child, r) })
		}
	}
	if f := ops.Drop; f != nil {
		ops.Drop = func(ctx context.Context, r contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
			r = r.Clone() // Capture every original value before admission can wait.
			return l.write(ctx, r.Target, false, func(child context.Context) contract.SchemaDesignOutcome { return f(child, r) })
		}
	}
	if f := ops.Copy; f != nil {
		ops.Copy = func(ctx context.Context, r contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
			r = r.Clone() // Capture every original value before admission can wait.
			return l.write(ctx, r.Original.Target, false, func(child context.Context) contract.SchemaDesignOutcome { return f(child, r) })
		}
	}
	if f := ops.Rename; f != nil {
		ops.Rename = func(ctx context.Context, r contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
			r = r.Clone() // Capture every original value before admission can wait.
			return l.write(ctx, r.Original.Target, true, func(child context.Context) contract.SchemaDesignOutcome { return f(child, r) })
		}
	}
	return ops
}

func (l *schemaDesignDocumentLease) admission(ctx context.Context, target contract.SchemaDesignTarget) (context.Context, func(), error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	if target != l.request.Target {
		l.mu.Unlock()
		return nil, nil, connections.ErrInvalid
	}
	l.active.Add(1)
	l.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	done := func() { stop(); cancel(); l.active.Done() }
	if err := child.Err(); err != nil {
		done()
		return nil, nil, err
	}
	return child, done, nil
}

func (l *schemaDesignDocumentLease) writable(target contract.SchemaDesignTarget) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.closed && !l.blocked && target == l.request.Target
}

func (l *schemaDesignDocumentLease) write(ctx context.Context, target contract.SchemaDesignTarget, retarget bool, work func(context.Context) contract.SchemaDesignOutcome) contract.SchemaDesignOutcome {
	child, done, err := l.admission(ctx, target)
	if err != nil {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
	}
	defer done()
	select {
	case <-child.Done():
		return contract.SchemaDesignOutcome{State: "not_executed", Error: child.Err()}
	case l.writes <- struct{}{}:
	}
	defer func() { <-l.writes }()
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return contract.SchemaDesignOutcome{State: "not_executed", Error: contract.ErrRevoked}
	}
	if l.blocked {
		l.mu.Unlock()
		return contract.SchemaDesignOutcome{State: "not_executed", Error: contract.ErrSchemaCommitUncertain}
	}
	if target != l.request.Target {
		l.mu.Unlock()
		return contract.SchemaDesignOutcome{State: "not_executed", Error: connections.ErrInvalid}
	}
	l.mu.Unlock()
	if child.Err() != nil {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: child.Err()}
	}
	out := work(child)
	l.mu.Lock()
	defer l.mu.Unlock()
	if out.Receipt != nil {
		if out.Receipt.Sequence <= l.sequence {
			out.Verified, out.Actual = false, nil
			out.Error = errors.Join(out.Error, contract.ErrSchemaCommitUncertain)
			if out.State != "committed" {
				out.State = "uncertain"
			}
		} else {
			l.sequence = out.Receipt.Sequence
		}
	}
	if out.State != "not_executed" {
		original := out.Clone()
		l.original = &original
	}
	if out.State == "uncertain" || out.State == "committed" && (!out.Verified || out.Error != nil) {
		l.blocked = true
	}
	if retarget && !l.closed && out.State == "committed" && out.Verified && out.Error == nil && out.Actual != nil {
		l.request.Target = out.Actual.Target
	}
	// Preserve original settlement even when Close races its acknowledgement.
	return out
}

func (l *schemaDesignDocumentLease) OriginalOutcome() *contract.SchemaDesignOutcome {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.original == nil {
		return nil
	}
	original := l.original.Clone()
	return &original
}

func (l *schemaDesignDocumentLease) Close() error {
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
	delete(l.s.schemaDocuments, l.request.DocumentID)
	l.s.mu.Unlock()
	return err
}

func (m *Manager) ReserveSchemaDesignDocument(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
	s, err := m.Session(r.Target.Object.ProfileID)
	if err != nil {
		return nil, err
	}
	p, err := contract.Require[contract.SchemaDesignDocumentsProvider](s)
	if err != nil {
		return nil, err
	}
	return p.ReserveSchemaDesignDocument(r)
}

var _ contract.SchemaDesignDocumentsProvider = (*session)(nil)
var _ contract.SchemaDesignDocumentLease = (*schemaDesignDocumentLease)(nil)
