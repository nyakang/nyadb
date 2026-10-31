package shell

import (
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/engines"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
	"github.com/nyakang/nyadb/locales"
	"io"
)

func (s *State) initEngines() {
	s.engines = engines.NewManager()
	s.sessions.SetEngineRuntime(sessionbrowser.EngineRuntime{Connect: s.engines.Connect, Session: s.engineSession, Disconnect: s.engines.Disconnect, Close: s.engines.Close})
	s.engineData = dataworkspace.NewEngineState(dataworkspace.EngineDependencies{Session: s.engineSession, Active: func(d *dataworkspace.EngineDocument) bool { return s.currentTab().engine == d }, Status: func(message string) { s.status = message }})
}
func (s *State) engineSession(id int64) (contract.Session, error) { return s.engines.Session(id) }
func (s *State) sqlRuntime() sqlworkspace.Runtime {
	return sqlworkspace.CompositeRuntime(sqlworkspace.MySQLRuntime(s.sessions.Manager), sqlworkspace.EngineRuntime(s.engineSession))
}
func (s *State) SetEngineFiles(pick func(string) (string, error), write func(string, func(io.Writer) error) error) {
	s.engineData.SetFiles(pick, write)
}
func (s *State) engineSessionToolbar(c *ui.Context) bool {
	p, ok := s.currentProfile()
	if !ok || p.Kind == connections.MySQL {
		return false
	}
	status := s.sessions.StatusFor(p.ID)
	ui.Column(c).Gap(5).Children(func() {
		if p.Kind == connections.PostgreSQL && status != "已连接" {
			ui.TextInput(c, &s.sessions.Current().Password).Password().Label(locales.Text("PostgreSQL 会话密码"))
		}
		ui.Row(c).Gap(5).Children(func() {
			if status == "连接中" || status == "已连接" {
				if ui.Button(c, locales.Text("断开连接")).Clicked() {
					s.disconnectMySQLProfile(p.ID)
				}
			} else {
				if ui.Button(c, locales.Text("连接")).Label(locales.Text("连接所选数据库")).Clicked() {
					if !s.guardMySQLProfileLeave(p.ID, func(next ui.Services) { s.sessions.Connect(p, next.Invalidate) }) {
						s.sessions.Connect(p, c.Services().Invalidate)
					}
				}
			}
		})
		if w := s.sessions.Workspace(p.ID); w != nil && w.Error != "" {
			ui.Text(c, w.Error)
		}
	})
	return true
}

// openEngineData uses the selected profile's fixed catalog and real schema.
func (s *State) openEngineData(services ui.Services, schema, name string) bool {
	w := s.sessions.Current()
	if w.Kind == "" || w.Kind == connections.MySQL {
		return false
	}
	session, e := s.engineSession(w.ProfileID)
	if e != nil {
		s.status = e.Error()
		return true
	}
	kind := "table"
	if s.objectKindFor(w.ProfileID, schema, name) == "视图" {
		kind = "view"
	}
	o := contract.ObjectRef{ProfileID: w.ProfileID, Catalog: session.Catalog(), Schema: schema, Kind: kind, Name: name}
	for i, t := range s.tabs {
		if t.engine != nil && t.engine.Object == o {
			s.activeTab = i
			return true
		}
	}
	s.nextTabID++
	d := s.engineData.Open(s.nextTabID, o)
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabEngineData, title: schema + "." + name + " · 数据", engine: d})
	s.activeTab = len(s.tabs) - 1
	s.engineData.SetInvalidate(services.Invalidate)
	s.engineData.Load(d)
	return true
}
func (s *State) engineObjectCapabilities() contract.Capabilities {
	w := s.sessions.Current()
	if w.Kind == connections.MySQL || w.Kind == "" {
		return contract.Capabilities{DesignTables: true, Import: true, Backup: true}
	}
	session, e := s.engineSession(w.ProfileID)
	if e != nil {
		return contract.Capabilities{}
	}
	return session.Capabilities()
}

// Explicit recovery of a SQL document still routes through sqlworkspace guards.
func (s *State) connectEngineProfile(ctx context.Context, p connections.Profile, password string) error {
	return s.engines.Connect(ctx, p, password)
}

func (s *State) openEngineStructure(services ui.Services, schema, name string) bool {
	if !s.openEngineData(services, schema, name) {
		return false
	}
	if d := s.currentTab().engine; d != nil {
		d.StructureView = true
	}
	return true
}

func (s *State) objectCapabilitiesFor(id int64) contract.Capabilities {
	if w := s.sessions.Workspace(id); w != nil && w.Kind != "" && w.Kind != connections.MySQL {
		session, e := s.engineSession(id)
		if e != nil {
			return contract.Capabilities{}
		}
		return session.Capabilities()
	}
	return contract.Capabilities{DesignTables: true, Import: true, Backup: true, EditableTables: true}
}

func (s *State) sqlStatementAtCaret(id int64, source string, cursor int) string {
	for _, p := range s.navigation.Profiles() {
		if p.ID == id && p.Kind != connections.MySQL {
			return sqltext.PortableStatementAt(source, cursor)
		}
	}
	return ""
}

func (s *State) sqlFormat(id int64, source string) (string, error) {
	for _, p := range s.navigation.Profiles() {
		if p.ID == id && p.Kind != connections.MySQL {
			return sqltext.FormatPortable(source)
		}
	}
	return sqltext.Format(source)
}

func (s *State) sqlValidateExplain(id int64, source string) error {
	for _, p := range s.navigation.Profiles() {
		if p.ID == id && p.Kind != connections.MySQL {
			return sqltext.SinglePortableStatement(source)
		}
	}
	return sqltext.SingleMySQLStatement(source)
}
