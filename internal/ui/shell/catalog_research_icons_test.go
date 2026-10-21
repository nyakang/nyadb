package shell

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func testResearchPNG(t *testing.T, dir, name string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestResearchCatalogIconLoadingAndFallback(t *testing.T) {
	s := New()
	dir := t.TempDir()
	loaded, err := s.LoadLocalResearchIcons(dir)
	if err != nil || loaded != 0 || len(s.researchIcons) != 0 {
		t.Fatalf("missing research icons should fall back: %d %v", loaded, err)
	}
	testResearchPNG(t, dir, researchIconResources["MySQL"], 64, 64)
	testResearchPNG(t, dir, researchIconResources["SQLite"], 64, 64)
	testResearchPNG(t, dir, researchIconResources["Azure SQL"], 64, 64)
	loaded, err = s.LoadLocalResearchIcons(dir)
	if err != nil || loaded != 3 {
		t.Fatalf("load expected images: %d %v", loaded, err)
	}
	if w, h := s.researchIcons["MySQL"].Size(); w != 64 || h != 64 {
		t.Fatalf("wrong image dimensions: %dx%d", w, h)
	}
	if s.researchIcons["PostgreSQL"] != nil {
		t.Fatal("missing asset must fall back, not fabricate an image")
	}
	if s.researchIcons["Azure SQL"] == nil {
		t.Fatal("cloud catalog resource was not loaded by its exact name")
	}
	tt := ui.NewTester(s.View, 1280, 800)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("选择 MySQL"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.Kind != "mysql" {
		t.Fatal("local research images must not affect the connection workflow")
	}
	loaded, err = s.LoadLocalResearchIcons("")
	if err != nil || loaded != 0 || s.researchIcons != nil {
		t.Fatal("disabling local research icons must clear the previous images")
	}
}

func TestResearchCatalogIconsRejectInvalidFilesIndividually(t *testing.T) {
	s := New()
	dir := t.TempDir()
	testResearchPNG(t, dir, researchIconResources["PostgreSQL"], 64, 64)
	testResearchPNG(t, dir, researchIconResources["MySQL"], 16, 16)
	if err := os.WriteFile(filepath.Join(dir, researchIconResources["SQLite"]), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadLocalResearchIcons(dir)
	if loaded != 1 || err == nil || s.researchIcons["PostgreSQL"] == nil {
		t.Fatalf("bad assets must not block good assets: count=%d err=%v", loaded, err)
	}
	testResearchPNG(t, dir, researchIconResources["MySQL"], 64, 64)
	if err := os.WriteFile(filepath.Join(dir, researchIconResources["Redis"]), bytes.Repeat([]byte{'x'}, researchIconMaxBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.LoadLocalResearchIcons(dir)
	if loaded != 2 || err == nil {
		t.Fatalf("oversized image must be rejected, other PNGs preserved: count=%d err=%v", loaded, err)
	}
}

func TestResearchIconResourceMapCoverage(t *testing.T) {
	if len(researchIconResources) != 20 {
		t.Fatalf("unexpected research engine mapping count: %d", len(researchIconResources))
	}
	for _, entry := range databaseCatalog[:20] {
		if researchIconResources[entry.name] == "" {
			t.Errorf("missing mapped catalog item: %s", entry.name)
		}
	}
}
