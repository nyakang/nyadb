package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

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
