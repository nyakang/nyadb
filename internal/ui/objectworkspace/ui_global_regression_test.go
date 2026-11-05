package objectworkspace

import (
	"image/png"
	"os"
	"testing"

	"github.com/nyakang/nyadb/internal/db/mysqlddl"
)

func TestDesignNativeTabsKeepInvalidPrefix(t *testing.T) {
	s, d, _, tt := designTestState(t)
	s.changeDesignPage(d, 1)
	s.addDesignClause(d, mysqlddl.Index)
	tt.Frame()
	f := d.design.constraint
	f.columnRow = 0
	tt.Frame()
	if output := os.Getenv("NYADB_UI_OPT_CAPTURE"); output != "" {
		file, e := os.Create(output)
		if e != nil {
			t.Fatal(e)
		}
		png.Encode(file, tt.Image())
		file.Close()
	}
	if err := tt.Click("索引前缀长度"); err != nil {
		t.Fatal(err)
	}
	if !tt.Focused("索引前缀长度") {
		t.Fatal("prefix input never received focus")
	}
	f.prefix = "invalid"
	if err := tt.Click("外键"); err != nil {
		t.Fatal(err)
	}
	if d.design.page != 1 || f.prefix != "invalid" || !tt.Focused("索引前缀长度") {
		for _, label := range tt.Texts() {
			if tt.Focused(label) {
				t.Logf("focused: %s", label)
			}
		}
		t.Fatalf("invalid prefix guard lost state/focus: page=%d prefix=%q focus=%v pending=%v", d.design.page, f.prefix, tt.Focused("索引前缀长度"), d.formFocusPending)
	}
}
