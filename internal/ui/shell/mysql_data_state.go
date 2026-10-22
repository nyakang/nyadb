package shell

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type mysqlDataLoader func(context.Context, int64, string, string, int64, int) (*connections.MySQLDataPage, error)

type mysqlDataResult struct {
	generation uint64
	profileID  int64
	request    uint64
	offset     int64
	page       *connections.MySQLDataPage
	err        error
}

// Real data is never mixed with the legacy mock tabData preview. Separate
// profile, schema and table names isolate caches across workspaces.
func (s *State) openMySQLData(services ui.Services, database, table string) {
	if s.mysqlSessionStatus != "已连接" || s.mysqlSessionProfileID <= 0 ||
		!validStructureSelection(database, table) {
		return
	}
	for i, t := range s.tabs {
		if t.kind == tabMySQLData && t.dataDatabase == database && t.dataTable == table &&
			t.dataProfileID == s.mysqlSessionProfileID {
			s.activeTab = i
			if t.dataPage == nil && !t.dataLoading && t.dataErr == "" {
				s.loadMySQLData(services, t, false)
			}
			return
		}
	}
	s.nextTabID++
	t := &tab{
		id: s.nextTabID, kind: tabMySQLData,
		title:        database + "." + table + " · 数据",
		dataDatabase: database, dataTable: table, dataProfileID: s.mysqlSessionProfileID,
		dataLimit: connections.MySQLDataDefaultPageSize,
	}
	s.tabs = append(s.tabs, t)
	s.activeTab = len(s.tabs) - 1
	s.loadMySQLData(services, t, false)
}
func (s *State) cancelMySQLData(t *tab) {
	t.dataRequest++
	if t.dataCancel != nil {
		t.dataCancel()
	}
	t.dataCancel = nil
	t.dataResults = nil
	t.dataLoading = false
}
func (s *State) loadMySQLData(services ui.Services, t *tab, refresh bool) {
	if t == nil || t.kind != tabMySQLData || t.dataProfileID != s.mysqlSessionProfileID ||
		s.mysqlSessionStatus != "已连接" || s.mysqlSessionProfileID <= 0 {
		return
	}
	if t.dataLoading && !refresh {
		return
	}
	if t.dataPage != nil && !refresh {
		return
	}
	s.cancelMySQLData(t)
	t.dataErr = ""
	t.dataLoading = true
	ctx, cancel := context.WithCancel(context.Background())
	t.dataCancel = cancel
	request, gen, profile := t.dataRequest, s.mysqlSessionGeneration, s.mysqlSessionProfileID
	database, table, offset, limit := t.dataDatabase, t.dataTable, t.dataOffset, t.dataLimit
	loader := s.mysqlDataLoader
	if loader == nil {
		if s.mysqlSessionManager == nil {
			s.cancelMySQLData(t)
			t.dataErr = "MySQL 会话不可用"
			return
		}
		loader = s.mysqlSessionManager.TableDataPage
	}
	out := make(chan mysqlDataResult, 1)
	t.dataResults = out
	go func() {
		page, err := loader(ctx, profile, database, table, offset, limit)
		out <- mysqlDataResult{generation: gen, profileID: profile, request: request, offset: offset, page: page, err: err}
		services.Invalidate()
	}()
}
func (s *State) changeMySQLDataPage(services ui.Services, t *tab, offset int64) {
	if t == nil || t.kind != tabMySQLData || offset < 0 || t.dataLoading {
		return
	}
	t.dataOffset = offset
	t.dataPage = nil
	t.dataColumns = nil
	t.dataState = ui.ListState{}
	s.loadMySQLData(services, t, true)
}
func (s *State) consumeMySQLData() {
	for _, t := range s.tabs {
		if t.kind != tabMySQLData || t.dataResults == nil {
			continue
		}
		select {
		case result := <-t.dataResults:
			if result.generation != s.mysqlSessionGeneration ||
				result.profileID != s.mysqlSessionProfileID ||
				result.profileID != t.dataProfileID ||
				result.request != t.dataRequest ||
				result.offset != t.dataOffset {
				continue
			}
			s.cancelMySQLData(t)
			if result.err != nil {
				if !errors.Is(result.err, context.Canceled) {
					t.dataErr = result.err.Error()
				}
				continue
			}
			if result.page == nil {
				t.dataErr = "数据库没有返回数据页"
				continue
			}
			t.dataPage = result.page
			t.dataErr = ""
			t.dataColumns = make([]ui.TableColumn, len(result.page.Columns))
			for i, name := range result.page.Columns {
				t.dataColumns[i] = ui.TableColumn{Title: name, Width: 155}
			}
		default:
		}
	}
}
