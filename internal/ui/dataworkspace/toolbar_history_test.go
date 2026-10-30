package dataworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestDataHistoryRecordsAcceptedReadsOnceAndBoundsOwnedRules(t *testing.T) {
	s := New(Dependencies{})
	d := layoutTestTab()
	d.page.QuerySQL = "SELECT `id` FROM `sample`.`entries` WHERE `label` = ?"
	d.page.CompletedAt = time.Now()
	d.filterRules = []mysqlsvc.MySQLDataFilterRule{{Column: "label", Mode: "equal", Value: "original"}}
	s.recordDataHistory(d)
	s.recordDataHistory(d)
	if len(d.tools.history) != 1 {
		t.Fatal("redrawing a successful page created duplicate history")
	}
	d.filterRules[0].Value = "mutated"
	if d.tools.history[0].Filters[0].Value != "original" {
		t.Fatal("history rules shared live document state")
	}
	d.loading = true
	d.request++
	s.recordDataHistory(d)
	d.loading, d.err = false, "failed read"
	s.recordDataHistory(d)
	if len(d.tools.history) != 1 {
		t.Fatal("pending or failed query was recorded as success")
	}
	d.err = ""
	for i := range mysqlDataHistoryLimit + 5 {
		d.request++
		d.filterRules[0].Value = fmt.Sprint(i)
		s.recordDataHistory(d)
	}
	if len(d.tools.history) != mysqlDataHistoryLimit || d.tools.history[0].Filters[0].Value != "54" || d.tools.history[49].Filters[0].Value != "5" {
		t.Fatal("history did not evict the oldest successful entries")
	}
	s.Close(d)
	if len(d.tools.history) != 0 || d.tools.historyRecorded {
		t.Fatal("closing a document retained session history")
	}
}

func TestDataHistoryRestoresOnlyRuleDraftsWithoutReplayOrDiscard(t *testing.T) {
	s := New(Dependencies{})
	d := layoutTestTab()
	d.columns[0].Sortable = true
	d.offset, d.limit = 100, 50
	d.filterColumn, d.filterMode, d.filterValue = "label", "equal", "active"
	d.sortColumn, d.sort = "id", ui.SortOrder{Column: "id"}
	d.edit.Drafts = map[string]*mysqlRowDraft{"row": {Kind: "update", Values: map[string]mysqlsvc.MySQLDataValue{"label": {Text: "draft"}}}}
	page := d.page
	entry := mysqlDataHistoryEntry{Offset: 0, Limit: 200,
		Filters: []mysqlsvc.MySQLDataFilterRule{{Column: "label", Mode: "contains", Value: "recovered"}},
		Sorts:   []mysqlsvc.MySQLDataSortRule{{Column: "id", Descending: true}}}
	s.restoreDataHistory(d, entry)
	if d.page != page || d.offset != 100 || d.limit != 50 || d.loading || !d.HasDraft() || d.filterValue != "active" || d.sort.Descending {
		t.Fatal("restoring history replayed a query or mutated active page settings/drafts")
	}
	if !d.rulesExpanded || d.filterValueDraft != "recovered" || d.filterModeDraft != "包含" || len(d.sortDraft) != 1 || d.sortDraft[0].Direction != "降序" {
		t.Fatal("history did not recover the request as editable rules")
	}
	entry.Filters[0].Value = "later"
	if d.filterValueDraft != "recovered" {
		t.Fatal("recovered drafts shared the history entry")
	}
	entry.Filters[0].Column = "removed"
	s.restoreDataHistory(d, entry)
	if d.filterValueDraft != "recovered" {
		t.Fatal("incompatible history overwrote existing rule drafts")
	}
}

func TestDataHistoryWriteSummariesExcludeCellsKeysAndRawErrors(t *testing.T) {
	s := New(Dependencies{})
	d := layoutTestTab()
	secret := "private_cell_value_091"
	ref := &mysqlsvc.MySQLRowSnapshot{Ref: mysqlsvc.MySQLRowRef{Columns: []string{"key`name"}, Values: [][]byte{[]byte(secret)}}}
	edits := []mysqlsvc.MySQLDataEdit{{Kind: "insert", Values: map[string]mysqlsvc.MySQLDataValue{"note": {Text: secret}}}, {Kind: "update", Original: ref, Values: map[string]mysqlsvc.MySQLDataValue{"note": {Text: secret}, "payload": {Binary: true, Bytes: []byte(secret)}}}, {Kind: "delete", Original: ref}}
	entries := mysqlDataWriteHistoryRequest("db`name", "table", edits, true)
	if len(entries) != 3 || !strings.Contains(entries[1].SQL, "`key``name` = ?") || !strings.Contains(entries[1].SQL, "`payload` = ?") {
		t.Fatal("write summaries did not retain qualified parameterized identity")
	}
	s.recordDataWriteHistory(d, entries, nil, errors.New("password=credential_failure_0123 "+secret), false)
	encoded, err := json.Marshal(d.tools.history)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "credential_failure_0123") {
		t.Fatal("history retained raw database values or error credentials")
	}
	if d.tools.historyStats.Insert != 1 || d.tools.historyStats.Update != 1 || d.tools.historyStats.Delete != 1 || d.tools.historyStats.Errors != 3 || d.tools.historyStats.Total != 3 {
		t.Fatal("write history statistics concealed failed request kinds")
	}
	for _, e := range d.tools.history {
		if !e.Failed || !e.Summary || mysqlDataHistoryCanRestore(e) {
			t.Fatal("failed write history became replayable")
		}
	}
}

func TestDataHistoryCountsOutliveEvictionAndFiltersRetainedKinds(t *testing.T) {
	s := New(Dependencies{})
	d := layoutTestTab()
	kinds := []string{"SELECT", "INSERT", "UPDATE", "DELETE"}
	for i := range 80 {
		s.appendDataHistory(d, mysqlDataHistoryEntry{Kind: kinds[i%4], Failed: i%8 == 0, SQL: "parameterized request"})
	}
	stats := d.tools.historyStats
	if len(d.tools.history) != 50 || stats.Select != 20 || stats.Insert != 20 || stats.Update != 20 || stats.Delete != 20 || stats.Errors != 10 || stats.Total != 80 {
		t.Fatal("eviction incorrectly reset cumulative counts")
	}
	d.tools.historyFilter = "UPDATE"
	indices := mysqlDataHistoryVisible(d)
	if len(indices) == 0 {
		t.Fatal("operation filter removed all matching history")
	}
	for _, i := range indices {
		if d.tools.history[i].Kind != "UPDATE" {
			t.Fatal("operation filter leaked another kind")
		}
	}
	d.tools.historyFilter = "errors"
	indices = mysqlDataHistoryVisible(d)
	if len(indices) != 6 {
		t.Fatal("error filter used cumulative count as retained row count")
	}
	for _, i := range indices {
		if !d.tools.history[i].Failed {
			t.Fatal("error filter leaked successful requests")
		}
	}
	s.clearDataHistory(d)
	if len(d.tools.history) != 0 || d.tools.historyStats != (mysqlDataHistoryCounts{}) || d.tools.historyFilter != "all" {
		t.Fatal("clear retained history counters or filters")
	}
}

func TestDataHistoryReadWorkersRecordFailureAndOwnOriginalRules(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	started, release := make(chan struct{}), make(chan struct{})
	d.filterRules = []mysqlsvc.MySQLDataFilterRule{{Column: "note", Mode: "equal", Value: "original_filter"}}
	s.AdvancedLoader = func(context.Context, int64, string, string, mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		close(started)
		<-release
		return &mysqlsvc.MySQLDataPage{Columns: []string{"id", "note"}, QuerySQL: "SELECT `id`,`note` FROM `db`.`orders` WHERE `note` = ?"}, nil
	}
	s.Load(ui.Services{}, d, true)
	<-started
	d.filterRules[0].Value = "later_filter"
	close(release)
	consumeTransactionPage(t, s, d)
	if len(d.tools.history) != 1 || d.tools.history[0].Failed || d.tools.history[0].Filters[0].Value != "original_filter" {
		t.Fatal("accepted query history used mutable current rules")
	}
	s.consumePages()
	s.recordDataHistory(d)
	if d.tools.historyStats.Total != 1 {
		t.Fatal("redraw recorded the same query twice")
	}
	s.AdvancedLoader = func(context.Context, int64, string, string, mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		return nil, errors.New("private_password_in_error")
	}
	s.Load(ui.Services{}, d, true)
	consumeTransactionPage(t, s, d)
	if len(d.tools.history) != 2 || !d.tools.history[0].Failed || d.tools.history[0].Kind != "SELECT" || d.tools.historyStats.Errors != 1 || strings.Contains(d.tools.history[0].Message, "private_password") {
		t.Fatal("failed page read was omitted or retained raw credentials")
	}
	if mysqlDataHistoryCanRestore(d.tools.history[0]) {
		t.Fatal("failed SELECT restored a request")
	}
}

func TestDataHistorySaveWorkerCapturesKindAndUncommittedStatus(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	started, release := make(chan struct{}), make(chan struct{})
	saveFn := s.Transactions.Save
	s.Transactions.Save = func(ctx context.Context, token *mysqlsvc.MySQLDataTransaction, p int64, db, tb string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		close(started)
		<-release
		return saveFn(ctx, token, p, db, tb, edits)
	}
	s.beginDataTransaction(ui.Services{}, d)
	consumeTransactionResult(t, s, d)
	consumeTransactionPage(t, s, d)
	draft := s.mysqlRowDraft(d, 0)
	draft.Values["note"] = mysqlsvc.MySQLDataValue{Text: "private_written_value"}
	s.saveMySQLData(ui.Services{}, d)
	<-started
	draft.Kind = "delete"
	draft.Values["note"] = mysqlsvc.MySQLDataValue{Text: "later_private_value"}
	close(release)
	select {
	case r := <-d.edit.Results:
		out := make(chan mysqlEditResult, 1)
		out <- r
		d.edit.Results = out
		s.consumeEdits(ui.Services{})
	case <-time.After(time.Second):
		t.Fatal("save executor did not complete")
	}
	entry := d.tools.history[0]
	if entry.Kind != "UPDATE" || !entry.Pending || entry.Failed || !strings.Contains(entry.Message, "未提交") || mysqlDataHistoryCanRestore(entry) {
		t.Fatal("history confused pending transaction writes with committed or replayable operations")
	}
	encoded, _ := json.Marshal(entry)
	if strings.Contains(string(encoded), "private_") {
		t.Fatal("worker history retained edited cell data")
	}
}

func TestDataHistoryRestoreRejectsWritesAndFailedSelect(t *testing.T) {
	s := New(Dependencies{})
	d := layoutTestTab()
	d.filterValueDraft = "keep"
	for _, entry := range []mysqlDataHistoryEntry{{Kind: "UPDATE", Filters: []mysqlsvc.MySQLDataFilterRule{{Column: "label", Mode: "equal", Value: "write"}}}, {Kind: "SELECT", Failed: true, Filters: []mysqlsvc.MySQLDataFilterRule{{Column: "label", Mode: "equal", Value: "failed"}}}} {
		s.restoreDataHistory(d, entry)
		if d.filterValueDraft != "keep" || d.loading {
			t.Fatal("write or failed read restored rules or replayed SQL")
		}
	}
}

func TestDataHistoryCustomReadSQLCannotRetainLiteralValuesOrComments(t *testing.T) {
	for _, query := range []string{"SELECT 'private_value'", "SELECT 1; DELETE FROM x", "SELECT 1 -- password=private_value", "SELECT 1 /* private_value */", "SELECT @private_value"} {
		if mysqlDataHistorySafeReadSQL(query) {
			t.Fatalf("unsafe loader SQL entered history: %q", query)
		}
	}
	if !mysqlDataHistorySafeReadSQL("SELECT `apostrophe'column` FROM `db`.`t` WHERE `note` = ?") {
		t.Fatal("quoted metadata identifiers rejected")
	}
}

func TestDataHistoryImportWorkerRecordsOriginalInsertWithoutInputValues(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	d.transfer.Input.Rows[0][0].Text = "987654321012345"
	s.Saver = func(context.Context, int64, string, string, []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		return &mysqlsvc.MySQLDataEditResult{Outcomes: []mysqlsvc.MySQLDataEditOutcome{{Kind: "insert"}}}, nil
	}
	s.startDataImport(ui.Services{}, d)
	select {
	case r := <-d.transfer.Results:
		out := make(chan mysqlDataTransferResult, 1)
		out <- r
		d.transfer.Results = out
		s.consumeDataTransfers(ui.Services{})
	case <-time.After(time.Second):
		t.Fatal("import did not finish")
	}
	if len(d.tools.history) != 1 || d.tools.history[0].Kind != "INSERT" || d.tools.history[0].Failed || d.tools.historyStats.Insert != 1 {
		t.Fatal("import omitted its insert request from history")
	}
	encoded, _ := json.Marshal(d.tools.history)
	if strings.Contains(string(encoded), "987654321012345") {
		t.Fatal("import history retained imported values")
	}
}

func TestDataHistoryDockShowsClassCountsErrorsAndFiltersRows(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	entries := []mysqlDataHistoryEntry{{Kind: "SELECT", SQL: "SELECT `note` FROM `db`.`orders` LIMIT ?"}, {Kind: "INSERT", SQL: "INSERT INTO `db`.`orders` (`note`) VALUES (?)"}, {Kind: "DELETE", Failed: true, SQL: "DELETE FROM `db`.`orders` WHERE `id` = ?"}}
	for _, entry := range entries {
		s.appendDataHistory(d, entry)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); ui.Column(c).Fill().Children(func() { s.dataHistoryDock(c, d) }) }, 1200, 200)
	statsLabel := fmt.Sprintf(locales.Text("SELECT：%d · INSERT：%d · UPDATE：%d · DELETE：%d · 错误：%d · 总数：%d"), 1, 1, 0, 1, 1, 3)
	if !tt.HasText(statsLabel) || !tt.HasText(entries[0].SQL) || !tt.HasText(entries[2].SQL) {
		t.Fatalf("history dock omitted query rows or class counts: %v", tt.Texts())
	}
	d.tools.historyFilter = "errors"
	tt.Frame()
	if tt.HasText(entries[0].SQL) || tt.HasText(entries[1].SQL) || !tt.HasText(entries[2].SQL) || !tt.HasText(statsLabel) {
		t.Fatal("error filter changed total counts or retained successful rows")
	}
}

func TestDataHistoryCanceledReadRecordsOriginalRequestOnce(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	started, done := make(chan struct{}), make(chan struct{})
	d.filterRules = []mysqlsvc.MySQLDataFilterRule{{Column: "note", Mode: "equal", Value: "before_cancel"}}
	s.AdvancedLoader = func(ctx context.Context, _ int64, _, _ string, _ mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		close(started)
		<-ctx.Done()
		close(done)
		return nil, ctx.Err()
	}
	s.Load(ui.Services{}, d, true)
	<-started
	d.filterRules[0].Value = "after_cancel"
	s.Cancel(d)
	<-done
	if len(d.tools.history) != 1 || !d.tools.history[0].Failed || d.tools.history[0].Filters[0].Value != "before_cancel" || d.tools.historyStats.Total != 1 {
		t.Fatal("canceled page read omitted or mutated its original history request")
	}
	s.consumePages()
	s.Cancel(d)
	if d.tools.historyStats.Total != 1 || d.tools.historyActiveRead != nil {
		t.Fatal("canceled or consumed read was counted twice")
	}
}

func TestDataHistoryModalClearCannotIndexEvictedSelection(t *testing.T) {
	s, d, _ := transactionWorkspace(t)
	s.appendDataHistory(d, mysqlDataHistoryEntry{Kind: "SELECT", SQL: "SELECT `id` FROM `db`.`orders` LIMIT ?"})
	d.tools.historyOpen = true
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.historyModal(c, d) }, 800, 650)
	if err := tt.Click(locales.Text("清除查询历史")); err != nil {
		t.Fatal(err)
	}
	if len(d.tools.history) != 0 || d.tools.historyStats.Total != 0 {
		t.Fatal("modal clear retained query entries")
	}
}
