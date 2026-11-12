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

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type mysqlDataSaver func(context.Context, int64, string, string, []contract.DataEdit) (*contract.DataEditResult, error)
type mysqlDataRowReader func(context.Context, int64, string, string, contract.RowRef) (*contract.DataRow, error)
type mysqlRowDraft struct {
	GraphEndpoints *contract.DataGraphEndpoints
	Key            string
	Row            int
	Kind           string
	Original       *contract.RowSnapshot
	Before         []contract.DataCell
	Values         map[string]contract.DataValue
}
type mysqlCellEditor struct {
	Kind, InitialKind                             string
	Key, Field, Text, Initial                     string
	Row                                           int
	Original                                      contract.DataValue
	Dedicated, Open, Focused, Selected, Composing bool
	Refocus                                       bool
}
type mysqlEditResult struct {
	History             []mysqlDataHistoryEntry
	Generation, Request uint64
	Profile             int64
	Keys                []string
	Saved               *contract.DataEditResult
	Row                 *contract.DataRow
	TargetRow           int
	TargetKey, Field    string
	Dedicated           bool
	Err                 error
	Verification        *contract.DataVerification
	MutationEdits       []contract.DataEdit
}
type mysqlCellHit struct {
	Row, Source int
	Bounds      ui.Rect
	Control     ui.Handle
}
type mysqlDataEditState struct {
	LastAsync                 *contract.DataWriteObservation
	AsyncEvidenceOpen         bool
	AsyncCloseOpen            bool
	AsyncCloseApproved        bool
	AsyncStatusActive         bool
	Undo, Redo                []draftChange
	historyGroup              int
	Drafts                    map[string]*mysqlRowDraft
	Editor                    *mysqlCellEditor
	Busy, Batch, FocusGrid    bool
	Err                       string
	Zoom                      int
	Request                   uint64
	Cancel                    context.CancelFunc
	Results                   <-chan mysqlEditResult
	Uncertain                 *contract.DataEditResult
	UncertainKeys             []string
	SaveErrorOpen             bool
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
		if d.Row == row && (d.Kind == "insert" || d.Kind == "copy") {
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
	if t == nil || t.page == nil || !t.editable() || hasDataKeyCopyDraft(t) || t.loading || t.edit.Busy || dataTransactionUnavailable(t) || t.transfer.Busy || t.transfer.Uncertain || t.edit.Uncertain != nil || row < 0 || row >= len(t.page.Rows) || source < 0 || source >= len(t.page.ColumnMetadata) {
		return false
	}
	if t.page.ColumnMetadata[source].Generated || contract.DataColumnGenerated(t.page.ColumnMetadata[source]) {
		return false
	}
	if t.page.ReadOnlyRows[row] {
		return false
	}
	if d := mysqlDataDraftForRow(t, row); d != nil {
		return !dataEditDeletesRow(d.Kind) && t.allowsDataEdit(d.Kind)
	}
	return t.allowsDataEdit("update") && row < len(t.page.RowSnapshots) && t.page.RowSnapshots[row] != nil && mysqlDataRowKey(t, row) != ""
}
func mysqlDataHasDraft(t *Document) bool {
	if t == nil {
		return false
	}
	if len(t.edit.Drafts) > 0 || t.prefixDelete != nil || t.prefixSet != nil {
		return true
	}
	e := t.edit.Editor
	return e != nil && (e.Text != e.Initial || e.Kind != e.InitialKind)
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
	d := &mysqlRowDraft{Key: key, Row: row, Kind: "update", Original: cloneSnapshot(t.page.RowSnapshots[row]), Before: clonePreviews(t.page.Rows[row]), Values: map[string]contract.DataValue{}}
	t.edit.Drafts[key] = d
	return d
}
func mysqlDataValuePreview(v contract.DataValue) contract.DataCell {
	c := contract.DataCell{Null: v.Null, Text: v.Text}
	if v.Kind != "" {
		value := contract.Cell{Kind: v.Kind, Text: v.Text, Bytes: append([]byte(nil), v.Bytes...)}
		if v.Null {
			value.Kind = contract.Null
		}
		if v.Binary {
			value.Kind = contract.Binary
		}
		c.Value = &value
	}
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
func (s *State) setMySQLDataCell(services ui.Services, t *Document, row, source int, v contract.DataValue) {
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
	if !accept || (e.Text == e.Initial && e.Kind == e.InitialKind) {
		return
	}
	source := slices.Index(t.page.Columns, e.Field)
	if source < 0 || mysqlDataRowKey(t, e.Row) != e.Key {
		return
	}
	v := contract.DataValue{Kind: e.Original.Kind, Text: e.Text}
	if v.Kind == contract.Null {
		v.Kind = t.page.ColumnMetadata[source].ValueKind
	}
	if t.portable() || s.runtimeFor(t).EditValueKinds != nil {
		v.Kind = typedKind(e.Kind)
		if kinds := s.runtimeFor(t).EditValueKinds; kinds != nil && !slices.Contains(kinds, v.Kind) {
			t.edit.Editor = e
			t.edit.Err = locales.Text("此字段类型暂不支持编辑")
			return
		}
		v.Null = v.Kind == contract.Null
		v.Binary = v.Kind == contract.Binary
		if v.Null {
			v.Text = ""
		}
	}
	if v.Binary || (!t.portable() && s.runtimeFor(t).EditValueKinds == nil && e.Original.Binary) {
		raw, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(e.Text), "0x"))
		if err != nil {
			t.edit.Editor = e
			t.edit.Err = locales.Text("二进制值必须是十六进制字节")
			return
		}
		v = contract.DataValue{Kind: contract.Binary, Binary: true, Bytes: raw}
	}
	if len(v.Text)+len(v.Bytes) > contract.DataEditMaxBytes {
		t.edit.Editor = e
		t.edit.Err = locales.Text("编辑内容超过 1 MiB 限制")
		return
	}
	d := s.mysqlRowDraft(t, e.Row)
	if d == nil || dataEditDeletesRow(d.Kind) {
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
			if d.Row != row && !dataEditDeletesRow(d.Kind) {
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
	v := contract.DataValue{Text: cell.Text, Null: cell.Null}
	if cell.Value != nil {
		v.Kind, v.Text, v.Bytes, v.Binary = cell.Value.Kind, cell.Value.Text, append([]byte(nil), cell.Value.Bytes...), cell.Value.Kind == contract.Binary
	}
	d := mysqlDataDraftForRow(t, row)
	hasFullDraft := false
	if d != nil {
		if draft, ok := d.Values[field]; ok {
			v = draft
			hasFullDraft = true
		} else if d.Kind == "insert" {
			v = contract.DataValue{Default: true}
			if s.runtimeFor(t).InsertOmittedValues {
				v = contract.DataValue{Kind: t.page.ColumnMetadata[source].ValueKind, Null: true}
				if v.Kind == "" || v.Kind == contract.Null {
					v.Kind = contract.Text
				}
			}
			if s.runtimeFor(t).Selector.Dialect == "qdrant" {
				v.Kind = t.page.ColumnMetadata[source].ValueKind
			}
		}
	}
	kind := strings.ToLower(t.page.ColumnMetadata[source].DataType)
	binary := mysqlDataBinaryField(t.page.ColumnMetadata[source]) || v.Binary
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

func (s *State) openMySQLCellEditor(services ui.Services, t *Document, row, source int, v contract.DataValue, dedicated bool) {
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
	t.edit.Editor = &mysqlCellEditor{Key: mysqlDataRowKey(t, row), Field: field, Row: row, Text: text, Initial: text, Kind: typedKindLabel(v.Kind), InitialKind: typedKindLabel(v.Kind), Original: v, Dedicated: dedicated, Open: dedicated}
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
	clearDraftHistory(t)
	t.edit.Request++
	request, gen, profile := t.edit.Request, s.sessionFor(t).Generation, t.profileID
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
	if t == nil || t.page == nil || !t.editable() || !t.allowsDataEdit("insert") || hasDataKeyCopyDraft(t) || t.loading || t.edit.Busy || dataTransactionUnavailable(t) || t.transfer.Busy || t.transfer.Uncertain || t.edit.Uncertain != nil {
		return
	}
	if !t.edit.Batch && mysqlDataHasDraft(t) {
		s.finishMySQLCellEdit(t, true)
		t.edit.AfterSave = func(next ui.Services) { s.addMySQLDataRecord(next, t) }
		s.saveMySQLData(services, t)
		return
	}
	if len(t.edit.Drafts) >= contract.DataMaxPageSize {
		return
	}
	s.finishMySQLCellEdit(t, true)
	if t.edit.Editor != nil {
		return
	}
	if s.runtimeFor(t).ReadGraphEndpoints != nil {
		s.beginDataGraphEndpoints(t)
		return
	}
	s.stageMySQLDataRecord(services, t, nil)
}

func (s *State) stageMySQLDataRecord(services ui.Services, t *Document, endpoints *contract.DataGraphEndpoints) {
	done := beginDraftChange(t)
	defer done()
	row := len(t.page.Rows)
	values := make([]contract.DataCell, len(t.page.Columns))
	for i := range values {
		values[i].Text = "(Default)"
		if s.runtimeFor(t).InsertOmittedValues {
			values[i].Text = "未设置"
			if i < len(t.page.ColumnMetadata) && (t.page.ColumnMetadata[i].Generated || contract.DataColumnGenerated(t.page.ColumnMetadata[i])) {
				values[i].Text = "提交后生成"
			}
		}
		if endpoints != nil {
			switch t.page.Columns[i] {
			case "start_element_id":
				values[i].Text = endpoints.Source.ElementID
			case "end_element_id":
				values[i].Text = endpoints.Target.ElementID
			case "type":
				values[i].Text = endpoints.Object.Name
			}
		}
	}
	t.page.Rows = append(t.page.Rows, values)
	t.page.RowRefs = append(t.page.RowRefs, nil)
	t.page.RowSnapshots = append(t.page.RowSnapshots, nil)
	if t.edit.Drafts == nil {
		t.edit.Drafts = map[string]*mysqlRowDraft{}
	}
	key := fmt.Sprintf("new:%d:%d", t.id, row)
	t.edit.Drafts[key] = &mysqlRowDraft{Key: key, Row: row, Kind: "insert", Values: map[string]contract.DataValue{}, GraphEndpoints: contract.CloneDataGraphEndpoints(endpoints)}
	t.selectedRow = row
	t.selection.Clear()
	t.selection.Add(row)
	mysqlDataClearColumnSelection(t)
	mysqlDataScrollRecordIntoView(t, row)
	for i, col := range t.page.ColumnMetadata {
		if !col.Generated && !contract.DataColumnGenerated(col) && !strings.Contains(col.Extra, "auto_increment") && !t.hiddenColumns[col.Name] {
			s.beginMySQLCellEdit(services, t, row, i, false)
			break
		}
	}
}
func (s *State) deleteMySQLDataRecords(services ui.Services, t *Document, row int) {
	if t == nil || t.page == nil || !t.editable() || !t.allowsDataEdit("delete") || hasDataKeyCopyDraft(t) || t.loading || t.edit.Busy || dataTransactionUnavailable(t) || t.transfer.Busy || t.transfer.Uncertain || t.edit.Uncertain != nil {
		return
	}
	s.finishMySQLCellEdit(t, true)
	if t.edit.Editor != nil {
		return
	}
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
		if t.page.ReadOnlyRows[i] {
			t.edit.Err = locales.Text("所选记录没有写入权限")
			return
		}
		if i < 0 || i >= len(t.page.RowSnapshots) || t.page.RowSnapshots[i] == nil {
			t.edit.Err = locales.Text("不能唯一定位此记录，已禁止修改和删除")
			return
		}
	}
	if t.allowsDataEdit("detach_delete") {
		for _, index := range indices {
			if draft := mysqlDataDraftForRow(t, index); draft == nil || draft.Kind != "insert" {
				s.beginDataDeleteChoice(t, indices)
				return
			}
		}
	}
	s.stageDataDeleteIndices(services, t, indices, "delete")
}

func (s *State) stageDataDeleteIndices(services ui.Services, t *Document, indices []int, kind string) {
	done := beginDraftChange(t)
	defer done()
	for _, i := range indices {
		d := s.mysqlRowDraft(t, i)
		if d == nil {
			continue
		}
		if d.Kind == "insert" {
			d.Kind = "discard_insert"
		} else {
			d.Kind = kind
		}
		d.Values = map[string]contract.DataValue{}
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
	if t == nil || t.closed || t.edit.Busy || dataTransactionUnavailable(t) || t.transfer.Busy || t.transfer.Uncertain || t.edit.Uncertain != nil || !s.canUseSession(t) {
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
	edits := []contract.DataEdit{}
	for _, key := range keys {
		d := t.edit.Drafts[key]
		edits = append(edits, dataDraftEdit(d))
	}
	if len(edits) == 0 {
		return
	}
	if runtime := s.runtimeFor(t); requiresDataMutationPreview(runtime, edits) {
		s.previewDataMutation(services, t, runtime, keys, edits)
		return
	}
	saver := s.saverFor(t)
	if saver == nil {
		t.edit.Err = locales.Text("数据编辑服务不可用")
		t.edit.AfterSave = nil
		return
	}
	t.edit.Request++
	request, gen, profile := t.edit.Request, s.sessionFor(t).Generation, t.profileID
	ctx, cancel := context.WithCancel(context.Background())
	t.edit.Cancel = cancel
	t.edit.Busy = true
	t.edit.Err = ""
	out := make(chan mysqlEditResult, 1)
	t.edit.Results = out
	db, table := t.database, t.table
	pending := t.transaction.token != nil
	history := mysqlDataWriteHistoryRequest(db, table, edits, pending)
	if runtime := s.runtimeFor(t); runtime.WriteDescription != "" {
		for i := range history {
			history[i].SQL, history[i].Summary = runtime.WriteDescription, true
		}
	}
	complete := s.beginActivity(t, "data_write", dataActivitySQL(history))
	go func() {
		started := time.Now()
		result, err := saver(ctx, profile, db, table, edits)
		contract.CompleteActivity(complete, dataActivityOutcome(err, result, len(edits), pending))
		for i := range history {
			history[i].At, history[i].Duration = time.Now(), time.Since(started)
		}
		out <- mysqlEditResult{History: history, Generation: gen, Request: request, Profile: profile, Keys: keys, Saved: result, Err: err}
		services.Invalidate()
	}()
}
func (s *State) stopMySQLDataEdit(t *Document) {
	s.cancelKeyPrefixDelete(t)
	s.stopKeyPrefixSet(t)
	s.cancelDataGraphEndpoints(t)
	s.cancelDataKeyCopy(t)
	s.cancelDataDeleteChoice(t)
	if t.edit.Cancel != nil {
		t.edit.Cancel()
	}
	s.cancelDataMutation(t)
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
		if d.Kind != "insert" && d.Kind != "copy" && d.Before != nil && d.Row < len(t.page.Rows) {
			t.page.Rows[d.Row] = d.Before
		}
	}
	s.removeMySQLDataDraftRows(t, func(d *mysqlRowDraft) bool { return d.Kind == "insert" || d.Kind == "copy" })
	t.edit.Drafts = nil
	clearDraftHistory(t)
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
		if len(t.page.ReadOnlyRows) > 0 {
			shifted := make(map[int]bool, len(t.page.ReadOnlyRows))
			for row, readOnly := range t.page.ReadOnlyRows {
				if row < i {
					shifted[row] = readOnly
				} else if row > i {
					shifted[row-1] = readOnly
				}
			}
			t.page.ReadOnlyRows = shifted
		}
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
func (s *State) acceptMySQLDataSaved(t *Document, keys []string, saved *contract.DataEditResult) {
	clearDraftHistory(t)
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
		if !dataEditDeletesRow(d.Kind) && outcome.Saved != nil {
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
					value := contract.DataValue{Text: string(outcome.Saved.Ref.Values[idx])}
					if j < len(t.page.ColumnMetadata) && mysqlDataBinaryField(t.page.ColumnMetadata[j]) {
						value = contract.DataValue{Binary: true, Bytes: outcome.Saved.Ref.Values[idx]}
					}
					t.page.Rows[row][j] = mysqlDataValuePreview(value)
				}
			}
		}
	}
	s.removeMySQLDataDraftRows(t, func(d *mysqlRowDraft) bool { return dataEditDeletesRow(d.Kind) })
	for _, key := range keys {
		delete(t.edit.Drafts, key)
	}
	t.edit.Editor = nil
	t.edit.Err = ""
}
func (s *State) consumeEdits(services ui.Services) {
	for _, t := range s.documents {
		if !t.closed && s.consumeFor(t) {
			s.consumeKeyPrefixDelete(services, t)
			s.consumeKeyPrefixSet(services, t)
			s.consumeDataKeyCopy(t)
			s.consumeDataGraphEndpoints(t)
			s.consumeDataDeleteChoice(t)
			s.consumeDataMutation(t)
		}
		if !t.closed && s.consumeFor(t) {
			consumeDataNativeEvidence(&t.prefixEvidence)
			consumeDataNativeEvidence(&t.prefixSetObservation)
			consumeDataNativeEvidence(&t.prefixSetLedger)
			consumeDataNativeEvidence(&t.prefixSetSelectedReceipt)
			s.consumeNativeWriteReceipt(t)
		}
		if t.closed || t.edit.Results == nil || !s.consumeFor(t) {
			continue
		}
		select {
		case r := <-t.edit.Results:
			if r.Profile != s.sessionFor(t).ProfileID || r.Profile != t.profileID || r.Request != t.edit.Request {
				continue
			}
			if r.Saved != nil && r.Saved.NativeJSON != "" {
				t.nativeReceipt.JSON = r.Saved.NativeJSON
			}
			historyErr := r.Err
			if historyErr == nil && len(r.MutationEdits) > 0 && !dataMutationReceiptComplete(t.object, r.MutationEdits, r.Saved) {
				historyErr = errors.New(locales.Text("提交结果不完整，请核验数据库"))
			}
			s.recordDataWriteHistory(t, r.History, r.Saved, historyErr, r.Generation != s.sessionFor(t).Generation || !s.sessionFor(t).Connected)
			if t.edit.Cancel != nil {
				t.edit.Cancel()
			}
			t.edit.Cancel = nil
			t.edit.Results = nil
			t.edit.Busy = false
			t.edit.AsyncStatusActive = false
			if r.Saved != nil && r.Saved.Async != nil {
				observation := *r.Saved.Async
				t.edit.LastAsync = &observation
			}
			if r.Generation != s.sessionFor(t).Generation || !s.sessionFor(t).Connected {
				t.edit.AfterSave = nil
				t.edit.Err = locales.Text("原数据库会话已失效，请刷新表数据")
				// A write acknowledged by another generation must never replace
				// this grid or become an automatic retry of the retained draft.
				if len(r.Keys) > 0 {
					t.edit.Uncertain = cloneEditResult(r.Saved)
					if t.edit.Uncertain == nil {
						t.edit.Uncertain = &contract.DataEditResult{}
					}
					t.edit.Uncertain.CommitUncertain = true
					t.edit.UncertainKeys = append([]string(nil), r.Keys...)
					t.edit.Err = locales.Text("提交结果仍不确定，禁止重放；请检查数据库")
				}
				continue
			}
			if r.Err == nil && r.Saved != nil && r.Saved.Async != nil && !r.Saved.CommitConfirmed {
				t.edit.AsyncStatusActive = true
				t.edit.Uncertain = cloneEditResult(r.Saved)
				observation := *r.Saved.Async
				t.edit.LastAsync = &observation
				t.edit.UncertainKeys = append([]string(nil), r.Keys...)
				t.edit.AfterSave = nil
				if s.guardDoc == t {
					s.guardDoc = nil
				}
				t.edit.Err = asyncDataWriteStatus(r.Saved)
				continue
			}
			if r.Verification != nil && r.Verification.Recovered != nil && r.Verification.Recovered.Async != nil {
				t.edit.AsyncStatusActive = r.Err == nil
				t.edit.Uncertain = cloneEditResult(r.Verification.Recovered)
				observation := *r.Verification.Recovered.Async
				t.edit.LastAsync = &observation
			}
			if r.Err != nil {
				if errors.Is(r.Err, contract.ErrDataTransactionLost) {
					t.transaction.lost = true
					t.transaction.err = r.Err.Error()
				}
				t.edit.Err = r.Err.Error()
				if s.guardDoc == t {
					t.edit.SaveErrorOpen = true
					s.guardDoc = nil
				}
				t.edit.AfterSave = nil
				if r.Saved != nil && (r.Saved.CommitUncertain || r.Saved.Incomplete) {
					t.edit.Uncertain = r.Saved
					t.edit.UncertainKeys = r.Keys
					if !t.portable() && !(t.capabilities.Documents || t.capabilities.KeyValues) {
						s.verifyMySQLDataSave(services, t)
					}
				}
				continue
			}
			if r.Verification != nil {
				if r.Verification.Status == "applied" {
					if r.Verification.Recovered != nil {
						t.edit.Uncertain = cloneEditResult(r.Verification.Recovered)
					}
					if t.edit.Uncertain == nil || len(t.edit.Uncertain.Outcomes) != len(t.edit.UncertainKeys) {
						t.edit.Err = locales.Text("提交结果不完整，请核验数据库")
						continue
					}
					refresh := t.edit.Uncertain.RefreshRequired
					s.acceptMySQLDataSaved(t, t.edit.UncertainKeys, t.edit.Uncertain)
					t.edit.Uncertain = nil
					t.edit.UncertainKeys = nil
					if t.transaction.uncertain {
						s.closeDataTransaction(t)
					}
					t.edit.Err = locales.Text("已核验：数据更改已提交")
					if refresh {
						s.refreshDataAfterWrite(services, t)
					}
				} else if r.Verification.Status == "not_applied" {
					t.edit.Uncertain = nil
					t.edit.UncertainKeys = nil
					if t.transaction.uncertain {
						s.closeDataTransaction(t)
					}
					t.edit.Err = locales.Text("已核验：更改未提交，草稿已保留")
				} else if t.edit.Uncertain != nil && t.edit.Uncertain.Async != nil {
					t.edit.Err = asyncDataWriteStatus(t.edit.Uncertain)
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
					t.edit.Err = contract.ErrDataConflict.Error()
					continue
				}
				field := r.Field
				v, ok := r.Row.Values[field]
				if !ok {
					t.edit.Err = contract.ErrDataPage.Error()
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
			if r.Saved == nil || r.Saved.CommitUncertain || r.Saved.Incomplete || len(r.Saved.Outcomes) != len(r.Keys) || len(r.MutationEdits) > 0 && !dataMutationCommitted(t, r.Keys, r.MutationEdits, r.Saved) {
				t.edit.Uncertain = cloneEditResult(r.Saved)
				if t.edit.Uncertain == nil {
					t.edit.Uncertain = &contract.DataEditResult{}
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
			} else if r.Saved.RefreshRequired {
				s.refreshDataAfterWrite(services, t)
			}
		default:
		}
	}
}
func (s *State) refreshDataAfterWrite(services ui.Services, t *Document) {
	t.offset, t.after, t.cursorHistory = 0, nil, nil
	t.useKeyset = t.keysetEnabled()
	s.Load(services, t, true)
}
func (s *State) verifyMySQLDataSave(services ui.Services, t *Document) {
	if t != nil && t.transfer.DocumentImport && t.transfer.Uncertain {
		s.observeDocumentImport(services, t)
		return
	}
	if !s.canUseSession(t) || (dataTransactionActive(t) && !t.transaction.uncertain) || t.edit.Uncertain == nil || t.edit.Busy || s.verifierFor(t) == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.edit.Cancel = cancel
	t.edit.Busy = true
	t.edit.Request++
	request, gen, profile := t.edit.Request, s.sessionFor(t).Generation, t.profileID
	out := make(chan mysqlEditResult, 1)
	t.edit.Results = out
	verify := s.verifierFor(t)
	db, table, result := t.database, t.table, cloneEditResult(t.edit.Uncertain)
	go func() {
		v, e := verify(ctx, profile, db, table, result)
		out <- mysqlEditResult{Generation: gen, Request: request, Profile: profile, Verification: v, Err: e}
		services.Invalidate()
	}()
}

// LeavePending is presentation state for a host that suspended its own modal.
// It does not discard a draft or run a saved continuation.
func (t *Document) LeavePending() bool {
	return t != nil && !t.closed && (t.edit.LeaveOpen || t.edit.AsyncCloseOpen || t.nativeReceipt.CloseOpen || t.edit.Busy || t.edit.SaveErrorOpen || t.transaction.leaveOpen || dataTransactionBusy(t) || t.transfer.LeaveOpen || t.transfer.Busy)
}

func (s *State) GuardLeave(t *Document, next func(ui.Services)) bool {
	if t == nil || t.closed {
		return false
	}
	if t.edit.AsyncCloseApproved && !t.Busy() {
		t.edit.AsyncCloseApproved = false
		return false
	}
	if t.nativeReceipt.CloseApproved && !t.Busy() {
		t.nativeReceipt.CloseApproved = false
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

func stageMySQLDataValue(t *Document, d *mysqlRowDraft, source int, v contract.DataValue) {
	done := beginDraftChange(t)
	defer done()
	v.Bytes = append([]byte(nil), v.Bytes...)
	field := t.page.Columns[source]
	preview := mysqlDataValuePreview(v)
	if d.Kind == "update" && source < len(d.Before) && !d.Before[source].Truncated && !v.Default && samePreview(preview, d.Before[source]) {
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
	theme.BindNativeEdit(c, input, fmt.Sprintf("data/cell/%d", t.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return !t.closed && t.edit.Editor == e }, !t.edit.Busy, false)
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
		if source >= 0 && !t.hiddenColumns[name] && !t.page.ColumnMetadata[source].Generated && !contract.DataColumnGenerated(t.page.ColumnMetadata[source]) {
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
		s.asyncDataEvidenceModal(c, t)
		s.nativeWriteReceiptModal(c, t)
		s.dataMutationModal(c, t)
		s.keyPrefixEvidenceModal(c, t)
		s.keyPrefixDeleteModal(c, t)
		s.keyPrefixSetModal(c, t)
		s.keyPrefixSetEvidenceModals(c, t)
		s.dataKeyCopyModal(c, t)
		s.dataDeleteChoiceModal(c, t)
		s.dataGraphEndpointsModal(c, t)
		s.toolsModals(c, t)
		s.transferModals(c, t)
		s.dataFillModal(c, t)
		if e := t.edit.Editor; e != nil && e.Dedicated && !mysqlDataEditorDockOpen(t) {
			theme.Modal(c, &e.Open, func() {
				ui.Column(c).Width(640).MaxWidthPercent(95).Gap(8).Children(func() {
					ui.Text(c, locales.Text("单元格编辑器")+" · "+e.Field).FontSize(theme.Font(c, theme.FontSize))
					if e.Original.Binary {
						ui.Text(c, locales.Text("二进制值以十六进制编辑"))
					}
					s.typedEditorChoice(c, t, e)
					field := ui.TextArea(c, &e.Text).Label(locales.Text("专用单元格编辑") + " " + e.Field).FillWidth().Height(280).AutoFocus().FontSize(theme.Font(c, 12))
					theme.BindNativeEdit(c, field, fmt.Sprintf("data/cell/%d", t.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return !t.closed && t.edit.Editor == e }, !t.edit.Busy, false)
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
		if t.edit.SaveErrorOpen {
			ui.AlertDialog(c, &t.edit.SaveErrorOpen, locales.Text("数据编辑服务不可用"), t.edit.Err, locales.Text("确定"))
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
				t.edit.AfterSave = func(services ui.Services) {
					s.guardDoc = nil
					if next != nil {
						next(services)
					}
				}
				s.guardDoc = t
				s.saveMySQLData(c.Services(), t)
			}
		}
	}
}
func (s *State) mysqlDataEditStatus(c *ui.Context, t *Document) {
	// Use the footer's existing fixed-height command row. Inserting a status
	// row above the grid moves every cell while a read is pending or fails.
	palette := theme.Colors(c)
	if t.edit.Busy && !t.mutation.open && t.deleteChoice == nil && t.keyCopy == nil && t.prefixDelete == nil && t.prefixSet == nil || t.prefixSet != nil && t.prefixSet.pending() || t.prefixDelete != nil && (t.prefixDelete.previewResults != nil || t.prefixDelete.results != nil) || dataTransactionBusy(t) {
		ui.Spinner(c).Size(14, 14)
	}
	message, color := "", palette.Muted
	if t.transfer.DocumentImport && t.edit.AsyncStatusActive {
		message = documentImportStatus(t)
	} else if t.edit.AsyncStatusActive && t.edit.Uncertain != nil {
		message, color = asyncDataWriteStatus(t.edit.Uncertain), palette.Warning
	} else if t.edit.AsyncStatusActive && t.edit.LastAsync != nil && !mysqlDataHasDraft(t) {
		message = locales.Text("已核验：数据更改已提交")
	} else if t.edit.Err != "" {
		message, color = t.edit.Err, palette.Error
	} else if t.transaction.err != "" {
		message, color = t.transaction.err, palette.Error
	} else if dataTransactionBusy(t) {
		message = locales.Text("正在处理事务…")
	} else if t.prefixSet != nil {
		message = prefixSetProgressText(t.prefixSetProgress)
		if t.prefixSet.captureResults != nil {
			message = locales.Text("正在捕获完整前缀候选键…")
		}
	} else if t.prefixDelete != nil {
		switch {
		case t.prefixDelete.previewResults != nil:
			message = locales.Text("正在读取前缀候选键…")
		case t.prefixDelete.results != nil:
			message = locales.Text("正在提交候选键删除…")
		case t.prefixDelete.previewOpen:
			message = locales.Text("等待确认候选键删除")
		default:
			message = locales.Text("等待输入完整删除前缀")
		}
	} else if t.keyCopy != nil {
		message = locales.Text("等待输入复制目标键")
	} else if t.deleteChoice != nil {
		message = locales.Text("等待选择节点删除方式")
	} else if t.mutation.open {
		message = locales.Text("等待确认实体更改")
	} else if t.edit.Busy {
		message = locales.Text("正在处理表数据…")
	} else if s.runtimeFor(t).AsyncWrites {
		message = locales.Text("异步文档写入：JSON 完整替换或删除；提交后请观察任务结果")
		if s.runtimeFor(t).Selector.Dialect == "qdrant" {
			message = locales.Text("点批次写入：完整 point JSON 替换或删除；提交后请核验原点结果")
		}
	} else if mysqlDataHasDraft(t) {
		message, color = locales.Text("有待保存的数据更改"), palette.Warning
	} else if dataTransactionActive(t) {
		message, color = locales.Text("事务进行中，更改尚未提交"), palette.Warning
	}
	if message != "" {
		ui.Text(c, message).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(color).SingleLine().Grow(1).MinWidth(0).Tooltip(message)
	}
	if !s.runtimeFor(t).AsyncWrites && s.verifierFor(t) != nil && t.edit.Err != "" && t.edit.Uncertain != nil && ui.Button(c, locales.Text("核验提交结果")).Height(theme.RowHeight(c, theme.ControlHeight)).Disabled(t.edit.Busy).Clicked() {
		s.verifyMySQLDataSave(c.Services(), t)
	}
}

func asyncDataWriteStatus(result *contract.DataEditResult) string {
	if result == nil || result.Async == nil {
		return locales.Text("提交结果仍不确定，禁止重放；请检查数据库")
	}
	if result.Async.Status == "completed" || result.Async.Status == "acknowledged" || result.Async.Status == "interrupted" {
		status := locales.Text("批次尚未核验；草稿已保留，禁止重发")
		if result.Async.Status == "completed" {
			status = locales.Text("批次已完成，等待原点回读核验；草稿已保留，禁止重发")
		}
		return status + " · " + locales.Text("操作编号") + " " + result.Async.TaskID
	}
	status := locales.Text("任务已受理，尚未确认完成；草稿已保留，禁止重复提交")
	if result.Async.Status == "failed" || result.Async.Status == "canceled" {
		status = locales.Text("任务失败或已取消；草稿已保留，禁止重放，请检查任务证据")
	} else if result.Async.Status == "succeeded" {
		status = locales.Text("任务成功，但数据回读未能确认；草稿已保留，禁止重放")
	}
	return status + " · " + locales.Text("任务编号") + " " + result.Async.TaskID
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

func samePreview(a, b contract.DataCell) bool {
	if a.Null && b.Null {
		return true
	}
	if a.Null != b.Null || a.Text != b.Text || a.Truncated != b.Truncated {
		return false
	}
	if a.Value == nil || b.Value == nil {
		return true
	}
	return reflect.DeepEqual(a.Value, b.Value)
}
func (s *State) verifierFor(d *Document) Verifier {
	if s.Verify != nil {
		return s.Verify
	}
	return s.runtimeFor(d).Verify
}
