package kvworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type snapshotTestPlan struct {
	owner   *snapshotTestLease
	request contract.KVSnapshotRequest
	scope   contract.KVSnapshotScope
	used    atomic.Bool
}

func TestSnapshotCancelAndCloseJoinOriginalBlockedCapture(t *testing.T) {
	for _, closeDocument := range []bool{false, true} {
		t.Run(strconv.FormatBool(closeDocument), func(t *testing.T) {
			s, d, l, a, _ := snapshotTestState(t)
			l.entered, l.release = make(chan struct{}), make(chan struct{})
			s.PrepareSnapshot(d)
			d.snapshot.confirmation.checked = true
			s.ConfirmSnapshot(d)
			await(t, l.entered)
			finished := d.job.finished
			if closeDocument {
				closed := make(chan struct{})
				go func() { s.Close(d); close(closed) }()
				await(t, d.ctx.Done())
				select {
				case <-closed:
					t.Fatal("Close detached original native capture")
				case <-time.After(20 * time.Millisecond):
				}
				close(l.release)
				await(t, closed)
				if a.last == nil || !a.last.closed.Load() {
					t.Fatal("unconsumed cancelled artifact retained")
				}
			} else {
				s.RequestCancel(d)
				if !d.Busy() || !d.CancelRequested() {
					t.Fatal("cancel falsely settled original native call")
				}
				close(l.release)
				await(t, finished)
				s.Consume()
				if d.Busy() || !d.snapshot.captureCanceled || !d.snapshot.original.Original.Canceled {
					t.Fatal("original cancellation not retained")
				}
			}
		})
	}
}

func TestSnapshotPureRecoveryPreservesOriginalAcrossGenerationsAndRejectsForeignSource(t *testing.T) {
	for _, mode := range []string{"matching", "missing", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			s, d, _, a, f := snapshotTestState(t)
			snapshotTestCapture(t, s, d)
			original := d.snapshot.original.Clone()
			artifact := d.snapshot.artifact
			d.unknown = &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true}}
			unknown := d.unknown
			for generation := uint64(12); generation <= 13; generation++ {
				previous := d.lease
				previous.(*snapshotTestLease).lost.Store(true)
				s.DisconnectProfile(7)
				next := &snapshotTestLease{fakeLease: &fakeLease{scope: testScope()}, document: d.id}
				next.scope.Generation = generation
				var source *contract.KVSnapshotResult
				if mode != "missing" {
					copy := original.Original.Clone()
					source = &copy
					if mode == "foreign" {
						source.Scope.DocumentID++
					}
				}
				p := &fakeProvider{lease: next.fakeLease, recover: func(captured contract.KVRequestLease) (contract.KVRecovery, error) {
					if captured != previous {
						t.Error("recovery selected a different physical lease")
					}
					return contract.KVRecovery{Lease: next, SnapshotResult: source}, nil
				}}
				s.deps.ProviderFor = func(int64) (contract.KVRequestsProvider, error) { return p, nil }
				s.Recover(d)
				await(t, d.job.finished)
				s.Consume()
				if !reflect.DeepEqual(*d.snapshot.original, original) || d.snapshot.artifact != artifact || d.unknown != unknown || !d.hasUnknownWrites() || a.captures.Load() != 1 || f.publishes.Load() != 0 {
					t.Fatal("recovery replayed, rebound or erased original evidence")
				}
				if mode == "foreign" {
					if !d.NeedsRecovery() || !next.closed.Load() || d.lease != previous {
						t.Fatal("foreign recovery imported")
					}
					break
				}
				if d.NeedsRecovery() || d.scope.Generation != generation {
					t.Fatal("pure original recovery failed", d.Error())
				}
			}
		})
	}
}

func TestSnapshotCompletionAndNewFileChoiceInvalidateOriginalLeavePermit(t *testing.T) {
	s, d, _, _, _ := snapshotTestState(t)
	snapshotTestCapture(t, s, d)
	continueLeave := func(ui.Services) {}
	if !s.GuardLeave(d, continueLeave) {
		t.Fatal("original capture did not guard leave")
	}
	s.DecideLeave(ui.Services{}, true)
	if s.GuardLeave(d, continueLeave) {
		t.Fatal("unchanged approved original leave rejected")
	}
	snapshotTestChooseFile(t, s, d, contract.KVSnapshotBackendFile)
	if !s.GuardLeave(d, continueLeave) {
		t.Fatal("new original file choice reused previous leave permit")
	}
	s.DecideLeave(ui.Services{}, false)
	snapshotTestPublish(t, s, d)
	if !s.GuardLeave(d, continueLeave) {
		t.Fatal("file publication failed to guard retained original records")
	}
}

func TestSnapshotOriginalAuthoredLimitsAndRequestedFileCancellationRemainIndependent(t *testing.T) {
	s, d, _, _, _ := snapshotTestState(t)
	authored := d.Request()
	snapshotTestCapture(t, s, d)
	s.SetSnapshotInputs(d, "9007199254740993", "1", "2")
	snapshotTestChooseFile(t, s, d, contract.KVSnapshotBackendFile)
	d.snapshot.fileConfirmation.checked = true
	s.ConfirmSnapshotFile(d)
	await(t, d.io.finished)
	// The executor has already returned a complete successful file receipt.
	// A later UI cancel request cannot rewrite that original acknowledgement.
	s.CancelFile(d)
	s.Consume()
	files := d.SnapshotFileReceipts()
	if len(files) != 1 || files[0].Canceled || !files[0].Published || !d.snapshot.fileCancelRequested[0] {
		t.Fatal("requested cancellation rewrote original executor result", files)
	}
	raw, err := s.SnapshotRecord(d)
	var record struct {
		Authored contract.KVRequest `json:"original_authored_request"`
		Capture  struct {
			Native struct {
				Limits map[string]string `json:"original_limits"`
			} `json:"original_native_source"`
		} `json:"original_capture"`
		Files []struct{ CancelRequested, Canceled bool } `json:"original_file_publications"`
	}
	if err != nil || json.Unmarshal(raw, &record) != nil || record.Authored != authored || record.Authored == d.Request() || record.Capture.Native.Limits["body_bytes"] != "67108864" || len(record.Files) != 1 || !record.Files[0].CancelRequested || record.Files[0].Canceled {
		t.Fatal("complete metadata lost original authored limits or cancel acknowledgement", err)
	}
}

func (p *snapshotTestPlan) Snapshot() contract.KVSnapshotRequest { return p.request }
func (p *snapshotTestPlan) Scope() contract.KVSnapshotScope      { return p.scope.Clone() }

type snapshotTestLease struct {
	*fakeLease
	document         uint64
	mu               sync.Mutex
	last             contract.KVSnapshotResult
	calls            atomic.Int32
	entered, release chan struct{}
	sourceFailure    bool
}

func (l *snapshotTestLease) PrepareSnapshot(r contract.KVSnapshotRequest) (contract.KVSnapshotPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	return &snapshotTestPlan{owner: l, request: r, scope: contract.KVSnapshotScope{Original: l.Scope(), DocumentID: l.document}}, nil
}

var snapshotTestBody = []byte("complete original backend\x00\xff")
var snapshotTestJournal = []byte("complete original native frames and terminal\x00\xfe")

func (l *snapshotTestLease) ExecuteSnapshot(ctx context.Context, prep contract.KVSnapshotPreparation, o contract.KVSnapshotOutputs) (contract.KVSnapshotResult, error) {
	p, ok := prep.(*snapshotTestPlan)
	if !ok || p.owner != l || p.used.Swap(true) {
		return contract.KVSnapshotResult{}, contract.ErrKVPlanUsed
	}
	l.calls.Add(1)
	if l.entered != nil {
		close(l.entered)
		<-l.release
	}
	bodySum := sha256.Sum256(snapshotTestBody)
	full := append(bytes.Clone(snapshotTestBody), bodySum[:]...)
	n, e := o.Snapshot.Write(full)
	j, je := o.Journal.Write(snapshotTestJournal)
	journalSum := sha256.Sum256(snapshotTestJournal)
	r := contract.KVSnapshotResult{Scope: p.Scope(), Request: p.request, Attempted: true, Ended: true, NativeTerminalObserved: true, EOF: true, SourceComplete: true, ChecksumMatched: true, OutputAcknowledged: true, JournalComplete: true, Completed: true, Frames: 2, BodyBytes: uint64(len(snapshotTestBody)), ExpectedBodyBytes: uint64(len(snapshotTestBody)), SnapshotBytesWritten: uint64(n), JournalBytesWritten: uint64(j), BodySHA256: bodySum[:], Checksum: bodySum[:], JournalSHA256: journalSum[:], StorageVersion: "3.7.0", Before: contract.KVResult{Request: contract.KVRequest{Method: "/etcdserverpb.Maintenance/Status", JSON: "{}"}, Attempted: true, Completed: true, NativeCaptured: true, Native: []byte{0, 255, 3}, JSON: `{"header":{"cluster_id":"18446744073709551615"}}`, Header: l.scope.Header}}
	e = errors.Join(e, je, ctx.Err())
	if e != nil || l.sourceFailure {
		r.Completed = false
		r.SourceComplete = false
		r.EOF = false
		r.NativeCode = "Unavailable"
		e = errors.Join(e, errors.New("original source EOF withheld"))
	}
	r.Canceled = ctx.Err() != nil
	l.mu.Lock()
	l.last = r.Clone()
	l.mu.Unlock()
	return r, e
}
func (l *snapshotTestLease) LastSnapshotResult() contract.KVSnapshotResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last.Clone()
}

type snapshotTestCapturePlan struct {
	owner   *snapshotTestArtifacts
	lease   *snapshotTestLease
	native  contract.KVSnapshotPreparation
	request contract.KVSnapshotRequest
	scope   contract.KVSnapshotScope
	used    atomic.Bool
}

func (p *snapshotTestCapturePlan) Snapshot() contract.KVSnapshotRequest { return p.request }
func (p *snapshotTestCapturePlan) Scope() contract.KVSnapshotScope      { return p.scope.Clone() }

type snapshotTestArtifact struct {
	receipt           contract.KVSnapshotCapture
	snapshot, journal []byte
	closed            atomic.Bool
	closes            atomic.Int32
	closeErr          error
}

func (a *snapshotTestArtifact) Receipt() contract.KVSnapshotCapture { return a.receipt.Clone() }
func (a *snapshotTestArtifact) OpenSnapshot() (io.ReadCloser, error) {
	if a.closed.Load() {
		return nil, contract.ErrRevoked
	}
	return io.NopCloser(bytes.NewReader(a.snapshot)), nil
}
func (a *snapshotTestArtifact) OpenJournal() (io.ReadCloser, error) {
	if a.closed.Load() {
		return nil, contract.ErrRevoked
	}
	return io.NopCloser(bytes.NewReader(a.journal)), nil
}
func (a *snapshotTestArtifact) Close() error {
	if !a.closed.Swap(true) {
		a.closes.Add(1)
	}
	return a.closeErr
}

type snapshotTestArtifacts struct {
	preps, captures atomic.Int32
	storageFailure  bool
	last            *snapshotTestArtifact
}

func (a *snapshotTestArtifacts) Prepare(lease contract.KVRequestLease, r contract.KVSnapshotRequest) (contract.KVSnapshotCapturePreparation, error) {
	l, ok := lease.(*snapshotTestLease)
	if !ok {
		return nil, contract.ErrUnsupported
	}
	native, e := l.PrepareSnapshot(r)
	if e != nil {
		return nil, e
	}
	a.preps.Add(1)
	return &snapshotTestCapturePlan{owner: a, lease: l, native: native, request: r, scope: native.Scope()}, nil
}
func (a *snapshotTestArtifacts) Capture(ctx context.Context, preparation contract.KVSnapshotCapturePreparation) (contract.KVSnapshotArtifact, contract.KVSnapshotCapture, error) {
	p, ok := preparation.(*snapshotTestCapturePlan)
	if !ok || p.owner != a || p.used.Swap(true) {
		return nil, contract.KVSnapshotCapture{}, contract.ErrKVPlanUsed
	}
	a.captures.Add(1)
	var snapshot, journal bytes.Buffer
	native, e := p.lease.ExecuteSnapshot(ctx, p.native, contract.KVSnapshotOutputs{Snapshot: &snapshot, Journal: &journal})
	ssum, jsum := sha256.Sum256(snapshot.Bytes()), sha256.Sum256(journal.Bytes())
	r := contract.KVSnapshotCapture{Original: native.Clone(), SnapshotBytes: uint64(snapshot.Len()), JournalBytes: uint64(journal.Len()), SnapshotSHA256: ssum[:], JournalSHA256: jsum[:], Stored: true, FilesSynced: true, Complete: native.Completed}
	if a.storageFailure {
		r.Stored = false
		r.FilesSynced = false
		r.Complete = false
		r.StorageError = "original storage verification failed"
		return nil, r, errors.Join(e, errors.New(r.StorageError))
	}
	artifact := &snapshotTestArtifact{receipt: r.Clone(), snapshot: bytes.Clone(snapshot.Bytes()), journal: bytes.Clone(journal.Bytes())}
	a.last = artifact
	return artifact, r.Clone(), e
}

type snapshotTestFilePlan struct {
	owner        *snapshotTestFiles
	observation  contract.KVSnapshotFileObservation
	used, closed atomic.Bool
}

func (p *snapshotTestFilePlan) Observation() contract.KVSnapshotFileObservation {
	return p.observation.Clone()
}
func (p *snapshotTestFilePlan) Close() error { p.closed.Store(true); return nil }

type snapshotTestFiles struct {
	preps, publishes                                           atomic.Int32
	existing                                                   bool
	unknown                                                    bool
	prepareEntered, prepareRelease, writeEntered, writeRelease chan struct{}
	lastPlan                                                   *snapshotTestFilePlan
}

func (f *snapshotTestFiles) PrepareFile(ctx context.Context, a contract.KVSnapshotArtifact, c contract.KVSnapshotFileChoice) (contract.KVSnapshotFilePreparation, error) {
	f.preps.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.prepareEntered != nil {
		close(f.prepareEntered)
		<-f.prepareRelease
	}
	if f.existing && !c.Replace {
		return nil, fs.ErrExist
	}
	obs := contract.KVSnapshotFileObservation{Source: a.Receipt(), Choice: c}
	if f.existing {
		obs.Existing = &contract.SnapshotSource{Name: "original-old", Size: 9007199254740993, SHA256: "original-old-hash"}
	}
	p := &snapshotTestFilePlan{owner: f, observation: obs.Clone()}
	f.lastPlan = p
	return p, nil
}
func (f *snapshotTestFiles) PublishFile(ctx context.Context, preparation contract.KVSnapshotFilePreparation) (contract.KVSnapshotFileResult, error) {
	p, ok := preparation.(*snapshotTestFilePlan)
	if !ok || p.owner != f || p.closed.Load() || p.used.Swap(true) {
		return contract.KVSnapshotFileResult{}, contract.ErrKVPlanUsed
	}
	f.publishes.Add(1)
	if f.writeEntered != nil {
		close(f.writeEntered)
		<-f.writeRelease
	}
	r := contract.KVSnapshotFileResult{Original: p.Observation(), Attempted: true, Ended: true, SourceVerified: true, FileSynced: true, PublicationAttempted: true, Published: true, DestinationMatched: true, PathMatched: true, DirectorySynced: true, BytesWritten: p.observation.Source.SnapshotBytes, SHA256: bytes.Clone(p.observation.Source.SnapshotSHA256), DestinationPath: p.observation.Choice.Path}
	if p.observation.Choice.Kind == contract.KVSnapshotJournalFile {
		r.BytesWritten = p.observation.Source.JournalBytes
		r.SHA256 = bytes.Clone(p.observation.Source.JournalSHA256)
	}
	if p.observation.Choice.Replace {
		r.Displaced = true
		r.DisplacedRetained = true
		r.AuxiliaryPathMatched = true
		r.DisplacedPath = "original-parent/private/displaced"
	}
	var e error
	if f.unknown {
		r.Published = false
		r.PublicationUnknown = true
		r.DirectorySynced = false
		r.Error = "original publication acknowledgement lost"
		e = errors.New(r.Error)
	}
	if ctx.Err() != nil {
		r.Canceled = true
		e = errors.Join(e, ctx.Err())
	}
	return r, e
}
func snapshotTestState(t *testing.T) (*State, *Document, *snapshotTestLease, *snapshotTestArtifacts, *snapshotTestFiles) {
	t.Helper()
	l := &snapshotTestLease{fakeLease: &fakeLease{scope: testScope()}, document: 9007199254740995}
	p := &fakeProvider{lease: l.fakeLease, reserve: func(id uint64) (contract.KVRequestLease, error) {
		if id != l.document {
			t.Error("foreign document reservation")
		}
		return l, nil
	}}
	a, f := &snapshotTestArtifacts{}, &snapshotTestFiles{}
	s := New(Dependencies{ProviderFor: func(id int64) (contract.KVRequestsProvider, error) {
		if id != 7 {
			t.Error("foreign profile")
		}
		return p, nil
	}, SnapshotArtifacts: a, SnapshotFiles: SnapshotFiles{Operations: f, SavePath: func(contract.KVSnapshotFileKind, string) (string, error) { return "original-parent/chosen-file", nil }}})
	d := s.Open(l.document, 7, DefaultSnapshotRequest())
	t.Cleanup(s.CloseAll)
	return s, d, l, a, f
}
func snapshotTestCapture(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.PrepareSnapshot(d)
	if d.snapshot == nil || d.snapshot.confirmation == nil {
		t.Fatal("capture preparation missing", d.Error())
	}
	d.snapshot.confirmation.checked = true
	s.ConfirmSnapshot(d)
	if d.job == nil {
		t.Fatal("capture dispatch missing", d.Error())
	}
	await(t, d.job.finished)
	s.Consume()
}
func snapshotTestChooseFile(t *testing.T, s *State, d *Document, kind contract.KVSnapshotFileKind) {
	t.Helper()
	s.SelectSnapshotFile(d, kind)
	if d.io == nil {
		t.Fatal("file preparation missing", d.Error())
	}
	await(t, d.io.finished)
	s.Consume()
	if d.snapshot.fileConfirmation == nil {
		t.Fatal("file confirmation missing", d.Error())
	}
}
func snapshotTestPublish(t *testing.T, s *State, d *Document) {
	t.Helper()
	d.snapshot.fileConfirmation.checked = true
	s.ConfirmSnapshotFile(d)
	if d.io == nil {
		t.Fatal("file publication missing", d.Error())
	}
	await(t, d.io.finished)
	s.Consume()
}
func TestSnapshotRequestsExactStringsLimitsAndRequestFileRoundtrip(t *testing.T) {
	request := DefaultSnapshotRequest()
	r, e := snapshotRequestValues(request)
	if e != nil || r.BodyBytes != 64<<20 || r.JournalBytes != 128<<20 || r.Frames != 32768 {
		t.Fatal(r, e)
	}
	raw, e := EncodeRequest(request)
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := DecodeRequest(raw)
	if e != nil || parsed != request {
		t.Fatal(parsed, e)
	}
	for _, text := range []string{`{"body_bytes":64,"journal_bytes":"1","frames":"2"}`, `{"body_bytes":"1.0","journal_bytes":"1","frames":"2"}`, `{"body_bytes":"+1","journal_bytes":"1","frames":"2"}`, `{"body_bytes":"","journal_bytes":"1","frames":"2"}`, `{"body_bytes":"1e3","journal_bytes":"1","frames":"2"}`, `{"body_bytes":"１","journal_bytes":"1","frames":"2"}`, `{"body_bytes":"1","journal_bytes":"1","frames":"1"}`, `{"body_bytes":"9223372036854775775","journal_bytes":"1","frames":"2"}`, `{"body_bytes":"18446744073709551615","journal_bytes":"1","frames":"2"}`, `{"body_bytes":"1","journal_bytes":"1","frames":"2","prefix":"/browse"}`, `{"body_bytes":"1","body_bytes":"2","journal_bytes":"1","frames":"2"}`} {
		if _, e := snapshotRequestValues(contract.KVRequest{Method: SnapshotMethod, JSON: text}); e == nil {
			t.Fatal("unsupported limits accepted", text)
		}
	}
	exact, e := snapshotRequestValues(contract.KVRequest{Method: SnapshotMethod, JSON: `{"body_bytes":"9007199254740993","journal_bytes":"1","frames":"9007199254740995"}`})
	if e != nil || exact.BodyBytes != 9007199254740993 || exact.Frames != 9007199254740995 {
		t.Fatal("precision lost", exact, e)
	}
}
func TestSnapshotOriginalCaptureConfirmationClonesOneUseAndEarlierWriteBarrier(t *testing.T) {
	s, d, l, a, _ := snapshotTestState(t)
	unknown := &Receipt{Scope: d.scope.Clone(), Result: contract.KVResult{WriteOutcomeUnknown: true, Native: []byte{1, 2}}}
	d.unknown = unknown
	s.PrepareSnapshot(d)
	if a.preps.Load() != 1 || a.captures.Load() != 0 || l.calls.Load() != 0 || d.snapshot.confirmation.checked {
		t.Fatal("preparation executed or implicitly checked")
	}
	scope := d.snapshot.confirmation.preparation.Scope()
	scope.Original.Prefix[0] = 7
	if !bytes.Equal(d.snapshot.confirmation.scope.Original.Prefix, testScope().Prefix) {
		t.Fatal("scope alias")
	}
	s.ConfirmSnapshot(d)
	if d.job != nil {
		t.Fatal("unchecked source capture")
	}
	d.snapshot.confirmation.checked = true
	// Changing the injected store must not select another original executor.
	replacement := &snapshotTestArtifacts{}
	s.deps.SnapshotArtifacts = replacement
	s.ConfirmSnapshot(d)
	await(t, d.job.finished)
	s.Consume()
	if a.captures.Load() != 1 || replacement.captures.Load() != 0 || l.calls.Load() != 1 || d.unknown != unknown || !d.hasUnknownWrites() {
		t.Fatal("original source/barrier changed")
	}
	r, ok := d.SnapshotReceipt()
	if !ok || !r.Complete || r.Original.Scope.DocumentID != d.ID() || r.Original.Scope.Original.Header.ClusterID != math.MaxUint64 {
		t.Fatal(r)
	}
	r.Original.Scope.Original.Prefix[0] = 9
	r.SnapshotSHA256[0] ^= 1
	original, _ := d.SnapshotReceipt()
	if !bytes.Equal(original.Original.Scope.Original.Prefix, testScope().Prefix) {
		t.Fatal("mutable source receipt")
	}
	s.PrepareSnapshot(d)
	s.ConfirmSnapshot(d)
	if a.captures.Load() != 1 {
		t.Fatal("replayed capture")
	}
}
func TestSnapshotChangedInputsAndForeignConfirmationCannotDispatch(t *testing.T) {
	s, d, l, a, _ := snapshotTestState(t)
	s.PrepareSnapshot(d)
	d.snapshot.confirmation.checked = true
	d.Editor.Buffer.Set(strings.Replace(d.Request().JSON, "67108864", "67108865", 1))
	s.ConfirmSnapshot(d)
	s.Consume()
	if d.job != nil || a.captures.Load() != 0 || l.calls.Load() != 0 || d.snapshot.confirmation != nil {
		t.Fatal("stale confirmation dispatched")
	}
	s.PrepareSnapshot(d)
	p := d.snapshot.confirmation
	p.checked = true
	p.scope.DocumentID++
	s.ConfirmSnapshot(d)
	if d.job != nil {
		t.Fatal("foreign confirmation dispatched")
	}
}
func TestSnapshotFileExplicitExistingChoiceUsesOriginalOperationsAfterDisconnect(t *testing.T) {
	s, d, l, a, f := snapshotTestState(t)
	snapshotTestCapture(t, s, d)
	f.existing = true
	l.lost.Store(true)
	s.DisconnectProfile(7)
	snapshotTestChooseFile(t, s, d, contract.KVSnapshotBackendFile)
	p := d.snapshot.fileConfirmation
	if p.checked || p.observation.Existing == nil || !p.observation.Choice.Replace || f.preps.Load() != 2 {
		t.Fatal("existing choice not independently confirmed")
	}
	s.ConfirmSnapshotFile(d)
	if f.publishes.Load() != 0 {
		t.Fatal("unchecked existing-file publication")
	}
	replacement := &snapshotTestFiles{}
	s.deps.SnapshotFiles.Operations = replacement
	snapshotTestPublish(t, s, d)
	receipts := d.SnapshotFileReceipts()
	if len(receipts) != 1 || !receipts[0].Published || !receipts[0].DisplacedRetained || receipts[0].Original.Existing.Size != 9007199254740993 || replacement.publishes.Load() != 0 || f.publishes.Load() != 1 || l.calls.Load() != 1 || a.captures.Load() != 1 {
		t.Fatal(receipts)
	}
	receipts[0].Original.Source.Original.Scope.Original.Prefix[0] = 7
	if d.SnapshotFileReceipts()[0].Original.Source.Original.Scope.Original.Prefix[0] != 0 {
		t.Fatal("file receipt alias")
	}
	if !d.NeedsRecovery() {
		t.Fatal("file saving recovered connection")
	}
}
func TestSnapshotFileUnknownObservationNeverPromotedAndLaterSaveRetainsOriginal(t *testing.T) {
	s, d, l, _, f := snapshotTestState(t)
	snapshotTestCapture(t, s, d)
	unknown := &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true}}
	d.unknown = unknown
	f.unknown = true
	snapshotTestChooseFile(t, s, d, contract.KVSnapshotBackendFile)
	snapshotTestPublish(t, s, d)
	first := d.SnapshotFileReceipts()[0]
	if first.Published || !first.PublicationUnknown || !first.DestinationMatched || d.unknown != unknown {
		t.Fatal(first)
	}
	f.unknown = false
	snapshotTestChooseFile(t, s, d, contract.KVSnapshotJournalFile)
	snapshotTestPublish(t, s, d)
	receipts := d.SnapshotFileReceipts()
	if len(receipts) != 2 || !receipts[0].PublicationUnknown || receipts[0].Published || !receipts[1].Published || l.calls.Load() != 1 || d.unknown != unknown {
		t.Fatal(receipts)
	}
	raw, e := s.SnapshotRecord(d)
	if e != nil || !bytes.Contains(raw, []byte(`"original_document_id": "9007199254740995"`)) || !bytes.Contains(raw, []byte(`"PublicationUnknown": true`)) {
		t.Fatal(string(raw), e)
	}
}
func TestSnapshotFailureSourceJournalAndStorageProofStaySeparate(t *testing.T) {
	for _, phase := range []string{"native", "storage"} {
		t.Run(phase, func(t *testing.T) {
			s, d, l, a, f := snapshotTestState(t)
			l.sourceFailure = phase == "native"
			a.storageFailure = phase == "storage"
			snapshotTestCapture(t, s, d)
			r, _ := d.SnapshotReceipt()
			if r.Complete || r.Original.Completed == (phase == "native") {
				t.Fatal(r)
			}
			s.SelectSnapshotFile(d, contract.KVSnapshotBackendFile)
			if d.io != nil || f.preps.Load() != 0 {
				t.Fatal("incomplete backend acquired file plan")
			}
			if phase == "native" {
				snapshotTestChooseFile(t, s, d, contract.KVSnapshotJournalFile)
				snapshotTestPublish(t, s, d)
				if !d.SnapshotFileReceipts()[0].Published {
					t.Fatal("complete original failure journal rejected")
				}
			} else {
				s.SelectSnapshotFile(d, contract.KVSnapshotJournalFile)
				if d.io != nil {
					t.Fatal("unverified journal exposed")
				}
			}
		})
	}
}
func TestSnapshotCaptureAndFileCompletionParkOnTheirOriginalDocument(t *testing.T) {
	s, d, _, _, f := snapshotTestState(t)
	active := true
	s.deps.Active = func(candidate *Document) bool { return candidate == d && active }
	s.PrepareSnapshot(d)
	d.snapshot.confirmation.checked = true
	s.ConfirmSnapshot(d)
	await(t, d.job.finished)
	active = false
	s.Consume()
	if d.snapshot.original != nil || d.job == nil {
		t.Fatal("parked source consumed")
	}
	active = true
	s.Consume()
	snapshotTestChooseFile(t, s, d, contract.KVSnapshotBackendFile)
	d.snapshot.fileConfirmation.checked = true
	s.ConfirmSnapshotFile(d)
	await(t, d.io.finished)
	active = false
	s.Consume()
	if len(d.snapshot.files) != 0 || d.io == nil {
		t.Fatal("parked file consumed")
	}
	active = true
	s.Consume()
	if len(d.snapshot.files) != 1 || f.publishes.Load() != 1 {
		t.Fatal("original file missing")
	}
}
func TestSnapshotCloseWaitsOriginalPublicationAndRevokesUnconsumedPlans(t *testing.T) {
	for _, phase := range []string{"prepare", "publication"} {
		t.Run(phase, func(t *testing.T) {
			s, d, _, a, f := snapshotTestState(t)
			snapshotTestCapture(t, s, d)
			entered, release := make(chan struct{}), make(chan struct{})
			if phase == "prepare" {
				f.prepareEntered = entered
				f.prepareRelease = release
				s.SelectSnapshotFile(d, contract.KVSnapshotBackendFile)
			} else {
				snapshotTestChooseFile(t, s, d, contract.KVSnapshotBackendFile)
				f.writeEntered = entered
				f.writeRelease = release
				d.snapshot.fileConfirmation.checked = true
				s.ConfirmSnapshotFile(d)
			}
			await(t, entered)
			finished := d.io.finished
			closed := make(chan struct{})
			go func() { s.Close(d); close(closed) }()
			await(t, d.ctx.Done())
			select {
			case <-closed:
				t.Fatal("close detached original file operation")
			case <-time.After(20 * time.Millisecond):
			}
			if a.last.closed.Load() {
				t.Fatal("artifact closed before original publication acknowledged")
			}
			close(release)
			await(t, finished)
			await(t, closed)
			if !a.last.closed.Load() || a.last.closes.Load() != 1 {
				t.Fatal("artifact leaked")
			}
			if f.lastPlan != nil && !f.lastPlan.closed.Load() {
				t.Fatal("unconsumed plan leaked")
			}
		})
	}
}
func TestSnapshotCloseReleasesParkedCaptureAndClearKeepsCleanupEvidence(t *testing.T) {
	s, d, _, a, _ := snapshotTestState(t)
	s.PrepareSnapshot(d)
	d.snapshot.confirmation.checked = true
	s.ConfirmSnapshot(d)
	await(t, d.job.finished)
	s.Close(d)
	if a.last == nil || !a.last.closed.Load() || a.last.closes.Load() != 1 {
		t.Fatal("unconsumed capture leaked")
	}
	s, d, _, a, _ = snapshotTestState(t)
	snapshotTestCapture(t, s, d)
	original, _ := d.SnapshotReceipt()
	a.last.closeErr = errors.New("original cleanup failed")
	s.ClearSnapshot(d)
	if d.snapshot.artifact != nil || d.snapshot.cleanupError == "" || !reflect.DeepEqual(original, *d.snapshot.original) {
		t.Fatal("cleanup failure revived artifact or rewrote original")
	}
}
