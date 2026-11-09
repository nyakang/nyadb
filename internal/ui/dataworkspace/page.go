package dataworkspace

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

type mysqlDataLoader func(context.Context, int64, string, string, int64, int) (*contract.DataPage, error)
type mysqlDataAdvancedLoader func(context.Context, int64, string, string, contract.DataOptions) (*contract.DataPage, error)

type mysqlDataResult struct {
	History    mysqlDataHistoryEntry
	generation uint64
	profileID  int64
	request    uint64
	offset     int64
	page       *contract.DataPage
	err        error
}

func (s *State) Cancel(t *Document) {
	if t == nil || t.closed {
		return
	}
	if s.runtimeFor(t).PageFailureRequiresReopen && t.loading {
		t.readRetired, t.readStopping = true, true
		if t.cancel != nil {
			t.cancel()
		}
		return
	}
	s.settlePage(t)
}

// Completed page disposal never retires a successful range. Auxiliary readers
// bound to the original document are independent of page cancellation.
func (s *State) settlePage(t *Document) {
	if entry := t.tools.historyActiveRead; t.loading && entry != nil {
		canceled := *entry
		canceled.At, canceled.Duration = time.Now(), time.Since(entry.At)
		canceled.Failed, canceled.Message = true, mysqlDataHistoryError(context.Canceled, false, false, false)
		s.appendDataHistory(t, canceled)
	}
	t.tools.historyActiveRead = nil
	t.request++
	if !s.runtimeFor(t).PageFailureRequiresReopen {
		s.cancelMySQLChunk(t)
	}
	if t.cancel != nil {
		t.cancel()
	}
	t.cancel = nil
	t.results = nil
	t.loading = false
}
func (s *State) Load(services ui.Services, t *Document, refresh bool) {
	if t == nil || t.closed || t.edit.Busy || t.edit.Uncertain != nil || t.transfer.Busy || t.transfer.Uncertain || dataTransactionUnavailable(t) || t.profileID != s.sessionFor(t).ProfileID ||
		!s.sessionFor(t).Connected || s.sessionFor(t).ProfileID <= 0 {
		return
	}
	if t.object.ProfileID != 0 && t.generation != s.sessionFor(t).Generation {
		t.err = "原数据库会话已失效，请刷新表数据"
		return
	}
	if t.readRetired {
		t.err = "原数据读取范围已失效，请重新打开数据"
		return
	}
	if t.loading && s.runtimeFor(t).PageFailureRequiresReopen {
		if refresh {
			s.Cancel(t)
		}
		return
	}
	if t.loading && !refresh {
		return
	}
	if t.page != nil && !refresh {
		return
	}
	s.Cancel(t)
	if refresh && t.runtimeLease != nil && !t.pagingRead {
		t.offset = 0
		t.after = nil
		t.cursorHistory = nil
	}
	t.err = ""
	t.readInfo.Canceled = false
	t.loading = true
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	request, gen, profile := t.request, s.sessionFor(t).Generation, s.sessionFor(t).ProfileID
	database, table, offset, limit := t.database, t.table, t.offset, t.limit
	options := contract.DataOptions{Condition: cloneDataCondition(t.filterCondition), Object: t.object, Offset: offset, Limit: limit,
		FilterColumn: t.filterColumn, FilterValue: t.filterValue,
		FilterMode: t.filterMode, SortColumn: t.sortColumn,
		SortDescending: t.sortDescending, UseKeyset: t.useKeyset, After: cloneCursor(t.after),
		Filters: append([]contract.DataFilterRule(nil), t.filterRules...), Sorts: append([]contract.DataSortRule(nil), t.sortRules...)}
	advanced := s.advancedLoaderFor(t)
	legacy := s.pageLoader()
	if s.Loader == nil {
		legacy = s.runtimeFor(t).Loader
	}
	if advanced == nil && legacy == nil {
		s.Cancel(t)
		t.err = "数据库会话不可用"
		return
	}
	out := make(chan mysqlDataResult, 1)
	t.results = out
	history := mysqlDataReadHistoryRequest(database, table, options)
	if describe := s.runtimeFor(t).ReadDescription; describe != nil {
		history.SQL, history.Summary = describe(options), false
	}
	complete := s.beginActivity(t, "data_read", history.SQL)
	activeHistory := history
	t.tools.historyActiveRead = &activeHistory
	go func() {
		started := time.Now()
		var page *contract.DataPage
		var err error
		if advanced != nil {
			page, err = advanced(ctx, profile, database, table, options)
		} else if options.Condition == nil && len(options.Filters) == 0 && len(options.Sorts) == 0 && options.FilterMode == "" && options.SortColumn == "" {
			page, err = legacy(ctx, profile, database, table, offset, limit)
		} else {
			err = contract.ErrDataPage // mock-only legacy loader cannot silently ignore a filter.
		}
		history.At, history.Duration = time.Now(), time.Since(started)
		activity := contract.ActivityOutcome(err)
		if err != nil {
			activity.Error = mysqlDataHistoryError(err, false, false, false)
		}
		if err == nil && page == nil {
			activity.State = "error"
			activity.Error = mysqlDataHistoryError(nil, false, true, false)
		}
		if page != nil {
			activity.Rows = int64(len(page.Rows))
		}
		contract.CompleteActivity(complete, activity)
		out <- mysqlDataResult{History: history, generation: gen, profileID: profile, request: request, offset: offset, page: page, err: err}
		services.Invalidate()
	}()
}
func (s *State) changeMySQLDataPage(services ui.Services, t *Document, offset int64) {
	if s.GuardLeave(t, func(next ui.Services) { s.changeMySQLDataPage(next, t, offset) }) {
		return
	}
	if t == nil || t.closed || offset < 0 || t.loading || t.readRetired {
		return
	}
	if !documentCursorAllows(t, offset) {
		t.pageNumberError = locales.Text("原生游标仅支持已读取页或下一页")
		return
	}
	// Providers may attach original document boundaries to OFFSET pages as well
	// as seek pages. A boundary does not imply keyset or snapshot semantics.
	if previous, ok := t.cursorHistory[offset]; ok && offset == 0 {
		t.useKeyset = t.keysetEnabled()
		t.after = previous
	} else if offset == 0 {
		t.useKeyset = t.keysetEnabled()
		t.after = nil
	} else if offset == t.offset+int64(t.limit) && t.page != nil &&
		t.page.NextCursor != nil && (t.page.KeysetAvailable || t.runtimeLease != nil && t.page.NextCursor.Offset == offset) {
		if t.cursorHistory == nil {
			t.cursorHistory = map[int64]*contract.DataCursor{}
		}
		t.cursorHistory[offset] = t.page.NextCursor
		t.useKeyset = t.keysetEnabled()
		t.after = t.page.NextCursor
	} else if previous, ok := t.cursorHistory[offset]; ok {
		t.useKeyset = t.keysetEnabled()
		t.after = previous
	} else {
		t.useKeyset = false
		t.after = nil
		if t.runtimeLease != nil {
			t.cursorHistory = nil
		}
	}
	clearDraftHistory(t)
	t.offset = offset
	if !s.runtimeFor(t).PageFailureRequiresReopen {
		t.page = nil
		t.columns = nil
	}
	t.selectedRow = -1
	t.selection.Clear()
	s.cancelMySQLChunk(t)
	mysqlDataClearColumnSelection(t)
	t.grid.Cells = ui.ColumnRange{}
	t.pagingRead = true
	s.Load(services, t, true)
	t.pagingRead = false
}
func (s *State) consumePages() {
	for _, t := range s.documents {
		if t.closed || t.results == nil || !s.consumeFor(t) {
			continue
		}
		select {
		case result := <-t.results:
			if result.profileID != s.sessionFor(t).ProfileID ||
				result.profileID != t.profileID ||
				result.request != t.request ||
				result.offset != t.offset {
				continue
			}
			s.recordDataPageHistory(t, result, result.generation != s.sessionFor(t).Generation || !s.sessionFor(t).Connected)
			stopping := t.readStopping
			s.settlePage(t)
			t.readStopping = false
			if s.runtimeFor(t).PageFailureRequiresReopen && t.page != nil &&
				(stopping || result.err != nil || result.page == nil || result.generation != s.sessionFor(t).Generation || !s.sessionFor(t).Connected) {
				t.offset = t.page.Offset
				t.after = cloneCursor(t.page.CurrentCursor)
			}
			if !result.History.At.IsZero() {
				t.tools.historyRequest, t.tools.historyRecorded = t.request, true
			}
			if result.generation != s.sessionFor(t).Generation || !s.sessionFor(t).Connected {
				t.err = "原数据库会话已失效，请刷新表数据"
				continue
			}
			if stopping || s.runtimeFor(t).PageFailureRequiresReopen && (result.err != nil || result.page == nil) {
				t.readRetired = true
				t.err = "原数据读取范围已失效，请重新打开数据"
				continue
			}
			if result.err != nil {
				if errors.Is(result.err, contract.ErrDataTransactionLost) {
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
			clearDraftHistory(t)
			t.page = result.page
			if result.page.CurrentCursor != nil {
				if t.cursorHistory == nil {
					t.cursorHistory = map[int64]*contract.DataCursor{}
				}
				t.cursorHistory[t.offset] = cloneCursor(result.page.CurrentCursor)
			}
			t.readInfo.accept(result.page)
			t.err = ""
			t.columns = make([]ui.TableColumn, len(result.page.Columns))
			for i, name := range result.page.Columns {
				kind := ""
				if i < len(result.page.ColumnKinds) {
					kind = result.page.ColumnKinds[i]
				}
				sortable := t.portable() || t.capabilities.Documents && s.runtimeFor(t).SortColumns != nil || kind != "" && !strings.Contains(kind, "blob") && !strings.Contains(kind, "text") && kind != "json" && kind != "geometry" && kind != "binary" && kind != "varbinary"
				t.columns[i] = ui.TableColumn{Title: name, ID: name, Width: 155, MinWidth: 65, Sortable: sortable && s.sortable(t, name)}
				if t.capabilities.Documents || t.capabilities.KeyValues {
					t.columns[i].Width = 240
					if name == "document" || name == "point" || t.capabilities.KeyValues && name == "value" {
						t.columns[i].Width = 500
						t.columns[i].Sortable = false
					}
				}
				if t.capabilities.KeyValues {
					t.columns[i].Sortable = false
					if name == "type" {
						t.columns[i].Width = 90
					}
					if name == "ttl_ms" {
						t.columns[i].Width = 115
					}
					if name == "key" {
						t.columns[i].Width = 200
					}
				}
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
	if s.guardDoc == t {
		s.guardDoc = nil
	}
	s.flushMySQLDataLayout(t)
	s.cancelMySQLDataEdit(t)
	s.Cancel(t)
	s.closeDataTransaction(t)
	s.inspector.Close(t.panel)
	s.closeDataTransfer(t)
	s.closeDataAnalysis(t)
	if t.export.Cancel != nil {
		t.export.Cancel()
	}
	if t.runtimeLease != nil {
		if err := t.runtimeLease.Close(); err != nil && s.deps.Status != nil {
			s.deps.Status(err.Error())
		}
		t.runtimeLease = nil
		t.boundRuntime = nil
	}
	s.settlePage(t)
	t.readStopping = false
	t.tools = mysqlDataToolsState{}
	t.hostAction, t.hostProfile = "", ""
	t.toolbar = mysqlDataToolbarState{}
	t.fill = mysqlDataFillState{}
	t.layout.columnWidth = mysqlDataColumnWidthState{}
	t.layout.filterFocus = ""
	t.closed = true
	t.page, t.panel, t.columns = nil, nil, nil
	t.edit = mysqlDataEditState{}
	t.export = mysqlDataExportState{}
	t.cursorHistory, t.after = nil, nil
	t.hiddenColumns = nil
	t.filterExtraDraft, t.filterRules, t.sortDraft, t.sortRules = nil, nil, nil, nil
	t.filterCondition, t.groupDraft = nil, nil
	t.selectorDraft, t.selectorError, t.selectorOpen = "", "", false
	t.selectorHistory, t.selectorHistorySorts = false, nil
	t.selectorResponseOpen = false
	t.disabledGroupRules = nil
	t.selection.Clear()
	t.grid = ui.ListState{}
	if s.documents[t.id] == t {
		delete(s.documents, t.id)
	}
}
