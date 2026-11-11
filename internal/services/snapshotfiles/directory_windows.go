//go:build windows

package snapshotfiles

import (
	"errors"
	"os"
)

// OpenRoot's initial Win32 handle does not share deletion. Clone it through
// Root.OpenRoot, whose relative NT handle shares deletion, before releasing it.
// The clone keeps the original directory identity while allowing renames.
func snapshotOpenRoot(name string) (*os.Root, error) {
	initial, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	root, err := initial.OpenRoot(".")
	err = errors.Join(err, initial.Close())
	if err != nil {
		if root != nil {
			_ = root.Close()
		}
		return nil, err
	}
	return root, nil
}

// Windows does not support fsync on these read-only directory handles. Keep
// this fact separate from successful file flushing and rename acknowledgement.
func snapshotSyncDirectory(directory *os.File) (bool, error) {
	_, err := directory.Stat()
	return false, err
}
