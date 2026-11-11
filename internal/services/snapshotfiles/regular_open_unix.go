//go:build linux || darwin

package snapshotfiles

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// Names are validated basenames in a captured root. No-follow rejects a replaced
// symlink; nonblocking prevents a replaced FIFO from waiting for a peer. The
// caller checks regular-file identity on the opened descriptor before reading.
func snapshotOpenRegular(root *os.Root, name string) (*os.File, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	defer runtime.KeepAlive(dir)
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}
