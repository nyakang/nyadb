package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

type TableProfile struct{ Name, Value string }
type tableProfileMenu struct {
	profile         int64
	generation      uint64
	database, table string
	choices         []TableProfile
	browser         *browserState
}

func (s *State) tableProfilesMenu(services ui.Services, m *ui.Menu, profile int64, database, table string) {
	selected := ""
	var choices []TableProfile
	var err error
	if s.deps.TableProfiles != nil {
		choices, err = s.deps.TableProfiles(profile, database, table)
	}
	if err != nil {
		m.Item(locales.Text("读取表配置文件失败")).Disabled(true)
		return
	}
	if len(choices) == 0 {
		m.Item(locales.Text("无配置文件")).Disabled(true)
	}
	for _, p := range choices {
		if m.Item(p.Name).Chosen() {
			selected = p.Name
		}
	}
	session := s.sessionFor(profile)
	if selected == "" {
		s.profileMenu = &tableProfileMenu{profile, session.Generation, database, table, append([]TableProfile(nil), choices...), s.currentBrowser().browserState}
		return
	}
	original := s.profileMenu
	s.profileMenu = nil
	if original == nil || original.profile != profile || original.generation != session.Generation || original.database != database || original.table != table || !session.Connected || s.session().ProfileID != profile || s.deps.OpenTableProfile == nil {
		return
	}
	if browser := s.currentBrowser(); browser.ProfileID != profile || browser.Database != database || browser.browserState != original.browser {
		return
	}
	for _, p := range original.choices {
		if p.Name == selected {
			s.deps.OpenTableProfile(services, profile, database, table, p.Value)
			return
		}
	}
}
