package shell

import (
	"context"
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type mysqlDataLoader func(context.Context, int64, string, string, int64, int) (*mysqlsvc.MySQLDataPage, error)
type mysqlDataAdvancedLoader func(context.Context, int64, string, string, mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error)

type mysqlDataResult struct {
	generation uint64
	profileID  int64
	request    uint64
	offset     int64
	page       *mysqlsvc.MySQLDataPage
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
		dataLimit:             mysqlsvc.MySQLDataDefaultPageSize,
		dataSelectedRow:       -1,
		dataFilterColumnDraft: "选择字段",
		dataFilterModeDraft:   "包含",
		dataRulesExpanded:     false, // Navicat-like compact bar by default; per-tab draft state remains intact.
		dataShowFieldTypes:    true,
		dataHiddenColumns:     map[string]bool{},
		dataUseKeyset:         true,
		dataChunkRow:          -1,
	}
	s.tabs = append(s.tabs, t)
	s.activeTab = len(s.tabs) - 1
	s.loadMySQLData(services, t, false)
}
func (s *State) cancelMySQLData(t *tab) {
	t.dataRequest++
	s.cancelMySQLChunk(t)
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
	t.dataReadInfo.Canceled = false
	t.dataLoading = true
	ctx, cancel := context.WithCancel(context.Background())
	t.dataCancel = cancel
	request, gen, profile := t.dataRequest, s.mysqlSessionGeneration, s.mysqlSessionProfileID
	database, table, offset, limit := t.dataDatabase, t.dataTable, t.dataOffset, t.dataLimit
	options := mysqlsvc.MySQLDataOptions{Offset: offset, Limit: limit,
		FilterColumn: t.dataFilterColumn, FilterValue: t.dataFilterValue,
		FilterMode: t.dataFilterMode, SortColumn: t.dataSortColumn,
		SortDescending: t.dataSortDescending, UseKeyset: t.dataUseKeyset, After: t.dataAfter,
		Filters: append([]mysqlsvc.MySQLDataFilterRule(nil), t.dataFilterRules...), Sorts: append([]mysqlsvc.MySQLDataSortRule(nil), t.dataSortRules...)}
	advanced := s.mysqlDataAdvancedLoader
	legacy := s.mysqlDataLoader
	if advanced == nil && legacy == nil {
		if s.mysqlSessionManager == nil {
			s.cancelMySQLData(t)
			t.dataErr = "MySQL 会话不可用"
			return
		}
		advanced = s.mysqlSessionManager.TableDataPageWithOptions
	}
	out := make(chan mysqlDataResult, 1)
	t.dataResults = out
	go func() {
		var page *mysqlsvc.MySQLDataPage
		var err error
		if advanced != nil {
			page, err = advanced(ctx, profile, database, table, options)
		} else if options.FilterMode == "" && options.SortColumn == "" {
			page, err = legacy(ctx, profile, database, table, offset, limit)
		} else {
			err = mysqlsvc.ErrMySQLDataPage // mock-only legacy loader cannot silently ignore a filter.
		}
		out <- mysqlDataResult{generation: gen, profileID: profile, request: request, offset: offset, page: page, err: err}
		services.Invalidate()
	}()
}
func (s *State) changeMySQLDataPage(services ui.Services, t *tab, offset int64) {
	if s.guardMySQLDataLeave(t, func(next ui.Services) { s.changeMySQLDataPage(next, t, offset) }) {
		return
	}
	if t == nil || t.kind != tabMySQLData || offset < 0 || t.dataLoading {
		return
	}
	// Full unique-key results advance by a seek cursor; other tables retain
	// the legacy OFFSET fallback and its explicit UI warning.
	if offset == 0 {
		t.dataUseKeyset = true
		t.dataAfter = nil
	} else if offset > t.dataOffset && t.dataPage != nil &&
		t.dataPage.KeysetAvailable && t.dataPage.NextCursor != nil {
		if t.dataCursorHistory == nil {
			t.dataCursorHistory = map[int64]*mysqlsvc.MySQLDataCursor{}
		}
		t.dataCursorHistory[offset] = t.dataPage.NextCursor
		t.dataUseKeyset = true
		t.dataAfter = t.dataPage.NextCursor
	} else if previous, ok := t.dataCursorHistory[offset]; ok {
		t.dataUseKeyset = true
		t.dataAfter = previous
	} else {
		t.dataUseKeyset = false
		t.dataAfter = nil
	}
	t.dataOffset = offset
	t.dataPage = nil
	t.dataColumns = nil
	t.dataSelectedRow = -1
	t.dataSelection.Clear()
	s.cancelMySQLChunk(t)
	t.dataState.Cells = ui.ColumnRange{}
	s.loadMySQLData(services, t, true)
}
func (s *State) consumeMySQLData() {
	for _, t := range s.tabs {
		if t.kind != tabMySQLData || t.dataResults == nil || t.dataProfileID != s.mysqlSessionProfileID {
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
			t.dataReadInfo.accept(result.page)
			t.dataErr = ""
			t.dataColumns = make([]ui.TableColumn, len(result.page.Columns))
			for i, name := range result.page.Columns {
				kind := ""
				if i < len(result.page.ColumnKinds) {
					kind = result.page.ColumnKinds[i]
				}
				sortable := kind != "" && !strings.Contains(kind, "blob") && !strings.Contains(kind, "text") && kind != "json" && kind != "geometry" && kind != "binary" && kind != "varbinary"
				t.dataColumns[i] = ui.TableColumn{Title: name, ID: name, Width: 155, MinWidth: 65, Sortable: sortable}
			}
		default:
		}
	}
}
