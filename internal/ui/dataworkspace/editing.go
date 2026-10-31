package dataworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type mysqlDataSaver func(context.Context, int64, string, string, []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error)
type mysqlDataRowReader func(context.Context, int64, string, string, mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error)
type mysqlRowDraft struct {
	Key      string
	Row      int
	Kind     string
	Original *mysqlsvc.MySQLRowSnapshot
	Before   []mysqlsvc.MySQLDataCell
	Values   map[string]mysqlsvc.MySQLDataValue
}
type mysqlCellEditor struct {
	Key, Field, Text, Initial                     string
	Row                                           int
	Original                                      mysqlsvc.MySQLDataValue
	Dedicated, Open, Focused, Selected, Composing bool
}
type mysqlEditResult struct {
	History             []mysqlDataHistoryEntry
	Generation, Request uint64
	Profile             int64
	Keys                []string
	Saved               *mysqlsvc.MySQLDataEditResult
	Row                 *mysqlsvc.MySQLDataRow
	TargetRow           int
	TargetKey, Field    string
	Dedicated           bool
	Err                 error
	Verification        *mysqlsvc.MySQLDataVerification
}
type mysqlCellHit struct {
	Row, Source int
	Bounds      ui.Rect
	Control     ui.Handle
}
type mysqlDataEditState struct {
	Drafts                    map[string]*mysqlRowDraft
	Editor                    *mysqlCellEditor
	Busy, Batch, FocusGrid    bool
	Err                       string
	Zoom                      int
	Request                   uint64
	Cancel                    context.CancelFunc
	Results                   <-chan mysqlEditResult
	Uncertain                 *mysqlsvc.MySQLDataEditResult
	UncertainKeys             []string
	LeaveOpen                 bool
	LeaveNext                 func(ui.Services)
	AfterSave                 func(ui.Services)
	Hits                      []mysqlCellHit
	PointerRow, PointerSource int
	PointerActive             bool
}

func mysqlDataRowKey(t *Document, row int) string {
	if t == nil || t.page == nil || row < 0 || row >= len(t.page.Rows) {
		return ""
	}
	for key, d := range t.edit.Drafts {
		if d.Row == row && d.Kind == "insert" {
			return key
		}
	}
	if row >= len(t.page.RowRefs) || t.page.RowRefs[row] == nil {
		return ""
	}
	raw, _ := json.Marshal(t.page.RowRefs[row])
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func mysqlDataDraftForRow(t *Document, row int) *mysqlRowDraft {
	return t.edit.Drafts[mysqlDataRowKey(t, row)]
}
func mysqlDataCanEdit(t *Document, row, source int) bool {
	if t == nil || t.page == nil || t.loading || t.edit.Busy || dataTransactionUnavailable(t) || t.transfer.Busy || t.transfer.Uncertain || t.edit.Uncertain != nil || row < 0 || row >= len(t.page.Rows) || source < 0 || source >= len(t.page.ColumnMetadata) {
		return false
	}
	if mysqlsvc.MySQLDataColumnGenerated(t.page.ColumnMetadata[source]) {
		return false
	}
	if d := mysqlDataDraftForRow(t, row); d != nil {
		return d.Kind != "delete"
	}
	return row < len(t.page.RowSnapshots) && t.page.RowSnapshots[row] != nil && mysqlDataRowKey(t, row) != ""
}
func mysqlDataHasDraft(t *Document) bool {
	if t == nil {
		return false
	}
	if len(t.edit.Drafts) > 0 {
		return true
	}
	e := t.edit.Editor
	return e != nil && e.Text != e.Initial
}
func (s *State) mysqlRowDraft(t *Document, row int) *mysqlRowDraft {
	key := mysqlDataRowKey(t, row)
	if key == "" {
		return nil
	}
	if t.edit.Drafts == nil {
		t.edit.Drafts = map[string]*mysqlRowDraft{}
	}
	if d := t.edit.Drafts[key]; d != nil {
		return d
	}
	if row >= len(t.page.RowSnapshots) || t.page.RowSnapshots[row] == nil {
		return nil
	}
	d := &mysqlRowDraft{Key: key, Row: row, Kind: "update", Original: t.page.RowSnapshots[row], Before: slices.Clone(t.page.Rows[row]), Values: map[string]mysqlsvc.MySQLDataValue{}}
	t.edit.Drafts[key] = d
	return d
}
func mysqlDataValuePreview(v mysqlsvc.MySQLDataValue) mysqlsvc.MySQLDataCell {
	c := mysqlsvc.MySQLDataCell{Null: v.Null, Text: v.Text}
	if v.Default {
		c.Text = "(Default)"
	}
	if v.Binary {
		c.Text = "0x" + hex.EncodeToString(v.Bytes)
	}
	if len(c.Text) > 2048 {
		c.Text = c.Text[:2048]
		for !utf8.ValidString(c.Text) && len(c.Text) > 0 {
			c.Text = c.Text[:len(c.Text)-1]
		}
		c.Truncated = true
	}
	return c
}
func (s *State) setMySQLDataCell(services ui.Services, t *Document, row, source int, v mysqlsvc.MySQLDataValue) {
	if !mysqlDataCanEdit(t, row, source) {
		return
	}
	if t.edit.Editor != nil {
		s.finishMySQLCellEdit(t, true)
	}
	d := s.mysqlRowDraft(t, row)
	if d == nil {
		return
	}
	stageMySQLDataValue(t, d, source, v)
	t.edit.Err = ""
	services.Invalidate()
}
func (s *State) finishMySQLCellEdit(t *Document, accept bool) {
	e := t.edit.Editor
	if e == nil {
		return
	}
	t.edit.Editor = nil
	if !accept || e.Text == e.Initial {
		return
	}
	source := slices.Index(t.page.Columns, e.Field)
	if source < 0 || mysqlDataRowKey(t, e.Row) != e.Key {
		return
	}
	v := mysqlsvc.MySQLDataValue{Text: e.Text}
	if e.Original.Binary {
		raw, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(e.Text), "0x"))
		if err != nil {
			t.edit.Editor = e
			t.edit.Err = locales.Text("二进制值必须是十六进制字节")
			return
		}
		v = mysqlsvc.MySQLDataValue{Binary: true, Bytes: raw}
	}
	if len(v.Text)+len(v.Bytes) > mysqlsvc.MySQLDataEditMaxBytes {
		t.edit.Editor = e
		t.edit.Err = locales.Text("编辑内容超过 1 MiB 限制")
		return
	}
	d := s.mysqlRowDraft(t, e.Row)
	if d == nil || d.Kind == "delete" {
		return
	}
	stageMySQLDataValue(t, d, source, v)
}
func (s *State) beginMySQLCellEdit(services ui.Services, t *Document, row, source int, dedicated bool) {
	if !mysqlDataCanEdit(t, row, source) {
		return
	}
	if e := t.edit.Editor; e != nil && e.Row == row && e.Field == t.page.Columns[source] && !dedicated {
		return
	}
	previous := -1
	if t.edit.Editor != nil {
		previous = t.edit.Editor.Row
		s.finishMySQLCellEdit(t, true)
		if t.edit.Editor != nil {
			return
		}
	}
	if !t.edit.Batch {
		for _, d := range t.edit.Drafts {
			if d.Row != row && d.Kind != "delete" {
				previous = d.Row
				break
			}
		}
		if previous >= 0 && previous != row && mysqlDataHasDraft(t) {
			t.edit.AfterSave = func(next ui.Services) { s.beginMySQLCellEdit(next, t, row, source, dedicated) }
			s.saveMySQLData(services, t)
			return
		}
	}
	field := t.page.Columns[source]
	cell := t.page.Rows[row][source]
	v := mysqlsvc.MySQLDataValue{Text: cell.Text, Null: cell.Null}
	d := mysqlDataDraftForRow(t, row)
	hasFullDraft := false
	if d != nil {
		if draft, ok := d.Values[field]; ok {
			v = draft
			hasFullDraft = true
		} else if d.Kind == "insert" {
			v = mysqlsvc.MySQLDataValue{Default: true}
		}
	}
	kind := strings.ToLower(t.page.ColumnMetadata[source].DataType)
	binary := strings.Contains(kind, "blob") || kind == "binary" || kind == "varbinary" || kind == "bit"
	spatial := strings.Contains(kind, "geometry") || kind == "point" || strings.Contains(kind, "line") || strings.Contains(kind, "polygon")
	if spatial {
		t.edit.Err = locales.Text("此字段类型暂不支持编辑")
		return
	}
	dedicated = dedicated || binary
	if !hasFullDraft && (dedicated || cell.Truncated) && (d == nil || d.Kind != "insert") {
		reader := s.rowReaderFor(t)
		if reader == nil {
			t.edit.Err = locales.Text("完整字段读取不可用")
			return
		}
		s.startMySQLDataEditRead(services, t, row, source, dedicated, reader)
		return
	}
	v.Binary = binary
	s.openMySQLCellEditor(services, t, row, source, v, dedicated)
}

func (s *State) openMySQLCellEditor(services ui.Services, t *Document, row, source int, v mysqlsvc.MySQLDataValue, dedicated bool) {
	text := v.Text
	if v.Null || v.Default {
		text = ""
	}
	if v.Binary {
		text = hex.EncodeToString(v.Bytes)
	}
	field := t.page.Columns[source]
	dedicated = dedicated || mysqlDataEditorDockOpen(t)
	t.edit.Err = ""
	t.edit.Editor = &mysqlCellEditor{Key: mysqlDataRowKey(t, row), Field: field, Row: row, Text: text, Initial: text, Original: v, Dedicated: dedicated, Open: dedicated}
	t.selectedRow = row
	t.selection.Clear()
	t.selection.Add(row)
	mysqlDataClearColumnSelection(t)
	t.grid.Cells = ui.ColumnRange{Anchor: field, Cursor: field}
	services.Invalidate()
}
func (s *State) startMySQLDataEditRead(services ui.Services, t *Document, row, source int, dedicated bool, reader mysqlDataRowReader) {
	ctx, cancel := context.WithCancel(context.Background())
	t.edit.Cancel = cancel
	t.edit.Busy = true
	t.edit.Err = ""
	t.edit.Request++
	request, gen, profile := t.edit.Request, s.session().Generation, t.profileID
	ref := cloneRowRef(*t.page.RowRefs[row])
	key, field := mysqlDataRowKey(t, row), t.page.Columns[source]
	db, table := t.database, t.table
	out := make(chan mysqlEditResult, 1)
	t.edit.Results = out
	go func() {
		value, err := reader(ctx, profile, db, table, ref)
		out <- mysqlEditResult{Generation: gen, Request: request, Profile: profile, Row: value, TargetRow: row, TargetKey: key, Field: field, Dedicated: dedicated, Err: err}
		services.Invalidate()
	}()
}
func (s *State) addMySQLDataRecord(services ui.Services, t *Document) {
	if t == nil || t.page == nil || t.loading || t.edit.Busy || dataTransactionUnavailable(t) || t.transfer.Busy || t.transfer.Uncertain || t.edit.Uncertain != nil {
		return
	}
	if !t.edit.Batch && mysqlDataHasDraft(t) {
		s.finishMySQLCellEdit(t, true)
		t.edit.AfterSave = func(next ui.Services) { s.addMySQLDataRecord(next, t) }
		s.saveMySQLData(services, t)
		return
	}
	if len(t.edit.Drafts) >= mysqlsvc.MySQLDataMaxPageSize {
		return
	}
	s.finishMySQLCellEdit(t, true)
	row := len(t.page.Rows)
	values := make([]mysqlsvc.MySQLDataCell, len(t.page.Columns))
	for i := range values {
		values[i].Text = "(Default)"
	}
	t.page.Rows = append(t.page.Rows, values)
	t.page.RowRefs = append(t.page.RowRefs, nil)
	t.page.RowSnapshots = append(t.page.RowSnapshots, nil)
	if t.edit.Drafts == nil {
		t.edit.Drafts = map[string]*mysqlRowDraft{}
	}
	key := fmt.Sprintf("new:%d:%d", t.id, row)
	t.edit.Drafts[key] = &mysqlRowDraft{Key: key, Row: row, Kind: "insert", Values: map[string]mysqlsvc.MySQLDataValue{}}
	t.selectedRow = row
	t.selection.Clear()
	t.selection.Add(row)
	mysqlDataClearColumnSelection(t)
	mysqlDataScrollRecordIntoView(t, row)
	for i, col := range t.page.ColumnMetadata {
		if !mysqlsvc.MySQLDataColumnGenerated(col) && !strings.Contains(col.Extra, "auto_increment") && !t.hiddenColumns[col.Name] {
			s.beginMySQLCellEdit(services, t, row, i, false)
			break
		}
	}
}
func (s *State) deleteMySQLDataRecords(services ui.Services, t *Document, row int) {
	if t == nil || t.page == nil || t.loading || t.edit.Busy || dataTransactionUnavailable(t) || t.transfer.Busy || t.transfer.Uncertain || t.edit.Uncertain != nil {
		return
	}
	s.finishMySQLCellEdit(t, true)
	indices := []int{}
	for i := range t.page.Rows {
		if t.selection.Has(i) {
			indices = append(indices, i)
		}
	}
	if len(indices) == 0 {
		indices = append(indices, row)
	}
	for _, i := range indices {
		if d := mysqlDataDraftForRow(t, i); d != nil && d.Kind == "insert" {
			continue
		}
		if i < 0 || i >= len(t.page.RowSnapshots) || t.page.RowSnapshots[i] == nil {
			t.edit.Err = locales.Text("不能唯一定位此记录，已禁止修改和删除")
			return
		}
	}
	for _, i := range indices {
		d := s.mysqlRowDraft(t, i)
		if d == nil {
			continue
		}
		if d.Kind == "insert" {
			d.Kind = "discard_insert"
		} else {
			d.Kind = "delete"
		}
		d.Values = map[string]mysqlsvc.MySQLDataValue{}
	}
	s.removeMySQLDataDraftRows(t, func(d *mysqlRowDraft) bool { return d.Kind == "discard_insert" })
	for key, d := range t.edit.Drafts {
		if d.Kind == "discard_insert" {
			delete(t.edit.Drafts, key)
		}
	}

	if !t.edit.Batch {
		s.saveMySQLData(services, t)
	}
}
func (s *State) saveMySQLData(services ui.Services, t *Document) {
	if t == nil || t.closed || t.edit.Busy || dataTransactionUnavailable(t) || t.transfer.Busy || t.transfer.Uncertain || t.edit.Uncertain != nil || t.profileID != s.session().ProfileID || !s.session().Connected {
		return
	}
	s.finishMySQLCellEdit(t, true)
	if t.edit.Editor != nil {
		return
	}
	keys := []string{}
	for key := range t.edit.Drafts {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int { return t.edit.Drafts[a].Row - t.edit.Drafts[b].Row })
	edits := []mysqlsvc.MySQLDataEdit{}
	for _, key := range keys {
		d := t.edit.Drafts[key]
		edits = append(edits, mysqlsvc.MySQLDataEdit{Kind: d.Kind, Original: cloneSnapshot(d.Original), Values: cloneValues(d.Values)})
	}
	if len(edits) == 0 {
		return
	}
	saver := s.saverFor(t)
	if saver == nil {
		t.edit.Err = locales.Text("数据编辑服务不可用")
		t.edit.AfterSave = nil
		return
	}
	t.edit.Request++
	request, gen, profile := t.edit.Request, s.session().Generation, t.profileID
	ctx, cancel := context.WithCancel(context.Background())
	t.edit.Cancel = cancel
	t.edit.Busy = true
	t.edit.Err = ""
	out := make(chan mysqlEditResult, 1)
	t.edit.Results = out
	db, table := t.database, t.table
	history := mysqlDataWriteHistoryRequest(db, table, edits, t.transaction.token != nil)
	go func() {
		started := time.Now()
		result, err := saver(ctx, profile, db, table, edits)
		for i := range history {
			history[i].At, history[i].Duration = time.Now(), time.Since(started)
		}
		out <- mysqlEditResult{History: history, Generation: gen, Request: request, Profile: profile, Keys: keys, Saved: result, Err: err}
		services.Invalidate()
	}()
}
func (s *State) stopMySQLDataEdit(t *Document) {
	if t.edit.Cancel != nil {
		t.edit.Cancel()
	}
	t.edit.AfterSave = nil
}
func (s *State) cancelMySQLDataEdit(t *Document) {
	s.stopMySQLDataEdit(t)
	t.edit.Request++
	t.edit.Results = nil
	t.edit.Cancel = nil
	t.edit.Busy = false
}
func (s *State) discardMySQLData(t *Document) {
	if t == nil || t.edit.Busy || t.edit.Uncertain != nil {
		return
	}
	t.edit.Editor = nil
	for _, d := range t.edit.Drafts {
		if d.Kind != "insert" && d.Before != nil && d.Row < len(t.page.Rows) {
			t.page.Rows[d.Row] = d.Before
		}
	}
	s.removeMySQLDataDraftRows(t, func(d *mysqlRowDraft) bool { return d.Kind == "insert" })
	t.edit.Drafts = nil
	t.edit.Err = ""
	t.edit.AfterSave = nil
}
func (s *State) removeMySQLDataDraftRows(t *Document, remove func(*mysqlRowDraft) bool) {
	indices := []int{}
	for _, d := range t.edit.Drafts {
		if remove(d) {
			indices = append(indices, d.Row)
		}
	}
	slices.Sort(indices)
	slices.Reverse(indices)
	for _, i := range indices {
		t.page.Rows = slices.Delete(t.page.Rows, i, i+1)
		if i < len(t.page.RowRefs) {
			t.page.RowRefs = slices.Delete(t.page.RowRefs, i, i+1)
		}
		if i < len(t.page.RowSnapshots) {
			t.page.RowSnapshots = slices.Delete(t.page.RowSnapshots, i, i+1)
		}
		for _, d := range t.edit.Drafts {
			if d.Row > i {
				d.Row--
			}
		}
	}
	t.selectedRow = min(t.selectedRow, len(t.page.Rows)-1)
	t.selection.Clear()
	mysqlDataClearColumnSelection(t)
}
func (s *State) acceptMySQLDataSaved(t *Document, keys []string, saved *mysqlsvc.MySQLDataEditResult) {
	if saved == nil || len(saved.Outcomes) != len(keys) {
		t.edit.Err = locales.Text("提交结果不完整，请核验数据库")
		return
	}
	for i, key := range keys {
		d := t.edit.Drafts[key]
		if d == nil {
			continue
		}
		outcome := saved.Outcomes[i]
		if d.Kind != "delete" && outcome.Saved != nil {
			for j, name := range t.page.Columns {
				idx := slices.Index(outcome.PreviewColumns, name)
				if idx >= 0 && idx < len(outcome.Previews) {
					t.page.Rows[d.Row][j] = outcome.Previews[idx]
				}
			}
			row := d.Row
			t.page.RowRefs[row] = &outcome.Saved.Ref
			t.page.RowSnapshots[row] = outcome.Saved
			for j, name := range t.page.Columns {
				if slices.Contains(outcome.Saved.Ref.Columns, name) {
					preview := slices.Index(outcome.PreviewColumns, name)
					if preview >= 0 && preview < len(outcome.Previews) {
						continue
					}
					idx := slices.Index(outcome.Saved.Ref.Columns, name)
					value := mysqlsvc.MySQLDataValue{Text: string(outcome.Saved.Ref.Values[idx])}
					if j < len(t.page.ColumnMetadata) && mysqlDataBinaryField(t.page.ColumnMetadata[j]) {
						value = mysqlsvc.MySQLDataValue{Binary: true, Bytes: outcome.Saved.Ref.Values[idx]}
					}
					t.page.Rows[row][j] = mysqlDataValuePreview(value)
				}
			}
		}
	}
	s.removeMySQLDataDraftRows(t, func(d *mysqlRowDraft) bool { return d.Kind == "delete" })
	for _, key := range keys {
		delete(t.edit.Drafts, key)
	}
	t.edit.Editor = nil
	t.edit.Err = ""
}
func (s *State) consumeEdits(services ui.Services) {
	for _, t := range s.documents {
		if t.closed || t.edit.Results == nil || t.profileID != s.session().ProfileID {
			continue
		}
		select {
		case r := <-t.edit.Results:
			if r.Profile != s.session().ProfileID || r.Profile != t.profileID || r.Request != t.edit.Request {
				continue
			}
			s.recordDataWriteHistory(t, r.History, r.Saved, r.Err, r.Generation != s.session().Generation || !s.session().Connected)
			if t.edit.Cancel != nil {
				t.edit.Cancel()
			}
			t.edit.Cancel = nil
			t.edit.Results = nil
			t.edit.Busy = false
			if r.Generation != s.session().Generation || !s.session().Connected {
				t.edit.AfterSave = nil
				t.edit.Err = locales.Text("原 MySQL 会话已失效，请刷新表数据")
				// A write acknowledged by another generation must never replace
				// this grid or become an automatic retry of the retained draft.
				if len(r.Keys) > 0 {
					t.edit.Uncertain = cloneEditResult(r.Saved)
					if t.edit.Uncertain == nil {
						t.edit.Uncertain = &mysqlsvc.MySQLDataEditResult{}
					}
					t.edit.Uncertain.CommitUncertain = true
					t.edit.UncertainKeys = append([]string(nil), r.Keys...)
					t.edit.Err = locales.Text("提交结果仍不确定，禁止重放；请检查数据库")
				}
				continue
			}
			if r.Err != nil {
				if errors.Is(r.Err, mysqlsvc.ErrMySQLDataTransactionLost) {
					t.transaction.lost = true
					t.transaction.err = r.Err.Error()
				}
				t.edit.Err = r.Err.Error()
				t.edit.AfterSave = nil
				if r.Saved != nil && r.Saved.CommitUncertain {
					t.edit.Uncertain = r.Saved
					t.edit.UncertainKeys = r.Keys
					s.verifyMySQLDataSave(services, t)
				}
				continue
			}
			if r.Verification != nil {
				if r.Verification.Status == "applied" {
					if t.edit.Uncertain == nil || len(t.edit.Uncertain.Outcomes) != len(t.edit.UncertainKeys) {
						t.edit.Err = locales.Text("提交结果不完整，请核验数据库")
						continue
					}
					s.acceptMySQLDataSaved(t, t.edit.UncertainKeys, t.edit.Uncertain)
					t.edit.Uncertain = nil
					t.edit.UncertainKeys = nil
					t.edit.Err = locales.Text("已核验：数据更改已提交")
				} else if r.Verification.Status == "not_applied" {
					t.edit.Uncertain = nil
					t.edit.UncertainKeys = nil
					t.edit.Err = locales.Text("已核验：更改未提交，草稿已保留")
				} else {
					t.edit.Err = locales.Text("提交结果仍不确定，禁止重放；请检查数据库")
				}
				continue
			}
			if r.Field != "" {
				row := r.TargetRow
				source := -1
				if t.page != nil {
					source = slices.Index(t.page.Columns, r.Field)
				}
				if r.Row == nil || source < 0 || !mysqlDataCanEdit(t, row, source) || mysqlDataRowKey(t, row) != r.TargetKey || row >= len(t.page.RowSnapshots) || !reflect.DeepEqual(t.page.RowSnapshots[row], r.Row.Snapshot) {
					t.edit.Err = mysqlsvc.ErrMySQLDataConflict.Error()
					continue
				}
				field := r.Field
				v, ok := r.Row.Values[field]
				if !ok {
					t.edit.Err = mysqlsvc.ErrMySQLDataPage.Error()
					continue
				}
				if d := mysqlDataDraftForRow(t, row); d != nil {
					if value, ok := d.Values[field]; ok {
						v = value
					}
				}
				s.openMySQLCellEditor(services, t, row, source, v, r.Dedicated)
				continue
			}
			if r.Saved == nil || len(r.Saved.Outcomes) != len(r.Keys) {
				t.edit.Uncertain = cloneEditResult(r.Saved)
				if t.edit.Uncertain == nil {
					t.edit.Uncertain = &mysqlsvc.MySQLDataEditResult{}
				}
				t.edit.Uncertain.CommitUncertain = true
				t.edit.UncertainKeys = append([]string(nil), r.Keys...)
				t.edit.Err = locales.Text("提交结果不完整，请核验数据库")
				t.edit.AfterSave = nil
				continue
			}
			s.rememberDataTransactionSave(t, r.Keys, r.Saved)
			s.acceptMySQLDataSaved(t, r.Keys, r.Saved)
			for _, outcome := range r.Saved.Outcomes {
				if outcome.Kind == "insert" && outcome.Saved == nil {
					s.Load(services, t, true)
					break
				}
			}
			after := t.edit.AfterSave
			t.edit.AfterSave = nil
			if after != nil {
				after(services)
			}
		default:
		}
	}
}
func (s *State) verifyMySQLDataSave(services ui.Services, t *Document) {
	if dataTransactionActive(t) || t.edit.Uncertain == nil || t.edit.Busy || s.verifier() == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.edit.Cancel = cancel
	t.edit.Busy = true
	t.edit.Request++
	request, gen, profile := t.edit.Request, s.session().Generation, t.profileID
	out := make(chan mysqlEditResult, 1)
	t.edit.Results = out
	verify := s.verifier()
	db, table, result := t.database, t.table, cloneEditResult(t.edit.Uncertain)
	go func() {
		v, e := verify(ctx, profile, db, table, result)
		out <- mysqlEditResult{Generation: gen, Request: request, Profile: profile, Verification: v, Err: e}
		services.Invalidate()
	}()
}
func (s *State) GuardLeave(t *Document, next func(ui.Services)) bool {
	if t == nil || t.closed {
		return false
	}
	if t.edit.Busy || t.edit.Uncertain != nil || dataTransactionUnavailable(t) || t.transfer.Busy || t.transfer.Uncertain {
		return true
	}
	s.finishMySQLCellEdit(t, true)
	if !mysqlDataHasDraft(t) {
		return false
	}
	t.edit.LeaveOpen = true
	t.edit.LeaveNext = next
	return true
}

func stageMySQLDataValue(t *Document, d *mysqlRowDraft, source int, v mysqlsvc.MySQLDataValue) {
	field := t.page.Columns[source]
	preview := mysqlDataValuePreview(v)
	if d.Kind == "update" && source < len(d.Before) && !d.Before[source].Truncated && !v.Default && preview == d.Before[source] {
		delete(d.Values, field)
	} else {
		d.Values[field] = v
	}
	t.page.Rows[d.Row][source] = preview
	if d.Kind == "update" && len(d.Values) == 0 {
		delete(t.edit.Drafts, d.Key)
	}
}

// Pointer observation only finishes the previous editor. Selection, drag
// thresholds and edge scrolling remain owned by the native Table.
func (s *State) mysqlDataCellPointer(_ ui.Services, t *Document, row, source int, _ ui.Rect, ev ui.InputEvent) bool {
	if ev.Kind == ui.InputPointerDown && ev.Button == 0 && !t.edit.Busy {
		t.edit.PointerRow, t.edit.PointerSource = row, source
		t.edit.PointerActive = true
		s.finishMySQLCellEdit(t, true)
	} else if ev.Kind == ui.InputPointerUp {
		t.edit.PointerActive = false
	}
	return false
}

func mysqlDataEditFont(t *Document) float32 {
	return 12 * float32(mysqlDataZoom(t)) / 100
}
func (s *State) mysqlDataInlineEditor(c *ui.Context, t *Document, row, source int) bool {
	e := t.edit.Editor
	if e == nil || e.Dedicated || e.Row != row || e.Field != t.page.Columns[source] || e.Key != mysqlDataRowKey(t, row) {
		return false
	}
	services := c.Services()
	// Keep the preview's line measurement in flow. The input covers its
	// native parent cell, including padding, without changing the row size.
	font := theme.Font(c, mysqlDataEditFont(t))
	ui.Text(c, " ").SingleLine().FontSize(font).Opacity(0).Role(ui.RoleNone)
	// The native grid paints its final bottom/right pixel above children;
	// reserve one extra border pixel there so all four blue edges survive.
	input := ui.TextInputBase(c.Key("cell-editor"), &e.Text).Label(locales.Text("单元格编辑")+" "+e.Field).
		Absolute().Top(0).Right(0).Bottom(0).Left(0).MinWidth(0).
		FontSize(font).Padding(0, c.Theme().Space(2.5)-1).Radius(0).
		Background(theme.Colors(c).App).Border(1, theme.Colors(c).Focus).BorderWidth(1, 2, 2, 1).AutoFocus().ReadOnly(t.edit.Busy)
	input.OnTextKey(func(k ui.TextKey) bool {
		if e.Composing && (k.Key == ui.KeyEnter || k.Key == ui.KeyTab || k.Key == ui.KeyEscape) {
			return true
		}
		switch {
		case k.Key == ui.KeyEscape && k.Mods == 0:
			e.Text = e.Initial
			s.finishMySQLCellEdit(t, false)
			t.edit.FocusGrid = true
			return true
		case k.Key == ui.KeyEnter && k.Mods == 0:
			e.Text = k.Text()
			s.finishMySQLCellEdit(t, true)
			t.edit.FocusGrid = t.edit.Editor == nil
			return true
		case k.Key == ui.KeyTab && (k.Mods == 0 || k.Mods == ui.Shift):
			e.Text = k.Text()
			s.finishMySQLCellEdit(t, true)
			if t.edit.Editor != nil {
				return true
			}
			s.navigateMySQLCellEdit(services, t, row, e.Field, k.Mods == ui.Shift)
			return true
		}
		return false
	})
	if !e.Selected {
		input.SetTextSelection(0, utf8.RuneCountInString(e.Text))
		e.Selected = true
	}
	e.Composing = input.Composing()
	if input.Focused() {
		e.Focused = true
	} else if e.Focused && !e.Composing {
		s.finishMySQLCellEdit(t, true)
	}
	return true
}
func (s *State) navigateMySQLCellEdit(services ui.Services, t *Document, row int, field string, back bool) {
	visible := []string{}
	order := slices.Clone(t.grid.Columns.Order)
	for _, name := range t.page.Columns {
		if !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	for _, name := range order {
		source := slices.Index(t.page.Columns, name)
		if source >= 0 && !t.hiddenColumns[name] && !mysqlsvc.MySQLDataColumnGenerated(t.page.ColumnMetadata[source]) {
			visible = append(visible, name)
		}
	}
	if len(visible) == 0 {
		return
	}
	at := slices.Index(visible, field)
	if back {
		at--
	} else {
		at++
	}
	if at >= len(visible) {
		row++
		at = 0
	}
	if at < 0 {
		row--
		at = len(visible) - 1
	}
	if row < 0 || row >= len(t.page.Rows) {
		return
	}
	// Keyboard navigation may leave the viewport. A pointer edit already
	// has a visible target and must not re-anchor the list when it opens.
	mysqlDataRevealEditRow(t, row)
	s.beginMySQLCellEdit(services, t, row, slices.Index(t.page.Columns, visible[at]), false)
}

func mysqlDataRevealEditRow(t *Document, row int) {
	if row < 0 {
		return
	}
	first, last := t.grid.Visible()
	first -= t.toolbar.gridOffset
	last -= t.toolbar.gridOffset
	if row < first || row > last {
		mysqlDataScrollRecordIntoView(t, row)
	}
}
func (s *State) Modals(c *ui.Context) {
	s.dataTransactionModals(c)
	for _, t := range s.documents {
		if t.closed {
			continue
		}
		s.toolsModals(c, t)
		s.transferModals(c, t)
		s.dataFillModal(c, t)
		if e := t.edit.Editor; e != nil && e.Dedicated && !mysqlDataEditorDockOpen(t) {
			ui.Modal(c, &e.Open, func() {
				ui.Column(c).Width(640).MaxWidthPercent(95).Padding(12).Gap(8).Children(func() {
					ui.Text(c, locales.Text("单元格编辑器")+" · "+e.Field).FontSize(theme.Font(c, theme.FontSize))
					if e.Original.Binary {
						ui.Text(c, locales.Text("二进制值以十六进制编辑"))
					}
					ui.TextArea(c, &e.Text).Label(locales.Text("专用单元格编辑") + " " + e.Field).FillWidth().Height(280).AutoFocus().FontSize(theme.Font(c, 12))
					if t.edit.Err != "" {
						ui.Text(c, t.edit.Err).TextColor(theme.Colors(c).Error)
					}
					ui.Row(c).Gap(8).Children(func() {
						if ui.Button(c, locales.Text("取消单元格编辑")).Clicked() {
							s.finishMySQLCellEdit(t, false)
						}
						if ui.Button(c, locales.Text("应用单元格编辑")).Clicked() {
							s.finishMySQLCellEdit(t, true)
						}
					})
				})
			})
			if !e.Open && t.edit.Editor == e {
				s.finishMySQLCellEdit(t, false)
			}
		}
		if t.edit.LeaveOpen {
			choice := ui.AlertDialog(c, &t.edit.LeaveOpen, locales.Text("待处理记录"), locales.Text("是否将待保存的数据更改应用到数据库？"), locales.Text("应用更改"), locales.Text("放弃"), locales.Text("取消"))
			next := t.edit.LeaveNext
			if choice == 2 {
				t.edit.LeaveNext = nil
			}
			if choice == 1 {
				t.edit.LeaveNext = nil
				s.discardMySQLData(t)
				if next != nil {
					next(c.Services())
				}
			}
			if choice == 0 {
				t.edit.LeaveNext = nil
				t.edit.AfterSave = next
				s.activate(t)
				s.saveMySQLData(c.Services(), t)
			}
		}
	}
}
func (s *State) mysqlDataEditStatus(c *ui.Context, t *Document) {
	// Use the footer's existing fixed-height command row. Inserting a status
	// row above the grid moves every cell while a read is pending or fails.
	palette := theme.Colors(c)
	if t.edit.Busy || dataTransactionBusy(t) {
		ui.Spinner(c).Size(14, 14)
	}
	message, color := "", palette.Muted
	if t.edit.Err != "" {
		message, color = t.edit.Err, palette.Error
	} else if t.transaction.err != "" {
		message, color = t.transaction.err, palette.Error
	} else if dataTransactionBusy(t) {
		message = locales.Text("正在处理事务…")
	} else if t.edit.Busy {
		message = locales.Text("正在处理表数据…")
	} else if mysqlDataHasDraft(t) {
		message, color = locales.Text("有待保存的数据更改"), palette.Warning
	} else if dataTransactionActive(t) {
		message, color = locales.Text("事务进行中，更改尚未提交"), palette.Warning
	}
	if message != "" {
		ui.Text(c, message).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(color).SingleLine().Grow(1).MinWidth(0).Tooltip(message)
	}
	if t.edit.Err != "" && t.edit.Uncertain != nil && ui.Button(c, locales.Text("核验提交结果")).Height(theme.RowHeight(c, theme.ControlHeight)).Disabled(t.edit.Busy).Clicked() {
		s.verifyMySQLDataSave(c.Services(), t)
	}
}

func mysqlDataCellContains(bounds ui.Rect, x, y float32) bool {
	return x >= bounds.X && x < bounds.X+bounds.W && y >= bounds.Y && y < bounds.Y+bounds.H
}

// Element.Bounds describes the previous build, which may predate the first
// paint or a resize. Input must use the frame currently visible to the user.
func refreshMySQLDataCellHits(c *ui.Context, t *Document) {
	for i := range t.edit.Hits {
		hit := &t.edit.Hits[i]
		hit.Bounds = hit.Control.Bounds(c)
	}
}

// Route captured pointer gestures by exact cell bounds. Header and row-number
// areas remain native, and an edited cell delegates to its text input.
func (s *State) mysqlDataGridPointer(services ui.Services, t *Document, bounds ui.Rect, ev ui.InputEvent) bool {
	x, y := bounds.X+ev.X, bounds.Y+ev.Y
	for _, hit := range t.edit.Hits {
		b := hit.Bounds
		in := mysqlDataCellContains(b, x, y)
		if ev.Kind == ui.InputPointerDown && in || t.edit.PointerActive && hit.Row == t.edit.PointerRow && hit.Source == t.edit.PointerSource {
			ev.X, ev.Y = x-b.X, y-b.Y
			return s.mysqlDataCellPointer(services, t, hit.Row, hit.Source, b, ev)
		}
	}
	return false
}
