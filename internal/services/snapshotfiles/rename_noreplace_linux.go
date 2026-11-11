//go:build linux

package snapshotfiles

import (
	"golang.org/x/sys/unix"
	"os"
	"runtime"
)

// Both names are validated basenames. Captured directory handles keep a moved
// parent or a replaced private directory from selecting another path owner.
func snapshotMoveNoReplace(from *os.File, old string, to *os.File, next string) error {
	defer runtime.KeepAlive(from)
	defer runtime.KeepAlive(to)
	return unix.Renameat2(int(from.Fd()), old, int(to.Fd()), next, unix.RENAME_NOREPLACE)
}
