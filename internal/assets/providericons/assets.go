// Package providericons embeds NyaDB's vendor filter badges. Individual
// source art is pinned and documented; no Navicat Qt images are included.
package providericons

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed icons/*.png manifest.json
var assets embed.FS

type Entry struct {
	Name    string `json:"name"`
	File    string `json:"file"`
	Source  string `json:"source"`
	Artwork string `json:"artwork"`
	SHA256  string `json:"sha256"`
}

type Manifest struct {
	Format  int     `json:"format"`
	Entries []Entry `json:"entries"`
}

func List() (Manifest, error) {
	data, err := assets.ReadFile("manifest.json")
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.Format != 1 {
		return Manifest{}, fmt.Errorf("unsupported provider icon manifest format %d", manifest.Format)
	}
	return manifest, nil
}

func Read(file string) ([]byte, error) {
	if !strings.HasSuffix(file, ".png") {
		return nil, fmt.Errorf("invalid PNG filename")
	}
	for _, r := range file {
		if r != '.' && r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return nil, fmt.Errorf("invalid PNG filename")
		}
	}
	if strings.Contains(file, "..") {
		return nil, fmt.Errorf("invalid PNG filename")
	}
	return assets.ReadFile("icons/" + file)
}
