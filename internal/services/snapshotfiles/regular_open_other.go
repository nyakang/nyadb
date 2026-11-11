//go:build !linux && !darwin

package snapshotfiles

import "os"

func snapshotOpenRegular(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
