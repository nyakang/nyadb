//go:build !linux && !darwin && !windows

package snapshotfiles

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"os"
)

func snapshotMoveNoReplace(from *os.File, old string, to *os.File, next string) error {
	return contract.ErrUnsupported
}
