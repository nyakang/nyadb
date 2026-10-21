// Package lucide provides pinned, embedded Lucide SVG icons for NyaDB's
// general-purpose UI. Database brand artwork lives in connectionicons.
package lucide

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// Version is the pinned upstream Lucide release.
const Version = "0.468.0"

//go:embed icons/*.svg LICENSE
var assets embed.FS

// Read loads a committed SVG by its Lucide icon name. Paths are not accepted.
func Read(name string) ([]byte, error) {
	if name == "" || strings.ContainsAny(name, "./\\") {
		return nil, fmt.Errorf("invalid Lucide icon name %q", name)
	}
	for _, r := range name {
		if r != '-' && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return nil, fmt.Errorf("invalid Lucide icon name %q", name)
		}
	}
	data, err := assets.ReadFile("icons/" + name + ".svg")
	if err != nil {
		return nil, fmt.Errorf("Lucide icon %q: %w", name, err)
	}
	return data, nil
}

// Names lists the SVG icons available in this build.
func Names() ([]string, error) {
	files, err := fs.Glob(assets, "icons/*.svg")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for _, path := range files {
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(path, "icons/"), ".svg"))
	}
	return names, nil
}

// License supplies the upstream attribution included in this build.
func License() ([]byte, error) {
	return assets.ReadFile("LICENSE")
}
