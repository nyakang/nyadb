package snapshotfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type artifactTestPreparation struct {
	request contract.KVSnapshotRequest
	scope   contract.KVSnapshotScope
	used    atomic.Bool
}

func (p *artifactTestPreparation) Snapshot() contract.KVSnapshotRequest { return p.request }
func (p *artifactTestPreparation) Scope() contract.KVSnapshotScope      { return p.scope.Clone() }

type artifactTestLease struct {
	contract.KVRequestLease
	scope    contract.KVSnapshotScope
	dispatch atomic.Int32
	execute  func(context.Context, *artifactTestPreparation, contract.KVSnapshotOutputs) (contract.KVSnapshotResult, error)
}

func (l *artifactTestLease) Scope() contract.KVScope { return l.scope.Original.Clone() }
func (l *artifactTestLease) CheckKVLease() error     { return nil }
func (l *artifactTestLease) PrepareSnapshot(r contract.KVSnapshotRequest) (contract.KVSnapshotPreparation, error) {
	return &artifactTestPreparation{request: r, scope: l.scope.Clone()}, nil
}
func (l *artifactTestLease) LastSnapshotResult() contract.KVSnapshotResult {
	return contract.KVSnapshotResult{}
}
func (l *artifactTestLease) ExecuteSnapshot(ctx context.Context, p contract.KVSnapshotPreparation, o contract.KVSnapshotOutputs) (contract.KVSnapshotResult, error) {
	plan := p.(*artifactTestPreparation)
	if !plan.used.CompareAndSwap(false, true) {
		return contract.KVSnapshotResult{}, contract.ErrKVPlanUsed
	}
	l.dispatch.Add(1)
	if l.execute != nil {
		return l.execute(ctx, plan, o)
	}
	return artifactTestComplete(ctx, plan, o)
}

var artifactTestBody = []byte("original complete backend\x00\xff")
var artifactTestJournal = []byte("original complete native frames\x00\xfe")

func artifactTestComplete(ctx context.Context, p *artifactTestPreparation, o contract.KVSnapshotOutputs) (contract.KVSnapshotResult, error) {
	bodySum := sha256.Sum256(artifactTestBody)
	output := append(bytes.Clone(artifactTestBody), bodySum[:]...)
	n, err := o.Snapshot.Write(output)
	j, e := o.Journal.Write(artifactTestJournal)
	journalSum := sha256.Sum256(artifactTestJournal[:j])
	r := contract.KVSnapshotResult{Scope: p.Scope(), Request: p.request, Attempted: true, Ended: true, NativeTerminalObserved: true, Frames: 2, BodyBytes: uint64(len(artifactTestBody)), ExpectedBodyBytes: uint64(len(artifactTestBody)), SnapshotBytesWritten: uint64(n), JournalBytesWritten: uint64(j), BodySHA256: bodySum[:], Checksum: bodySum[:], JournalSHA256: journalSum[:], Before: contract.KVResult{Native: []byte{9, 8, 7}}}
	err = errors.Join(err, e, ctx.Err())
	r.Completed = err == nil
	r.SourceComplete = err == nil
	r.EOF = err == nil
	r.ChecksumMatched = err == nil
	r.OutputAcknowledged = err == nil
	r.JournalComplete = e == nil
	r.Canceled = ctx.Err() != nil
	return r, err
}
func artifactTestStore(t *testing.T, limit uint64) (*KVSnapshotStore, *artifactTestLease, string) {
	t.Helper()
	directory := t.TempDir()
	store, err := NewKVSnapshotStore(directory, limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	scope := contract.KVSnapshotScope{DocumentID: 9007199254740995, Original: contract.KVScope{Prefix: []byte{0, 255}, Version: "3.7.3", Header: contract.KVHeader{ClusterID: math.MaxUint64, MemberID: 9007199254740993}}}
	return store, &artifactTestLease{scope: scope}, directory
}
func artifactTestRequest() contract.KVSnapshotRequest {
	return contract.KVSnapshotRequest{BodyBytes: 1024, JournalBytes: 2048, Frames: 99}
}
func artifactTestCapture(t *testing.T, s *KVSnapshotStore, l *artifactTestLease) (contract.KVSnapshotArtifact, contract.KVSnapshotCapture) {
	t.Helper()
	p, err := s.Prepare(l, artifactTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	a, r, err := s.Capture(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return a, r
}
func artifactTestRead(t *testing.T, r io.ReadCloser) []byte {
	t.Helper()
	b, e := io.ReadAll(r)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	return b
}
func TestKVArtifactsRetainExactOriginalFilesIndependentReadersAndClonedReceipts(t *testing.T) {
	s, l, dir := artifactTestStore(t, 10000)
	a, r := artifactTestCapture(t, s, l)
	if !r.Stored || !r.Complete || !r.FilesSynced || !reflect.DeepEqual(r.Original.Scope, l.scope) {
		t.Fatal(r)
	}
	if s.used != r.SnapshotBytes+r.JournalBytes {
		t.Fatal("reservation did not shrink", s.used)
	}
	first, e := a.OpenSnapshot()
	if e != nil {
		t.Fatal(e)
	}
	one := make([]byte, 1)
	if _, e = first.Read(one); e != nil || one[0] != artifactTestBody[0] {
		t.Fatal(one, e)
	}
	second, e := a.OpenSnapshot()
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(artifactTestBody)
	expected := append(bytes.Clone(artifactTestBody), sum[:]...)
	if !bytes.Equal(artifactTestRead(t, second), expected) || !bytes.Equal(artifactTestRead(t, first), expected[1:]) {
		t.Fatal("read offsets shared")
	}
	journal, e := a.OpenJournal()
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(artifactTestRead(t, journal), artifactTestJournal) {
		t.Fatal("journal differs")
	}
	r.Original.Scope.Original.Prefix[0] = 42
	r.Original.Before.Native[0] = 42
	r.SnapshotSHA256[0] ^= 255
	original := a.Receipt()
	if !reflect.DeepEqual(original.Original.Scope, l.scope) || original.Original.Before.Native[0] != 9 || bytes.Equal(original.SnapshotSHA256, r.SnapshotSHA256) {
		t.Fatal("inspection modified original receipt")
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 1 {
		t.Fatal(entries, e)
	}
	for _, file := range []string{"snapshot", "journal"} {
		info, e := os.Stat(dir + "/" + entries[0].Name() + "/" + file)
		if e != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal(info, e)
		}
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = a.OpenSnapshot(); !errors.Is(e, ErrArtifactClosed) {
		t.Fatal(e)
	}
	if s.used != 0 {
		t.Fatal("closed artifact retains charge", s.used)
	}
	entries, e = os.ReadDir(dir)
	if e != nil || len(entries) != 0 {
		t.Fatal("original files retained", entries, e)
	}
}
func TestKVArtifactsValidateFiniteBudgetsAndQuotaBeforeNativeDispatch(t *testing.T) {
	s, l, dir := artifactTestStore(t, 3104)
	for _, r := range []contract.KVSnapshotRequest{{}, {BodyBytes: math.MaxUint64, JournalBytes: 1, Frames: 2}, {BodyBytes: 1, JournalBytes: math.MaxUint64, Frames: 2}, {BodyBytes: 1, JournalBytes: 1, Frames: 1}, {BodyBytes: math.MaxInt64 - 32, JournalBytes: 1, Frames: 2}} {
		if _, e := s.Prepare(l, r); !errors.Is(e, ErrArtifactQuota) {
			t.Fatal(r, e)
		}
	}
	p, e := s.Prepare(l, artifactTestRequest())
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	s.used = 1
	s.mu.Unlock()
	if a, _, e := s.Capture(context.Background(), p); !errors.Is(e, ErrArtifactQuota) || a != nil || l.dispatch.Load() != 0 {
		t.Fatal(a, e)
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 0 {
		t.Fatal(entries, e)
	}
	s.mu.Lock()
	s.used = 0
	s.mu.Unlock()
	a, _, e := s.Capture(context.Background(), p)
	if e != nil || l.dispatch.Load() != 1 {
		t.Fatal(a, e)
	}
	if _, _, e = s.Capture(context.Background(), p); !errors.Is(e, contract.ErrKVPlanUsed) {
		t.Fatal("capture replayed", e)
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestKVArtifactsPreparationAndArtifactCopiesCannotRecreateAuthority(t *testing.T) {
	s, l, _ := artifactTestStore(t, 10000)
	other, _, _ := artifactTestStore(t, 10000)
	p, e := s.Prepare(l, artifactTestRequest())
	if e != nil {
		t.Fatal(e)
	}
	copied := reflect.New(reflect.TypeOf(p).Elem()).Interface().(*kvCapturePreparation)
	reflect.ValueOf(copied).Elem().Set(reflect.ValueOf(p).Elem())
	for _, v := range []struct {
		s *KVSnapshotStore
		p contract.KVSnapshotCapturePreparation
	}{{s, copied}, {other, p}} {
		if _, _, e = v.s.Capture(context.Background(), v.p); !errors.Is(e, contract.ErrKVPlanUsed) {
			t.Fatal(e)
		}
	}
	a, _, e := s.Capture(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	original := a.(*kvArtifact)
	duplicate := reflect.New(reflect.TypeOf(original).Elem()).Interface().(*kvArtifact)
	reflect.ValueOf(duplicate).Elem().Set(reflect.ValueOf(original).Elem())
	if e = duplicate.Close(); !errors.Is(e, ErrArtifactClosed) {
		t.Fatal(e)
	}
	if _, e = duplicate.OpenSnapshot(); !errors.Is(e, ErrArtifactClosed) {
		t.Fatal(e)
	}
	reader, e := a.OpenSnapshot()
	if e != nil {
		t.Fatal("copy closed original", e)
	}
	artifactTestRead(t, reader)
}
func TestKVArtifactsRetainIncompleteOriginalEvidenceWithoutCompleteness(t *testing.T) {
	s, l, _ := artifactTestStore(t, 10000)
	l.execute = func(ctx context.Context, p *artifactTestPreparation, o contract.KVSnapshotOutputs) (contract.KVSnapshotResult, error) {
		r, e := artifactTestComplete(ctx, p, o)
		r.Completed = false
		r.SourceComplete = false
		r.EOF = false
		r.ChecksumMatched = false
		r.NativeStatus = []byte{0, 255, 1}
		r.NativeCode = "Unavailable"
		return r, errors.Join(e, io.ErrUnexpectedEOF)
	}
	p, e := s.Prepare(l, artifactTestRequest())
	if e != nil {
		t.Fatal(e)
	}
	a, r, e := s.Capture(context.Background(), p)
	if !errors.Is(e, io.ErrUnexpectedEOF) || a == nil || !r.Stored || r.Complete || !r.FilesSynced || r.StorageError != "" || !bytes.Equal(r.Original.NativeStatus, []byte{0, 255, 1}) {
		t.Fatal(a, r, e)
	}
}
func TestKVArtifactsRejectDiskFaultsAndReceiptOrSealedFileMismatch(t *testing.T) {
	for _, fault := range []string{"snapshot-short", "journal-short", "sync", "receipt-count", "receipt-hash", "body-hash", "footer", "file-mutated", "close", "frame-bound", "no-terminal"} {
		t.Run(fault, func(t *testing.T) {
			s, l, dir := artifactTestStore(t, 10000)
			if strings.HasSuffix(fault, "-short") {
				s.write = func(f *os.File, b []byte) (int, error) {
					if strings.HasSuffix(f.Name(), strings.TrimSuffix(fault, "-short")) {
						return f.Write(b[:len(b)/2])
					}
					return f.Write(b)
				}
			}
			if fault == "sync" {
				s.syncFile = func(*os.File) error { return errors.New("actual sync failed") }
			}
			l.execute = func(ctx context.Context, p *artifactTestPreparation, o contract.KVSnapshotOutputs) (contract.KVSnapshotResult, error) {
				r, e := artifactTestComplete(ctx, p, o)
				switch fault {
				case "close":
					if e := o.Snapshot.(*kvCaptureFile).file.Close(); e != nil {
						t.Fatal(e)
					}
				case "frame-bound":
					r.Frames = p.request.Frames + 1
				case "no-terminal":
					r.NativeTerminalObserved = false
				case "receipt-count":
					r.SnapshotBytesWritten++
				case "receipt-hash":
					r.JournalSHA256[0] ^= 255
				case "body-hash":
					r.BodySHA256 = bytes.Clone(r.BodySHA256)
					r.BodySHA256[0] ^= 255
				case "footer":
					r.Checksum = bytes.Repeat([]byte{8}, 32)
				case "file-mutated":
					file := o.Snapshot.(*kvCaptureFile).file
					if x := os.WriteFile(file.Name(), []byte("mutated"), 0600); x != nil {
						t.Fatal(x)
					}
				}
				return r, e
			}
			p, e := s.Prepare(l, artifactTestRequest())
			if e != nil {
				t.Fatal(e)
			}
			a, r, e := s.Capture(context.Background(), p)
			if e == nil || a != nil || r.Stored || r.Complete || r.StorageError == "" || r.CleanupError != "" {
				t.Fatal(a, r, e)
			}
			if s.used != 0 {
				t.Fatal("failure budget not released", s.used)
			}
			entries, e := os.ReadDir(dir)
			if e != nil || len(entries) != 0 {
				t.Fatal("own partial files not removed", entries, e)
			}
		})
	}
}
func TestKVArtifactsCloseWaitsForOriginalWriterAcknowledgementAndCleansOwnFiles(t *testing.T) {
	s, l, dir := artifactTestStore(t, 10000)
	entered, release := make(chan struct{}), make(chan struct{})
	s.write = func(f *os.File, b []byte) (int, error) {
		if strings.HasSuffix(f.Name(), "snapshot") {
			close(entered)
			<-release
		}
		return f.Write(b)
	}
	p, e := s.Prepare(l, artifactTestRequest())
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		a, _, e := s.Capture(context.Background(), p)
		if a != nil {
			e = errors.Join(e, errors.New("canceled capture retained artifact"))
		}
		done <- e
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case e := <-closed:
		t.Fatal("close returned before original write acknowledgement", e)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if e = <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e = <-closed; e != nil {
		t.Fatal(e)
	}
	if s.used != 0 {
		t.Fatal("close retained charge", s.used)
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 0 {
		t.Fatal(entries, e)
	}
}
func TestKVArtifactsCloseWaitsForReadsAndRevokesParkedReaders(t *testing.T) {
	s, l, _ := artifactTestStore(t, 10000)
	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	s.readAt = func(f *os.File, b []byte, o int64) (int, error) {
		if armed.Load() {
			close(entered)
			<-release
		}
		return f.ReadAt(b, o)
	}
	a, _ := artifactTestCapture(t, s, l)
	armed.Store(true)
	reader, e := a.OpenSnapshot()
	if e != nil {
		t.Fatal(e)
	}
	parked, e := a.OpenJournal()
	if e != nil {
		t.Fatal(e)
	}
	read := make(chan error, 1)
	go func() { _, e := reader.Read(make([]byte, 4)); read <- e }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case e := <-closed:
		t.Fatal("close returned before original read", e)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if e = <-read; e != nil {
		t.Fatal(e)
	}
	if e = <-closed; e != nil {
		t.Fatal(e)
	}
	if _, e = parked.Read(make([]byte, 1)); !errors.Is(e, ErrArtifactClosed) {
		t.Fatal(e)
	}
	if e = reader.Close(); e != nil {
		t.Fatal(e)
	}
	if e = parked.Close(); e != nil {
		t.Fatal(e)
	}
}

func TestKVArtifactsCancellationDuringOriginalHashWaitsForReadAndPreservesSourceProof(t *testing.T) {
	for _, operation := range []string{"cancel", "store-close"} {
		t.Run(operation, func(t *testing.T) {
			s, l, directory := artifactTestStore(t, 10000)
			entered, release := make(chan struct{}), make(chan struct{})
			s.readAt = func(f *os.File, b []byte, offset int64) (int, error) {
				close(entered)
				<-release
				return f.ReadAt(b, offset)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p, e := s.Prepare(l, artifactTestRequest())
			if e != nil {
				t.Fatal(e)
			}
			type result struct {
				a contract.KVSnapshotArtifact
				r contract.KVSnapshotCapture
				e error
			}
			done := make(chan result, 1)
			go func() { a, r, e := s.Capture(ctx, p); done <- result{a, r, e} }()
			<-entered
			closed := make(chan error, 1)
			if operation == "cancel" {
				cancel()
			} else {
				go func() { closed <- s.Close() }()
			}
			select {
			case x := <-done:
				t.Fatal("capture returned before original file read acknowledgement", x.e)
			case <-time.After(30 * time.Millisecond):
			}
			if operation == "store-close" {
				select {
				case e := <-closed:
					t.Fatal("store close returned before original file read", e)
				default:
				}
			}
			close(release)
			x := <-done
			if x.a != nil || x.r.Stored || x.r.Complete || !x.r.FilesSynced || !x.r.Original.Completed || !x.r.Original.SourceComplete || !errors.Is(x.e, context.Canceled) {
				t.Fatal("late local cancellation changed source proof or kept an artifact", x.r, x.e)
			}
			if operation == "store-close" {
				if e := <-closed; e != nil {
					t.Fatal(e)
				}
			}
			entries, e := os.ReadDir(directory)
			if e != nil || len(entries) != 0 || s.used != 0 {
				t.Fatal("canceled seal retained own files or budget", entries, s.used, e)
			}
		})
	}
}
func TestKVArtifactsCleanupRefusesForeignReplacementAndKeepsUnreleasedCharge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-file rename semantics differ on Windows")
	}
	directory := t.TempDir()
	s, e := NewKVSnapshotStore(directory, 10000)
	if e != nil {
		t.Fatal(e)
	}
	l := &artifactTestLease{scope: contract.KVSnapshotScope{DocumentID: 7, Original: contract.KVScope{Prefix: []byte("original")}}}
	a, _ := artifactTestCapture(t, s, l)
	original := a.(*kvArtifact)
	path := directory + "/" + original.directory + "/snapshot"
	parked := directory + "/own-parked-snapshot"
	if e := os.Rename(path, parked); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte("foreign replacement"), 0600); e != nil {
		t.Fatal(e)
	}
	charge := s.used
	if e := a.Close(); !errors.Is(e, ErrChanged) || s.used != charge {
		t.Fatal("foreign file cleanup succeeded or released still retained budget", s.used, e)
	}
	if a.Receipt().CleanupError == "" {
		t.Fatal("original cleanup error missing from receipt")
	}
	if e := s.Close(); !errors.Is(e, ErrChanged) {
		t.Fatal("store omitted original cleanup failure", e)
	}
	raw, e := os.ReadFile(path)
	if e != nil || string(raw) != "foreign replacement" {
		t.Fatal("foreign file removed", string(raw), e)
	}
	raw, e = os.ReadFile(parked)
	sum := sha256.Sum256(artifactTestBody)
	if e != nil || !bytes.Equal(raw, append(bytes.Clone(artifactTestBody), sum[:]...)) {
		t.Fatal("renamed original handle changed", e)
	}
}
