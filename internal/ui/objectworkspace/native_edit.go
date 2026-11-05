package objectworkspace

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) designTextInput(c *ui.Context, d *Document, text *string) ui.Element {
	e := ui.TextInput(c, text)
	return s.bindDesignText(c, d, text, e)
}
func (s *State) designTextArea(c *ui.Context, d *Document, text *string) ui.Element {
	e := ui.TextArea(c, text)
	return s.bindDesignText(c, d, text, e)
}
func (s *State) bindDesignText(c *ui.Context, d *Document, text *string, e ui.Element) ui.Element {
	if e.Focused() {
		d.formFocusTarget = text
	}
	if d.formFocusTarget == text {
		e.Bind(&d.formFocusHandle)
		if d.formFocusPending {
			e.ScrollIntoView()
			d.formFocusHandle.Focus(c)
			if e.Focused() {
				d.formFocusPending = false
			}
		}
	}
	design, schema := d.design, d.schemaDesign
	page := -1
	if design != nil {
		page = design.page
	}
	if schema != nil {
		page = schema.page
	}
	return theme.BindNativeEdit(c, e, fmt.Sprintf("object/form/%d/%p", d.id, text), s.deps.EditFocus, s.deps.EditTarget, func() bool {
		if d.closed || design != d.design || schema != d.schemaDesign {
			return false
		}
		if design != nil {
			return design.page == page
		}
		return schema != nil && schema.page == page
	}, !d.DesignBusy(), false)
}

func (s *State) designFieldInput(c *ui.Context, d *Document, label string, text *string) ui.Element {
	var input ui.Element
	field := theme.Field(c, locales.Text(label), func() { input = s.designTextInput(c, d, text).Label(locales.Text(label)) }).MinWidth(0)
	if label == "索引前缀长度" && d.design != nil && d.design.formError != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(*text)); strings.TrimSpace(*text) != "" && (err != nil || n < 0 || n > 65535) {
			field.Error(locales.Text(d.design.formError))
		}
	}
	return input
}
