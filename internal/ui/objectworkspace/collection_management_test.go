package objectworkspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type collectionTestProvider struct{ lease *collectionTestLease }

func (p *collectionTestProvider) SupportsCollectionManagement(o contract.ObjectRef, action string) bool {
	return collectionManagementObject(o)
}
func (p *collectionTestProvider) ReserveCollectionManagementDocument(o contract.ObjectRef, id uint64) (contract.CollectionManagementLease, error) {
	p.lease.object = o
	return p.lease, nil
}

type collectionTestLease struct {
	object                                     contract.ObjectRef
	reads, prepares, submits, verifies, closes atomic.Int32
	read                                       func(context.Context, []contract.NamespaceRef) (contract.CollectionManagementObservation, error)
	submit                                     func(context.Context, contract.CollectionManagementPlan) (contract.CollectionManagementResult, error)
	verify                                     func(context.Context) (contract.CollectionManagementResult, error)
	close                                      func()
	lastDraft                                  contract.CollectionManagementDraft
}

func (l *collectionTestLease) Object() contract.ObjectRef { return l.object }
func (l *collectionTestLease) Generation() uint64         { return 7 }
func (l *collectionTestLease) Read(ctx context.Context, refs []contract.NamespaceRef) (contract.CollectionManagementObservation, error) {
	l.reads.Add(1)
	if l.read != nil {
		return l.read(ctx, refs)
	}
	return &collectionTestObservation{contract.CollectionManagementSnapshot{Object: l.object, Targets: refs, Collections: refs, Configurations: []contract.CollectionConfiguration{{Collection: refs[0], JSON: `{"metadata":{"n":9007199254740993}}`}}, Aliases: []contract.CollectionAlias{{Alias: collectionNamespace(l.object.ProfileID, "alias"), Collection: refs[0]}}}}, nil
}
func (l *collectionTestLease) Prepare(_ contract.CollectionManagementObservation, d contract.CollectionManagementDraft) (contract.CollectionManagementPlan, error) {
	l.prepares.Add(1)
	l.lastDraft = contract.CloneCollectionManagementDraft(d)
	return &collectionTestPlan{action: d.Action, request: contract.CollectionManagementRequest{Method: "PATCH", Path: "/collections/" + d.Collection.Catalog, Body: []byte(d.Configuration)}, affected: []contract.NamespaceRef{collectionNamespace(l.object.ProfileID, "owned")}}, nil
}
func (l *collectionTestLease) Submit(ctx context.Context, p contract.CollectionManagementPlan) (contract.CollectionManagementResult, error) {
	l.submits.Add(1)
	if l.submit != nil {
		return l.submit(ctx, p)
	}
	return collectionTestResult("response"), nil
}
func (l *collectionTestLease) Verify(ctx context.Context) (contract.CollectionManagementResult, error) {
	l.verifies.Add(1)
	if l.verify != nil {
		return l.verify(ctx)
	}
	return collectionTestResult("applied"), nil
}
func (l *collectionTestLease) Close() error {
	l.closes.Add(1)
	if l.close != nil {
		l.close()
	}
	return nil
}

type collectionTestObservation struct {
	snapshot contract.CollectionManagementSnapshot
}

func (o *collectionTestObservation) Snapshot() contract.CollectionManagementSnapshot {
	return contract.CloneCollectionManagementSnapshot(o.snapshot)
}

type collectionTestPlan struct {
	action   string
	request  contract.CollectionManagementRequest
	affected []contract.NamespaceRef
}

func (p *collectionTestPlan) Action() string { return p.action }
func (p *collectionTestPlan) Preview() contract.CollectionManagementRequest {
	return contract.CloneCollectionManagementRequest(p.request)
}
func (p *collectionTestPlan) Affected() []contract.NamespaceRef { return p.affected }
func collectionTestResult(state string) contract.CollectionManagementResult {
	return contract.CollectionManagementResult{State: state, Receipt: contract.CollectionManagementReceipt{Action: "collection_update", HasResult: true, Result: true, Response: contract.CollectionManagementResponse{Status: 200, Body: []byte(`{"result":true}`)}}, Matched: state == "applied"}
}
func collectionTestState(t *testing.T) (*State, *Document, *Session, *collectionTestLease) {
	t.Helper()
	current := &Session{ProfileID: 1, Generation: 7, Connected: true}
	lease := &collectionTestLease{}
	s := New(Dependencies{Session: func() Session { return *current }, SessionFor: func(id int64) Session {
		return Session{ProfileID: id, Generation: current.Generation, Connected: current.Connected}
	}, CollectionManagementFor: func(int64) (contract.CollectionManagementProvider, error) { return &collectionTestProvider{lease}, nil }})
	d := s.OpenCollectionManagement(1, contract.ObjectRef{ProfileID: 1, Kind: "collection_management", Name: "collection_management"})
	if d == nil {
		t.Fatal("native document unavailable")
	}
	t.Cleanup(s.CloseAll)
	s.SetCollectionManagementForm(d, CollectionManagementForm{Action: "collection_update", Collection: "owned", Configuration: `{"metadata":{"n":9007199254740993}}`, Timeout: "30"})
	return s, d, current, lease
}
func settleCollections(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for d.loading {
		s.Consume(ui.Services{})
		if time.Now().After(deadline) {
			t.Fatal("original management operation did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}
func prepareTestCollection(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.ReadCollectionManagement(d, nil)
	settleCollections(t, s, d)
	if err := s.PrepareCollectionManagement(d); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionManagementDraftPreviewReadbackEvidenceAndNoReplay(t *testing.T) {
	s, d, _, lease := collectionTestState(t)
	prepareTestCollection(t, s, d)
	if lease.submits.Load() != 0 || lease.lastDraft.Configuration != d.collections.form.Configuration {
		t.Fatal("preparation performed writes or changed exact JSON")
	}
	snapshot := s.CollectionManagementSnapshot(d)
	snapshot.Configurations[0].JSON = "changed"
	if !strings.Contains(s.CollectionManagementSnapshot(d).Configurations[0].JSON, "9007199254740993") {
		t.Fatal("snapshot copy modified original cache")
	}
	f := s.CollectionManagementForm(d)
	f.Configuration = `{"metadata":{}}`
	s.SetCollectionManagementForm(d, f)
	if s.CanSubmitCollectionManagement(d) {
		t.Fatal("changed draft retained original preview")
	}
	if err := s.PrepareCollectionManagement(d); err != nil {
		t.Fatal(err)
	}
	s.SubmitCollectionManagement(d, ui.Services{})
	settleCollections(t, s, d)
	if !s.CollectionManagementPending(d) || lease.submits.Load() != 1 || s.CanSubmitCollectionManagement(d) {
		t.Fatal("original receipt treated as verified or replayable")
	}
	r := s.CollectionManagementResult(d)
	r.Receipt.Response.Body[0] = 'x'
	s.ReadCollectionManagement(d, nil)
	s.SetCollectionManagementForm(d, f)
	s.SubmitCollectionManagement(d, ui.Services{})
	if lease.reads.Load() != 1 || lease.submits.Load() != 1 {
		t.Fatal("pending operation was replaced/replayed")
	}
	lease.verify = func(context.Context) (contract.CollectionManagementResult, error) {
		return contract.CollectionManagementResult{}, errors.New("original readback lost")
	}
	s.VerifyCollectionManagement(d, nil)
	settleCollections(t, s, d)
	if !s.CollectionManagementPending(d) || string(s.CollectionManagementResult(d).Receipt.Response.Body) != `{"result":true}` {
		t.Fatal("readback error discarded original receipt")
	}
	lease.verify = nil
	s.VerifyCollectionManagement(d, nil)
	settleCollections(t, s, d)
	if s.CollectionManagementPending(d) || d.DesignDirty() || lease.submits.Load() != 1 || lease.verifies.Load() != 2 {
		t.Fatal("explicit verification replayed or retained settled draft")
	}
}
func TestCollectionManagementReadCapturesFormAndAliasSourceIdentity(t *testing.T) {
	s, d, _, lease := collectionTestState(t)
	entered, release := make(chan []contract.NamespaceRef, 1), make(chan struct{})
	lease.read = func(_ context.Context, refs []contract.NamespaceRef) (contract.CollectionManagementObservation, error) {
		entered <- refs
		<-release
		return &collectionTestObservation{contract.CollectionManagementSnapshot{Object: lease.object, Targets: refs}}, nil
	}
	s.ReadCollectionManagement(d, nil)
	refs := <-entered
	d.collections.form.Collection = "changed after request"
	close(release)
	settleCollections(t, s, d)
	if refs[0].Catalog != "owned" || d.collections.observation != nil {
		t.Fatal("caller input changed original request or replaced current form")
	}
	d.collections.form.Collection = "owned"
	lease.read = nil
	s.ReadCollectionManagement(d, nil)
	settleCollections(t, s, d)
	f := s.CollectionManagementForm(d)
	f.Action = "alias_retarget"
	f.Alias = "alias"
	f.DestinationCollection = "destination"
	s.SetCollectionManagementForm(d, f)
	s.ReadCollectionManagement(d, nil)
	settleCollections(t, s, d)
	if err := s.PrepareCollectionManagement(d); err != nil {
		t.Fatal(err)
	}
	if len(lease.lastDraft.Aliases) != 2 || lease.lastDraft.Aliases[0].Action != "delete_alias" || lease.lastDraft.Aliases[1].Collection.Catalog != "destination" {
		t.Fatal("retarget was not one ordered native plan")
	}
	f.Alias = "foreign"
	s.SetCollectionManagementForm(d, f)
	s.ReadCollectionManagement(d, nil)
	settleCollections(t, s, d)
	if err := s.PrepareCollectionManagement(d); err == nil {
		t.Fatal("unobserved original alias accepted")
	}
}
func TestCollectionManagementParkedAndReplacedGenerationKeepOriginalReceipt(t *testing.T) {
	s, d, current, lease := collectionTestState(t)
	prepareTestCollection(t, s, d)
	entered, release := make(chan struct{}), make(chan struct{})
	lease.submit = func(context.Context, contract.CollectionManagementPlan) (contract.CollectionManagementResult, error) {
		close(entered)
		<-release
		return collectionTestResult("response"), nil
	}
	s.SubmitCollectionManagement(d, ui.Services{})
	<-entered
	current.ProfileID = 2
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for len(d.collections.results) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("original result never became buffered")
		}
		time.Sleep(time.Millisecond)
	}
	s.Consume(ui.Services{})
	if !d.loading || d.collections.last.Receipt.HasResult {
		t.Fatal("parked write reply consumed on a different profile")
	}
	current.ProfileID = 1
	current.Generation = 8
	settleCollections(t, s, d)
	if !d.collections.lost || !s.CollectionManagementPending(d) || !s.CollectionManagementResult(d).Receipt.HasResult || d.collections.form.Configuration == "" {
		t.Fatal("replacement discarded original draft or receipt")
	}
	s.VerifyCollectionManagement(d, nil)
	s.SubmitCollectionManagement(d, ui.Services{})
	if lease.verifies.Load() != 0 || lease.submits.Load() != 1 {
		t.Fatal("old document followed replacement")
	}
}
func TestCollectionManagementCancellationWaitsAndCloseRevokesContinuation(t *testing.T) {
	s, d, _, lease := collectionTestState(t)
	entered, ack, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	lease.read = func(ctx context.Context, refs []contract.NamespaceRef) (contract.CollectionManagementObservation, error) {
		close(entered)
		<-ctx.Done()
		close(ack)
		<-release
		return nil, ctx.Err()
	}
	s.ReadCollectionManagement(d, nil)
	<-entered
	s.CancelCollectionManagement(d)
	<-ack
	if !d.loading || !d.collections.cancelRequested {
		t.Fatal("cancellation returned before executor acknowledgement")
	}
	s.ReadCollectionManagement(d, nil)
	if lease.reads.Load() != 1 {
		t.Fatal("canceled request was replaced before acknowledgement")
	}
	close(release)
	settleCollections(t, s, d)
	lease.read = nil
	prepareTestCollection(t, s, d)
	var next func(ui.Services)
	s.deps.BeforeCollections = func(_ ui.Services, id uint64, profile int64, generation uint64, _ contract.CollectionManagementPlan, continueWork func(ui.Services)) bool {
		if id != d.id || profile != 1 || generation != 7 {
			t.Error("guard lost original identity")
		}
		next = continueWork
		return true
	}
	s.SubmitCollectionManagement(d, ui.Services{})
	if next == nil || !d.collections.guarding {
		t.Fatal("original guard was not requested")
	}
	s.Close(d)
	next(ui.Services{})
	if lease.submits.Load() != 0 || lease.closes.Load() != 1 || len(s.documents) != 0 {
		t.Fatal("closed guard dispatched or retained original lease")
	}
}
func TestCollectionManagementNameConfirmationAndTruncatedPreviewReview(t *testing.T) {
	s, d, _, _ := collectionTestState(t)
	f := s.CollectionManagementForm(d)
	f.Action = "collection_delete"
	s.SetCollectionManagementForm(d, f)
	prepareTestCollection(t, s, d)
	if s.CanSubmitCollectionManagement(d) {
		t.Fatal("destructive name confirmation bypassed")
	}
	d.collections.confirmation = "other"
	if s.CanSubmitCollectionManagement(d) {
		t.Fatal("foreign confirmation accepted")
	}
	d.collections.confirmation = "owned"
	if !s.CanSubmitCollectionManagement(d) {
		t.Fatal("original confirmation rejected")
	}
	d.collections.request.Body = []byte(strings.Repeat("x", 22000))
	if s.CanSubmitCollectionManagement(d) {
		t.Fatal("truncated native preview did not require full review")
	}
	d.collections.reviewed = true
	if !s.CanSubmitCollectionManagement(d) {
		t.Fatal("explicit full review rejected")
	}
	if !s.guardCollectionsLeave(d, nil) {
		t.Fatal("original preview lacked leave guard")
	}
	s.abandonCollections(d)
	if s.collectionsReady(d) {
		t.Fatal("abandoned unknown original became replayable")
	}
	if s.guardCollectionsLeave(d, nil) {
		t.Fatal(fmt.Sprint("acknowledged abandonment continued to guard"))
	}
}

func TestCollectionManagementPresentationPreservesExactNativeConfigurationAndCopiesEvidence(t *testing.T) {
	s, d, _, _ := collectionTestState(t)
	prepareTestCollection(t, s, d)
	visible := collectionSnapshotText(s.CollectionManagementSnapshot(d))
	if !strings.Contains(visible, "9007199254740993") || strings.Contains(visible, "collection_management") || !strings.Contains(visible, `"alias": "alias"`) {
		t.Fatal("native presentation lost exact values or included private document identity", visible)
	}
	s.SubmitCollectionManagement(d, ui.Services{})
	settleCollections(t, s, d)
	evidence := collectionEvidence(d.collections)
	if !strings.Contains(evidence, "collection_management") || !strings.Contains(evidence, "9007199254740993") {
		t.Fatal("explicit original evidence lost identity/native configuration")
	}
	persisted := s.DocumentSnapshot(d)
	if strings.Contains(string(persisted), "metadata") || strings.Contains(string(persisted), "result") || strings.Contains(string(persisted), "9007199254740993") {
		t.Fatal("workspace persisted management draft/receipt")
	}
}

func TestCollectionManagementCompactViewKeepsOriginalVerificationAndCopyAccessible(t *testing.T) {
	s, d, _, _ := collectionTestState(t)
	prepareTestCollection(t, s, d)
	s.SubmitCollectionManagement(d, ui.Services{})
	settleCollections(t, s, d)
	for _, size := range [][2]int{{460, 360}, {900, 600}} {
		tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, size[0], size[1])
		for _, label := range []string{"核验原集合操作", "复制完整集合请求"} {
			r, ok := tt.Find(label)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
				t.Fatal("original evidence control outside compact view", size, label, r)
			}
		}
		if err := tt.Click("核验原集合操作"); err != nil {
			t.Fatal("original verification inaccessible", size, err)
		}
		settleCollections(t, s, d)
		if s.CollectionManagementResult(d).State != "applied" {
			t.Fatal("compact verification did not execute")
		}
		// Retain a fresh original receipt for the next viewport.
		if err := s.PrepareCollectionManagement(d); err == nil {
			t.Fatal("stale observation reused after submission")
		}
		prepareTestCollection(t, s, d)
		s.SubmitCollectionManagement(d, ui.Services{})
		settleCollections(t, s, d)
	}
}
