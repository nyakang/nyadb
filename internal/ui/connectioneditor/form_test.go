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

	near := func(a, b float32) bool { return math.Abs(float64(a-b)) <= 1.1 }
	find := func(label string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("missing form field %q", label)
		}
		return r
	}
	general := []string{"主机", "端口", "用户名", "密码"}
	var prev ui.Rect
	for i, name := range general {
		rect := find(name)
		if !near(rect.X, 314) || !near(rect.H, connectionFormInputHeight) {
			t.Fatalf("general %s geometry: %+v", name, rect)
		}
		if i > 0 && !near(rect.Y-prev.Y, connectionFormRowHeight) {
			t.Fatalf("general row %q no longer uses %g-DIP rhythm: %v vs %v", name, connectionFormRowHeight, rect, prev)
		}
		prev = rect
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	names := []string{"设置位置", "客户端驱动程序", "客户端字符集"}
	for i, name := range names {
		rect := find(name)
		if !near(rect.X, 314) || !near(rect.H, connectionFormInputHeight) {
			t.Fatalf("advanced %s geometry: %+v", name, rect)
		}
		if i > 0 && !near(rect.Y-prev.Y, connectionFormRowHeight) {
			t.Fatalf("advanced row %q off-grid: %+v vs %+v", name, rect, prev)
		}
		prev = rect
	}
	if err := tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SSL 客户端密钥：", "SSL 客户端证书：", "SSL CA 证书："} {
		rect := find(name)
		if !near(rect.X, 186) || !near(rect.H, connectionFormInputHeight) {
			t.Fatalf("SSL field %s does not share label column: %+v", name, rect)
		}
		if !near(rect.W, 665) {
			t.Fatalf("SSL paths should extend to the right-side browse buttons: %+v", rect)
		}
	}
	if err := tt.Click("SSH"); err != nil {
		t.Fatal(err)
	}
	if r := find("SSH 端口"); !near(r.X, 186) || !near(r.H, connectionFormInputHeight) {
		t.Fatalf("SSH port is off-grid: %+v", r)
	}
	if err := tt.Click("HTTP"); err != nil {
		t.Fatal(err)
	}
	if r := find("HTTP 隧道网址："); !near(r.X, 186) || !near(r.W, 694) || !near(r.H, connectionFormInputHeight) {
		t.Fatalf("HTTP URL is off-grid: %+v", r)
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
