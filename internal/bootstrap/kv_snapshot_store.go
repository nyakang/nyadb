package bootstrap

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/nyakang/nyadb/internal/services/snapshotfiles"
)

// Defaults reserve 192 MiB per capture and at most 128 MiB per output copy.
const kvSnapshotDiskLimit = 512 << 20

type kvSnapshotStore struct {
	store  *snapshotfiles.KVSnapshotStore
	root   *os.Root
	parent *os.Root
	info   os.FileInfo
	path   string
	name   string
	once   sync.Once
	err    error
}

func newKVSnapshotStore(parentPath string) (*kvSnapshotStore, error) {
	path, err := os.MkdirTemp(parentPath, "nyadb-etcd-snapshot-")
	if err != nil {
		return nil, err
	}
	x := &kvSnapshotStore{path: path, name: filepath.Base(path)}
	x.info, err = os.Lstat(path)
	if err != nil {
		// No captured identity means no deletion authority.
		return nil, fmt.Errorf("capture Snapshot directory identity (%s): %w", path, err)
	}
	x.parent, err = os.OpenRoot(filepath.Dir(path))
	if err == nil {
		x.root, err = x.parent.OpenRoot(x.name)
	}
	if err == nil {
		x.store, err = snapshotfiles.NewKVSnapshotStoreAt(x.root, kvSnapshotDiskLimit)
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("initialize Snapshot storage (%s): %w", path, err), x.Close())
	}
	return x, nil
}

func (x *kvSnapshotStore) Close() error {
	if x == nil {
		return nil
	}
	x.once.Do(func() {
		if x.store != nil {
			x.err = x.store.Close()
		}
		// Remove only the original empty root; retained files prevent removal.
		if x.parent != nil && x.root != nil {
			current, err := x.parent.Lstat(x.name)
			if err == nil && (!current.IsDir() || !os.SameFile(current, x.info)) {
				err = errors.New("original Snapshot directory identity changed")
			}
			if err == nil {
				var f *os.File
				f, err = x.root.Open(".")
				if err == nil {
					var names []string
					names, err = f.Readdirnames(1)
					if len(names) != 0 {
						err = errors.New("original Snapshot directory retains cleanup resources")
					} else if err == io.EOF {
						err = nil
					}
					err = errors.Join(err, f.Close())
				}
			}
			if err == nil {
				err = x.parent.Remove(x.name)
			}
			if err != nil {
				x.err = errors.Join(x.err, fmt.Errorf("Snapshot directory cleanup (%s): %w", x.path, err))
			}
		}
		if x.root != nil {
			x.err = errors.Join(x.err, x.root.Close())
		}
		if x.parent != nil {
			x.err = errors.Join(x.err, x.parent.Close())
		}
	})
	return x.err
}
