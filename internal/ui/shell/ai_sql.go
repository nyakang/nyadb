package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/aisql"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) SetAISQL(store *aisql.Store, client aisql.Client) {
	s.sqlWorkspace.SetAI(store, client)
}

func (s *State) aiMenuCommands(m *ui.Menu) {
	current, session := s.currentTab(), s.sessions.Current()
	scope := documentMenuScope{tab: current.id, profile: session.ProfileID, generation: session.Generation}
	selected := ""
	if m.Item(locales.Text("AI 服务设置…")).Disabled(!s.sqlWorkspace.AIAvailable()).Chosen() {
		selected = "settings"
	}
	if m.Item(locales.Text("AI SQL 助手…")).Disabled(!s.sqlWorkspace.AIAvailable() || current.query == nil || current.query.Loading()).Chosen() {
		selected = "assistant"
	}
	if s.documentMenus == nil {
		s.documentMenus = map[string]documentMenuScope{}
	}
	if selected == "" {
		s.documentMenus["ai"] = scope
		return
	}
	original, found := s.documentMenus["ai"]
	delete(s.documentMenus, "ai")
	if !found || original.tab != scope.tab || original.profile != scope.profile || original.generation != scope.generation {
		return
	}
	if selected == "settings" {
		s.sqlWorkspace.OpenAISettings()
	} else {
		s.sqlWorkspace.OpenAI(current.query)
	}
}
func (s *State) openAIProposal(profile int64, database, text string) {
	s.nextTabID++
	d := s.sqlWorkspace.RestoreDocument(queries.Document{ID: s.nextTabID, ProfileID: profile, Database: database, Text: text})
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabSQL, title: d.Name(), query: d})
	s.selectTab(s.nextTabID)
}
