package connectioneditor

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSharedMySQLFormGeometry(t *testing.T) {
	s := newTestEditor()
	// Test the real child-window geometry, not the compact in-tree fallback.
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)

	for _, page := range []struct {
		tab    string
		fields []string
	}{
		{"常规", []string{"主机", "端口", "用户名", "密码"}},
		{"高级", []string{"设置位置", "客户端驱动程序", "客户端字符集"}},
		{"SSL", []string{"SSL 客户端密钥：", "SSL 客户端证书：", "SSL CA 证书："}},
		{"SSH", []string{"SSH 端口"}},
		{"HTTP", []string{"HTTP 隧道网址："}},
	} {
		if err := tt.Click(page.tab); err != nil {
			t.Fatal(err)
		}
		previousBottom := float32(-1)
		left := float32(-1)
		for _, name := range page.fields {
			rect, ok := tt.Find(name)
			if !ok || rect.W <= 0 || rect.H < connectionFormInputHeight || rect.X < 0 || (rect.X+rect.W) > 900 {
				t.Fatalf("invalid native field %q: %+v", name, rect)
			}
			if previousBottom > rect.Y {
				t.Fatalf("form fields overlap: %s", name)
			}
			previousBottom = (rect.Y + rect.H)
			if left >= 0 && math.Abs(float64(left-rect.X)) > 1.1 {
				t.Fatalf("form controls do not share a measured label column: %s", name)
			}
			left = rect.X
		}
	}

}

// The refined 14-DIP glyph must keep CheckboxBase input semantics, not just
// resemble the original screenshot. The disabled setting must not toggle.
func TestConnectionFormCheckboxNativeInput(t *testing.T) {
	checked, disabled := false, false
	view := func(c *ui.Context) {
		applyConnectionEditorFormTheme(c)
		ui.Column(c).Gap(0).Children(func() {
			connectionFormCheckbox(c, &checked, "可用复选框", false)
			connectionFormCheckbox(c, &disabled, "禁用复选框", true)
		})
	}
	tt := ui.NewTester(view, 300, 90)
	rect, ok := tt.Find("可用复选框")
	if !ok || rect.H != connectionFormInputHeight {
		t.Fatalf("checkbox is not on shared form grid: %+v (%v)", rect, ok)
	}
	// Check the rasterized mark as well as its interaction: rendering a
	// diagonal with Painter.Line regressed into a single short pixel.
	lightMarks := func() int {
		count := 0
		y0 := int(rect.Y + (rect.H-connectionCheckboxSize)/2)
		for y := y0 + 2; y < y0+int(connectionCheckboxSize)-2; y++ {
			for x := int(rect.X) + 2; x < int(rect.X+connectionCheckboxSize)-2; x++ {
				p := tt.Image().RGBAAt(x, y)
				if p.R > 155 && p.G > 155 && p.B > 155 {
					count++
				}
			}
		}
		return count
	}
	before := lightMarks()
	if err := tt.Click("可用复选框"); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("click did not toggle CheckboxBase")
	}
	if lightMarks() <= before {
		t.Fatal("checked state did not draw the full pale checkmark")
	}
	tt.Key(0, ui.KeySpace)
	if checked {
		t.Fatal("focused native checkbox did not handle Space")
	}
	_ = tt.Click("禁用复选框")
	if disabled {
		t.Fatal("disabled checkbox toggled on click")
	}
	if !tt.HasText("可用复选框") || !tt.HasText("禁用复选框") {
		t.Fatal("checkbox descriptions are no longer accessible")
	}
}

func TestConnectionFormNativeInputFocusAndDisabled(t *testing.T) {
	enabled, disabled := "localhost", "secret-test-value"
	view := func(c *ui.Context) {
		applyConnectionEditorFormTheme(c)
		ui.Column(c).Gap(0).Children(func() {
			connectionFormTextInput(c, &enabled, "可编辑输入", 200, false, false)
			connectionFormTextInput(c, &disabled, "禁用输入", 200, true, false)
		})
	}
	tt := ui.NewTester(view, 310, 110)
	if err := tt.Click("可编辑输入"); err != nil {
		t.Fatal(err)
	}
	field, ok := tt.Find("可编辑输入")
	caret, active := tt.TextCaret()
	if !ok || !active || caret.Y < field.Y || caret.Y+caret.H > field.Y+field.H {
		t.Fatalf("native caret not vertically within input: field=%+v caret=%+v active=%v", field, caret, active)
	}
	tt.Type("x")
	if enabled == "localhost" {
		t.Fatal("input lost native text editing")
	}
	_ = tt.Click("禁用输入")
	tt.Type("should-not-appear")
	if disabled != "secret-test-value" {
		t.Fatal("disabled field became editable")
	}
}

func TestMySQLEditorSharedChromeAndTabNavigation(t *testing.T) {
	s := newTestEditor()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)

	// Every tab must retain the same shared chrome and bottom actions.
	for _, tab := range []string{"常规", "高级", "数据库", "SSL", "SSH", "HTTP", "备注"} {
		if err := tt.Click(tab); err != nil {
			t.Fatalf("navigate to %s: %v", tab, err)
		}
		if !tt.HasText("URI") || !tt.HasText("测试连接") ||
			!tt.HasText("上一步") || !tt.HasText("确定") || !tt.HasText("取消") ||
			!tt.HasText("NyaDB") || !tt.HasText("数据库") {
			t.Fatalf("common chrome disappeared on %s", tab)
		}
	}
	// The tunnel endpoint belongs to the shared diagram, not a particular
	// form. It must remain present while switching between other tabs.
	if err := tt.Click("HTTP"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("使用 HTTP 隧道"); err != nil {
		t.Fatal(err)
	}
	for _, tab := range []string{"常规", "高级", "备注", "SSL"} {
		if err := tt.Click(tab); err != nil {
			t.Fatal(err)
		}
		if !tt.HasText("HTTP 服务器") {
			t.Fatalf("shared tunnel diagram lost its HTTP node on %s", tab)
		}
	}
}
