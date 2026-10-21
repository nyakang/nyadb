package connectionicons

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	_ "image/png"
	"strings"
	"testing"
)

func TestCommittedCatalogIconsAndProvenance(t *testing.T) {
	m, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 20 {
		t.Fatalf("expected 20 committed catalog icons, got %d", len(m.Entries))
	}
	seen := make(map[string]bool)
	for _, entry := range m.Entries {
		if entry.Name == "" || entry.File == "" || entry.Source == "" || entry.Artwork == "" {
			t.Fatalf("incomplete provenance: %+v", entry)
		}
		if strings.HasPrefix(entry.File, "OV_64_") || seen[entry.Name] {
			t.Fatalf("Navicat resource or duplicate name: %+v", entry)
		}
		seen[entry.Name] = true
		pngBytes, err := Read(entry.File)
		if err != nil {
			t.Fatal(err)
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(pngBytes))
		if err != nil || format != "png" || config.Width != 64 || config.Height != 64 {
			t.Fatalf("bad image %s: %dx%d %s %v", entry.File, config.Width, config.Height, format, err)
		}
		hash := sha256.Sum256(pngBytes)
		if got := hex.EncodeToString(hash[:]); got != entry.SHA256 {
			t.Fatalf("manifest checksum mismatch: %s %s", entry.File, got)
		}
	}
}

func TestRejectArbitraryEmbeddedPaths(t *testing.T) {
	for _, name := range []string{"../manifest.json", "internal.png/../foo.png", "OV_64_mysql.PNG", "foo.svg", "my name.png"} {
		if data, err := Read(name); err == nil {
			t.Fatalf("unexpected read for %q (%d bytes)", name, len(data))
		}
	}
}
