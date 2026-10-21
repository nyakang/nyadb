package shell

import "github.com/egoist/mygo/ui"

// Shared metrics for the MySQL connection editor. The 604-DIP centered form,
// 166-DIP label column and 31-DIP row rhythm come from the observed original
// 900×700 client area. Keep the native widgets; don't move them with offsets.
const (
	connectionFormMaxWidth    float32 = 604
	connectionFormSideInset   float32 = 44
	connectionFormLabelWidth  float32 = 166
	connectionFormRowHeight   float32 = 31
	connectionFormInputHeight float32 = 26
	connectionFormBrowseSize  float32 = 22
	connectionFormBrowseGap   float32 = 7
)

func connectionFormWidth(clientWidth float32) float32 {
	return min(connectionFormMaxWidth, clientWidth-connectionFormSideInset)
}

func connectionFormRow(c *ui.Context) ui.Element {
	return ui.Row(c).Height(connectionFormRowHeight).Gap(0).AlignItems(ui.Center)
}

func connectionFormTextInput(c *ui.Context, target *string, name string, width float32, disabled, secret bool) ui.Element {
	field := ui.TextInput(c, target).Label(name).Width(width).
		Height(connectionFormInputHeight).Padding(2, 8).Disabled(disabled)
	if secret {
		field.Password()
	}
	return field
}

func connectionFormSelect(c *ui.Context, value *string, options []string, name string, width float32, disabled bool) ui.Element {
	return ui.Select(c, value, options).Label(name).
		Width(width).Height(connectionFormInputHeight).Disabled(disabled)
}

func connectionFormBrowse(c *ui.Context, label string, disabled bool, action func()) {
	if ui.Button(c, "…").Label(label).
		Width(connectionFormBrowseSize).Height(connectionFormBrowseSize).Padding(0, 0).
		Disabled(disabled || action == nil).Clicked() && action != nil {
		action()
	}
}
