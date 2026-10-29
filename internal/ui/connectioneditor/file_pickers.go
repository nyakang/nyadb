package connectioneditor

import (
	"github.com/nyakang/nyadb/internal/services/connections"
)

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
