package objectworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

type keysTestLease struct {
	list                   func(context.Context, uint64, uint64) (contract.KeyPage, error)
	read                   func(context.Context, contract.KeyRecord) (contract.KeyRecord, error)
	apply                  func(context.Context, contract.KeyPlan) (contract.KeyResult, error)
	calls, closes, reveals atomic.Int32
	work                   sync.WaitGroup
}

func (l *keysTestLease) List(ctx context.Context, offset, limit uint64) (contract.KeyPage, error) {
	l.calls.Add(1)
	l.work.Add(1)
	defer l.work.Done()
	if l.list != nil {
		return l.list(ctx, offset, limit)
	}
	return keysTestPage(offset, limit), nil
}
func (l *keysTestLease) Read(ctx context.Context, r contract.KeyRecord) (contract.KeyRecord, error) {
	l.calls.Add(1)
	if l.read != nil {
		return l.read(ctx, r)
	}
	return r, nil
}
func (l *keysTestLease) Prepare(action string, r contract.KeyRecord, draft contract.KeyDraft) (contract.KeyPlan, error) {
	return contract.KeyPlan{Action: action, Request: "original " + action, Body: "redacted request", Original: r, Draft: draft, Sequence: 1, Token: "original-plan"}, nil
}
func (l *keysTestLease) Apply(ctx context.Context, p contract.KeyPlan) (contract.KeyResult, error) {
	l.calls.Add(1)
	l.work.Add(1)
	defer l.work.Done()
	if l.apply != nil {
		return l.apply(ctx, p)
	}
	r := keysTestRecord()
	r.Name, r.Description = p.Draft.Name, p.Draft.Description
	return contract.KeyResult{State: "completed", Status: 201, Action: p.Action, Record: r, UID: r.UID, Request: p.Request, Response: r.Raw}, nil
}
func (l *keysTestLease) Reveal(r contract.KeyRecord) (string, error) {
	l.reveals.Add(1)
	if r.Token == "" {
		return "", contract.ErrKeyConflict
	}
	return "original-private-key", nil
}
func (l *keysTestLease) Close() error { l.closes.Add(1); l.work.Wait(); return nil }

type keysTestProvider struct {
	lease, recovered *keysTestLease
	uncertain        bool
	last             contract.KeyResult
}

func (p *keysTestProvider) SupportsKeysDocument(o contract.ObjectRef) bool { return keysObject(o) }
func (p *keysTestProvider) ReserveKeysDocument(contract.ObjectRef) (contract.KeysDocumentLease, error) {
	return p.lease, nil
}
func (p *keysTestProvider) RecoverKeysDocument(_ contract.ObjectRef, old contract.KeysDocumentLease) (contract.KeyRecovery, error) {
	_ = old.Close()
	page := keysTestPage(0, 20)
	page.Results[0].Token = "recovered-original-proof"
	return contract.KeyRecovery{Lease: p.recovered, Page: page, Last: p.last, Uncertain: p.uncertain}, nil
}
func keysTestRecord() contract.KeyRecord {
	return contract.KeyRecord{UID: "be602e9b-f9d9-4fa7-844a-4372640abf83", Name: "owned", Description: "original description", Actions: []string{"search"}, Indexes: []string{"owned*"}, Raw: `{"name":"owned","key":"[hidden]","exact":9007199254740993}`, Token: "original-record", Fingerprint: "original-proof"}
}
func keysTestPage(offset, limit uint64) contract.KeyPage {
	return contract.KeyPage{Results: []contract.KeyRecord{keysTestRecord()}, Offset: offset, Limit: limit, Total: 100, Raw: `{"key":"[hidden]","exact":9007199254740993}`}
}
func keysTestState(t *testing.T, p *keysTestProvider) (*State, *Session, *Document) {
	t.Helper()
	current := &Session{ProfileID: 7, Generation: 8, Connected: true}
	s := New(Dependencies{Session: func() Session { return *current }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Generation: current.Generation, Connected: true} }, KeysDocumentsFor: func(int64) (contract.KeysDocumentsProvider, error) { return p, nil }})
	t.Cleanup(s.CloseAll)
	d := s.OpenKeys(1, contract.ObjectRef{ProfileID: 7, Kind: "keys", Name: "keys"})
	if d == nil {
		t.Fatal("original key document unavailable")
	}
	return s, current, d
}
func waitKeys(t *testing.T, s *State, d *Document) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for d.Loading() && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.Loading() {
		t.Fatal("original key worker did not settle")
	}
}

func TestKeysParkedReadsGenerationCacheAndSecretVisibility(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	l := &keysTestLease{list: func(_ context.Context, offset, limit uint64) (contract.KeyPage, error) {
		close(entered)
		<-release
		return keysTestPage(offset, limit), nil
	}}
	s, current, d := keysTestState(t, &keysTestProvider{lease: l})
	s.LoadKeys(d, nil, false)
	<-entered
	current.ProfileID = 9
	close(release)
	time.Sleep(5 * time.Millisecond)
	s.Consume(ui.Services{})
	if !d.Loading() || s.KeysPage(d).Raw != "" {
		t.Fatal("parked original key response consumed by another profile")
	}
	current.ProfileID = 7
	waitKeys(t, s, d)
	s.SelectKey(d, 0)
	s.RevealKey(d)
	if d.keys.secret != "original-private-key" || l.calls.Load() != 1 {
		t.Fatal("explicit secret reveal performed I/O or lost value")
	}
	current.ProfileID = 9
	s.Consume(ui.Services{})
	if d.keys.secret != "" || d.keys.secretOpen {
		t.Fatal("parked secret remained visible")
	}
	current.ProfileID = 7
	s.RevealKey(d)
	current.Generation++
	s.Consume(ui.Services{})
	if !d.keys.lost || d.keys.fresh || s.KeysPage(d).Raw == "" || d.keys.secret != "" {
		t.Fatal("generation replacement lost cached page or exposed secret")
	}
	if _, err := s.KeySecret(d); !errors.Is(err, contract.ErrKeyLeaseLost) {
		t.Fatal("secret followed replacement")
	}
	s.Close(d)
	s.Close(d)
	if l.closes.Load() != 1 || d.keys != nil || len(s.documents) != 0 {
		t.Fatal("original key document close leaked or repeated")
	}
}
func TestKeysConcretePreviewModifiedDraftSelectionAndSnapshotInert(t *testing.T) {
	l := &keysTestLease{}
	s, _, d := keysTestState(t, &keysTestProvider{lease: l})
	s.LoadKeys(d, nil, false)
	waitKeys(t, s, d)
	s.SelectKey(d, 0)
	s.EditKey(d, false)
	d.keys.form.Name = "changed"
	if err := s.PrepareKey(d, false); err != nil {
		t.Fatal(err)
	}
	d.keys.form.Name = "changed after preview"
	s.ApplyKey(d, nil)
	if l.calls.Load() != 1 || d.Loading() || d.keys.plan != nil {
		t.Fatal("modified preview transmitted")
	}
	if err := s.PrepareKey(d, false); err != nil {
		t.Fatal(err)
	}
	payload := d.keys.plan.Draft
	d.keys.form.Actions = "different"
	if payload.Actions[0] != "search" {
		t.Fatal("preview policy aliased form input")
	}
	s.ApplyKey(d, nil)
	if l.calls.Load() != 1 {
		t.Fatal("modified policy preview transmitted")
	}
	s.DiscardKeyDraft(d)
	s.RevealKey(d)
	snapshot := s.DocumentSnapshot(d)
	for _, bad := range []string{"original-private-key", "original-record", "original-plan", "9007199254740993", "owned", "changed"} {
		if strings.Contains(string(snapshot), bad) {
			t.Fatal("key snapshot persisted runtime/draft/secret content")
		}
	}
	fresh := s.OpenKeys(2, d.Object())
	s.RestoreDocumentPresentation(fresh, snapshot)
	if fresh.keys.lease != nil || fresh.keys.page != nil || fresh.keys.secret != "" || l.calls.Load() != 1 {
		t.Fatal("key presentation restoration performed I/O or retained runtime state")
	}
	if s.KeysScopeSupported(contract.ObjectRef{ProfileID: 7, Catalog: "other", Kind: "keys", Name: "keys"}) {
		t.Fatal("key page expanded to qualified index")
	}
}

func TestKeysDisconnectedActiveWriteSettlesWithoutConsumingParkedEvidence(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	l := &keysTestLease{apply: func(_ context.Context, p contract.KeyPlan) (contract.KeyResult, error) {
		close(entered)
		<-release
		return contract.KeyResult{State: "uncertain", Request: p.Request}, errors.New("original acknowledgement lost")
	}}
	s, current, d := keysTestState(t, &keysTestProvider{lease: l})
	active := true
	s.deps.DocumentActive = func(id uint64) bool { return active && id == d.id }
	s.EditKey(d, true)
	if err := s.PrepareKey(d, false); err != nil {
		t.Fatal(err)
	}
	s.ApplyKey(d, nil)
	<-entered
	current.ProfileID = 0
	s.deps.SessionFor = func(int64) Session { return Session{} }
	active = false
	close(release)
	time.Sleep(5 * time.Millisecond)
	s.Consume(ui.Services{})
	if !d.Loading() || s.KeysUncertain(d) {
		t.Fatal("disconnected inactive document consumed parked result")
	}
	active = true
	waitKeys(t, s, d)
	if !s.KeysUncertain(d) || !d.DesignDirty() || s.KeyResult(d).Request == "" || !d.keys.lost || s.CanCreateKey(d) {
		t.Fatal("disconnected displayed document lost its original unknown write and draft")
	}
}
func TestKeysSynchronousCompletionCancelAcknowledgementAndUncertainRecovery(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	l := &keysTestLease{apply: func(ctx context.Context, p contract.KeyPlan) (contract.KeyResult, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return contract.KeyResult{State: "uncertain", Request: p.Request}, errors.Join(contract.ErrKeyLeaseLost, context.Canceled)
	}}
	next := &keysTestLease{}
	provider := &keysTestProvider{lease: l, recovered: next, uncertain: true, last: contract.KeyResult{State: "uncertain", Request: "original create"}}
	s, _, d := keysTestState(t, provider)
	s.LoadKeys(d, nil, false)
	waitKeys(t, s, d)
	s.EditKey(d, true)
	d.keys.form.Name = "must retain"
	if err := s.PrepareKey(d, false); err != nil {
		t.Fatal(err)
	}
	s.ApplyKey(d, nil)
	<-entered
	s.Cancel(d)
	if !d.Loading() || !d.keys.cancelRequested || !d.DesignDirty() {
		t.Fatal("cancel cleared busy/draft before acknowledgement")
	}
	s.LoadKeys(d, nil, true)
	s.RecoverKeys(d)
	if l.calls.Load() != 2 || next.calls.Load() != 0 {
		t.Fatal("cancel caused replacement or replay")
	}
	close(release)
	waitKeys(t, s, d)
	if !s.KeysUncertain(d) || !d.DesignDirty() || s.KeyResult(d).Request == "" || s.KeysPage(d).Raw == "" {
		t.Fatal("unknown original key submission lost evidence/draft")
	}
	s.RecoverKeys(d)
	if next.calls.Load() != 0 || !s.KeysUncertain(d) || s.CanCreateKey(d) || s.KeyResult(d).Request != "original create" {
		t.Fatal("recovery read or enabled replay of unknown key write")
	}
	s.DiscardKeyDraft(d)
	s.LoadKeys(d, nil, true)
	waitKeys(t, s, d)
	if s.CanCreateKey(d) || !s.KeysUncertain(d) {
		t.Fatal("explicit current read erased uncertainty")
	}
}
func TestKeysCompletedOriginalWriteDoesNotEnqueueAndCloseWaitsExecutor(t *testing.T) {
	l := &keysTestLease{}
	s, _, d := keysTestState(t, &keysTestProvider{lease: l})
	s.EditKey(d, true)
	d.keys.form.Name = "created"
	if err := s.PrepareKey(d, false); err != nil {
		t.Fatal(err)
	}
	s.ApplyKey(d, nil)
	waitKeys(t, s, d)
	if s.KeyResult(d).State != "completed" || s.KeyResult(d).Status != 201 || d.DesignDirty() || !s.CanModifyKey(d) {
		t.Fatal("synchronous key completion became pending admission or lost created record")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	l.apply = func(ctx context.Context, p contract.KeyPlan) (contract.KeyResult, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return contract.KeyResult{State: "uncertain", Request: p.Request}, context.Canceled
	}
	s.EditKey(d, false)
	d.keys.form.Name = "closing"
	if err := s.PrepareKey(d, false); err != nil {
		t.Fatal(err)
	}
	s.ApplyKey(d, nil)
	<-entered
	checked := make(chan struct{})
	go func() { time.Sleep(10 * time.Millisecond); close(release); close(checked) }()
	s.Close(d)
	<-checked
	if d.keys != nil || l.closes.Load() != 1 || d.Loading() {
		t.Fatal("close returned before executor or retained key caches")
	}
}
func TestKeysLocalizedPreviewLeaveGuardAndSecretModalSmallWindows(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		l := &keysTestLease{}
		s, _, d := keysTestState(t, &keysTestProvider{lease: l})
		s.LoadKeys(d, nil, false)
		waitKeys(t, s, d)
		s.SelectKey(d, 0)
		s.EditKey(d, false)
		d.keys.form.Name = "updated"
		if err := s.PrepareKey(d, false); err != nil {
			t.Fatal(err)
		}
		tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 720, 520)
		if r, ok := tt.Find(locales.Text("确认提交密钥操作")); !ok || r.H <= 0 || r.Y < 0 || r.Y+r.H > 520 {
			t.Fatal("localized key confirmation outside viewport", language, r)
		}
		if err := tt.Click(locales.Text("返回编辑")); err != nil {
			t.Fatal(err)
		}
		var next int
		if !s.GuardDesignLeave(d, func(ui.Services) { next++ }) {
			t.Fatal("key draft leave unguarded")
		}
		tt.Frame()
		if err := tt.Click(locales.Text("放弃本地密钥草稿并离开")); err != nil {
			t.Fatal(err)
		}
		if next != 1 || d.DesignDirty() || s.GuardDesignLeave(d, nil) {
			t.Fatal("key leave acknowledgement repeated or kept draft")
		}
		s.RevealKey(d)
		tt.Frame()
		if !tt.HasText("original-private-key") {
			t.Fatal("explicit temporary key missing")
		}
		if err := tt.Click(locales.Text("隐藏密钥")); err != nil {
			t.Fatal(err)
		}
		if tt.HasText("original-private-key") || d.keys.secret != "" {
			t.Fatal("hidden secret remained in view/model")
		}
		raw, _ := json.Marshal(s.KeysPage(d))
		if strings.Contains(string(raw), "original-private-key") {
			t.Fatal("public key page leaked secret")
		}
	}
}
