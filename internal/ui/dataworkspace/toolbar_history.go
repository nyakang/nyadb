package dataworkspace

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

const mysqlDataHistoryLimit = 50

// Query history belongs to this document session. Only the parameterized SQL
// and read settings are retained, never connection secrets or result rows.
type mysqlDataHistoryEntry struct {
	Kind                     string
	Failed, Pending, Summary bool
	Message                  string
	At                       time.Time
	Duration                 time.Duration
	SQL                      string
	Offset                   int64
	Limit                    int
	Rows                     int
	Filters                  []mysqlsvc.MySQLDataFilterRule
	Sorts                    []mysqlsvc.MySQLDataSortRule
}

// mysqlDataHistoryCounts tracks the whole document session independently of
// the 50 retained entries. Clear and Close reset both statistics and details.
type mysqlDataHistoryCounts struct{ Select, Insert, Update, Delete, Errors, Total uint64 }

func (s *State) appendDataHistory(t *Document, entry mysqlDataHistoryEntry) {
	if t == nil || t.closed {
		return
	}
	if entry.At.IsZero() {
		entry.At = time.Now()
	}
	if len(entry.SQL) > 64<<10 {
		entry.SQL = ""
	}
	entry.Filters = append([]mysqlsvc.MySQLDataFilterRule(nil), entry.Filters...)
	entry.Sorts = append([]mysqlsvc.MySQLDataSortRule(nil), entry.Sorts...)
	stats := &t.tools.historyStats
	switch entry.Kind {
	case "SELECT":
		stats.Select++
	case "INSERT":
		stats.Insert++
	case "UPDATE":
		stats.Update++
	case "DELETE":
		stats.Delete++
	}
	stats.Total++
	if entry.Failed {
		stats.Errors++
	}
	t.tools.history = append([]mysqlDataHistoryEntry{entry}, t.tools.history...)
	if len(t.tools.history) > mysqlDataHistoryLimit {
		t.tools.history = append([]mysqlDataHistoryEntry(nil), t.tools.history[:mysqlDataHistoryLimit]...)
	}
}

// Kept for accepted-page callers and tests. Worker callbacks use the immutable
// request entry instead, so failed reads also retain their original rules.
func (s *State) recordDataHistory(t *Document) {
	if t == nil || t.closed || t.page == nil || t.loading || t.err != "" || (t.tools.historyRecorded && t.tools.historyRequest == t.request) {
		return
	}
	t.tools.historyRequest, t.tools.historyRecorded = t.request, true
	filters, sorts := mysqlDataCurrentRules(t)
	entry := mysqlDataReadHistoryRequest(t.database, t.table, mysqlsvc.MySQLDataOptions{Offset: t.offset, Limit: t.limit, Filters: filters, Sorts: sorts})
	entry.At, entry.Duration, entry.Rows = t.page.CompletedAt, t.page.Duration, len(t.page.Rows)
	if mysqlDataHistorySafeReadSQL(t.page.QuerySQL) {
		entry.SQL = t.page.QuerySQL
		entry.Summary = false
	}
	entry.Message = locales.Text("查询成功")
	s.appendDataHistory(t, entry)
}

func mysqlDataHistoryIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
func mysqlDataHistoryTable(database, table string) string {
	return mysqlDataHistoryIdentifier(database) + "." + mysqlDataHistoryIdentifier(table)
}

// Safe SQL text comes from the bounded page query builder. Custom loaders
// cannot put string literals, comments or multiple statements into history.
func mysqlDataHistorySafeReadSQL(query string) bool {
	if len(query) > 64<<10 || !strings.HasPrefix(strings.TrimSpace(query), "SELECT ") {
		return false
	}
	quoted := false
	for i := 0; i < len(query); i++ {
		if query[i] == '`' {
			if quoted && i+1 < len(query) && query[i+1] == '`' {
				i++
				continue
			}
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		if query[i] == '\'' || query[i] == '"' || query[i] == ';' || query[i] == '#' || query[i] == '@' {
			return false
		}
		if i+1 < len(query) && (query[i] == '-' && query[i+1] == '-' || query[i] == '/' && query[i+1] == '*') {
			return false
		}
	}
	return !quoted
}

func mysqlDataReadHistoryRequest(database, table string, options mysqlsvc.MySQLDataOptions) mysqlDataHistoryEntry {
	filters := append([]mysqlsvc.MySQLDataFilterRule(nil), options.Filters...)
	if len(filters) == 0 && options.FilterMode != "" {
		filters = []mysqlsvc.MySQLDataFilterRule{{Column: options.FilterColumn, Mode: options.FilterMode, Value: options.FilterValue}}
	}
	sorts := append([]mysqlsvc.MySQLDataSortRule(nil), options.Sorts...)
	if len(sorts) == 0 && options.SortColumn != "" {
		sorts = []mysqlsvc.MySQLDataSortRule{{Column: options.SortColumn, Descending: options.SortDescending}}
	}
	query := "SELECT * FROM " + mysqlDataHistoryTable(database, table)
	var where []string
	for _, f := range filters {
		column := mysqlDataHistoryIdentifier(f.Column)
		op := map[string]string{"equal": " = ?", "not_equal": " <> ?", "less": " < ?", "greater": " > ?", "like": " LIKE ?", "not_like": " NOT LIKE ?", "is_null": " IS NULL", "is_not_null": " IS NOT NULL"}[f.Mode]
		if f.Mode == "contains" {
			where = append(where, "LOCATE(?, "+column+") > 0")
		} else if f.Mode == "not_contains" {
			where = append(where, "LOCATE(?, "+column+") = 0")
		} else if op != "" {
			where = append(where, column+op)
		}
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	if len(sorts) > 0 {
		parts := make([]string, len(sorts))
		for i, sort := range sorts {
			parts[i] = mysqlDataHistoryIdentifier(sort.Column) + " ASC"
			if sort.Descending {
				parts[i] = mysqlDataHistoryIdentifier(sort.Column) + " DESC"
			}
		}
		query += " ORDER BY " + strings.Join(parts, ",")
	}
	query += " LIMIT ? OFFSET ?"
	return mysqlDataHistoryEntry{At: time.Now(), Kind: "SELECT", SQL: query, Summary: true, Offset: options.Offset, Limit: options.Limit, Filters: filters, Sorts: sorts}
}

func mysqlDataWriteHistoryRequest(database, table string, edits []mysqlsvc.MySQLDataEdit, pending bool) []mysqlDataHistoryEntry {
	entries := make([]mysqlDataHistoryEntry, 0, len(edits))
	for _, edit := range edits {
		kind := strings.ToUpper(edit.Kind)
		if kind != "INSERT" && kind != "UPDATE" && kind != "DELETE" {
			continue
		}
		names := make([]string, 0, len(edit.Values))
		for name := range edit.Values {
			names = append(names, name)
		}
		slices.Sort(names)
		query := kind + " " + mysqlDataHistoryTable(database, table)
		if kind == "INSERT" {
			columns, values := make([]string, len(names)), make([]string, len(names))
			for i, name := range names {
				columns[i] = mysqlDataHistoryIdentifier(name)
				values[i] = "?"
			}
			query = "INSERT INTO " + mysqlDataHistoryTable(database, table) + " (" + strings.Join(columns, ",") + ") VALUES (" + strings.Join(values, ",") + ")"
		}
		if kind == "UPDATE" {
			assignments := make([]string, len(names))
			for i, name := range names {
				assignments[i] = mysqlDataHistoryIdentifier(name) + " = ?"
			}
			query += " SET " + strings.Join(assignments, ",")
		}
		if kind == "DELETE" {
			query = "DELETE FROM " + mysqlDataHistoryTable(database, table)
		}
		if kind != "INSERT" {
			var keys []string
			if edit.Original != nil {
				for _, name := range edit.Original.Ref.Columns {
					keys = append(keys, mysqlDataHistoryIdentifier(name)+" = ?")
				}
			}
			if len(keys) == 0 {
				keys = []string{"?"}
			}
			query += " WHERE " + strings.Join(keys, " AND ")
		}
		entries = append(entries, mysqlDataHistoryEntry{At: time.Now(), Kind: kind, SQL: query, Summary: true, Pending: pending, Rows: 1})
	}
	return entries
}

func mysqlDataHistoryError(err error, stale, incomplete, uncertain bool) string {
	if stale {
		return locales.Text("会话已失效")
	}
	if uncertain {
		return locales.Text("提交结果不确定")
	}
	if incomplete {
		return locales.Text("返回结果不完整")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return locales.Text("操作已取消或超时")
	}
	if errors.Is(err, mysqlsvc.ErrMySQLDataConflict) {
		return locales.Text("记录冲突")
	}
	if errors.Is(err, mysqlsvc.ErrMySQLDataReadOnly) {
		return locales.Text("表或记录不可编辑")
	}
	if errors.Is(err, mysqlsvc.ErrMySQLDataTransactionLost) || errors.Is(err, mysqlsvc.ErrMySQLNotConnected) {
		return locales.Text("连接已失效")
	}
	return locales.Text("数据库操作失败")
}

func (s *State) recordDataPageHistory(t *Document, result mysqlDataResult, stale bool) {
	t.tools.historyActiveRead = nil
	if result.History.At.IsZero() {
		return
	}
	entry := result.History
	entry.Failed = result.err != nil || result.page == nil || stale
	if entry.Failed {
		entry.Message = mysqlDataHistoryError(result.err, stale, result.err == nil && result.page == nil, false)
		entry.Rows = 0
	} else {
		entry.Message = locales.Text("查询成功")
		entry.Rows = len(result.page.Rows)
		if mysqlDataHistorySafeReadSQL(result.page.QuerySQL) {
			entry.SQL = result.page.QuerySQL
			entry.Summary = false
		}
	}
	s.appendDataHistory(t, entry)
}

func (s *State) recordDataWriteHistory(t *Document, entries []mysqlDataHistoryEntry, result *mysqlsvc.MySQLDataEditResult, err error, stale bool) {
	if len(entries) == 0 {
		return
	}
	uncertain := result != nil && result.CommitUncertain
	incomplete := err == nil && (result == nil || len(result.Outcomes) != len(entries))
	failed := err != nil || stale || incomplete || uncertain
	message := locales.Text("更改已提交")
	if entries[0].Pending {
		message = locales.Text("更改已应用，事务未提交")
	}
	if failed {
		message = mysqlDataHistoryError(err, stale, incomplete, uncertain)
		if len(entries) > 1 {
			message = locales.Text("批次失败") + " · " + message
		}
	}
	for _, entry := range entries {
		entry.Message, entry.Failed = message, failed
		if failed {
			entry.Rows = 0
		}
		s.appendDataHistory(t, entry)
	}
}

func mysqlDataHistoryCanRestore(entry mysqlDataHistoryEntry) bool {
	return (entry.Kind == "" || entry.Kind == "SELECT") && !entry.Failed
}
func mysqlDataHistoryVisible(t *Document) []int {
	indices := make([]int, 0, len(t.tools.history))
	for i, e := range t.tools.history {
		if t.tools.historyFilter == "" || t.tools.historyFilter == "all" || t.tools.historyFilter == "errors" && e.Failed || t.tools.historyFilter == e.Kind {
			indices = append(indices, i)
		}
	}
	return indices
}
func mysqlDataHistoryMessage(entry mysqlDataHistoryEntry) string {
	message := entry.Message
	if message == "" {
		message = locales.Text("查询成功")
		if entry.Failed {
			message = locales.Text("数据库操作失败")
		}
	}
	if entry.Summary {
		message = locales.Text("请求概要") + " · " + message
	}
	return message
}
func (s *State) clearDataHistory(t *Document) {
	t.tools.history = nil
	t.tools.historyChoice = ""
	t.tools.historyFilter = "all"
	t.tools.historyStats = mysqlDataHistoryCounts{}
	t.tools.historyRequest, t.tools.historyRecorded = 0, false
	t.toolbar.historyRow = -1
}

func mysqlDataRestoreRuleDrafts(t *Document, filters []mysqlsvc.MySQLDataFilterRule, sorts []mysqlsvc.MySQLDataSortRule) {
	t.filterColumnDraft, t.filterModeDraft, t.filterValueDraft = "选择字段", "包含", ""
	t.filterExtraDraft, t.sortDraft = nil, nil
	for i, f := range filters {
		d := mysqlDataRuleDraft{Column: f.Column, Mode: mysqlDataFilterModeLabel(f.Mode), Value: f.Value}
		if i == 0 {
			t.filterColumnDraft, t.filterModeDraft, t.filterValueDraft = d.Column, d.Mode, d.Value
		} else {
			t.filterExtraDraft = append(t.filterExtraDraft, d)
		}
	}
	for _, rule := range sorts {
		direction := "升序"
		if rule.Descending {
			direction = "降序"
		}
		t.sortDraft = append(t.sortDraft, mysqlDataSortDraft{Column: rule.Column, Direction: direction})
	}
}

func (s *State) restoreDataHistory(t *Document, entry mysqlDataHistoryEntry) {
	if !mysqlDataHistoryCanRestore(entry) {
		return
	}
	if t == nil || t.closed || t.page == nil || !mysqlDataRulesAvailable(t, entry.Filters, entry.Sorts) {
		s.setStatus(locales.Text("历史中的筛选或排序字段已不可用"))
		return
	}
	mysqlDataRestoreRuleDrafts(t, entry.Filters, entry.Sorts)
	t.rulesExpanded = true
	t.ruleError = ""
	t.tools.historyOpen = false
	s.setStatus(locales.Text("历史规则已恢复；请点击应用筛选 & 排序执行查询"))
}

func mysqlDataHistoryLabel(entry mysqlDataHistoryEntry, index int) string {
	kind := entry.Kind
	if kind == "" {
		kind = "SELECT"
	}
	label := fmt.Sprintf(locales.Text("%d. %s · 第 %d–%d 行 · 筛选 %d · 排序 %d"), index+1,
		entry.At.Format("15:04:05"), entry.Offset+1, entry.Offset+int64(entry.Rows), len(entry.Filters), len(entry.Sorts))
	return kind + " · " + label + " · " + mysqlDataHistoryMessage(entry)
}

func (s *State) historyMenu(c *ui.Context, t *Document, m *ui.Menu) {
	if m.Item(locales.Text("查看查询历史…")).Disabled(len(t.tools.history) == 0).Chosen() {
		t.tools.historyOpen = true
		t.tools.historyChoice = mysqlDataHistoryLabel(t.tools.history[0], 0)
	}
	m.Submenu(locales.Text("恢复历史规则"), func(sub *ui.Menu) {
		if len(t.tools.history) == 0 {
			sub.Item(locales.Text("本会话尚无成功查询")).Disabled(true)
		}
		for i, entry := range t.tools.history[:min(10, len(t.tools.history))] {
			if sub.Item(mysqlDataHistoryLabel(entry, i)).Disabled(t.page == nil || t.loading || !mysqlDataHistoryCanRestore(entry)).Chosen() {
				s.restoreDataHistory(t, entry)
			}
		}
	})
	if m.Item(locales.Text("清除查询历史")).Disabled(len(t.tools.history) == 0).Chosen() {
		s.clearDataHistory(t)
	}
}

func mysqlDataHistoryRules(entry mysqlDataHistoryEntry) string {
	parts := make([]string, 0, len(entry.Filters)+len(entry.Sorts))
	for _, rule := range entry.Filters {
		line := rule.Column + " " + locales.Text(mysqlDataFilterModeLabel(rule.Mode))
		if rule.Mode != "is_null" && rule.Mode != "is_not_null" {
			line += " " + rule.Value
		}
		parts = append(parts, line)
	}
	for _, rule := range entry.Sorts {
		direction := locales.Text("升序")
		if rule.Descending {
			direction = locales.Text("降序")
		}
		parts = append(parts, rule.Column+" "+direction)
	}
	return strings.Join(parts, "\n")
}

func (s *State) historyModal(c *ui.Context, t *Document) {
	ui.Modal(c, &t.tools.historyOpen, func() {
		ui.Column(c).Width(650).MaxWidthPercent(95).Padding(18).Gap(10).Children(func() {
			ui.Text(c, locales.Text("查询历史")).Bold().FontSize(theme.Font(c, 15))
			ui.Text(c, locales.Text("仅保留此文档会话最近 50 次操作。仅成功查询可恢复规则，写操作不会重放。")).FontSize(theme.Font(c, 12))
			choices := make([]string, len(t.tools.history))
			selected := -1
			for i, entry := range t.tools.history {
				choices[i] = mysqlDataHistoryLabel(entry, i)
				if choices[i] == t.tools.historyChoice {
					selected = i
				}
			}
			if selected < 0 && len(choices) > 0 {
				selected, t.tools.historyChoice = 0, choices[0]
			}
			ui.Select(c, &t.tools.historyChoice, choices).Label(locales.Text("历史查询")).FillWidth()
			for i, choice := range choices {
				if choice == t.tools.historyChoice {
					selected = i
				}
			}
			if selected >= 0 {
				entry := t.tools.history[selected]
				ui.Text(c, mysqlDataHistoryMessage(entry))
				if entry.Kind == "" || entry.Kind == "SELECT" {
					ui.Text(c, fmt.Sprintf(locales.Text("每页 %d 行 · 返回 %d 行 · 用时 %s"), entry.Limit, entry.Rows, entry.Duration.Round(time.Millisecond)))
				}
				text := entry.SQL
				ui.TextArea(c, &text).Label(locales.Text("历史参数化 SQL")).ReadOnly(true).FillWidth().Height(100)
				if entry.Kind == "" || entry.Kind == "SELECT" {
					rules := mysqlDataHistoryRules(entry)
					ui.TextArea(c, &rules).Label(locales.Text("历史筛选与排序规则")).ReadOnly(true).FillWidth().Height(100)
				}
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("清除查询历史")).Clicked() {
					s.clearDataHistory(t)
				}
				if ui.Button(c, locales.Text("恢复规则")).Disabled(selected < 0 || selected >= len(t.tools.history) || t.loading || t.page == nil || !mysqlDataHistoryCanRestore(t.tools.history[selected])).Clicked() {
					s.restoreDataHistory(t, t.tools.history[selected])
				}
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					t.tools.historyOpen = false
				}
			})
		})
	})
}
