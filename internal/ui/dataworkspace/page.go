package dataworkspace

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type mysqlDataLoader func(context.Context, int64, string, string, int64, int) (*mysqlsvc.MySQLDataPage, error)
type mysqlDataAdvancedLoader func(context.Context, int64, string, string, mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error)

type mysqlDataResult struct {
	History    mysqlDataHistoryEntry
	generation uint64
	profileID  int64
	request    uint64
	offset     int64
	page       *mysqlsvc.MySQLDataPage
	err        error
}

func (s *State) Cancel(t *Document) {
	if t == nil || t.closed {
		return
	}
	if entry := t.tools.historyActiveRead; t.loading && entry != nil {
		canceled := *entry
		canceled.At, canceled.Duration = time.Now(), time.Since(entry.At)
		canceled.Failed, canceled.Message = true, mysqlDataHistoryError(context.Canceled, false, false, false)
		s.appendDataHistory(t, canceled)
	}
	t.tools.historyActiveRead = nil
	t.request++
	s.cancelMySQLChunk(t)
	if t.cancel != nil {
		t.cancel()
	}
	t.cancel = nil
	t.results = nil
	t.loading = false
}
func (s *State) Load(services ui.Services, t *Document, refresh bool) {
	if t == nil || t.closed || t.transfer.Busy || t.transfer.Uncertain || dataTransactionUnavailable(t) || t.profileID != s.session().ProfileID ||
		!s.session().Connected || s.session().ProfileID <= 0 {
		return
	}
	if t.loading && !refresh {
		return
	}
	if t.page != nil && !refresh {
		return
	}
	s.Cancel(t)
	t.err = ""
	t.readInfo.Canceled = false
	t.loading = true
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	request, gen, profile := t.request, s.session().Generation, s.session().ProfileID
	database, table, offset, limit := t.database, t.table, t.offset, t.limit
	options := mysqlsvc.MySQLDataOptions{Offset: offset, Limit: limit,
		FilterColumn: t.filterColumn, FilterValue: t.filterValue,
		FilterMode: t.filterMode, SortColumn: t.sortColumn,
		SortDescending: t.sortDescending, UseKeyset: t.useKeyset, After: cloneCursor(t.after),
		Filters: append([]mysqlsvc.MySQLDataFilterRule(nil), t.filterRules...), Sorts: append([]mysqlsvc.MySQLDataSortRule(nil), t.sortRules...)}
	advanced := s.advancedLoaderFor(t)
	legacy := s.pageLoader()
	if advanced == nil && legacy == nil {
		s.Cancel(t)
		t.err = "MySQL 会话不可用"
		return
	}
	out := make(chan mysqlDataResult, 1)
	t.results = out
	history := mysqlDataReadHistoryRequest(database, table, options)
	activeHistory := history
	t.tools.historyActiveRead = &activeHistory
	go func() {
		started := time.Now()
		var page *mysqlsvc.MySQLDataPage
		var err error
		if advanced != nil {
			page, err = advanced(ctx, profile, database, table, options)
		} else if options.FilterMode == "" && options.SortColumn == "" {
			page, err = legacy(ctx, profile, database, table, offset, limit)
		} else {
			err = mysqlsvc.ErrMySQLDataPage // mock-only legacy loader cannot silently ignore a filter.
		}
		history.At, history.Duration = time.Now(), time.Since(started)
		out <- mysqlDataResult{History: history, generation: gen, profileID: profile, request: request, offset: offset, page: page, err: err}
		services.Invalidate()
	}()
}
func (s *State) changeMySQLDataPage(services ui.Services, t *Document, offset int64) {
	if s.GuardLeave(t, func(next ui.Services) { s.changeMySQLDataPage(next, t, offset) }) {
		return
	}
	if t == nil || t.closed || offset < 0 || t.loading {
		return
	}
	// Full unique-key results advance by a seek cursor; other tables retain
	// the legacy OFFSET fallback and its explicit UI warning.
	if offset == 0 {
		t.useKeyset = true
		t.after = nil
	} else if offset > t.offset && t.page != nil &&
		t.page.KeysetAvailable && t.page.NextCursor != nil {
		if t.cursorHistory == nil {
			t.cursorHistory = map[int64]*mysqlsvc.MySQLDataCursor{}
		}
		t.cursorHistory[offset] = t.page.NextCursor
		t.useKeyset = true
		t.after = t.page.NextCursor
	} else if previous, ok := t.cursorHistory[offset]; ok {
		t.useKeyset = true
		t.after = previous
	} else {
		t.useKeyset = false
		t.after = nil
	}
	t.offset = offset
	t.page = nil
	t.columns = nil
	t.selectedRow = -1
	t.selection.Clear()
	s.cancelMySQLChunk(t)
	t.grid.Cells = ui.ColumnRange{}
	s.Load(services, t, true)
}
func (s *State) consumePages() {
	for _, t := range s.documents {
		if t.closed || t.results == nil || t.profileID != s.session().ProfileID {
			continue
		}
		select {
		case result := <-t.results:
			if result.profileID != s.session().ProfileID ||
				result.profileID != t.profileID ||
				result.request != t.request ||
				result.offset != t.offset {
				continue
			}
			s.recordDataPageHistory(t, result, result.generation != s.session().Generation || !s.session().Connected)
			s.Cancel(t)
			if !result.History.At.IsZero() {
				t.tools.historyRequest, t.tools.historyRecorded = t.request, true
			}
			if result.generation != s.session().Generation || !s.session().Connected {
				t.err = "原 MySQL 会话已失效，请刷新表数据"
				continue
			}
			if result.err != nil {
				if errors.Is(result.err, mysqlsvc.ErrMySQLDataTransactionLost) {
					t.transaction.lost = true
					t.transaction.err = result.err.Error()
				}
				if !errors.Is(result.err, context.Canceled) {
					t.err = result.err.Error()
				}
				continue
			}
			if result.page == nil {
				t.err = "数据库没有返回数据页"
				continue
			}
			t.page = result.page
			t.readInfo.accept(result.page)
			t.err = ""
			t.columns = make([]ui.TableColumn, len(result.page.Columns))
			for i, name := range result.page.Columns {
				kind := ""
				if i < len(result.page.ColumnKinds) {
					kind = result.page.ColumnKinds[i]
				}
				sortable := kind != "" && !strings.Contains(kind, "blob") && !strings.Contains(kind, "text") && kind != "json" && kind != "geometry" && kind != "binary" && kind != "varbinary"
				t.columns[i] = ui.TableColumn{Title: name, ID: name, Width: 155, MinWidth: 65, Sortable: sortable}
			}
		default:
		}
	}
}

// Closing a document cancels all of its independent readers. A normal data
// refresh only cancels the page reader, leaving table statistics undisturbed.
func (s *State) Close(t *Document) {
	if t == nil || t.closed {
		return
	}
	s.flushMySQLDataLayout(t)
	s.cancelMySQLDataEdit(t)
	s.Cancel(t)
	s.closeDataTransaction(t)
	s.inspector.Close(t.panel)
	s.closeDataTransfer(t)
	s.closeDataAnalysis(t)
	t.tools = mysqlDataToolsState{}
	t.toolbar = mysqlDataToolbarState{}
	t.fill = mysqlDataFillState{}
	t.closed = true
	t.page, t.panel, t.columns = nil, nil, nil
	t.edit = mysqlDataEditState{}
	if t.export.Cancel != nil {
		t.export.Cancel()
	}
	t.export = mysqlDataExportState{}
	t.cursorHistory, t.after = nil, nil
	t.hiddenColumns = nil
	t.filterExtraDraft, t.filterRules, t.sortDraft, t.sortRules = nil, nil, nil, nil
	t.selection.Clear()
	t.grid = ui.ListState{}
	if s.documents[t.id] == t {
		delete(s.documents, t.id)
	}
}
