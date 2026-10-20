package shell

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Regenerate the documented screenshot only on explicit request:
// NYADB_WRITE_PREVIEW=1 go test ./internal/ui/shell -run TestGenerateUIPreview -v
func TestGenerateUIPreview(t *testing.T) {
	if os.Getenv("NYADB_WRITE_PREVIEW") != "1" {
		t.Skip("set NYADB_WRITE_PREVIEW=1 to regenerate docs/ui-preview.png")
	}
	s := New()
	tt := ui.NewTester(s.View, 1552, 966)
	filename := filepath.Join("..", "..", "..", "docs", "ui-preview.png")
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}
