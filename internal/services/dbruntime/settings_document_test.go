package dbruntime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type settingsHandle struct {
	tasksHandle
	settings *settingsLease
}
type settingsLease struct {
	reads, closed int
	fields        []string
}

func (h *settingsHandle) SupportsSettingsDocument(o contract.ObjectRef) bool {
	return o.ProfileID == h.id && o.Kind == "settings"
}
func (h *settingsHandle) ReserveSettingsDocument(contract.ObjectRef) (contract.SettingsDocumentLease, error) {
	return h.settings, nil
}
func (h *settingsHandle) RecoverSettingsDocument(o contract.ObjectRef, previous contract.SettingsDocumentLease) (contract.SettingsRecovery, error) {
	_ = previous.Close()
	return contract.SettingsRecovery{Lease: h.settings, Snapshot: contract.SettingsSnapshot{Object: o, Raw: `{"original":true}`}, Receipt: contract.SettingsReceipt{Object: o, UID: "0", Raw: `{"taskUid":0}`}}, nil
}
func (l *settingsLease) Read(context.Context) (contract.SettingsSnapshot, error) {
	l.reads++
	return contract.SettingsSnapshot{}, nil
}
func (l *settingsLease) Prepare(s contract.SettingsSnapshot, draft string, reset bool) (contract.SettingsPlan, error) {
	return contract.SettingsPlan{Original: s, Draft: draft, Reset: reset}, nil
}
func (l *settingsLease) Apply(_ context.Context, p contract.SettingsPlan) (contract.SettingsResult, error) {
	l.reads++
	l.fields = p.Fields
	return contract.SettingsResult{State: "enqueued"}, nil
}
func (l *settingsLease) Observe(context.Context, contract.SettingsReceipt, time.Duration) (contract.SettingsResult, error) {
	l.reads++
	return contract.SettingsResult{}, nil
}
func (l *settingsLease) Close() error { l.closed++; return nil }
func TestSettingsDocumentsPublishedGenerationRecoveryOriginalEvidenceAndPlanCapture(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*settingsHandle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &settingsHandle{tasksHandle: tasksHandle{handle: handle{id: p.ID, caps: contract.Capabilities{Metadata: true}}}, settings: &settingsLease{}}
		handles = append(handles, h)
		return h, nil
	}))
	first, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := m.SettingsDocuments(1)
	if err != nil {
		t.Fatal(err)
	}
	o := contract.ObjectRef{ProfileID: 1, Catalog: "owned", Kind: "settings", Name: "settings"}
	lease, err := provider.ReserveSettingsDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	fields := []string{"original"}
	if _, err = lease.Apply(context.Background(), contract.SettingsPlan{Fields: fields}); err != nil {
		t.Fatal(err)
	}
	fields[0] = "caller-mutated"
	if handles[0].settings.fields[0] != "original" {
		t.Fatal("runtime settings plan exported caller-owned field slice")
	}
	second, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal("published settings generation did not advance", err)
	}
	if provider.SupportsSettingsDocument(o) {
		t.Fatal("original settings provider followed replacement")
	}
	if _, err = lease.Read(context.Background()); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old settings read rebound", err)
	}
	if _, err = lease.Prepare(contract.SettingsSnapshot{}, `{}`, false); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old settings preview rebound", err)
	}
	if _, err = lease.Apply(context.Background(), contract.SettingsPlan{}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old settings write rebound", err)
	}
	current, err := m.SettingsDocuments(1)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := current.RecoverSettingsDocument(o, lease)
	if err != nil || recovery.Receipt.UID != "0" || recovery.Snapshot.Object != o || handles[0].settings.closed != 1 || handles[1].settings.closed != 0 || handles[1].settings.reads != 0 {
		t.Fatal("settings recovery read/replayed or closed replacement", err)
	}
	foreign := o
	foreign.ProfileID = 2
	if _, err = current.RecoverSettingsDocument(foreign, lease); err == nil {
		t.Fatal("settings receipt recovery changed original profile")
	}
	if _, err = recovery.Lease.Observe(context.Background(), recovery.Receipt, time.Second); err != nil {
		t.Fatal(err)
	}
	if !m.IsConnected(1) || handles[1].settings.reads != 1 {
		t.Fatal("explicit settings observation lost current original profile")
	}
	_ = recovery.Lease.Close()
}
