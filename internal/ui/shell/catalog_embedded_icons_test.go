package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestBundledIconCacheForFullCatalog(t *testing.T) {
	loaded, err := bundledCatalogIcons()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 20 {
		t.Fatalf("expected 20 bundled icons, got %d", len(loaded))
	}
	for _, item := range databaseCatalog[:20] {
		icon := loaded[item.name]
		if icon == nil {
			t.Errorf("missing embedded icon for %q", item.name)
			continue
		}
		if w, h := icon.Size(); w != 64 || h != 64 {
			t.Errorf("wrong dimensions for %q: %d x %d", item.name, w, h)
		}
	}
	s := New()
	if len(s.bundledIcons) != 20 {
		t.Fatal("default app state must load bundled catalog images")
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
}
