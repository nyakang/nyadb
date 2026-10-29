package connectioneditor

import (
	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
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

const connectionCheckboxSize float32 = 14

// Keep MyGo's native text editing, selection, IME, popup keyboard handling,
// disabled semantics and focus border. Only its window-local theme is changed.
func applyConnectionEditorFormTheme(c *ui.Context) {
	theme.Apply(c)
	c.SetTheme(theme.InputTheme(c))
}

// connectionFormCheckbox uses MyGo CheckboxBase: pointer/keyboard interaction,
// focus, checked state and accessibility are all native. Only its look differs
// from the workbench default (dark box, pale tick rather than blue fill).
func connectionFormCheckbox(c *ui.Context, checked *bool, label string, disabled bool) ui.Element {
	return connectionFormCheckboxSized(c, checked, label, disabled, 12)
}

func connectionFormCheckboxCompact(c *ui.Context, checked *bool, label string, disabled bool) ui.Element {
	return connectionFormCheckboxSized(c, checked, label, disabled, 11)
}

func connectionFormCheckboxSized(c *ui.Context, checked *bool, label string, disabled bool, size float32) ui.Element {
	palette := theme.Colors(c)

	row := ui.CheckboxBase(c, checked).Label(locales.Text(label)).
		Height(theme.RowHeight(c, connectionFormInputHeight)).Gap(5).AlignItems(ui.Center).
		FocusRing(false).Disabled(disabled)
	row.Children(func() {
		fill := palette.Panel
		border := palette.Border
		tick := palette.Text
		ink := palette.Text
		if disabled {
			fill = palette.App
			border = palette.InputBorder
			tick = palette.Muted
			// MyGo dims disabled subtrees; avoid double-dimming the label.
			ink = palette.Text
		} else if row.Hovered() {
			border = palette.Muted
		}
		ui.Box(c).Size(connectionCheckboxSize, connectionCheckboxSize).
			Radius(1).Background(fill).Border(1, border).
			DrawOver(func(p *ui.Painter, r ui.Rect) {
				if *checked {
					var mark ui.Path
					mark.MoveTo(r.X+r.W*0.22, r.Y+r.H*0.52).
						LineTo(r.X+r.W*0.42, r.Y+r.H*0.72).
						LineTo(r.X+r.W*0.79, r.Y+r.H*0.29)
					p.StrokePath(&mark, 1.6, tick)
				}
				if row.FocusVisible() {
					p.Stroke(ui.Rect{X: r.X - 2, Y: r.Y - 2, W: r.W + 4, H: r.H + 4},
						palette.Focus, 3, 1)
				}
			})
		ui.Text(c, locales.Text(label)).FontSize(theme.Font(c, size)).TextColor(ink)
	})
	return row
}

// SetMySQLSettingsPicker is invoked by the native browse button.
// The app host is responsible for showing a directory chooser.
func (s *State) SetMySQLSettingsPicker(pick func()) {
	s.mysqlSettingsPicker = pick
}

func (s *State) SelectMySQLSettingsDirectory(path string) {
	if !s.active || s.connectionDraft.Kind != connections.MySQL {
		return
	}
	s.connectionDraft.MySQLAdvanced.SettingsLocation = path
	s.connectionError = ""
}

func (s *State) MySQLSettingsPickerError() {
	if s.active && s.connectionDraft.Kind == connections.MySQL {
		s.connectionError = "选择设置位置失败"
	}
}

// SetSQLiteFilePicker registers a native file-picker command owned by bootstrap.
// The UI does not import the application's native-window backend.
func (s *State) SetSQLiteFilePicker(pick func()) {
	s.sqliteFilePicker = pick
}

// SelectSQLiteFile must be called through the window's main-thread Update.
func (s *State) SelectSQLiteFile(path string) {
	if s.active && s.connectionDraft.Kind == connections.SQLite && path != "" {
		s.connectionDraft.Database = path
		s.connectionError = ""
	}
}

func (s *State) SQLiteFilePickerError() {
	if s.active {
		s.connectionError = "无法打开数据库文件选择器，请手动输入文件路径"
	}
}
