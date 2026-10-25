package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type mysqlSessionResult struct {
	generation  uint64
	id          int64
	names       []string
	err         error
	metadataErr error
}

type mysqlSessionOpener func(context.Context, connections.ActiveSettings, string) error
type mysqlDatabaseLister func(context.Context, int64) ([]string, error)

// MySQL sessions belong to the workbench, not the connection-editor window.
// Background goroutines NEVER write State; the main MyGo frame consumes results.
func (s *State) startMySQLSession(services ui.Services) {
	p, ok := s.currentProfile()
	if !ok || p.Kind != connections.MySQL || s.profileService == nil {
		s.status = "请选择 MySQL 连接"
		return
	}
	if s.mysqlSessionManager == nil {
		s.mysqlSessionManager = connections.NewMySQLSessionManager()
	}
	settings, err := connections.ResolveActiveSettings(p)
	if err == nil {
		_, err = connections.DirectMySQLConfig(settings, s.mysqlSessionPassword)
	}
	if err != nil {
		s.mysqlSessionStatus = "连接失败"
		s.mysqlSessionError = err.Error()
		s.status = s.mysqlSessionError
		return
	}
	s.disconnectMySQLSession()
	ctx, cancel := context.WithCancel(context.Background())
	s.mysqlSessionCancel = cancel
	s.mysqlSessionGeneration++
	generation := s.mysqlSessionGeneration
	s.mysqlSessionProfileID = p.ID
	s.mysqlSessionStatus = "连接中"
	s.mysqlSessionError = ""
	s.mysqlSessionDatabases = nil
	s.resetMySQLObjectBrowser()
	s.status = "正在连接 MySQL：" + p.Name
	password := s.mysqlSessionPassword
	s.mysqlSessionPassword = ""
	manager := s.mysqlSessionManager
	opener := s.mysqlSessionOpener
	lister := s.mysqlDatabaseLister
	if opener == nil {
		opener = func(ctx context.Context, st connections.ActiveSettings, pw string) error {
			return manager.Connect(ctx, st, pw)
		}
	}
	if lister == nil {
		lister = manager.Databases
	}
	results := make(chan mysqlSessionResult, 1)
	s.mysqlSessionResults = results
	go func() {
		err := opener(ctx, settings, password)
		var names []string
		var metaErr error
		if err == nil && ctx.Err() == nil {
			names, metaErr = lister(ctx, p.ID)
		}
		if err != nil && password != "" {
			err = errors.New(strings.ReplaceAll(err.Error(), password, "[已隐藏]"))
		}
		if metaErr != nil && password != "" {
			metaErr = errors.New(strings.ReplaceAll(metaErr.Error(), password, "[已隐藏]"))
		}
		results <- mysqlSessionResult{generation: generation, id: p.ID, names: names, err: err, metadataErr: metaErr}
		services.Invalidate()
	}()
}

func (s *State) consumeMySQLSessionResult() {
	if s.mysqlSessionResults == nil {
		return
	}
	select {
	case result := <-s.mysqlSessionResults:
		if result.generation != s.mysqlSessionGeneration || result.id != s.mysqlSessionProfileID {
			return
		}
		s.mysqlSessionResults = nil
		if result.err != nil {
			s.mysqlSessionStatus = "连接失败"
			s.mysqlSessionError = result.err.Error()
			s.status = "连接 MySQL 失败：" + s.mysqlSessionError
			return
		}
		s.mysqlSessionStatus = "已连接"
		s.explorerTree.Open.Add(profileNode(result.id))
		s.mysqlSessionError = ""
		if result.metadataErr != nil {
			s.mysqlSessionError = "数据库列表读取失败：" + result.metadataErr.Error()
		} else {
			s.mysqlSessionDatabases = result.names
			s.reconcileMySQLDatabases(result.names)
		}
		s.status = fmt.Sprintf("已连接 MySQL（%d 个数据库）", len(result.names))
	default:
	}
}

// disconnectMySQLSession also invalidates outstanding asynchronous results.
func (s *State) disconnectMySQLSession() {
	s.mysqlSessionGeneration++
	if s.mysqlSessionCancel != nil {
		s.mysqlSessionCancel()
	}
	s.mysqlSessionCancel = nil
	s.mysqlSessionResults = nil
	id := s.mysqlSessionProfileID
	s.mysqlSessionProfileID = 0
	s.mysqlSessionDatabases = nil
	s.resetMySQLObjectBrowser()
	s.clearMySQLStructureTabs()
	s.mysqlSessionError = ""
	s.mysqlSessionStatus = "未连接"
	if s.mysqlSessionManager != nil && id > 0 {
		_ = s.mysqlSessionManager.Disconnect(id)
	}
}

// CloseMySQLSessions releases all active database connections when the app exits.
func (s *State) CloseMySQLSessions() { s.closeMySQLSessions() }

func (s *State) closeMySQLSessions() {
	s.disconnectMySQLSession()
	s.mysqlSessionPassword = ""
	if s.mysqlSessionManager != nil {
		_ = s.mysqlSessionManager.Close()
	}
	s.mysqlSessionManager = nil
}

func (s *State) mysqlSessionStateFor(id int64) string {
	if id == s.mysqlSessionProfileID && s.mysqlSessionStatus != "" {
		return s.mysqlSessionStatus
	}
	return "未连接"
}

func (s *State) mysqlSessionToolbar(c *ui.Context) {
	p, ok := s.currentProfile()
	if !ok || p.Kind != connections.MySQL {
		return
	}
	ui.Column(c).Gap(5).Children(func() {
		if s.mysqlSessionStatus != "连接中" && s.mysqlSessionStateFor(p.ID) != "已连接" {
			ui.TextInput(c, &s.mysqlSessionPassword).Password().
				Label("本次 MySQL 会话密码").Placeholder("密码（仅本次连接）").Height(26)
		}
		ui.Row(c).Gap(5).Children(func() {
			if s.mysqlSessionStateFor(p.ID) == "连接中" {
				if ui.Button(c, "取消连接").Clicked() {
					s.disconnectMySQLSession()
					s.status = "MySQL 连接已取消"
				}
			} else if s.mysqlSessionStateFor(p.ID) == "已连接" {
				if ui.Button(c, "断开连接").Clicked() {
					s.disconnectMySQLSession()
					s.status = "MySQL 已断开"
				}
				if ui.Button(c, "重连").Clicked() {
					s.disconnectMySQLSession()
					s.status = "请重新输入密码后点击连接"
				}
			} else if ui.Button(c, "连接").Label("连接所选 MySQL").Clicked() {
				s.startMySQLSession(c.Services())
			}
		})
		if s.mysqlSessionStateFor(p.ID) == "连接失败" && s.mysqlSessionError != "" {
			ui.Text(c, s.mysqlSessionError).FontSize(11).SingleLine()
		}
	})
}
