package shell

import (
	"bytes"
	"encoding/json"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
	"github.com/nyakang/nyadb/locales"
	"io"
	"strings"
)

func (s *State) initRuntime() {
	s.mysqlTemplate = mysqlsvc.NewMySQLSessionManager()
	s.runtime = dbruntime.New()
	dbruntime.RegisterBuiltins(s.runtime, s.mysqlTemplate)
	s.sessions.SetEngineRuntime(sessionbrowser.EngineRuntime{Open: s.runtime.ConnectSession, Connect: s.runtime.Connect, Session: s.runtime.Session, Disconnect: s.runtime.Disconnect, Close: s.runtime.Close, IsConnected: s.runtime.IsConnected})
}

func (s *State) sqlRuntime() sqlworkspace.Runtime {
	return sqlworkspace.EngineRuntime(s.runtime.Session)
}
func (s *State) SetEngineFiles(pick func(string) (string, error), write func(string, func(io.Writer) error) error) {
	s.dataWorkspace.SetFiles(pick, write)
}
func (s *State) engineSessionToolbar(c *ui.Context) bool {
	p, ok := s.currentProfile()
	if !ok || p.Kind.IsMySQLFamily() {
		return false
	}
	status := s.sessions.StatusFor(p.ID)
	ui.Column(c).Gap(5).Children(func() {
		if (p.Kind == connections.PostgreSQL || p.Kind == connections.ClickHouse || p.Kind == connections.SQLServer || p.Kind == connections.Oracle || p.Kind == connections.MongoDB) && status != "已连接" {
			label := locales.Text("PostgreSQL 会话密码")
			if p.Kind == connections.MongoDB {
				label = locales.Text("MongoDB 会话密码")
			}
			if p.Kind == connections.Oracle {
				label = locales.Text("Oracle 会话密码")
			}
			if p.Kind == connections.SQLServer {
				label = locales.Text("SQL Server 会话密码")
			}
			if p.Kind == connections.ClickHouse {
				label = locales.Text("ClickHouse 会话密码")
			}
			ui.TextInput(c, &s.sessions.Current().Password).Password().Label(label)
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

func (s *State) objectCapabilitiesFor(id int64) contract.Capabilities {
	session, e := s.runtime.Session(id)
	if e != nil {
		return contract.Capabilities{}
	}
	return session.Capabilities()
}
func (s *State) sqlStatementAtCaret(id int64, source string, cursor int) string {
	for _, p := range s.navigation.Profiles() {
		if p.ID == id && p.Kind == connections.MongoDB {
			return strings.TrimSpace(source)
		}
		if p.ID == id && p.Kind == connections.Oracle {
			return sqltext.OracleStatementAt(source, cursor)
		}
		if p.ID == id && p.Kind == connections.DuckDB {
			return sqltext.DuckDBStatementAt(source, cursor)
		}
		if p.ID == id && p.Kind == connections.SQLServer {
			return sqltext.SQLServerStatementAt(source, cursor)
		}
		if p.ID == id && p.Kind == connections.ClickHouse {
			return sqltext.ClickHouseStatementAt(source, cursor)
		}
		if p.ID == id && !p.Kind.IsMySQLFamily() && p.Kind != connections.ClickHouse {
			return sqltext.PortableStatementAt(source, cursor)
		}
	}
	return ""
}

func (s *State) sqlFormat(id int64, source string) (string, error) {
	for _, p := range s.navigation.Profiles() {
		if p.ID == id && p.Kind == connections.MongoDB {
			var out bytes.Buffer
			err := json.Indent(&out, []byte(source), "", "  ")
			return out.String(), err
		}
		if p.ID == id && p.Kind == connections.Oracle {
			return sqltext.FormatOracle(source)
		}
		if p.ID == id && p.Kind == connections.DuckDB {
			return sqltext.FormatDuckDB(source)
		}
		if p.ID == id && p.Kind == connections.SQLServer {
			return sqltext.FormatSQLServer(source)
		}
		if p.ID == id && p.Kind == connections.ClickHouse {
			return sqltext.FormatClickHouse(source)
		}
		if p.ID == id && !p.Kind.IsMySQLFamily() && p.Kind != connections.ClickHouse {
			return sqltext.FormatPortable(source)
		}
	}
	return sqltext.Format(source)
}

func (s *State) sqlValidateExplain(id int64, source string) error {
	for _, p := range s.navigation.Profiles() {
		if p.ID == id && p.Kind == connections.Oracle {
			return sqltext.SingleOracleStatement(source)
		}
		if p.ID == id && p.Kind == connections.DuckDB {
			return sqltext.SingleDuckDBStatement(source)
		}
		if p.ID == id && p.Kind == connections.SQLServer {
			return sqltext.SingleSQLServerStatement(source)
		}
	}
	for _, p := range s.navigation.Profiles() {
		if p.ID == id && p.Kind == connections.ClickHouse {
			return sqltext.SingleClickHouseStatement(source)
		}
		if p.ID == id && !p.Kind.IsMySQLFamily() && p.Kind != connections.ClickHouse {
			return sqltext.SinglePortableStatement(source)
		}
	}
	return sqltext.SingleMySQLStatement(source)
}

func (s *State) sqlPlanEngine(id int64) string {
	for _, p := range s.navigation.Profiles() {
		if p.ID == id {
			return string(p.Kind)
		}
	}
	return "mysql"
}
func (s *State) sqlBuildExplain(id int64, text string) (string, error) {
	return queryplan.Build(s.sqlPlanEngine(id), text)
}
func (s *State) sqlParsePlan(id int64, result contract.SQLResult) (*contract.ExecutionPlan, error) {
	return queryplan.Parse(s.sqlPlanEngine(id), result)
}

func (s *State) sqlPlanParser(id int64) func(contract.SQLResult) (*contract.ExecutionPlan, error) {
	engine := s.sqlPlanEngine(id)
	return func(result contract.SQLResult) (*contract.ExecutionPlan, error) {
		return queryplan.Parse(engine, result)
	}
}
