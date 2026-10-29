package shell

import (
	"github.com/nyakang/nyadb/internal/services/connections"
)

// SetSQLiteFilePicker registers a native file-picker command owned by bootstrap.
// The UI does not import the application's native-window backend.
func (s *State) SetSQLiteFilePicker(pick func()) {
	s.sqliteFilePicker = pick
}

// SelectSQLiteFile must be called through the window's main-thread Update.
func (s *State) SelectSQLiteFile(path string) {
	if s.connectionDialog && s.connectionStep == 1 &&
		s.connectionDraft.Kind == connections.SQLite && path != "" {
		s.connectionDraft.Database = path
		s.connectionError = ""
	}
}

func (s *State) SQLiteFilePickerError() {
	if s.connectionDialog {
		s.connectionError = "无法打开数据库文件选择器，请手动输入文件路径"
	}
}
