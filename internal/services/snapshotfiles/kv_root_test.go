package snapshotfiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestKVSnapshotStoreClonesOriginalRootAcrossPathReplacementAndCallerClose(t *testing.T) {
	parent := t.TempDir()
	path, moved := filepath.Join(parent, "original"), filepath.Join(parent, "moved-original")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(path, "foreign")
	if err := os.WriteFile(foreign, []byte("foreign user resource"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewKVSnapshotStoreAt(root, 10000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	lease := &artifactTestLease{scope: contract.KVSnapshotScope{DocumentID: 7, Original: contract.KVScope{Generation: 11}}}
	a, r := artifactTestCapture(t, store, lease)
	if !r.Complete {
		t.Fatal("caller close revoked the store's original independent root")
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 1 || entries[0].Name() != "foreign" {
		t.Fatal("replacement path became the capture owner", err)
	}
	originalEntries, err := os.ReadDir(moved)
	if err != nil || len(originalEntries) != 1 {
		t.Fatal("capture did not use the opened original root", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(foreign); err != nil || string(raw) != "foreign user resource" {
		t.Fatal("store cleanup changed the foreign path owner", err)
	}
	if entries, err := os.ReadDir(moved); err != nil || len(entries) != 0 {
		t.Fatal("original captured resources retained", err)
	}
}
