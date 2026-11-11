//go:build darwin

package snapshotfiles

import (
	"golang.org/x/sys/unix"
	"os"
	"runtime"
)

func snapshotMoveNoReplace(from *os.File, old string, to *os.File, next string) error {
	defer runtime.KeepAlive(from)
	defer runtime.KeepAlive(to)
	return unix.RenameatxNp(int(from.Fd()), old, int(to.Fd()), next, unix.RENAME_EXCL)
}
