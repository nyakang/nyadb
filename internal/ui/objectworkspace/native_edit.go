package objectworkspace

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
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
