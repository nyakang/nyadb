package objectworkspace

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type settingsTestLease struct {
	read          func(context.Context) (contract.SettingsSnapshot, error)
	apply         func(context.Context, contract.SettingsPlan) (contract.SettingsResult, error)
	observe       func(context.Context, contract.SettingsReceipt, time.Duration) (contract.SettingsResult, error)
	calls, closes atomic.Int32
}

func (l *settingsTestLease) Read(ctx context.Context) (contract.SettingsSnapshot, error) {
	l.calls.Add(1)
	return l.read(ctx)
}
func (l *settingsTestLease) Prepare(o contract.SettingsSnapshot, draft string, reset bool) (contract.SettingsPlan, error) {
	return contract.SettingsPlan{Original: o, Draft: draft, Patch: `{"searchableAttributes":["title"]}`, Reset: reset, Sequence: 1, Token: "original-plan", Fields: []string{"searchableAttributes"}}, nil
}
func (l *settingsTestLease) Apply(ctx context.Context, p contract.SettingsPlan) (contract.SettingsResult, error) {
	l.calls.Add(1)
	return l.apply(ctx, p)
}
func (l *settingsTestLease) Observe(ctx context.Context, r contract.SettingsReceipt, b time.Duration) (contract.SettingsResult, error) {
	l.calls.Add(1)
	return l.observe(ctx, r, b)
}
func (l *settingsTestLease) Close() error { l.closes.Add(1); return nil }

type settingsTestProvider struct {
	lease     *settingsTestLease
	recovery  *settingsTestLease
	receipt   contract.SettingsReceipt
	uncertain bool
}

func (p *settingsTestProvider) SupportsSettingsDocument(o contract.ObjectRef) bool {
	return settingsObject(o)
}
func (p *settingsTestProvider) ReserveSettingsDocument(contract.ObjectRef) (contract.SettingsDocumentLease, error) {
	return p.lease, nil
}
func (p *settingsTestProvider) RecoverSettingsDocument(o contract.ObjectRef, old contract.SettingsDocumentLease) (contract.SettingsRecovery, error) {
	_ = old.Close()
	return contract.SettingsRecovery{Lease: p.recovery, Snapshot: settingsTestSnapshot(), Receipt: p.receipt, Uncertain: p.uncertain}, nil
}
func settingsTestSnapshot() contract.SettingsSnapshot {
	return contract.SettingsSnapshot{Object: contract.ObjectRef{ProfileID: 7, Catalog: "owned", Kind: "settings", Name: "settings"}, IndexCreated: "original-index-time", Raw: `{"searchableAttributes":["*"],"future":9007199254740993}`, Fingerprint: "original", Token: "original-settings-proof"}
}
func settingsTestState(t *testing.T, p *settingsTestProvider) (*State, *Session, *Document) {
	t.Helper()
	current := &Session{ProfileID: 7, Generation: 8, Connected: true}
	s := New(Dependencies{Session: func() Session { return *current }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Generation: current.Generation, Connected: true} }, SettingsDocumentsFor: func(int64) (contract.SettingsDocumentsProvider, error) { return p, nil }})
	t.Cleanup(s.CloseAll)
	d := s.OpenSettings(1, settingsTestSnapshot().Object)
	if d == nil {
		t.Fatal("settings document unavailable")
	}
	return s, current, d
}
func waitSettings(t *testing.T, s *State, d *Document) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for d.Loading() && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.Loading() {
		t.Fatal("original settings executor did not settle")
	}
}
func TestSettingsParkedReadsGenerationsAndClose(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	l := &settingsTestLease{read: func(context.Context) (contract.SettingsSnapshot, error) {
		close(entered)
		<-release
		return settingsTestSnapshot(), nil
	}}
	s, current, d := settingsTestState(t, &settingsTestProvider{lease: l})
	s.LoadSettings(d, nil, false)
	<-entered
	current.ProfileID = 9
	close(release)
	time.Sleep(10 * time.Millisecond)
	s.Consume(ui.Services{})
	if !d.Loading() || s.SettingsSnapshot(d).Raw != "" {
		t.Fatal("parked settings read was consumed by another profile")
	}
	current.ProfileID = 7
	waitSettings(t, s, d)
	if s.SettingsSnapshot(d).Raw == "" {
		t.Fatal("original parked settings lost")
	}
	current.Generation++
	s.Consume(ui.Services{})
	if !d.settings.lost || d.settings.fresh || s.SettingsSnapshot(d).Raw == "" {
		t.Fatal("idle replacement erased original settings cache")
	}
	s.Close(d)
	s.Close(d)
	if l.closes.Load() != 1 || d.settings != nil || len(s.documents) != 0 {
		t.Fatal("original settings lease/cache leaked")
	}
}
func TestSettingsAdmissionCancelAcknowledgementRecoveryAndNoReplay(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	l := &settingsTestLease{read: func(context.Context) (contract.SettingsSnapshot, error) { return settingsTestSnapshot(), nil }, apply: func(ctx context.Context, p contract.SettingsPlan) (contract.SettingsResult, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return contract.SettingsResult{State: "uncertain"}, errors.Join(contract.ErrSettingsLeaseLost, context.Canceled)
	}}
	next := &settingsTestLease{read: l.read}
	provider := &settingsTestProvider{lease: l, recovery: next, uncertain: true}
	s, _, d := settingsTestState(t, provider)
	s.LoadSettings(d, nil, false)
	waitSettings(t, s, d)
	s.EditSettings(d)
	d.settings.draft = `{"searchableAttributes":["title"]}`
	if s.PrepareSettings(d, false) != nil {
		t.Fatal("settings preview failed")
	}
	s.ApplySettings(d, nil)
	<-entered
	s.CancelSettings(d)
	if !d.Loading() || !d.settings.cancelRequested || !d.DesignDirty() {
		t.Fatal("settings cancellation discarded drafts/busy before acknowledgement")
	}
	s.LoadSettings(d, nil, true)
	if l.calls.Load() != 2 {
		t.Fatal("settings request replayed while canceling")
	}
	close(release)
	waitSettings(t, s, d)
	if !s.SettingsUncertain(d) || !d.DesignDirty() || d.settings.snapshot.Raw == "" {
		t.Fatal("uncertain original settings lost drafts/evidence")
	}
	s.RecoverSettings(d)
	if next.calls.Load() != 0 || !s.SettingsUncertain(d) || s.CanPrepareSettings(d) || d.settings.fresh {
		t.Fatal("settings recovery replayed or enabled uncertain write")
	}
	s.LoadSettings(d, nil, true)
	waitSettings(t, s, d)
	if s.CanPrepareSettings(d) || !d.DesignDirty() {
		t.Fatal("fresh read erased uncertain settings intent")
	}
}
func TestSettingsCompleteOriginalTaskKeepsNativeStateAndSnapshotInert(t *testing.T) {
	source := settingsTestSnapshot()
	receipt := contract.SettingsReceipt{Object: source.Object, IndexCreated: source.IndexCreated, UID: "0", Raw: `{"taskUid":0,"future":9007199254740993}`, Token: "original-receipt"}
	l := &settingsTestLease{read: func(context.Context) (contract.SettingsSnapshot, error) { return source, nil }, apply: func(context.Context, contract.SettingsPlan) (contract.SettingsResult, error) {
		return contract.SettingsResult{State: "enqueued", Receipt: receipt}, nil
	}, observe: func(context.Context, contract.SettingsReceipt, time.Duration) (contract.SettingsResult, error) {
		actual := source
		actual.Raw = `{"searchableAttributes":["title"],"future":9007199254740993}`
		return contract.SettingsResult{State: "succeeded", Receipt: receipt, TaskRaw: `{"uid":0,"status":"succeeded"}`, Snapshot: actual}, nil
	}}
	s, _, d := settingsTestState(t, &settingsTestProvider{lease: l})
	s.LoadSettings(d, nil, false)
	waitSettings(t, s, d)
	s.EditSettings(d)
	d.settings.draft = `{"searchableAttributes":["title"],"apiKey":"temporary-secret"}`
	if s.PrepareSettings(d, false) != nil {
		t.Fatal("settings prepare failed")
	}
	s.ApplySettings(d, nil)
	waitSettings(t, s, d)
	if s.SettingsResult(d).State != "enqueued" || !d.DesignDirty() || s.CanPrepareSettings(d) {
		t.Fatal("settings admission treated as completion")
	}
	snapshot := s.DocumentSnapshot(d)
	if strings.Contains(string(snapshot), "secret") || strings.Contains(string(snapshot), "taskUid") || strings.Contains(string(snapshot), "original-receipt") {
		t.Fatal("settings snapshot persisted credentials/native receipt")
	}
	s.ObserveSettings(d, nil)
	waitSettings(t, s, d)
	if d.DesignDirty() || s.SettingsResult(d).State != "succeeded" || !strings.Contains(s.SettingsSnapshot(d).Raw, "title") {
		t.Fatal("confirmed current native settings not published")
	}
	fresh := s.OpenSettings(2, source.Object)
	s.RestoreDocumentPresentation(fresh, snapshot)
	if fresh.settings.snapshot.Raw != "" || fresh.settings.lease != nil || l.calls.Load() != 3 {
		t.Fatal("settings snapshot replayed reads/writes")
	}
}
func TestSettingsLeaveDecisionSmallPreviewAndModifiedDraft(t *testing.T) {
	l := &settingsTestLease{read: func(context.Context) (contract.SettingsSnapshot, error) { return settingsTestSnapshot(), nil }}
	s, _, d := settingsTestState(t, &settingsTestProvider{lease: l})
	s.LoadSettings(d, nil, false)
	waitSettings(t, s, d)
	s.EditSettings(d)
	d.settings.draft = `{"searchableAttributes":["title"]}`
	if s.PrepareSettings(d, false) != nil {
		t.Fatal("settings prepare failed")
	}
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 720, 520)
	r, ok := tt.Find("确认提交设置更改")
	if !ok || r.H <= 0 || r.Y < 0 || r.Y+r.H > 520 {
		t.Fatal("settings confirmation outside viewport", r)
	}
	if err := tt.Click("返回编辑"); err != nil {
		t.Fatal(err)
	}
	d.settings.uncertain = true
	var calls int
	if !s.GuardDesignLeave(d, func(ui.Services) {
		calls++
		if s.GuardDesignLeave(d, nil) {
			t.Fatal("acknowledged settings leave repeated")
		}
	}) {
		t.Fatal("uncertain dirty settings leave was not guarded")
	}
	tt.Frame()
	if err := tt.Click("放弃本地草稿并离开"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || d.DesignDirty() || !s.SettingsUncertain(d) {
		t.Fatal("leave lost acknowledgment or erased unknown native outcome")
	}
}
