package dbruntime

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (m *Manager) CollectionManagement(id int64) (contract.CollectionManagementProvider, error) {
	published, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	s := published.(*session)
	if _, err = contract.Require[contract.CollectionManagementProvider](s.raw); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *session) SupportsCollectionManagement(o contract.ObjectRef, action string) bool {
	if s.require(s.Capabilities().Metadata, o.ProfileID) != nil {
		return false
	}
	p, err := contract.Require[contract.CollectionManagementProvider](s.raw)
	return err == nil && p.SupportsCollectionManagement(o, action)
}
func (s *session) ReserveCollectionManagementDocument(o contract.ObjectRef, document uint64) (contract.CollectionManagementLease, error) {
	if err := s.require(s.Capabilities().Metadata, o.ProfileID); err != nil {
		return nil, err
	}
	p, err := contract.Require[contract.CollectionManagementProvider](s.raw)
	if err != nil {
		return nil, err
	}
	raw, err := p.ReserveCollectionManagementDocument(o, document)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	return &collectionManagementDocument{session: s, object: o, raw: raw}, nil
}

type collectionManagementDocument struct {
	session    *session
	object     contract.ObjectRef
	raw        contract.CollectionManagementLease
	closed     atomic.Bool
	closeOnce  sync.Once
	closeErr   error
	evidenceMu sync.Mutex
	last       contract.CollectionManagementResult
}
type collectionManagementObservation struct {
	owner *collectionManagementDocument
	raw   contract.CollectionManagementObservation
}
type collectionManagementPlan struct {
	owner *collectionManagementDocument
	raw   contract.CollectionManagementPlan
}

func (d *collectionManagementDocument) Object() contract.ObjectRef { return d.object }
func (d *collectionManagementDocument) Generation() uint64         { return d.session.generation }
func (d *collectionManagementDocument) check() error {
	if d.closed.Load() {
		return contract.ErrRevoked
	}
	return d.session.require(d.session.Capabilities().Metadata, d.object.ProfileID)
}
func (o *collectionManagementObservation) Snapshot() contract.CollectionManagementSnapshot {
	return contract.CloneCollectionManagementSnapshot(o.raw.Snapshot())
}
func (p *collectionManagementPlan) Action() string { return p.raw.Action() }
func (p *collectionManagementPlan) Preview() contract.CollectionManagementRequest {
	return contract.CloneCollectionManagementRequest(p.raw.Preview())
}
func (p *collectionManagementPlan) Affected() []contract.NamespaceRef {
	return slices.Clone(p.raw.Affected())
}
func (d *collectionManagementDocument) Read(ctx context.Context, targets []contract.NamespaceRef) (contract.CollectionManagementObservation, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	o, err := d.raw.Read(ctx, slices.Clone(targets))
	if o == nil {
		return nil, err
	}
	return &collectionManagementObservation{owner: d, raw: o}, err
}
func (d *collectionManagementDocument) Prepare(before contract.CollectionManagementObservation, draft contract.CollectionManagementDraft) (contract.CollectionManagementPlan, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	o, ok := before.(*collectionManagementObservation)
	if !ok || o == nil || o.owner != d {
		return nil, contract.ErrNamespaceChanged
	}
	p, err := d.raw.Prepare(o.raw, contract.CloneCollectionManagementDraft(draft))
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, contract.ErrUnsupported
	}
	return &collectionManagementPlan{owner: d, raw: p}, nil
}
func (d *collectionManagementDocument) retain(r contract.CollectionManagementResult) contract.CollectionManagementResult {
	r = contract.CloneCollectionManagementResult(r)
	d.evidenceMu.Lock()
	if !d.closed.Load() && r.Receipt.Action != "" {
		d.last = contract.CloneCollectionManagementResult(r)
	}
	d.evidenceMu.Unlock()
	return r
}
func (d *collectionManagementDocument) retained() contract.CollectionManagementResult {
	d.evidenceMu.Lock()
	defer d.evidenceMu.Unlock()
	r := contract.CloneCollectionManagementResult(d.last)
	if r.State == "response" || r.State == "uncertain" {
		r.State = "indeterminate"
	}
	if r.State == "" {
		r.State = "not_submitted"
	}
	return r
}
func (d *collectionManagementDocument) Submit(ctx context.Context, plan contract.CollectionManagementPlan) (contract.CollectionManagementResult, error) {
	if err := d.check(); err != nil {
		return d.retained(), err
	}
	p, ok := plan.(*collectionManagementPlan)
	if !ok || p == nil || p.owner != d {
		return contract.CollectionManagementResult{State: "not_submitted"}, contract.ErrNamespaceChanged
	}
	r, err := d.raw.Submit(ctx, p.raw)
	return d.retain(r), err
}
func (d *collectionManagementDocument) Verify(ctx context.Context) (contract.CollectionManagementResult, error) {
	if err := d.check(); err != nil {
		return d.retained(), err
	}
	r, err := d.raw.Verify(ctx)
	return d.retain(r), err
}
func (d *collectionManagementDocument) Close() error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		d.closeErr = d.raw.Close()
		d.evidenceMu.Lock()
		d.last = contract.CollectionManagementResult{}
		d.evidenceMu.Unlock()
	})
	return d.closeErr
}
