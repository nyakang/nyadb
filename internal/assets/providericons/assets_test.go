package providericons

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	_ "image/png"
	"testing"
)

func TestProviderIconProvenanceAndPNG(t *testing.T) {
	m, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 15 {
		t.Fatalf("expected 15 provider badges, got %d", len(m.Entries))
	}
	seen := map[string]bool{}
	for _, e := range m.Entries {
		if e.Name == "" || e.Source == "" || e.Artwork == "" || e.SHA256 == "" || seen[e.Name] {
			t.Fatalf("incomplete or duplicate provider entry: %+v", e)
		}
		seen[e.Name] = true
		b, err := Read(e.File)
		if err != nil {
			t.Fatal(err)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil || format != "png" || cfg.Width != 64 || cfg.Height != 64 {
			t.Fatalf("bad provider PNG %s: %v", e.File, err)
		}
		hash := sha256.Sum256(b)
		if hex.EncodeToString(hash[:]) != e.SHA256 {
			t.Fatalf("manifest checksum mismatch: %s", e.File)
		}
	}
}
func TestProviderPathsRemainRestricted(t *testing.T) {
	for _, bad := range []string{"../manifest.json", "hello.svg", "A.png", "vendor/p.png", "..png"} {
		if _, err := Read(bad); err == nil {
			t.Errorf("invalid icon path %q accepted", bad)
		}
	}
}
