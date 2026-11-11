package snapshotfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func fileTestPlan(t *testing.T, s *KVSnapshotStore, a contract.KVSnapshotArtifact, path string, replace bool) *kvSnapshotFilePlan {
	t.Helper()
	p, e := s.PrepareFile(context.Background(), a, contract.KVSnapshotFileChoice{Path: path, Kind: contract.KVSnapshotBackendFile, Replace: replace})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p.(*kvSnapshotFilePlan)
}
func fileTestBytes(t *testing.T, path string, expected []byte) {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(b, expected) {
		t.Fatalf("bytes at %s: %q, %v", path, b, e)
	}
}
func fileTestBackend() []byte {
	sum := sha256.Sum256(artifactTestBody)
	return append(bytes.Clone(artifactTestBody), sum[:]...)
}
func TestKVSnapshotFilesExactBackendAndJournalOriginalReceiptsNoDispatch(t *testing.T) {
	s, l, _ := artifactTestStore(t, 10000)
	a, receipt := artifactTestCapture(t, s, l)
	originalCharge := s.used
	for _, kind := range []contract.KVSnapshotFileKind{contract.KVSnapshotBackendFile, contract.KVSnapshotJournalFile} {
		t.Run(string(kind), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "snapshot")
			choice := contract.KVSnapshotFileChoice{Path: path, Kind: kind}
			p, e := s.PrepareFile(context.Background(), a, choice)
			if e != nil {
				t.Fatal(e)
			}
			defer p.Close()
			observation := p.Observation()
			observation.Source.Original.Scope.Original.Prefix[0] = 9
			observation.Choice.Path = "foreign"
			if !reflect.DeepEqual(p.Observation().Source, receipt) {
				t.Fatal("mutable inspection")
			}
			r, e := s.PublishFile(context.Background(), p)
			if e != nil || !r.Ended || !r.Attempted || !r.Published || !r.SourceVerified || !r.FileSynced || r.DirectorySynced != (runtime.GOOS != "windows") || !r.DestinationMatched || !r.PathMatched || r.PublicationUnknown {
				t.Fatal(r, e)
			}
			want := fileTestBackend()
			if kind == contract.KVSnapshotJournalFile {
				want = artifactTestJournal
			}
			fileTestBytes(t, path, want)
			if r.BytesWritten != uint64(len(want)) || s.used != originalCharge || l.dispatch.Load() != 1 {
				t.Fatal("counters/dispatch", r, s.used, l.dispatch.Load())
			}
			if runtimeInfo, e := os.Stat(path); e != nil || runtime.GOOS != "windows" && runtimeInfo.Mode().Perm()&0077 != 0 {
				t.Fatal("private output mode", runtimeInfo, e)
			}
			clone := r.Clone()
			clone.SHA256[0] ^= 1
			clone.Original.Source.Original.Scope.Original.Prefix[0] ^= 1
			if !bytes.Equal(r.SHA256, p.(*kvSnapshotFilePlan).sum) || !reflect.DeepEqual(r.Original.Source, receipt) {
				t.Fatal("receipt alias")
			}
			if _, e := s.PublishFile(context.Background(), p); !errors.Is(e, contract.ErrKVPlanUsed) {
				t.Fatal("reuse", e)
			}
			if e := p.Close(); e != nil {
				t.Fatal("close after private directory cleanup", e)
			}
			if e := p.Close(); e != nil {
				t.Fatal("repeated close", e)
			}
		})
	}
}

func TestKVSnapshotFilesUnavailableDirectorySyncPreservesPublicationFacts(t *testing.T) {
	for _, unavailable := range []int{1, 2} {
		t.Run(strconv.Itoa(unavailable), func(t *testing.T) {
			s, l, _ := artifactTestStore(t, 10000)
			a, _ := artifactTestCapture(t, s, l)
			path := filepath.Join(t.TempDir(), "chosen")
			p := fileTestPlan(t, s, a, path, false)
			originalCharge := s.used
			calls := 0
			s.fileIO.syncDirectory = func(*os.File) (bool, error) {
				calls++
				return calls != unavailable, nil
			}
			r, err := s.PublishFile(context.Background(), p)
			if err != nil || !r.Ended || !r.Published || !r.FileSynced || r.DirectorySynced || !r.DestinationMatched || r.PublicationUnknown || r.TemporaryRetained || r.CleanupError != "" || calls != 2 {
				t.Fatal(r, err, calls)
			}
			if s.used != originalCharge || p.private != nil || p.privateFile != nil {
				t.Fatal("empty private directory retained resources", s.used)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			fileTestBytes(t, path, fileTestBackend())
		})
	}
}

func TestKVSnapshotFilesReplacementPreservesLargerOriginalFile(t *testing.T) {
	s, l, _ := artifactTestStore(t, 10000)
	a, _ := artifactTestCapture(t, s, l)
	path := filepath.Join(t.TempDir(), "existing")
	old := bytes.Repeat([]byte("old-backup\x00\xff"), 30000)
	if e := os.WriteFile(path, old, 0600); e != nil {
		t.Fatal(e)
	}
	p := fileTestPlan(t, s, a, path, true)
	inspection := p.Observation()
	inspection.Existing.SHA256 = "foreign"
	inspection.Existing.Size = 1
	r, e := s.PublishFile(context.Background(), p)
	if e != nil || !r.Displaced || !r.Published || r.Restored || r.DisplacedPath == "" || r.Original.Existing.Size != uint64(len(old)) {
		t.Fatal(r, e)
	}
	fileTestBytes(t, path, fileTestBackend())
	fileTestBytes(t, r.DisplacedPath, old)
	if e := p.Close(); e != nil {
		t.Fatal(e)
	}
	fileTestBytes(t, r.DisplacedPath, old)
	if e := a.Close(); e != nil {
		t.Fatal(e)
	}
	if s.used != 0 {
		t.Fatal("user backup charged", s.used)
	}
}
func TestKVSnapshotFilesAppearedTargetAndChangedReplacementPreserveAllBytes(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "appeared", true: "replaced"}[replacement], func(t *testing.T) {
			s, l, _ := artifactTestStore(t, 10000)
			a, _ := artifactTestCapture(t, s, l)
			path := filepath.Join(t.TempDir(), "chosen")
			if replacement {
				if e := os.WriteFile(path, []byte("old"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			p := fileTestPlan(t, s, a, path, replacement)
			if replacement {
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
			}
			foreign := []byte("foreign replacement all bytes")
			if e := os.WriteFile(path, foreign, 0600); e != nil {
				t.Fatal(e)
			}
			r, e := s.PublishFile(context.Background(), p)
			if e == nil || r.Published || r.PublicationUnknown || r.DisplacementUnknown {
				t.Fatal(r, e)
			}
			if replacement && (!r.Displaced || !r.Restored || r.DisplacedPath != "") {
				t.Fatal("foreign displaced file not restored", r)
			}
			fileTestBytes(t, path, foreign)
		})
	}
}
func TestKVSnapshotFilesDisplacedConflictRetainsOriginalAndAppearedTarget(t *testing.T) {
	s, l, _ := artifactTestStore(t, 10000)
	a, _ := artifactTestCapture(t, s, l)
	path := filepath.Join(t.TempDir(), "chosen")
	old := []byte("complete original old bytes")
	if e := os.WriteFile(path, old, 0600); e != nil {
		t.Fatal(e)
	}
	p := fileTestPlan(t, s, a, path, true)
	native := s.fileIO.move
	s.fileIO.move = func(from *os.File, oldName string, to *os.File, newName string) error {
		if oldName == "new" {
			if e := os.WriteFile(path, []byte("appeared"), 0600); e != nil {
				return e
			}
		}
		return native(from, oldName, to, newName)
	}
	r, e := s.PublishFile(context.Background(), p)
	if e == nil || r.Published || !r.Displaced || !r.RestorationAttempted || r.Restored || r.DisplacedPath == "" {
		t.Fatal(r, e)
	}
	fileTestBytes(t, path, []byte("appeared"))
	fileTestBytes(t, r.DisplacedPath, old)
	if e := p.Close(); e != nil {
		t.Fatal(e)
	}
	fileTestBytes(t, r.DisplacedPath, old)
}
func TestKVSnapshotFilesUnknownMoveAcknowledgementsNeverDeleteOrPromoteObservations(t *testing.T) {
	for _, phase := range []string{"displacement", "publication", "restoration"} {
		t.Run(phase, func(t *testing.T) {
			s, l, _ := artifactTestStore(t, 10000)
			a, _ := artifactTestCapture(t, s, l)
			path := filepath.Join(t.TempDir(), "chosen")
			old := []byte("original old bytes")
			if e := os.WriteFile(path, old, 0600); e != nil {
				t.Fatal(e)
			}
			p := fileTestPlan(t, s, a, path, true)
			native := s.fileIO.move
			fault := errors.New("filesystem acknowledgement lost")
			s.fileIO.move = func(from *os.File, name string, to *os.File, next string) error {
				if phase == "restoration" && name == "new" {
					return os.ErrExist
				}
				e := native(from, name, to, next)
				if e == nil && ((phase == "displacement" && next == "displaced") || (phase == "publication" && name == "new") || (phase == "restoration" && name == "displaced")) {
					return fault
				}
				return e
			}
			r, e := s.PublishFile(context.Background(), p)
			if !errors.Is(e, fault) || r.Published {
				t.Fatal(r, e)
			}
			switch phase {
			case "displacement":
				if !r.DisplacementUnknown || r.Displaced || !r.Restored || r.PublicationAttempted {
					t.Fatal(r)
				}
				fileTestBytes(t, path, old)
			case "publication":
				if !r.PublicationUnknown || !r.DestinationMatched || r.Restored || r.DisplacedPath == "" {
					t.Fatal(r)
				}
				fileTestBytes(t, path, fileTestBackend())
				fileTestBytes(t, r.DisplacedPath, old)
			case "restoration":
				if !r.RestorationUnknown || r.Restored || r.PublicationUnknown {
					t.Fatal(r)
				}
				fileTestBytes(t, path, old)
			}
		})
	}
}
func TestKVSnapshotFilesPrepublicationFaultsCleanupAndSeparatePublishedDurability(t *testing.T) {
	for _, phase := range []string{"short-write", "read", "hash", "file-sync", "file-close", "publication", "directory-sync", "cleanup"} {
		t.Run(phase, func(t *testing.T) {
			s, l, _ := artifactTestStore(t, 10000)
			a, _ := artifactTestCapture(t, s, l)
			path := filepath.Join(t.TempDir(), "chosen")
			p := fileTestPlan(t, s, a, path, false)
			fault := errors.New("original file fault")
			switch phase {
			case "short-write":
				s.fileIO.write = func(f *os.File, b []byte) (int, error) { return f.Write(b[:len(b)/2]) }
			case "read":
				s.readAt = func(f *os.File, b []byte, o int64) (int, error) { return 0, fault }
			case "hash":
				s.readAt = func(f *os.File, b []byte, o int64) (int, error) {
					n, e := f.ReadAt(b, o)
					if n > 0 {
						b[0] ^= 1
					}
					return n, e
				}
			case "file-sync":
				s.fileIO.syncFile = func(f *os.File) error { return fault }
			case "file-close":
				s.fileIO.closeFile = func(f *os.File) error { _ = f.Close(); return fault }
			case "publication":
				s.fileIO.move = func(*os.File, string, *os.File, string) error { return os.ErrExist }
			case "directory-sync":
				s.fileIO.syncDirectory = func(*os.File) (bool, error) { return false, fault }
			case "cleanup":
				native := s.fileIO.remove
				s.fileIO.remove = func(root *os.Root, name string) error {
					if strings.HasPrefix(name, ".nyadb-snapshot-save-") {
						return fault
					}
					return native(root, name)
				}
			}
			r, e := s.PublishFile(context.Background(), p)
			if e == nil || !r.Ended {
				t.Fatal(r, e)
			}
			if phase == "directory-sync" || phase == "cleanup" {
				if !r.Published || !r.DestinationMatched || r.PublicationUnknown {
					t.Fatal(r, e)
				}
				fileTestBytes(t, path, fileTestBackend())
			} else if r.Published || r.PublicationUnknown {
				t.Fatal(r, e)
			} else if _, e := os.Lstat(path); !os.IsNotExist(e) {
				t.Fatal("partial publication", e)
			}
			s.fileIO = defaultKVSnapshotFileIO()
			if e := p.Close(); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestKVSnapshotFilesCancelAndCloseWaitForOriginalWriteAndDoNotPublish(t *testing.T) {
	for _, phase := range []string{"cancel", "plan-close", "store-close"} {
		t.Run(phase, func(t *testing.T) {
			s, l, _ := artifactTestStore(t, 10000)
			a, _ := artifactTestCapture(t, s, l)
			path := filepath.Join(t.TempDir(), "chosen")
			p := fileTestPlan(t, s, a, path, false)
			entered, release := make(chan struct{}), make(chan struct{})
			s.fileIO.write = func(f *os.File, b []byte) (int, error) { close(entered); <-release; return f.Write(b) }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan contract.KVSnapshotFileResult, 1)
			go func() { r, _ := s.PublishFile(ctx, p); result <- r }()
			<-entered
			closed := make(chan error, 1)
			switch phase {
			case "cancel":
				cancel()
			case "plan-close":
				go func() { closed <- p.Close() }()
			case "store-close":
				go func() { closed <- s.Close() }()
			}
			select {
			case r := <-result:
				t.Fatal("detached executor", r)
			case <-time.After(20 * time.Millisecond):
			}
			if phase != "cancel" {
				select {
				case e := <-closed:
					t.Fatal("close detached executor", e)
				default:
				}
			}
			close(release)
			r := <-result
			if !r.Canceled || r.Published || !r.Ended {
				t.Fatal(r)
			}
			if phase != "cancel" {
				if e := <-closed; e != nil {
					t.Fatal(e)
				}
			}
			if _, e := os.Lstat(path); !os.IsNotExist(e) {
				t.Fatal("canceled destination", e)
			}
		})
	}
}
func TestKVSnapshotFilesCancellationAfterAcknowledgedPublicationRetainsUserFile(t *testing.T) {
	s, l, _ := artifactTestStore(t, 10000)
	a, _ := artifactTestCapture(t, s, l)
	path := filepath.Join(t.TempDir(), "chosen")
	p := fileTestPlan(t, s, a, path, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	native := s.fileIO.move
	s.fileIO.move = func(from *os.File, name string, to *os.File, next string) error {
		e := native(from, name, to, next)
		if name == "new" && e == nil {
			cancel()
		}
		return e
	}
	r, e := s.PublishFile(ctx, p)
	if !errors.Is(e, context.Canceled) || !r.Canceled || !r.Published || r.PublicationUnknown {
		t.Fatal(r, e)
	}
	if e := p.Close(); e != nil {
		t.Fatal(e)
	}
	fileTestBytes(t, path, fileTestBackend())
}
func TestKVSnapshotFilesClosedForeignCopiedAndCanceledPlansHaveNoAuthority(t *testing.T) {
	s, l, _ := artifactTestStore(t, 10000)
	a, _ := artifactTestCapture(t, s, l)
	other, _, _ := artifactTestStore(t, 10000)
	path := filepath.Join(t.TempDir(), "chosen")
	p := fileTestPlan(t, s, a, path, false)
	copyPlan := &kvSnapshotFilePlan{authority: p, store: s, artifact: p.artifact, observation: p.observation}
	for _, target := range []struct {
		s *KVSnapshotStore
		p contract.KVSnapshotFilePreparation
	}{{s, copyPlan}, {other, p}} {
		if _, e := target.s.PublishFile(context.Background(), target.p); !errors.Is(e, contract.ErrKVPlanUsed) {
			t.Fatal(e)
		}
	}
	if _, e := other.PrepareFile(context.Background(), a, p.Observation().Choice); !errors.Is(e, ErrArtifactEvidence) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, e := s.PublishFile(ctx, p); !errors.Is(e, context.Canceled) || r.Attempted {
		t.Fatal(r, e)
	}
	if e := a.Close(); e != nil {
		t.Fatal(e)
	}
	if r, e := s.PublishFile(context.Background(), p); e == nil || r.Published {
		t.Fatal(r, e)
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}
func TestKVSnapshotFilesCapturedParentMovedPublishesOnlyToOriginalDirectory(t *testing.T) {
	s, l, _ := artifactTestStore(t, 10000)
	a, _ := artifactTestCapture(t, s, l)
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	moved := parent + "-moved"
	if e := os.Mkdir(parent, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(parent, "chosen")
	p := fileTestPlan(t, s, a, path, false)
	if e := os.Rename(parent, moved); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(parent, 0700); e != nil {
		t.Fatal(e)
	}
	r, e := s.PublishFile(context.Background(), p)
	if e == nil || !r.Published || !r.DestinationMatched || r.PathMatched {
		t.Fatal(r, e)
	}
	fileTestBytes(t, filepath.Join(moved, "chosen"), fileTestBackend())
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("foreign parent selected", e)
	}
}
func TestKVSnapshotFilesSymlinksRejectedAndOutputQuotaReserved(t *testing.T) {
	s, l, _ := artifactTestStore(t, 10000)
	a, _ := artifactTestCapture(t, s, l)
	path := filepath.Join(t.TempDir(), "chosen")
	foreign := path + "-foreign"
	if e := os.WriteFile(foreign, []byte("foreign"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(foreign, path); e != nil {
		t.Fatal(e)
	}
	if _, e := s.PrepareFile(context.Background(), a, contract.KVSnapshotFileChoice{Path: path, Kind: contract.KVSnapshotBackendFile, Replace: true}); !errors.Is(e, ErrBounds) {
		t.Fatal(e)
	}
	fileTestBytes(t, foreign, []byte("foreign"))
	path = path + "-new"
	p := fileTestPlan(t, s, a, path, false)
	s.mu.Lock()
	s.limit = s.used + p.size - 1
	s.mu.Unlock()
	r, e := s.PublishFile(context.Background(), p)
	if !errors.Is(e, ErrArtifactQuota) || r.Published {
		t.Fatal(r, e)
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}
func TestKVSnapshotFilesOldInspectionGrowthAndCancellation(t *testing.T) {
	for _, phase := range []string{"growth", "cancel"} {
		t.Run(phase, func(t *testing.T) {
			s, l, _ := artifactTestStore(t, 10000)
			a, _ := artifactTestCapture(t, s, l)
			path := filepath.Join(t.TempDir(), "chosen")
			if e := os.WriteFile(path, []byte("old"), 0600); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			native := s.fileIO.readAt
			s.fileIO.readAt = func(f *os.File, b []byte, o int64) (int, error) {
				n, e := native(f, b, o)
				if o == 0 {
					if phase == "cancel" {
						cancel()
					} else {
						w, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
						if err != nil {
							return n, err
						}
						_, err = w.Write([]byte("grown"))
						_ = w.Close()
						if err != nil {
							return n, err
						}
					}
				}
				return n, e
			}
			p, e := s.PrepareFile(ctx, a, contract.KVSnapshotFileChoice{Path: path, Kind: contract.KVSnapshotBackendFile, Replace: true})
			if e == nil || p != nil {
				t.Fatal(p, e)
			}
			if phase == "cancel" && !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
		})
	}
}

var _ io.Reader = (*kvArtifactReader)(nil)

func TestKVSnapshotFilesCloseWaitsForOriginalSyncAndArtifactCloseRevokesBeforePublication(t *testing.T) {
	for _, phase := range []string{"plan-close", "artifact-close"} {
		t.Run(phase, func(t *testing.T) {
			s, l, _ := artifactTestStore(t, 10000)
			a, _ := artifactTestCapture(t, s, l)
			path := filepath.Join(t.TempDir(), "chosen")
			p := fileTestPlan(t, s, a, path, false)
			entered, release := make(chan struct{}), make(chan struct{})
			s.fileIO.syncFile = func(f *os.File) error { close(entered); <-release; return f.Sync() }
			result := make(chan contract.KVSnapshotFileResult, 1)
			go func() { r, _ := s.PublishFile(context.Background(), p); result <- r }()
			<-entered
			closed := make(chan error, 1)
			if phase == "plan-close" {
				go func() { closed <- p.Close() }()
				select {
				case e := <-closed:
					t.Fatal("close detached fsync", e)
				case <-time.After(20 * time.Millisecond):
				}
			} else if e := a.Close(); e != nil {
				t.Fatal(e)
			}
			close(release)
			r := <-result
			if r.Published || !r.Ended || (phase == "plan-close" && !r.Canceled) {
				t.Fatal(r)
			}
			if phase == "plan-close" {
				if e := <-closed; e != nil {
					t.Fatal(e)
				}
			}
			if _, e := os.Stat(path); !os.IsNotExist(e) {
				t.Fatal(e)
			}
		})
	}
}
func TestKVSnapshotFilesCleanupFailureKeepsExactTemporaryChargeAndCloseError(t *testing.T) {
	s, e := NewKVSnapshotStore(t.TempDir(), 10000)
	if e != nil {
		t.Fatal(e)
	}
	l := &artifactTestLease{scope: contract.KVSnapshotScope{DocumentID: 99}}
	a, _ := artifactTestCapture(t, s, l)
	path := filepath.Join(t.TempDir(), "chosen")
	p := fileTestPlan(t, s, a, path, false)
	base := s.used
	fault := errors.New("original partial cleanup failed")
	s.fileIO.write = func(f *os.File, b []byte) (int, error) { n, e := f.Write(b[:3]); return n, errors.Join(e, fault) }
	s.fileIO.remove = func(*os.Root, string) error { return fault }
	r, e := s.PublishFile(context.Background(), p)
	if !errors.Is(e, fault) || r.Published || !r.TemporaryRetained || r.BytesWritten != 3 || r.CleanupError == "" || s.used != base+p.size {
		t.Fatal(r, e, s.used)
	}
	if e := p.Close(); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	if e := a.Close(); e != nil {
		t.Fatal(e)
	}
	if s.used != p.size {
		t.Fatal("retained output charge lost", s.used)
	}
	if e := s.Close(); !errors.Is(e, fault) {
		t.Fatal("store hid failed original cleanup", e)
	}
	fileTestBytes(t, r.TemporaryPath, fileTestBackend()[:3])
}
func TestKVSnapshotFilesForeignPrivateTemporaryAndDirectoryPreserved(t *testing.T) {
	for _, phase := range []string{"temporary", "directory"} {
		t.Run(phase, func(t *testing.T) {
			s, e := NewKVSnapshotStore(t.TempDir(), 10000)
			if e != nil {
				t.Fatal(e)
			}
			l := &artifactTestLease{scope: contract.KVSnapshotScope{DocumentID: 99}}
			a, _ := artifactTestCapture(t, s, l)
			path := filepath.Join(t.TempDir(), "chosen")
			p := fileTestPlan(t, s, a, path, false)
			var foreignPath string
			if phase == "temporary" {
				s.fileIO.syncFile = func(*os.File) error {
					foreignPath = filepath.Join(filepath.Dir(path), p.directory, "new")
					if e := p.private.Remove("new"); e != nil {
						return e
					}
					return os.WriteFile(foreignPath, []byte("foreign"), 0600)
				}
			} else {
				// Windows rejects moving a directory with an open child file.
				// Replace it after close, before verification and publication.
				native := s.fileIO.closeFile
				s.fileIO.closeFile = func(f *os.File) error {
					if e := native(f); e != nil {
						return e
					}
					directory := filepath.Join(filepath.Dir(path), p.directory)
					if e := os.Rename(directory, directory+"-moved"); e != nil {
						return e
					}
					if e := os.Mkdir(directory, 0700); e != nil {
						return e
					}
					foreignPath = filepath.Join(directory, "foreign")
					return os.WriteFile(foreignPath, []byte("foreign"), 0600)
				}
			}
			r, e := s.PublishFile(context.Background(), p)
			if e == nil || r.CleanupError == "" {
				t.Fatal(r, e)
			}
			if phase == "temporary" && r.Published {
				t.Fatal(r)
			}
			if phase == "directory" && (!r.Published || r.AuxiliaryPathMatched) {
				t.Fatal(r)
			}
			if e := p.Close(); e == nil {
				t.Fatal("foreign cleanup hidden")
			}
			if e := s.Close(); e == nil {
				t.Fatal("store hid foreign private path")
			}
			fileTestBytes(t, foreignPath, []byte("foreign"))
		})
	}
}
