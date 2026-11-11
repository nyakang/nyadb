package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestKVSnapshotStorePrivateFiniteOwnedRootCleanup(t *testing.T) {
	parent := t.TempDir()
	x, err := newKVSnapshotStore(parent)
	if err != nil {
		t.Fatal(err)
	}
	if x.store == nil || x.info.Mode().Perm()&0077 != 0 && runtime.GOOS != "windows" {
		t.Fatal("Snapshot root is not private")
	}
	y, err := newKVSnapshotStore(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer y.Close()
	if x.path == y.path {
		t.Fatal("store roots collide")
	}
	if err := x.Close(); err != nil || x.Close() != err {
		t.Fatal("close not idempotent", err)
	}
	if _, err := os.Lstat(x.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("original empty root retained", err)
	}
	if _, err := os.Lstat(y.path); err != nil {
		t.Fatal("neighbor removed", err)
	}
}
func TestKVSnapshotStoreRetainsCleanupResourcesAndForeignReplacement(t *testing.T) {
	for _, mode := range []string{"retained", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			x, err := newKVSnapshotStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "replaced" {
				if err := os.Rename(x.path, x.path+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(x.path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(x.path, "owned-or-foreign-evidence")
			if err := os.WriteFile(path, []byte("must remain"), 0600); err != nil {
				t.Fatal(err)
			}
			err = x.Close()
			if err == nil || x.Close() != err || !strings.Contains(err.Error(), x.path) {
				t.Fatal("cleanup loss not retained", err)
			}
			if raw, e := os.ReadFile(path); e != nil || string(raw) != "must remain" {
				t.Fatal("cleanup removed retained or foreign file", e)
			}
		})
	}
}
