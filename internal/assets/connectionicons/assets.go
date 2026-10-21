// Package connectionicons embeds the independently sourced, redistributable
// connection-chooser images used by a normal NyaDB build.
//
// It intentionally does NOT embed Navicat-extracted Qt resource PNGs. Those
// stay under the ignored temp/research directory for optional local comparison.
package connectionicons

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed manifest.json icons/*.png
var assets embed.FS

// Entry describes one committed PNG and its provenance.
type Entry struct {
	Name    string `json:"name"`
	File    string `json:"file"`
	Source  string `json:"source"`
	Artwork string `json:"artwork"`
	SHA256  string `json:"sha256"`
}

type Manifest struct {
	Format           int     `json:"format"`
	SourceCollection string  `json:"sourceCollection"`
	SourceCommit     string  `json:"sourceCommit"`
	Generator        string  `json:"generator"`
	Entries          []Entry `json:"entries"`
}

// List reads the manifest compiled into the Go binary. No filesystem paths
// are used at runtime, so icon availability never depends on the cwd.
func List() (Manifest, error) {
	raw, err := assets.ReadFile("manifest.json")
	if err != nil {
		return Manifest{}, fmt.Errorf("embedded connection icons manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse embedded icon manifest: %w", err)
	}
	if manifest.Format != 1 {
		return Manifest{}, fmt.Errorf("unsupported embedded icon manifest version: %d", manifest.Format)
	}
	return manifest, nil
}

// Read provides an icon explicitly listed in the committed manifest.
func Read(file string) ([]byte, error) {
	// No user-controlled paths, folders or traversal.
	for _, r := range file {
		if !(r == '-' || r == '.' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return nil, fmt.Errorf("invalid embedded icon file name")
		}
	}
	if len(file) < 5 || file[len(file)-4:] != ".png" {
		return nil, fmt.Errorf("invalid embedded icon extension")
	}
	return assets.ReadFile("icons/" + file)
}
