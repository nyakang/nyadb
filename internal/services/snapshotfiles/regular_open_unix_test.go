//go:build linux || darwin

package snapshotfiles

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"golang.org/x/sys/unix"
)

func TestSnapshotInspectRejectsSymlinkAndFIFOReplacedDuringOpen(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			s, l, _ := artifactTestStore(t, 10000)
			a, _ := artifactTestCapture(t, s, l)
			dir := t.TempDir()
			path := filepath.Join(dir, "selected")
			if err := os.WriteFile(path, []byte("original user file"), 0600); err != nil {
				t.Fatal(err)
			}
			foreign := filepath.Join(dir, "foreign")
			if err := os.WriteFile(foreign, []byte("foreign target"), 0600); err != nil {
				t.Fatal(err)
			}
			s.fileIO.openRegular = func(root *os.Root, name string) (*os.File, error) {
				if err := root.Remove(name); err != nil {
					return nil, err
				}
				var err error
				if kind == "symlink" {
					err = os.Symlink(foreign, path)
				} else {
					err = unix.Mkfifo(path, 0600)
				}
				if err != nil {
					return nil, err
				}
				return snapshotOpenRegular(root, name)
			}
			done := make(chan error, 1)
			go func() {
				plan, err := s.PrepareFile(context.Background(), a, contract.KVSnapshotFileChoice{Path: path, Kind: contract.KVSnapshotBackendFile, Replace: true})
				if plan != nil {
					_ = plan.Close()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("special-file replacement acquired authority")
				}
			case <-time.After(2 * time.Second):
				// Unblock a regressed FIFO opener so the failure does not hang Close.
				if kind == "fifo" {
					fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK, 0)
					if err == nil {
						_ = unix.Close(fd)
					}
				}
				t.Fatal("original inspection waited on a special-file replacement")
			}
			fileTestBytes(t, foreign, []byte("foreign target"))
		})
	}
}

func TestSnapshotArtifactVerificationRejectsReplacedPrivateSymlinkAndFIFO(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			store, err := NewKVSnapshotStore(dir, 10000)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			lease := &artifactTestLease{scope: contract.KVSnapshotScope{DocumentID: 7}}
			foreign := filepath.Join(t.TempDir(), "foreign")
			if err := os.WriteFile(foreign, fileTestBackend(), 0600); err != nil {
				t.Fatal(err)
			}
			var reads atomic.Int32
			originalRead := store.readAt
			store.readAt = func(f *os.File, b []byte, offset int64) (int, error) { reads.Add(1); return originalRead(f, b, offset) }
			var replaced string
			store.syncFile = func(f *os.File) error {
				if err := f.Sync(); err != nil {
					return err
				}
				if replaced != "" {
					return nil
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".nyadb-kv-snapshot-") {
					return ErrArtifactEvidence
				}
				replaced = filepath.Join(dir, entries[0].Name(), "snapshot")
				if err := os.Remove(replaced); err != nil {
					return err
				}
				if kind == "symlink" {
					return os.Symlink(foreign, replaced)
				}
				return unix.Mkfifo(replaced, 0600)
			}
			plan, err := store.Prepare(lease, artifactTestRequest())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var a contract.KVSnapshotArtifact
			var r contract.KVSnapshotCapture
			var captureErr error
			go func() { a, r, captureErr = store.Capture(context.Background(), plan); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				if kind == "fifo" {
					fd, err := unix.Open(replaced, unix.O_WRONLY|unix.O_NONBLOCK, 0)
					if err == nil {
						_ = unix.Close(fd)
					}
				}
				t.Fatal("private verification waited on a foreign FIFO")
			}
			if captureErr == nil || a != nil || !r.Original.Completed || r.Stored || r.StorageError == "" || r.CleanupError == "" || reads.Load() != 0 {
				t.Fatal("foreign private file became a verified artifact", r, captureErr)
			}
			if err := store.Close(); err == nil {
				t.Fatal("retained changed resource cleanup was not reported")
			}
			if _, err := os.Lstat(replaced); err != nil {
				t.Fatal("cleanup removed a changed private resource", err)
			}
			fileTestBytes(t, foreign, fileTestBackend())
		})
	}
}
