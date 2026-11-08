package dbruntime

import (
	"context"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func cloneKeyRecord(r contract.KeyRecord) contract.KeyRecord {
	r.Actions = slices.Clone(r.Actions)
	r.Indexes = slices.Clone(r.Indexes)
	return r
}
func cloneKeyDraft(d contract.KeyDraft) contract.KeyDraft {
	d.Actions = slices.Clone(d.Actions)
	d.Indexes = slices.Clone(d.Indexes)
	return d
}
func (m *Manager) KeysDocuments(id int64) (contract.KeysDocumentsProvider, error) {
	published, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	s := published.(*session)
	if _, err = contract.Require[contract.KeysDocumentsProvider](s.raw); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *session) SupportsKeysDocument(o contract.ObjectRef) bool {
	if s.require(s.Capabilities().Metadata, o.ProfileID) != nil {
		return false
	}
	p, err := contract.Require[contract.KeysDocumentsProvider](s.raw)
	return err == nil && p.SupportsKeysDocument(o)
}
func (s *session) ReserveKeysDocument(o contract.ObjectRef) (contract.KeysDocumentLease, error) {
	if err := s.require(s.Capabilities().Metadata, o.ProfileID); err != nil {
		return nil, err
	}
	p, err := contract.Require[contract.KeysDocumentsProvider](s.raw)
	if err != nil {
		return nil, err
	}
	raw, err := p.ReserveKeysDocument(o)
	if err != nil {
		return nil, err
	}
	return &keysDocument{session: s, object: o, raw: raw}, nil
}
func (s *session) RecoverKeysDocument(o contract.ObjectRef, previous contract.KeysDocumentLease) (contract.KeyRecovery, error) {
	if err := s.require(s.Capabilities().Metadata, o.ProfileID); err != nil {
		return contract.KeyRecovery{}, err
	}
	old, ok := previous.(*keysDocument)
	if !ok || old.object != o || old.session.id != s.id {
		return contract.KeyRecovery{}, contract.ErrKeyConflict
	}
	p, err := contract.Require[contract.KeysDocumentsProvider](s.raw)
	if err != nil {
		return contract.KeyRecovery{}, err
	}
	r, err := p.RecoverKeysDocument(o, old.raw)
	if err != nil {
		return r, err
	}
	r.Lease = &keysDocument{session: s, object: o, raw: r.Lease}
	return r, nil
}

type keysDocument struct {
	session *session
	object  contract.ObjectRef
	raw     contract.KeysDocumentLease
}

func (d *keysDocument) check() error {
	return d.session.require(d.session.Capabilities().Metadata, d.session.id)
}
func (d *keysDocument) List(ctx context.Context, offset, limit uint64) (contract.KeyPage, error) {
	if err := d.check(); err != nil {
		return contract.KeyPage{}, err
	}
	return d.raw.List(ctx, offset, limit)
}
func (d *keysDocument) Read(ctx context.Context, r contract.KeyRecord) (contract.KeyRecord, error) {
	if err := d.check(); err != nil {
		return contract.KeyRecord{}, err
	}
	return d.raw.Read(ctx, cloneKeyRecord(r))
}
func (d *keysDocument) Reveal(r contract.KeyRecord) (string, error) {
	if err := d.check(); err != nil {
		return "", err
	}
	return d.raw.Reveal(cloneKeyRecord(r))
}
func (d *keysDocument) Prepare(action string, r contract.KeyRecord, draft contract.KeyDraft) (contract.KeyPlan, error) {
	if err := d.check(); err != nil {
		return contract.KeyPlan{}, err
	}
	return d.raw.Prepare(action, cloneKeyRecord(r), cloneKeyDraft(draft))
}
func (d *keysDocument) Apply(ctx context.Context, p contract.KeyPlan) (contract.KeyResult, error) {
	if err := d.check(); err != nil {
		return contract.KeyResult{State: "not_submitted"}, err
	}
	p.Original = cloneKeyRecord(p.Original)
	p.Draft = cloneKeyDraft(p.Draft)
	return d.raw.Apply(ctx, p)
}
func (d *keysDocument) Close() error { return d.raw.Close() }
