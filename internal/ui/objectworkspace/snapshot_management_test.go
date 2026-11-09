package objectworkspace

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type snapshotTestProvider struct{ lease *snapshotTestLease }

func (p *snapshotTestProvider) SupportsSnapshots(o contract.ObjectRef, target contract.SnapshotTarget, action string) bool {
	return snapshotManagementObject(o) && !(target.Scope == contract.SnapshotStorage && (action == "upload" || action == "recover" || action == "stream"))
}
func (p *snapshotTestProvider) ReserveSnapshotManagementDocument(o contract.ObjectRef, id uint64) (contract.SnapshotLease, error) {
	p.lease.object = o
	return p.lease, nil
}

type snapshotTestObservation struct{ metadata contract.SnapshotMetadata }

func (o *snapshotTestObservation) Snapshot() contract.SnapshotMetadata {
	return contract.CloneSnapshotMetadata(o.metadata)
}

type snapshotTestPlan struct {
	action string
	target contract.SnapshotTarget
	source contract.SnapshotSource
}

func (p *snapshotTestPlan) Action() string                  { return p.action }
func (p *snapshotTestPlan) Target() contract.SnapshotTarget { return p.target }
func (p *snapshotTestPlan) UploadSource() (contract.SnapshotSource, bool) {
	return p.source, p.action == "upload"
}
func (p *snapshotTestPlan) RestoresData() bool { return p.action == "upload" || p.action == "recover" }
func (p *snapshotTestPlan) Preview() contract.SnapshotRequest {
	return contract.SnapshotRequest{Method: "POST", Path: "/collections/owned/snapshots", Body: []byte(`{"priority":"snapshot"}`)}
}

type snapshotTestLease struct {
	object                                     contract.ObjectRef
	reads, prepares, submits, verifies, closes atomic.Int32
	read                                       func(context.Context, contract.SnapshotTarget) (contract.SnapshotObservation, error)
	submit                                     func(context.Context, contract.SnapshotPlan) (contract.SnapshotResult, error)
	verify                                     func(context.Context) (contract.SnapshotResult, error)
}

func (l *snapshotTestLease) Object() contract.ObjectRef { return l.object }
func (l *snapshotTestLease) Generation() uint64         { return 7 }
func (l *snapshotTestLease) Read(ctx context.Context, target contract.SnapshotTarget) (contract.SnapshotObservation, error) {
	l.reads.Add(1)
	if l.read != nil {
		return l.read(ctx, target)
	}
	return &snapshotTestObservation{contract.SnapshotMetadata{Object: l.object, Target: target, Configuration: `{"n":9007199254740993}`, Snapshots: []contract.SnapshotDescription{{Name: "owned.snapshot", Size: 9007199254740993, Raw: []byte(`{"size":9007199254740993}`)}}, Reads: []contract.SnapshotRead{{Response: contract.SnapshotResponse{Status: 200, Body: []byte(`{"result":[]}`)}}}}}, nil
}
func (l *snapshotTestLease) Prepare(o contract.SnapshotObservation, draft contract.SnapshotDraft) (contract.SnapshotPlan, error) {
	l.prepares.Add(1)
	return &snapshotTestPlan{draft.Action, o.Snapshot().Target, draft.Source}, nil
}
func snapshotTestResult(state string) contract.SnapshotResult {
	return contract.SnapshotResult{Action: "create", State: state, Receipt: contract.SnapshotReceipt{Action: "create", HasResult: true, Result: true, Response: contract.SnapshotResponse{Status: 200, Body: []byte(`{"result":true}`)}}}
}
func (l *snapshotTestLease) Submit(ctx context.Context, p contract.SnapshotPlan) (contract.SnapshotResult, error) {
	l.submits.Add(1)
	if l.submit != nil {
		return l.submit(ctx, p)
	}
	return snapshotTestResult("response"), nil
}
func (l *snapshotTestLease) Verify(ctx context.Context) (contract.SnapshotResult, error) {
	l.verifies.Add(1)
	if l.verify != nil {
		return l.verify(ctx)
	}
	return snapshotTestResult("applied"), nil
}
func (l *snapshotTestLease) Upload(ctx context.Context, p contract.SnapshotPlan, source io.ReadCloser, options contract.SnapshotTransferOptions) (contract.SnapshotResult, error) {
	defer source.Close()
	return l.Submit(ctx, p)
}
func (l *snapshotTestLease) Download(ctx context.Context, p contract.SnapshotPlan, sink io.WriteCloser, options contract.SnapshotTransferOptions) (contract.SnapshotResult, error) {
	defer sink.Close()
	return contract.SnapshotResult{Action: p.Action(), State: "transferred", Transfer: contract.SnapshotTransfer{Complete: true}}, nil
}
func (l *snapshotTestLease) Close() error { l.closes.Add(1); return nil }
func snapshotTestState(t *testing.T) (*State, *Document, *Session, *snapshotTestLease) {
	t.Helper()
	current := &Session{ProfileID: 1, Generation: 7, Connected: true}
	l := &snapshotTestLease{}
	s := New(Dependencies{Session: func() Session { return *current }, SessionFor: func(id int64) Session {
		return Session{ProfileID: id, Generation: current.Generation, Connected: current.Connected}
	}, SnapshotManagementFor: func(int64) (contract.SnapshotProvider, error) { return &snapshotTestProvider{l}, nil }})
	d := s.OpenSnapshotManagement(1, contract.ObjectRef{ProfileID: 1, Kind: "snapshot_management", Name: "snapshot_management"})
	if d == nil {
		t.Fatal("snapshot document unavailable")
	}
	t.Cleanup(s.CloseAll)
	f := s.SnapshotManagementForm(d)
	f.Collection = "owned"
	s.SetSnapshotManagementForm(d, f)
	return s, d, current, l
}
func settleSnapshots(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for d.Loading() {
		s.Consume(ui.Services{})
		if time.Now().After(deadline) {
			t.Fatal("original snapshot request did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}
func prepareTestSnapshot(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.ReadSnapshotManagement(d, nil)
	settleSnapshots(t, s, d)
	if err := s.PrepareSnapshotManagement(d, nil); err != nil {
		t.Fatal(err)
	}
	settleSnapshots(t, s, d)
	if !d.snapshots.preview {
		t.Fatal(d.Error())
	}
}
func TestSnapshotManagementOriginalReceiptReadbackNoReplayAndExactCopies(t *testing.T) {
	s, d, _, l := snapshotTestState(t)
	prepareTestSnapshot(t, s, d)
	if l.submits.Load() != 0 {
		t.Fatal("preview submitted operation")
	}
	m := s.SnapshotManagementSnapshot(d)
	m.Snapshots[0].Raw[0] = 'x'
	if !strings.Contains(string(s.SnapshotManagementSnapshot(d).Snapshots[0].Raw), "9007199254740993") {
		t.Fatal("snapshot cache aliased")
	}
	s.SubmitSnapshotManagement(d, ui.Services{})
	settleSnapshots(t, s, d)
	if !strings.Contains(snapshotDisplayEvidence(d.snapshots), `{"result":true}`) {
		t.Fatal("native JSON receipt is not readable")
	}
	for _, size := range [][2]int{{460, 360}, {720, 520}} {
		tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, size[0], size[1])
		r, ok := tt.Find("核验原快照操作")
		if !ok || r.W <= 0 || r.H <= 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
			t.Fatal("original verification hidden in compact viewport", size, r)
		}
	}
	if !s.SnapshotManagementPending(d) || s.CanSubmitSnapshotManagement(d) {
		t.Fatal("unverified receipt allowed replay")
	}
	s.ReadSnapshotManagement(d, nil)
	s.SubmitSnapshotManagement(d, ui.Services{})
	if l.reads.Load() != 1 || l.submits.Load() != 1 {
		t.Fatal("pending request replaced")
	}
	l.verify = func(context.Context) (contract.SnapshotResult, error) {
		return contract.SnapshotResult{}, errors.New("readback lost")
	}
	s.VerifySnapshotManagement(d, nil)
	settleSnapshots(t, s, d)
	if s.SnapshotManagementResult(d).Action != "create" || string(s.SnapshotManagementResult(d).Receipt.Response.Body) != `{"result":true}` || !s.SnapshotManagementPending(d) {
		t.Fatal("original receipt lost")
	}
	l.verify = nil
	s.VerifySnapshotManagement(d, nil)
	settleSnapshots(t, s, d)
	if s.SnapshotManagementPending(d) || d.DesignDirty() || l.submits.Load() != 1 {
		t.Fatal("verification replayed or did not settle")
	}
	if string(s.DocumentSnapshot(d)) != `{"Snapshots":true}` {
		t.Fatal("native evidence persisted")
	}
}
func TestSnapshotManagementCancellationAndParkedGenerationPreserveOriginal(t *testing.T) {
	s, d, current, l := snapshotTestState(t)
	entered, ack, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	l.read = func(ctx context.Context, target contract.SnapshotTarget) (contract.SnapshotObservation, error) {
		close(entered)
		<-ctx.Done()
		close(ack)
		<-release
		return nil, ctx.Err()
	}
	s.ReadSnapshotManagement(d, nil)
	<-entered
	s.CancelSnapshotManagement(d)
	<-ack
	if !d.Loading() {
		t.Fatal("cancel settled before executor acknowledgement")
	}
	close(release)
	settleSnapshots(t, s, d)
	l.read = nil
	prepareTestSnapshot(t, s, d)
	entered, release = make(chan struct{}), make(chan struct{})
	l.submit = func(context.Context, contract.SnapshotPlan) (contract.SnapshotResult, error) {
		close(entered)
		<-release
		return snapshotTestResult("response"), nil
	}
	s.SubmitSnapshotManagement(d, ui.Services{})
	<-entered
	current.ProfileID = 2
	close(release)
	<-d.snapshots.done
	s.Consume(ui.Services{})
	if !d.Loading() || d.snapshots.last.Receipt.HasResult {
		t.Fatal("parked result consumed for another profile")
	}
	current.ProfileID = 1
	current.Generation = 8
	settleSnapshots(t, s, d)
	if !d.snapshots.lost || !s.SnapshotManagementPending(d) || !s.SnapshotManagementResult(d).Receipt.HasResult {
		t.Fatal("replacement erased original evidence")
	}
	s.VerifySnapshotManagement(d, nil)
	s.SubmitSnapshotManagement(d, ui.Services{})
	if l.verifies.Load() != 0 || l.submits.Load() != 1 {
		t.Fatal("old operation followed replacement")
	}
}
func TestSnapshotManagementConfirmationStaleGuardAndCompactControls(t *testing.T) {
	s, d, _, l := snapshotTestState(t)
	f := s.SnapshotManagementForm(d)
	f.Action = "recover"
	f.Location = "file:///owned.snapshot"
	s.SetSnapshotManagementForm(d, f)
	prepareTestSnapshot(t, s, d)
	if s.CanSubmitSnapshotManagement(d) {
		t.Fatal("restoration skipped name confirmation")
	}
	s.SetSnapshotConfirmation(d, "owned")
	if !s.CanSubmitSnapshotManagement(d) {
		t.Fatal("original confirmation rejected")
	}
	for _, size := range [][2]int{{720, 520}, {1280, 800}} {
		tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, size[0], size[1])
		for _, label := range []string{"确认提交快照操作", "复制完整快照请求"} {
			r, ok := tt.Find(label)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
				t.Fatal("snapshot control outside viewport", size, label, r)
			}
		}
	}
	var next func(ui.Services)
	s.deps.BeforeSnapshots = func(_ ui.Services, id uint64, profile int64, generation uint64, plan contract.SnapshotPlan, continueWork func(ui.Services)) bool {
		next = continueWork
		return true
	}
	s.SubmitSnapshotManagement(d, ui.Services{})
	if next == nil {
		t.Fatal("original guard omitted")
	}
	s.Close(d)
	next(ui.Services{})
	if l.submits.Load() != 0 || l.closes.Load() != 1 {
		t.Fatal("closed continuation dispatched")
	}
}
