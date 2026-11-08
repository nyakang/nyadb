package dbruntime

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (m *Manager) Snapshots(id int64) (contract.SnapshotProvider, error) {
	published, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	s := published.(*session)
	if _, err = contract.Require[contract.SnapshotProvider](s.raw); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *session) SupportsSnapshots(o contract.ObjectRef, target contract.SnapshotTarget, action string) bool {
	if s.require(s.Capabilities().Metadata, o.ProfileID) != nil {
		return false
	}
	p, err := contract.Require[contract.SnapshotProvider](s.raw)
	return err == nil && p.SupportsSnapshots(o, target, action)
}
func (s *session) ReserveSnapshotManagementDocument(o contract.ObjectRef, document uint64) (contract.SnapshotLease, error) {
	if err := s.require(s.Capabilities().Metadata, o.ProfileID); err != nil {
		return nil, err
	}
	p, err := contract.Require[contract.SnapshotProvider](s.raw)
	if err != nil {
		return nil, err
	}
	raw, err := p.ReserveSnapshotManagementDocument(o, document)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	return &snapshotDocument{session: s, object: o, raw: raw}, nil
}

type snapshotDocument struct {
	session    *session
	object     contract.ObjectRef
	raw        contract.SnapshotLease
	closed     atomic.Bool
	closeOnce  sync.Once
	closeErr   error
	evidenceMu sync.Mutex
	last       contract.SnapshotResult
	streamMu   sync.Mutex
	streams    sync.WaitGroup
}

func (d *snapshotDocument) stream() func() {
	d.streamMu.Lock()
	defer d.streamMu.Unlock()
	if d.closed.Load() {
		return func() {}
	}
	d.streams.Add(1)
	d.session.mu.Lock()
	tracked := !d.session.closed
	if tracked {
		d.session.snapshotStreams.Add(1)
	}
	d.session.mu.Unlock()
	return func() {
		d.streams.Done()
		if tracked {
			d.session.snapshotStreams.Done()
		}
	}
}

type snapshotObservation struct {
	owner *snapshotDocument
	raw   contract.SnapshotObservation
}
type snapshotPlan struct {
	owner *snapshotDocument
	raw   contract.SnapshotPlan
}

func (d *snapshotDocument) Object() contract.ObjectRef { return d.object }
func (d *snapshotDocument) Generation() uint64         { return d.session.generation }
func (d *snapshotDocument) check() error {
	if d.closed.Load() {
		return contract.ErrRevoked
	}
	return d.session.require(d.session.Capabilities().Metadata, d.object.ProfileID)
}
func (o *snapshotObservation) Snapshot() contract.SnapshotMetadata {
	return contract.CloneSnapshotMetadata(o.raw.Snapshot())
}
func (p *snapshotPlan) Action() string                  { return p.raw.Action() }
func (p *snapshotPlan) Target() contract.SnapshotTarget { return p.raw.Target() }
func (p *snapshotPlan) Preview() contract.SnapshotRequest {
	return contract.CloneSnapshotRequest(p.raw.Preview())
}
func (p *snapshotPlan) UploadSource() (contract.SnapshotSource, bool) { return p.raw.UploadSource() }
func (p *snapshotPlan) RestoresData() bool                            { return p.raw.RestoresData() }
func (d *snapshotDocument) Read(ctx context.Context, target contract.SnapshotTarget) (contract.SnapshotObservation, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	o, err := d.raw.Read(ctx, target)
	if o == nil {
		return nil, err
	}
	return &snapshotObservation{owner: d, raw: o}, err
}
func (d *snapshotDocument) Prepare(before contract.SnapshotObservation, draft contract.SnapshotDraft) (contract.SnapshotPlan, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	o, ok := before.(*snapshotObservation)
	if !ok || o == nil || o.owner != d {
		return nil, contract.ErrNamespaceChanged
	}
	p, err := d.raw.Prepare(o.raw, contract.CloneSnapshotDraft(draft))
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, contract.ErrUnsupported
	}
	return &snapshotPlan{owner: d, raw: p}, nil
}
func (d *snapshotDocument) retain(r contract.SnapshotResult) contract.SnapshotResult {
	r = contract.CloneSnapshotResult(r)
	d.evidenceMu.Lock()
	if !d.closed.Load() && r.Action != "" {
		d.last = contract.CloneSnapshotResult(r)
	}
	d.evidenceMu.Unlock()
	return r
}
func (d *snapshotDocument) retained() contract.SnapshotResult {
	d.evidenceMu.Lock()
	defer d.evidenceMu.Unlock()
	r := contract.CloneSnapshotResult(d.last)
	switch r.State {
	case "response", "uncertain", "enqueued":
		r.State = "indeterminate"
	case "":
		r.State = "not_submitted"
		r.Object = d.object
	}
	return r
}
func (d *snapshotDocument) owned(plan contract.SnapshotPlan) (*snapshotPlan, error) {
	p, ok := plan.(*snapshotPlan)
	if !ok || p == nil || p.owner != d {
		return nil, contract.ErrNamespaceChanged
	}
	return p, nil
}
func (d *snapshotDocument) Submit(ctx context.Context, plan contract.SnapshotPlan) (contract.SnapshotResult, error) {
	if err := d.check(); err != nil {
		return d.retained(), err
	}
	p, err := d.owned(plan)
	if err != nil {
		return contract.SnapshotResult{Object: d.object, State: "not_submitted"}, err
	}
	r, err := d.raw.Submit(ctx, p.raw)
	return d.retain(r), err
}
func (d *snapshotDocument) Upload(ctx context.Context, plan contract.SnapshotPlan, source io.ReadCloser, options contract.SnapshotTransferOptions) (contract.SnapshotResult, error) {
	done := d.stream()
	defer done()
	if err := d.check(); err != nil {
		if source != nil {
			_ = source.Close()
		}
		return d.retained(), err
	}
	p, err := d.owned(plan)
	if err != nil {
		if source != nil {
			_ = source.Close()
		}
		return contract.SnapshotResult{Object: d.object, State: "not_submitted"}, err
	}
	r, err := d.raw.Upload(ctx, p.raw, source, options)
	return d.retain(r), err
}
func (d *snapshotDocument) Download(ctx context.Context, plan contract.SnapshotPlan, sink io.WriteCloser, options contract.SnapshotTransferOptions) (contract.SnapshotResult, error) {
	done := d.stream()
	defer done()
	if err := d.check(); err != nil {
		if sink != nil {
			_ = sink.Close()
		}
		return d.retained(), err
	}
	p, err := d.owned(plan)
	if err != nil {
		if sink != nil {
			_ = sink.Close()
		}
		return contract.SnapshotResult{Object: d.object, State: "not_submitted"}, err
	}
	r, err := d.raw.Download(ctx, p.raw, sink, options)
	return d.retain(r), err
}
func (d *snapshotDocument) Verify(ctx context.Context) (contract.SnapshotResult, error) {
	if err := d.check(); err != nil {
		return d.retained(), err
	}
	r, err := d.raw.Verify(ctx)
	return d.retain(r), err
}
func (d *snapshotDocument) Close() error {
	d.closeOnce.Do(func() {
		d.streamMu.Lock()
		d.closed.Store(true)
		d.streamMu.Unlock()
		d.closeErr = d.raw.Close()
		d.streams.Wait()
		d.evidenceMu.Lock()
		d.last = contract.SnapshotResult{}
		d.evidenceMu.Unlock()
	})
	return d.closeErr
}

var _ contract.SnapshotProvider = (*session)(nil)
var _ contract.SnapshotLease = (*snapshotDocument)(nil)
