package shell

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestProgramDefinitionTabIsReadOnlyAndScopedToProfile(t *testing.T) {
	s := New()
	s.sessions.Current().ProfileID = 41
	s.sessions.Current().Status = "已连接"
	s.sessions.Current().Cache = map[string][]mysqlsvc.MySQLDBObject{
		"app": {{Name: "run_cleanup", Kind: "存储过程", Definition: "DELETE FROM logs"}},
	}
	s.openMySQLProgram("app", "run_cleanup", "存储过程")
	if len(s.tabs) != 2 || s.currentTab().kind != tabProgram || s.currentTab().program.ProfileID() != 41 ||
		s.currentTab().program.Data().Definition != "DELETE FROM logs" {
		t.Fatalf("program tab did not retain metadata and session identity: %+v", s.currentTab())
	}
	s.openMySQLProgram("app", "run_cleanup", "存储过程")
	if len(s.tabs) != 2 {
		t.Fatal("same program opened duplicate tabs")
	}
	s.clearMySQLTabsFor(41)
	if len(s.tabs) != 1 || s.currentTab().kind != tabObjects {
		t.Fatal("disconnect must close only the scoped program definition tabs")
	}
}

func TestProgramObjectEntrypointsDoNotExecuteDefinitionsOrOpenData(t *testing.T) {
	for _, kind := range []string{"函数", "存储过程", "事件"} {
		for _, entry := range []string{"double-click", "enter", "toolbar", "context-menu", "navigator"} {
			t.Run(kind+"/"+entry, func(t *testing.T) {
				s, tt := structureTestBrowser(t)
				object := mysqlsvc.MySQLDBObject{Name: "mutating_body", Kind: kind, Definition: "DELETE FROM logs", Definer: "metadata@server"}
				s.sessions.Current().Cache["app_db"] = []mysqlsvc.MySQLDBObject{object}
				s.sessions.Current().Kind, s.sessions.Current().Row = "全部", 0
				var executions, dataReads, structureReads atomic.Int32
				s.sqlWorkspace.Executor = func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error) {
					executions.Add(1)
					return mysqlsvc.MySQLSQLResult{}, nil
				}
				s.mysqlDataLoader = func(context.Context, int64, string, string, int64, int) (*mysqlsvc.MySQLDataPage, error) {
					dataReads.Add(1)
					return nil, nil
				}
				s.structureViewer.Loader = func(context.Context, int64, string, string) (*mysqlsvc.MySQLTableStructure, error) {
					structureReads.Add(1)
					return nil, nil
				}
				tt.Frame()
				// Replacing fixture metadata invalidates the old keyed table row.
				s.sessions.Current().Row = 0
				tt.Frame()
				switch entry {
				case "double-click":
					cell, ok := tt.Find(object.Name)
					if !ok {
						t.Fatal("program missing from object grid")
					}
					// Selection reveals another same-named label in the navigator;
					// the second click must stay on the original central grid cell.
					tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
					tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
				case "enter":
					if err := tt.Click(object.Name); err != nil {
						t.Fatal(err)
					}
					tt.Key(0, ui.KeyEnter)
				case "toolbar":
					if err := tt.Click("查看程序定义"); err != nil {
						t.Fatal(err)
					}
				case "context-menu":
					if err := tt.RightClick(object.Name); err != nil {
						t.Fatal(err)
					}
					if err := tt.ChooseMenuItem("查看定义（只读）"); err != nil {
						t.Fatal(err)
					}
				case "navigator":
					node := navigation.Node{Kind: "object", ProfileID: s.sessions.Current().ProfileID, Database: "app_db", Category: kind, Object: object.Name}
					s.revealNavigationNode(node)
					// The same text is present in the central grid; target the actual tree row.
					tt.Frame()
					row, ok := tt.Find(object.Name)
					if !ok || row.X >= 250 {
						t.Fatal("program missing from navigator")
					}
					tt.RightClickAt(row.X+row.W/2, row.Y+row.H/2)
					if err := tt.ChooseMenuItem("查看定义（只读）"); err != nil {
						t.Fatal(err)
					}
				}
				tt.Frame()
				if len(s.tabs) != 2 || s.currentTab().kind != tabProgram || s.currentTab().program.Data().Definition != object.Definition || !tt.HasText("DELETE FROM logs") {
					t.Fatalf("%s did not open a read-only program document: kind=%v tabs=%d row=%d status=%s", entry, s.currentTab().kind, len(s.tabs), s.sessions.Current().Row, s.status)
				}
				tt.Key(ui.Ctrl, ui.KeyEnter)
				tt.Key(ui.Ctrl, ui.KeyD)
				tt.Key(0, ui.KeyDelete)
				tt.Frame()
				if executions.Load() != 0 || dataReads.Load() != 0 || structureReads.Load() != 0 || s.mysqlTableDialog || len(s.tabs) != 2 {
					t.Fatal("program entry or shortcut dispatched SQL, data paging or table editing")
				}
				s.CloseMySQLSessions()
			})
		}
	}
}

func TestProgramDocumentsStayScopedAcrossProfilesAndSchemaPruning(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	var profiles []connections.Profile
	for _, name := range []string{"Program server A", "Program server B"} {
		p, err := svc.Create(context.Background(), connections.Profile{Name: name, Kind: connections.MySQL, Host: "localhost", Port: 3306})
		if err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, p)
	}
	s.loadProfiles()
	s.sessions.Opener = func(context.Context, connections.ActiveSettings, string) error { return nil }
	s.sessions.DatabaseLister = func(context.Context, int64) ([]string, error) { return []string{"app", "analytics"}, nil }
	var documents []*tab
	for _, p := range profiles {
		s.selectProfileByName(p.Name)
		s.startMySQLSession(ui.Services{})
		waitMultiProfileSession(t, s, tt)
		for _, database := range []string{"app", "analytics"} {
			s.sessions.Current().Cache[database] = []mysqlsvc.MySQLDBObject{{Name: "same_name", Kind: "函数", Definition: fmt.Sprintf("SELECT '%s/%s'", p.Name, database)}, {Name: "same_name", Kind: "存储过程", Definition: "DELETE FROM logs"}}
			s.openMySQLProgram(database, "same_name", "函数")
			documents = append(documents, s.currentTab())
		}
	}
	if len(s.tabs) != 5 || documents[0].program == documents[2].program || documents[0].program == documents[1].program {
		t.Fatal("same-named objects shared a document across profile or database")
	}
	s.selectProfileByName(profiles[0].Name)
	if s.activeTab != 0 || s.sessions.Current().ProfileID != profiles[0].ID || s.sessions.StatusFor(profiles[1].ID) != "已连接" {
		t.Fatal("profile selection retained another profile's program view or disconnected it")
	}
	s.sessions.Current().Cache["app"][0].Definition = "SELECT refreshed_A"
	s.openMySQLProgram("app", "same_name", "函数")
	if s.currentTab() != documents[0] || len(s.tabs) != 5 || documents[0].program.Data().Definition != "SELECT refreshed_A" || documents[2].program.Data().Definition != "SELECT 'Program server B/app'" {
		t.Fatal("reopening failed to reuse/update only the matching program")
	}
	s.openMySQLProgram("app", "same_name", "存储过程")
	procedure := s.currentTab()
	if len(s.tabs) != 6 || procedure.program == documents[0].program {
		t.Fatal("same-named function and procedure shared a document")
	}
	s.activeTab = 3
	s.activateMySQLForCurrentTab()
	if s.sessions.Current().ProfileID != profiles[1].ID || s.selectedProfileID != profiles[1].ID {
		t.Fatal("activating a program tab did not restore its owning profile")
	}
	s.selectProfileByName(profiles[0].Name)
	s.openQuery()
	query := s.currentTab()
	query.query.Editor.Buffer.Set("SELECT preserved SQL")
	s.sessions.ReconcileDatabases([]string{"app"})
	if s.currentTab() != query || query.query.Editor.Buffer.String() != "SELECT preserved SQL" || documents[1].program.Data().Definition != "" || documents[3].program.Data().Definition == "" || documents[0].program.Data().Definition != "SELECT refreshed_A" {
		t.Fatal("schema pruning affected another profile, active SQL or surviving metadata")
	}
	s.disconnectMySQLProfile(profiles[1].ID)
	if s.currentTab() != query || documents[2].program.Data().Definition != "" || documents[3].program.Data().Definition != "" || documents[0].program.Data().Definition == "" || procedure.program.Data().Definition == "" {
		t.Fatal("disconnect closed the wrong program documents")
	}
	s.CloseMySQLSessions()
	if documents[0].program.Data().Definition != "" || procedure.program.Data().Definition != "" || query.query.Editor.Buffer.String() != "SELECT preserved SQL" {
		t.Fatal("terminal cleanup retained definitions or discarded SQL text")
	}
}

func TestProgramTabCloseAndCloseOthersReleaseOnlyRemovedDefinitions(t *testing.T) {
	for _, closeOthers := range []bool{false, true} {
		t.Run(fmt.Sprint(closeOthers), func(t *testing.T) {
			s := New()
			s.sessions.Current().ProfileID, s.sessions.Current().Status = 41, "已连接"
			s.sessions.Current().Cache = map[string][]mysqlsvc.MySQLDBObject{"app": {{Name: "first", Kind: "函数", Definition: "SELECT 1"}, {Name: "second", Kind: "事件", Definition: "DELETE FROM logs"}}}
			s.openMySQLProgram("app", "first", "函数")
			first := s.currentTab()
			s.openMySQLProgram("app", "second", "事件")
			second := s.currentTab()
			if closeOthers {
				s.closeOtherTabs(2)
			} else {
				s.closeTab(1)
			}
			if s.currentTab() != second || len(s.tabs) != 2 || first.program.Data().Definition != "" || second.program.Data().Definition != "DELETE FROM logs" {
				t.Fatal("closing an inactive definition changed its surviving sibling")
			}
			s.closeActive()
			if len(s.tabs) != 1 || second.program.Data().Definition != "" {
				t.Fatal("active program close retained metadata")
			}
		})
	}
}
