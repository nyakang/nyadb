package driverstore

import (
	"os"
	"path/filepath"
)

// Default opens the application's managed artifact directory. Profiles store
// explicit versions and digests; there is no implicit latest-version selection.
func Default() (*Store, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return New(filepath.Join(directory, "NyaDB", "drivers"))
}
