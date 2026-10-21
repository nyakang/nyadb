package shell

import "github.com/nyakang/nyadb/internal/services/connections"

// SetMySQLSettingsPicker is invoked by the native browse button.
// The app host is responsible for showing a directory chooser.
func (s *State) SetMySQLSettingsPicker(pick func()) {
	s.mysqlSettingsPicker = pick
}

func (s *State) SelectMySQLSettingsDirectory(path string) {
	if !s.connectionDialog || s.connectionStep != 1 || s.connectionDraft.Kind != connections.MySQL {
		return
	}
	s.connectionDraft.MySQLAdvanced.SettingsLocation = path
	s.connectionError = ""
}

func (s *State) MySQLSettingsPickerError() {
	if s.connectionDialog && s.connectionStep == 1 && s.connectionDraft.Kind == connections.MySQL {
		s.connectionError = "选择设置位置失败"
	}
}
