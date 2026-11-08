package dbruntime

import (
	"context"
	"slices"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (m *Manager) SettingsDocuments(id int64) (contract.SettingsDocumentsProvider, error) {
	published, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	s := published.(*session)
	if _, err = contract.Require[contract.SettingsDocumentsProvider](s.raw); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *session) SupportsSettingsDocument(o contract.ObjectRef) bool {
	if s.require(s.Capabilities().Metadata, o.ProfileID) != nil {
		return false
	}
	p, err := contract.Require[contract.SettingsDocumentsProvider](s.raw)
	return err == nil && p.SupportsSettingsDocument(o)
}
func (s *session) ReserveSettingsDocument(o contract.ObjectRef) (contract.SettingsDocumentLease, error) {
	if err := s.require(s.Capabilities().Metadata, o.ProfileID); err != nil {
		return nil, err
	}
	p, err := contract.Require[contract.SettingsDocumentsProvider](s.raw)
	if err != nil {
		return nil, err
	}
	raw, err := p.ReserveSettingsDocument(o)
	if err != nil {
		return nil, err
	}
	return &settingsDocument{session: s, object: o, raw: raw}, nil
}
func (s *session) RecoverSettingsDocument(o contract.ObjectRef, old contract.SettingsDocumentLease) (contract.SettingsRecovery, error) {
	if err := s.require(s.Capabilities().Metadata, o.ProfileID); err != nil {
		return contract.SettingsRecovery{}, err
	}
	previous, ok := old.(*settingsDocument)
	if !ok || previous.object != o || previous.session.id != s.id {
		return contract.SettingsRecovery{}, contract.ErrSettingsConflict
	}
	p, err := contract.Require[contract.SettingsDocumentsProvider](s.raw)
	if err != nil {
		return contract.SettingsRecovery{}, err
	}
	recovery, err := p.RecoverSettingsDocument(o, previous.raw)
	if err != nil {
		return recovery, err
	}
	recovery.Lease = &settingsDocument{session: s, object: o, raw: recovery.Lease}
	return recovery, nil
}

type settingsDocument struct {
	session *session
	object  contract.ObjectRef
	raw     contract.SettingsDocumentLease
}

func (d *settingsDocument) check() error {
	return d.session.require(d.session.Capabilities().Metadata, d.session.id)
}
func (d *settingsDocument) Read(ctx context.Context) (contract.SettingsSnapshot, error) {
	if err := d.check(); err != nil {
		return contract.SettingsSnapshot{}, err
	}
	return d.raw.Read(ctx)
}
func (d *settingsDocument) Prepare(s contract.SettingsSnapshot, draft string, reset bool) (contract.SettingsPlan, error) {
	if err := d.check(); err != nil {
		return contract.SettingsPlan{}, err
	}
	return d.raw.Prepare(s, draft, reset)
}
func (d *settingsDocument) Apply(ctx context.Context, p contract.SettingsPlan) (contract.SettingsResult, error) {
	if err := d.check(); err != nil {
		return contract.SettingsResult{State: "not_submitted"}, err
	}
	p.Fields = slices.Clone(p.Fields)
	return d.raw.Apply(ctx, p)
}
func (d *settingsDocument) Observe(ctx context.Context, r contract.SettingsReceipt, bound time.Duration) (contract.SettingsResult, error) {
	if err := d.check(); err != nil {
		return contract.SettingsResult{Receipt: r}, err
	}
	return d.raw.Observe(ctx, r, bound)
}
func (d *settingsDocument) Close() error { return d.raw.Close() }
