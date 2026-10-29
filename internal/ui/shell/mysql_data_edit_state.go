package shell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/locales"
)

type mysqlDataSaver func(context.Context, int64, string, string, []connections.MySQLDataEdit) (*connections.MySQLDataEditResult, error)
type mysqlDataRowReader func(context.Context, int64, string, string, connections.MySQLRowRef) (*connections.MySQLDataRow, error)
type mysqlRowDraft struct {
	Key      string
	Row      int
	Kind     string
	Original *connections.MySQLRowSnapshot
	Before   []connections.MySQLDataCell
	Values   map[string]connections.MySQLDataValue
}
type mysqlCellEditor struct {
	Key, Field, Text, Initial                     string
	Row                                           int
	Original                                      connections.MySQLDataValue
	Dedicated, Open, Focused, Selected, Composing bool
}
type mysqlEditResult struct {
	Generation, Request uint64
	Profile             int64
	Keys                []string
	Saved               *connections.MySQLDataEditResult
	Row                 *connections.MySQLDataRow
	TargetRow, Source   int
	Err                 error
	Verification        *connections.MySQLDataVerification
}
type mysqlCellHit struct {
	Row, Source int
	Bounds      ui.Rect
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
	Uncertain                 *connections.MySQLDataEditResult
	UncertainKeys             []string
	LeaveOpen                 bool
	LeaveNext                 func(ui.Services)
	AfterSave                 func(ui.Services)
	Hits                      []mysqlCellHit
	PointerRow, PointerSource int
	SelectionAnchor           int
	PointerMods               ui.Modifiers
	PointerX, PointerY        float32
	PointerActive             bool
	Drag                      bool
}

func mysqlDataRowKey(t *tab, row int) string {
	if t == nil || t.dataPage == nil || row < 0 || row >= len(t.dataPage.Rows) {
		return ""
	}
	for key, d := range t.dataEdit.Drafts {
		if d.Row == row && d.Kind == "insert" {
			return key
		}
	}
	if row >= len(t.dataPage.RowRefs) || t.dataPage.RowRefs[row] == nil {
		return ""
	}
	raw, _ := json.Marshal(t.dataPage.RowRefs[row])
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func mysqlDataDraftForRow(t *tab, row int) *mysqlRowDraft {
	return t.dataEdit.Drafts[mysqlDataRowKey(t, row)]
}
func mysqlDataCanEdit(t *tab, row, source int) bool {
	if t == nil || t.dataPage == nil || t.dataLoading || t.dataEdit.Busy || t.dataEdit.Uncertain != nil || row < 0 || row >= len(t.dataPage.Rows) || source < 0 || source >= len(t.dataPage.ColumnMetadata) {
		return false
	}
	if connections.MySQLDataColumnGenerated(t.dataPage.ColumnMetadata[source]) {
		return false
	}
	if d := mysqlDataDraftForRow(t, row); d != nil {
		return d.Kind != "delete"
	}
	return row < len(t.dataPage.RowSnapshots) && t.dataPage.RowSnapshots[row] != nil && mysqlDataRowKey(t, row) != ""
}
func mysqlDataHasDraft(t *tab) bool {
	if t == nil {
		return false
	}
	if len(t.dataEdit.Drafts) > 0 {
		return true
	}
	e := t.dataEdit.Editor
	return e != nil && e.Text != e.Initial
}
func (s *State) mysqlRowDraft(t *tab, row int) *mysqlRowDraft {
	key := mysqlDataRowKey(t, row)
	if key == "" {
		return nil
	}
	if t.dataEdit.Drafts == nil {
		t.dataEdit.Drafts = map[string]*mysqlRowDraft{}
	}
	if d := t.dataEdit.Drafts[key]; d != nil {
		return d
	}
	if row >= len(t.dataPage.RowSnapshots) || t.dataPage.RowSnapshots[row] == nil {
		return nil
	}
	d := &mysqlRowDraft{Key: key, Row: row, Kind: "update", Original: t.dataPage.RowSnapshots[row], Before: slices.Clone(t.dataPage.Rows[row]), Values: map[string]connections.MySQLDataValue{}}
	t.dataEdit.Drafts[key] = d
	return d
}
func mysqlDataValuePreview(v connections.MySQLDataValue) connections.MySQLDataCell {
	c := connections.MySQLDataCell{Null: v.Null, Text: v.Text}
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
func (s *State) setMySQLDataCell(services ui.Services, t *tab, row, source int, v connections.MySQLDataValue) {
	if !mysqlDataCanEdit(t, row, source) {
		return
	}
	if t.dataEdit.Editor != nil {
		s.finishMySQLCellEdit(t, true)
	}
	d := s.mysqlRowDraft(t, row)
	if d == nil {
		return
	}
	stageMySQLDataValue(t, d, source, v)
	t.dataEdit.Err = ""
	services.Invalidate()
}
func (s *State) finishMySQLCellEdit(t *tab, accept bool) {
	e := t.dataEdit.Editor
	if e == nil {
		return
	}
	t.dataEdit.Editor = nil
	if !accept || e.Text == e.Initial {
		return
	}
	source := slices.Index(t.dataPage.Columns, e.Field)
	if source < 0 || mysqlDataRowKey(t, e.Row) != e.Key {
		return
	}
	v := connections.MySQLDataValue{Text: e.Text}
	if e.Original.Binary {
		raw, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(e.Text), "0x"))
		if err != nil {
			t.dataEdit.Editor = e
			t.dataEdit.Err = locales.Text("二进制值必须是十六进制字节")
			return
		}
		v = connections.MySQLDataValue{Binary: true, Bytes: raw}
	}
	if len(v.Text)+len(v.Bytes) > connections.MySQLDataEditMaxBytes {
		t.dataEdit.Editor = e
		t.dataEdit.Err = locales.Text("编辑内容超过 1 MiB 限制")
		return
	}
	d := s.mysqlRowDraft(t, e.Row)
	if d == nil || d.Kind == "delete" {
		return
	}
	stageMySQLDataValue(t, d, source, v)
}
func (s *State) beginMySQLCellEdit(services ui.Services, t *tab, row, source int, dedicated bool) {
	if !mysqlDataCanEdit(t, row, source) {
		return
	}
	if e := t.dataEdit.Editor; e != nil && e.Row == row && e.Field == t.dataPage.Columns[source] && !dedicated {
		return
	}
	previous := -1
	if t.dataEdit.Editor != nil {
		previous = t.dataEdit.Editor.Row
		s.finishMySQLCellEdit(t, true)
		if t.dataEdit.Editor != nil {
			return
		}
	}
	if !t.dataEdit.Batch {
		for _, d := range t.dataEdit.Drafts {
			if d.Row != row && d.Kind != "delete" {
				previous = d.Row
				break
			}
		}
		if previous >= 0 && previous != row && mysqlDataHasDraft(t) {
			t.dataEdit.AfterSave = func(next ui.Services) { s.beginMySQLCellEdit(next, t, row, source, dedicated) }
			s.saveMySQLData(services, t)
			return
		}
	}
	key, field := mysqlDataRowKey(t, row), t.dataPage.Columns[source]
	cell := t.dataPage.Rows[row][source]
	v := connections.MySQLDataValue{Text: cell.Text, Null: cell.Null}
	if d := mysqlDataDraftForRow(t, row); d != nil {
		if draft, ok := d.Values[field]; ok {
			v = draft
		} else if d.Kind == "insert" {
			v = connections.MySQLDataValue{Default: true}
		}
	}
	kind := strings.ToLower(t.dataPage.ColumnMetadata[source].DataType)
	binary := strings.Contains(kind, "blob") || kind == "binary" || kind == "varbinary" || kind == "bit"
	spatial := strings.Contains(kind, "geometry") || kind == "point" || strings.Contains(kind, "line") || strings.Contains(kind, "polygon")
	if spatial {
		t.dataEdit.Err = locales.Text("此字段类型暂不支持编辑")
		return
	}
	if (cell.Truncated || binary) && !dedicated {
		return
	}
	if dedicated && (mysqlDataDraftForRow(t, row) == nil || mysqlDataDraftForRow(t, row).Kind != "insert") {
		reader := s.mysqlDataRowReader
		if reader == nil && s.mysqlSessionManager != nil {
			reader = s.mysqlSessionManager.ReadMySQLDataRow
		}
		if reader == nil {
			t.dataEdit.Err = locales.Text("完整字段读取不可用")
			return
		}
		s.startMySQLDataEditRead(services, t, row, source, reader)
		return
	}
	text := v.Text
	if v.Null || v.Default {
		text = ""
	}
	if v.Binary {
		text = hex.EncodeToString(v.Bytes)
	}
	t.dataEdit.Editor = &mysqlCellEditor{Key: key, Field: field, Row: row, Text: text, Initial: text, Original: v, Dedicated: dedicated, Open: dedicated}
	t.dataSelectedRow = row
	t.dataSelection.Clear()
	t.dataSelection.Add(row)
	t.dataState.Cells = ui.ColumnRange{Anchor: field, Cursor: field}
	t.dataState.ScrollIntoView(row)
	services.Invalidate()
}
func (s *State) startMySQLDataEditRead(services ui.Services, t *tab, row, source int, reader mysqlDataRowReader) {
	ctx, cancel := context.WithCancel(context.Background())
	t.dataEdit.Cancel = cancel
	t.dataEdit.Busy = true
	t.dataEdit.Err = ""
	t.dataEdit.Request++
	request, gen, profile := t.dataEdit.Request, s.mysqlSessionGeneration, t.dataProfileID
	ref := *t.dataPage.RowRefs[row]
	db, table := t.dataDatabase, t.dataTable
	out := make(chan mysqlEditResult, 1)
	t.dataEdit.Results = out
	go func() {
		value, err := reader(ctx, profile, db, table, ref)
		out <- mysqlEditResult{Generation: gen, Request: request, Profile: profile, Row: value, TargetRow: row, Source: source, Err: err}
		services.Invalidate()
	}()
}
func (s *State) addMySQLDataRecord(services ui.Services, t *tab) {
	if t == nil || t.dataPage == nil || t.dataLoading || t.dataEdit.Busy || t.dataEdit.Uncertain != nil {
		return
	}
	if !t.dataEdit.Batch && mysqlDataHasDraft(t) {
		s.finishMySQLCellEdit(t, true)
		t.dataEdit.AfterSave = func(next ui.Services) { s.addMySQLDataRecord(next, t) }
		s.saveMySQLData(services, t)
		return
	}
	if len(t.dataEdit.Drafts) >= connections.MySQLDataMaxPageSize {
		return
	}
	s.finishMySQLCellEdit(t, true)
	row := len(t.dataPage.Rows)
	values := make([]connections.MySQLDataCell, len(t.dataPage.Columns))
	for i := range values {
		values[i].Text = "(Default)"
	}
	t.dataPage.Rows = append(t.dataPage.Rows, values)
	t.dataPage.RowRefs = append(t.dataPage.RowRefs, nil)
	t.dataPage.RowSnapshots = append(t.dataPage.RowSnapshots, nil)
	if t.dataEdit.Drafts == nil {
		t.dataEdit.Drafts = map[string]*mysqlRowDraft{}
	}
	key := fmt.Sprintf("new:%d:%d", t.id, row)
	t.dataEdit.Drafts[key] = &mysqlRowDraft{Key: key, Row: row, Kind: "insert", Values: map[string]connections.MySQLDataValue{}}
	t.dataSelectedRow = row
	t.dataSelection.Clear()
	t.dataSelection.Add(row)
	t.dataState.ScrollIntoView(row)
	for i, col := range t.dataPage.ColumnMetadata {
		if !connections.MySQLDataColumnGenerated(col) && !strings.Contains(col.Extra, "auto_increment") && !t.dataHiddenColumns[col.Name] {
			s.beginMySQLCellEdit(services, t, row, i, false)
			break
		}
	}
}
func (s *State) deleteMySQLDataRecords(services ui.Services, t *tab, row int) {
	if t == nil || t.dataPage == nil || t.dataLoading || t.dataEdit.Busy || t.dataEdit.Uncertain != nil {
		return
	}
	s.finishMySQLCellEdit(t, true)
	indices := []int{}
	for i := range t.dataPage.Rows {
		if t.dataSelection.Has(i) {
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
		if i < 0 || i >= len(t.dataPage.RowSnapshots) || t.dataPage.RowSnapshots[i] == nil {
			t.dataEdit.Err = locales.Text("不能唯一定位此记录，已禁止修改和删除")
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
		d.Values = map[string]connections.MySQLDataValue{}
	}
	s.removeMySQLDataDraftRows(t, func(d *mysqlRowDraft) bool { return d.Kind == "discard_insert" })
	for key, d := range t.dataEdit.Drafts {
		if d.Kind == "discard_insert" {
			delete(t.dataEdit.Drafts, key)
		}
	}

	if !t.dataEdit.Batch {
		s.saveMySQLData(services, t)
	}
}
func (s *State) saveMySQLData(services ui.Services, t *tab) {
	if t == nil || t.dataEdit.Busy || t.dataEdit.Uncertain != nil || t.dataProfileID != s.mysqlSessionProfileID || s.mysqlSessionStatus != "已连接" {
		return
	}
	s.finishMySQLCellEdit(t, true)
	if t.dataEdit.Editor != nil {
		return
	}
	keys := []string{}
	for key := range t.dataEdit.Drafts {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int { return t.dataEdit.Drafts[a].Row - t.dataEdit.Drafts[b].Row })
	edits := []connections.MySQLDataEdit{}
	for _, key := range keys {
		d := t.dataEdit.Drafts[key]
		edits = append(edits, connections.MySQLDataEdit{Kind: d.Kind, Original: d.Original, Values: d.Values})
	}
	if len(edits) == 0 {
		return
	}
	saver := s.mysqlDataSaver
	if saver == nil && s.mysqlSessionManager != nil {
		saver = s.mysqlSessionManager.ApplyMySQLDataEdits
	}
	if saver == nil {
		t.dataEdit.Err = locales.Text("数据编辑服务不可用")
		t.dataEdit.AfterSave = nil
		return
	}
	t.dataEdit.Request++
	request, gen, profile := t.dataEdit.Request, s.mysqlSessionGeneration, t.dataProfileID
	ctx, cancel := context.WithCancel(context.Background())
	t.dataEdit.Cancel = cancel
	t.dataEdit.Busy = true
	t.dataEdit.Err = ""
	out := make(chan mysqlEditResult, 1)
	t.dataEdit.Results = out
	db, table := t.dataDatabase, t.dataTable
	go func() {
		result, err := saver(ctx, profile, db, table, edits)
		out <- mysqlEditResult{Generation: gen, Request: request, Profile: profile, Keys: keys, Saved: result, Err: err}
		services.Invalidate()
	}()
}
func (s *State) stopMySQLDataEdit(t *tab) {
	if t.dataEdit.Cancel != nil {
		t.dataEdit.Cancel()
	}
	t.dataEdit.AfterSave = nil
}
func (s *State) cancelMySQLDataEdit(t *tab) {
	s.stopMySQLDataEdit(t)
	t.dataEdit.Request++
	t.dataEdit.Results = nil
	t.dataEdit.Cancel = nil
	t.dataEdit.Busy = false
}
func (s *State) discardMySQLData(t *tab) {
	if t == nil || t.dataEdit.Busy || t.dataEdit.Uncertain != nil {
		return
	}
	t.dataEdit.Editor = nil
	for _, d := range t.dataEdit.Drafts {
		if d.Kind != "insert" && d.Before != nil && d.Row < len(t.dataPage.Rows) {
			t.dataPage.Rows[d.Row] = d.Before
		}
	}
	s.removeMySQLDataDraftRows(t, func(d *mysqlRowDraft) bool { return d.Kind == "insert" })
	t.dataEdit.Drafts = nil
	t.dataEdit.Err = ""
	t.dataEdit.AfterSave = nil
}
func (s *State) removeMySQLDataDraftRows(t *tab, remove func(*mysqlRowDraft) bool) {
	indices := []int{}
	for _, d := range t.dataEdit.Drafts {
		if remove(d) {
			indices = append(indices, d.Row)
		}
	}
	slices.Sort(indices)
	slices.Reverse(indices)
	for _, i := range indices {
		t.dataPage.Rows = slices.Delete(t.dataPage.Rows, i, i+1)
		if i < len(t.dataPage.RowRefs) {
			t.dataPage.RowRefs = slices.Delete(t.dataPage.RowRefs, i, i+1)
		}
		if i < len(t.dataPage.RowSnapshots) {
			t.dataPage.RowSnapshots = slices.Delete(t.dataPage.RowSnapshots, i, i+1)
		}
		for _, d := range t.dataEdit.Drafts {
			if d.Row > i {
				d.Row--
			}
		}
	}
	t.dataSelectedRow = min(t.dataSelectedRow, len(t.dataPage.Rows)-1)
	t.dataSelection.Clear()
}
func (s *State) acceptMySQLDataSaved(t *tab, keys []string, saved *connections.MySQLDataEditResult) {
	if saved == nil || len(saved.Outcomes) != len(keys) {
		t.dataEdit.Err = locales.Text("提交结果不完整，请核验数据库")
		return
	}
	for i, key := range keys {
		d := t.dataEdit.Drafts[key]
		if d == nil {
			continue
		}
		outcome := saved.Outcomes[i]
		if d.Kind != "delete" && outcome.Saved != nil {
			for j, name := range t.dataPage.Columns {
				idx := slices.Index(outcome.PreviewColumns, name)
				if idx >= 0 && idx < len(outcome.Previews) {
					t.dataPage.Rows[d.Row][j] = outcome.Previews[idx]
				}
			}
			row := d.Row
			t.dataPage.RowRefs[row] = &outcome.Saved.Ref
			t.dataPage.RowSnapshots[row] = outcome.Saved
			for j, name := range t.dataPage.Columns {
				if slices.Contains(outcome.Saved.Ref.Columns, name) {
					preview := slices.Index(outcome.PreviewColumns, name)
					if preview >= 0 && preview < len(outcome.Previews) {
						continue
					}
					idx := slices.Index(outcome.Saved.Ref.Columns, name)
					value := connections.MySQLDataValue{Text: string(outcome.Saved.Ref.Values[idx])}
					if j < len(t.dataPage.ColumnMetadata) && mysqlDataBinaryField(t.dataPage.ColumnMetadata[j]) {
						value = connections.MySQLDataValue{Binary: true, Bytes: outcome.Saved.Ref.Values[idx]}
					}
					t.dataPage.Rows[row][j] = mysqlDataValuePreview(value)
				}
			}
		}
	}
	s.removeMySQLDataDraftRows(t, func(d *mysqlRowDraft) bool { return d.Kind == "delete" })
	for _, key := range keys {
		delete(t.dataEdit.Drafts, key)
	}
	t.dataEdit.Editor = nil
	t.dataEdit.Err = ""
}
func (s *State) consumeMySQLDataEdits(services ui.Services) {
	for _, t := range s.tabs {
		if t.kind != tabMySQLData || t.dataEdit.Results == nil || t.dataProfileID != s.mysqlSessionProfileID {
			continue
		}
		select {
		case r := <-t.dataEdit.Results:
			if r.Generation != s.mysqlSessionGeneration || r.Profile != s.mysqlSessionProfileID || r.Profile != t.dataProfileID || r.Request != t.dataEdit.Request {
				continue
			}
			if t.dataEdit.Cancel != nil {
				t.dataEdit.Cancel()
			}
			t.dataEdit.Cancel = nil
			t.dataEdit.Results = nil
			t.dataEdit.Busy = false
			if r.Err != nil {
				t.dataEdit.Err = r.Err.Error()
				t.dataEdit.AfterSave = nil
				if r.Saved != nil && r.Saved.CommitUncertain {
					t.dataEdit.Uncertain = r.Saved
					t.dataEdit.UncertainKeys = r.Keys
					s.verifyMySQLDataSave(services, t)
				}
				continue
			}
			if r.Verification != nil {
				if r.Verification.Status == "applied" {
					s.acceptMySQLDataSaved(t, t.dataEdit.UncertainKeys, t.dataEdit.Uncertain)
					t.dataEdit.Uncertain = nil
					t.dataEdit.UncertainKeys = nil
					t.dataEdit.Err = locales.Text("已核验：数据更改已提交")
				} else if r.Verification.Status == "not_applied" {
					t.dataEdit.Uncertain = nil
					t.dataEdit.UncertainKeys = nil
					t.dataEdit.Err = locales.Text("已核验：更改未提交，草稿已保留")
				} else {
					t.dataEdit.Err = locales.Text("提交结果仍不确定，禁止重放；请检查数据库")
				}
				continue
			}
			if r.Row != nil {
				row, source := r.TargetRow, r.Source
				if row >= len(t.dataPage.RowSnapshots) || !reflect.DeepEqual(t.dataPage.RowSnapshots[row], r.Row.Snapshot) {
					t.dataEdit.Err = connections.ErrMySQLDataConflict.Error()
					continue
				}
				field := t.dataPage.Columns[source]
				v := r.Row.Values[field]
				if d := mysqlDataDraftForRow(t, row); d != nil {
					if value, ok := d.Values[field]; ok {
						v = value
					}
				}
				text := v.Text
				if v.Binary {
					text = hex.EncodeToString(v.Bytes)
				}
				t.dataEdit.Editor = &mysqlCellEditor{Key: mysqlDataRowKey(t, row), Row: row, Field: field, Text: text, Initial: text, Original: v, Dedicated: true, Open: true}
				continue
			}
			s.acceptMySQLDataSaved(t, r.Keys, r.Saved)
			for _, outcome := range r.Saved.Outcomes {
				if outcome.Kind == "insert" && outcome.Saved == nil {
					s.loadMySQLData(services, t, true)
					break
				}
			}
			after := t.dataEdit.AfterSave
			t.dataEdit.AfterSave = nil
			if after != nil {
				after(services)
			}
		default:
		}
	}
}
func (s *State) verifyMySQLDataSave(services ui.Services, t *tab) {
	if t.dataEdit.Uncertain == nil || t.dataEdit.Busy || s.mysqlSessionManager == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.dataEdit.Cancel = cancel
	t.dataEdit.Busy = true
	t.dataEdit.Request++
	request, gen, profile := t.dataEdit.Request, s.mysqlSessionGeneration, t.dataProfileID
	out := make(chan mysqlEditResult, 1)
	t.dataEdit.Results = out
	manager := s.mysqlSessionManager
	db, table, result := t.dataDatabase, t.dataTable, t.dataEdit.Uncertain
	go func() {
		v, e := manager.VerifyMySQLDataEdits(ctx, profile, db, table, result)
		out <- mysqlEditResult{Generation: gen, Request: request, Profile: profile, Verification: v, Err: e}
		services.Invalidate()
	}()
}
func (s *State) guardMySQLDataLeave(t *tab, next func(ui.Services)) bool {
	if t == nil {
		return false
	}
	if t.dataEdit.Busy || t.dataEdit.Uncertain != nil {
		return true
	}
	s.finishMySQLCellEdit(t, true)
	if !mysqlDataHasDraft(t) {
		return false
	}
	t.dataEdit.LeaveOpen = true
	t.dataEdit.LeaveNext = next
	return true
}

// RequestWorkbenchClose lets the native window veto closing until drafts are handled.
func (s *State) RequestWorkbenchClose(next func()) bool {
	for _, t := range s.tabs {
		if t.kind == tabMySQLData && s.guardMySQLDataLeave(t, func(_ ui.Services) { next() }) {
			return true
		}
	}
	return false
}
func (s *State) guardMySQLProfileLeave(id int64, next func(ui.Services)) bool {
	if s.mysqlForceClose {
		return false
	}
	for _, t := range s.tabs {
		if t.kind == tabMySQLData && (id == 0 || t.dataProfileID == id) {
			if s.guardMySQLDataLeave(t, next) {
				return true
			}
		}
	}
	return false
}

func stageMySQLDataValue(t *tab, d *mysqlRowDraft, source int, v connections.MySQLDataValue) {
	field := t.dataPage.Columns[source]
	preview := mysqlDataValuePreview(v)
	if d.Kind == "update" && source < len(d.Before) && !d.Before[source].Truncated && !v.Default && preview == d.Before[source] {
		delete(d.Values, field)
	} else {
		d.Values[field] = v
	}
	t.dataPage.Rows[d.Row][source] = preview
	if d.Kind == "update" && len(d.Values) == 0 {
		delete(t.dataEdit.Drafts, d.Key)
	}
}
