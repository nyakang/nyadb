package dataworkspace

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// The target form retains its original source/page/generation. It performs no
// I/O; the original preview executor alone obtains complete source bytes.
type dataKeyCopyChoice struct {
	page                   *contract.DataPage
	object                 contract.ObjectRef
	generation             uint64
	drafts                 draftSnapshot
	source                 *contract.RowSnapshot
	previews               []contract.DataCell
	sourceKey, destination string
	sourceTruncated        bool
	open                   bool
}

func hasDataKeyCopyDraft(d *Document) bool {
	if d != nil {
		for _, draft := range d.edit.Drafts {
			if draft.Kind == "copy" {
				return true
			}
		}
	}
	return false
}

func (s *State) canCopyDataKey(d *Document, row int) bool {
	if d == nil || d.closed || d.object.ProfileID != d.profileID || d.object.ProfileID <= 0 || !d.editable() || d.Busy() || d.HasDraft() || d.edit.Uncertain != nil || d.transfer.Uncertain || dataTransactionUnavailable(d) || dataTransactionActive(d) || !s.canUseSession(d) || d.profileID != s.session().ProfileID || !d.capabilities.KeyValues || !d.allowsDataEdit("copy") || s.runtimeFor(d).PreviewKeyCopy == nil || s.runtimeFor(d).SubmitEdits == nil || len(d.page.Rows) >= contract.DataMaxPageSize || row < 0 || row >= len(d.page.Rows) || row >= len(d.page.RowSnapshots) || d.page.RowSnapshots[row] == nil || d.page.RowSnapshots[row].Ref.Object != d.object || d.page.ReadOnlyRows[row] {
		return false
	}
	key := slices.Index(d.page.Columns, "key")
	return key >= 0 && key < len(d.page.Rows[row]) && d.page.Rows[row][key].Value != nil && d.page.Rows[row][key].Value.Kind == contract.Text
}

// CopyKey opens an original-source target form without reading or writing data.
func (s *State) CopyKey(d *Document, row int) { s.beginDataKeyCopy(d, row) }

func (s *State) beginDataKeyCopy(d *Document, row int) {
	if !s.canCopyDataKey(d, row) {
		return
	}
	s.finishMySQLCellEdit(d, false)
	drafts := captureDraft(d)
	drafts.Row = -1
	cell := d.page.Rows[row][slices.Index(d.page.Columns, "key")]
	key := cell.Value.Text
	destination := key + "-copy"
	if cell.Truncated || len(destination) > 4096 {
		destination = ""
	}
	d.keyCopy = &dataKeyCopyChoice{page: d.page, object: d.object, generation: s.sessionFor(d).Generation, drafts: drafts,
		source: cloneSnapshot(d.page.RowSnapshots[row]), previews: clonePreviews(d.page.Rows[row]), sourceKey: key, destination: destination, sourceTruncated: cell.Truncated, open: true}
	d.edit.Busy, d.edit.Err = true, ""
}

func (s *State) validDataKeyCopy(d *Document) bool {
	m := d.keyCopy
	if m == nil || d.closed || s.documents[d.id] != d || d.page != m.page || d.object != m.object || !s.canUseSession(d) || s.sessionFor(d).Generation != m.generation || !d.editable() || !d.capabilities.KeyValues || !d.allowsDataEdit("copy") || s.runtimeFor(d).PreviewKeyCopy == nil || s.runtimeFor(d).SubmitEdits == nil {
		return false
	}
	drafts := captureDraft(d)
	drafts.Row = -1
	return reflect.DeepEqual(drafts, m.drafts)
}

func (s *State) cancelDataKeyCopy(d *Document) {
	if d.keyCopy == nil {
		return
	}
	d.keyCopy = nil
	d.edit.Busy = false
	s.clearDataMutationContinuation(d)
}

func (s *State) consumeDataKeyCopy(d *Document) {
	if d.keyCopy != nil && !s.validDataKeyCopy(d) {
		s.cancelDataKeyCopy(d)
		d.edit.Err = locales.Text("原键或数据库会话已变化，请重新选择复制键")
	}
}

func validDataKeyCopyDestination(m *dataKeyCopyChoice) bool {
	return m != nil && m.destination != "" && (m.sourceTruncated || m.destination != m.sourceKey) && len(m.destination) <= 4096 && utf8.ValidString(m.destination) && !strings.ContainsRune(m.destination, 0)
}

func (s *State) stageDataKeyCopy(services ui.Services, d *Document) {
	if !s.validDataKeyCopy(d) || !validDataKeyCopyDestination(d.keyCopy) {
		if !s.validDataKeyCopy(d) {
			s.consumeDataKeyCopy(d)
		}
		return
	}
	m := d.keyCopy
	d.keyCopy = nil
	d.edit.Busy = false
	done := beginDraftChange(d)
	row := len(d.page.Rows)
	previews := clonePreviews(m.previews)
	for i, name := range d.page.Columns {
		if name == "key" {
			previews[i] = mysqlDataValuePreview(contract.DataValue{Kind: contract.Text, Text: m.destination})
		}
		if i < len(d.page.ColumnMetadata) && (d.page.ColumnMetadata[i].Generated || contract.DataColumnGenerated(d.page.ColumnMetadata[i])) {
			previews[i] = contract.DataCell{Text: "提交后生成"}
		}
	}
	d.page.Rows = append(d.page.Rows, previews)
	d.page.RowRefs = append(d.page.RowRefs, nil)
	d.page.RowSnapshots = append(d.page.RowSnapshots, nil)
	if d.edit.Drafts == nil {
		d.edit.Drafts = map[string]*mysqlRowDraft{}
	}
	key := fmt.Sprintf("copy:%d:%d", d.id, row)
	d.edit.Drafts[key] = &mysqlRowDraft{Key: key, Row: row, Kind: "copy", Original: cloneSnapshot(m.source), Values: map[string]contract.DataValue{"key": {Kind: contract.Text, Text: m.destination}}}
	d.selectedRow = row
	d.selection.Clear()
	d.selection.Add(row)
	mysqlDataClearColumnSelection(d)
	mysqlDataScrollRecordIntoView(d, row)
	done()
	s.saveMySQLData(services, d)
}

func (s *State) dataKeyCopyModal(c *ui.Context, d *Document) {
	m := d.keyCopy
	if m == nil || !s.consumeFor(d) {
		return
	}
	if !m.open {
		s.cancelDataKeyCopy(d)
		return
	}
	theme.Modal(c, &m.open, func() {
		ui.Column(c).Width(640).MaxWidthPercent(95).Gap(8).Children(func() {
			ui.Text(c, locales.Text("复制键到新键")).FontSize(theme.Font(c, theme.FontSize))
			ui.Text(c, d.database+" · "+d.table)
			ui.Text(c, locales.Text("复制源键的完整值和 Flags，保留源键。目标键必须不存在；源键变化将取消提交。"))
			sourceLabel := m.sourceKey
			if m.sourceTruncated {
				sourceLabel += "…"
			}
			ui.Text(c, locales.Text("源键")+": "+sourceLabel).FillWidth().SingleLine().MinWidth(0).Tooltip(sourceLabel)
			ui.Text(c, locales.Text("目标完整键"))
			input := ui.TextInput(c, &m.destination).Label(locales.Text("输入目标完整键")).FillWidth()
			theme.BindNativeEdit(c, input, fmt.Sprintf("data/key-copy/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return !d.closed && d.keyCopy == m && m.open }, true, false)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("取消")).Clicked() {
					m.open = false
				}
				if ui.Button(c, locales.Text("预览复制")).Disabled(!validDataKeyCopyDestination(m)).Clicked() {
					s.stageDataKeyCopy(c.Services(), d)
				}
			})
		})
	})
	if !m.open && d.keyCopy == m {
		s.cancelDataKeyCopy(d)
	}
}
