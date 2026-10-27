package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Shared metrics for the MySQL connection editor. The 604-DIP centered form,
// 166-DIP label column and 31-DIP row rhythm come from the observed original
// 900×700 client area. Keep the native widgets; don't move them with offsets.
const (
	connectionFormMaxWidth        float32 = 604
	connectionFormSideInset       float32 = 44
	connectionFormLabelWidth      float32 = 166
	connectionFormRowHeight       float32 = 31
	connectionFormInputHeight     float32 = 26
	connectionFormBrowseSize      float32 = 22
	connectionFormBrowseGap       float32 = 7
	connectionFooterControlHeight float32 = 24
)

func connectionFormWidth(clientWidth float32) float32 {
	return min(connectionFormMaxWidth, clientWidth-connectionFormSideInset)
}

// Runtime observation on 2026-10-25 confirms the security/tunnel pages use
// the full window with 20-DIP insets, unlike the centered General/Advanced.
func connectionWideFormWidth(clientWidth float32) float32 {
	return clientWidth - 40
}

func connectionFormRow(c *ui.Context) ui.Element {
	return ui.Row(c).Height(theme.RowHeight(c, connectionFormRowHeight)).Gap(0).AlignItems(ui.Center)
}

func connectionFormTextInput(c *ui.Context, target *string, name string, width float32, disabled, secret bool) ui.Element {
	field := ui.TextInput(c, target).Label(locales.Text(name)).Width(width).
		Height(theme.RowHeight(c, connectionFormInputHeight)).Padding(2, 8).Disabled(disabled)
	if secret {
		field.Password()
	}
	return field
}

func connectionFormSelect(c *ui.Context, value *string, options []string, name string, width float32, disabled bool) ui.Element {
	return ui.Select(c, value, options).Label(locales.Text(name)).
		Width(width).Height(theme.RowHeight(c, connectionFormInputHeight)).Disabled(disabled)
}

func connectionFormBrowse(c *ui.Context, label string, disabled bool, action func()) {
	if ui.Button(c, "…").Label(locales.Text(label)).
		Width(connectionFormBrowseSize).Height(connectionFormBrowseSize).Padding(0, 0).
		Disabled(disabled || action == nil).Clicked() && action != nil {
		action()
	}
}
