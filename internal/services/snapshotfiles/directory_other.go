//go:build !windows

package snapshotfiles

import "os"

func snapshotOpenRoot(name string) (*os.Root, error) {
	return os.OpenRoot(name)
}

func snapshotSyncDirectory(directory *os.File) (bool, error) {
	err := directory.Sync()
	return err == nil, err
}
