package dataworkspace

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func completeValuesRow(d *Document) *contract.DataRow {
	cached := cloneSnapshot(d.page.RowSnapshots[0])
	cached.Mode, cached.Metadata, cached.Fingerprints = "values", "original observation", nil
	cached.Columns = append([]string(nil), d.page.Columns...)
	cached.Original = []contract.Cell{{Kind: contract.Integer, Text: "1"}, {Kind: contract.Text, Text: "stable"}, {Kind: contract.Text, Text: "猫", Truncated: true}}
	d.page.RowSnapshots[0] = cached
	d.page.Rows[0][2] = contract.DataCell{Text: "猫", Truncated: true}
	full := cloneSnapshot(cached)
	full.Original[2] = contract.Cell{Kind: contract.Text, Text: strings.Repeat("猫", 1025)}
	row := &contract.DataRow{Columns: d.page.ColumnMetadata, Snapshot: full, PreviewSnapshot: cloneSnapshot(cached), Values: map[string]contract.DataValue{}}
	for i, name := range full.Columns {
		c := full.Original[i]
		row.Values[name] = contract.DataValue{Kind: c.Kind, Text: c.Text}
	}
	return row
}

func TestCompleteValuesReadUpgradesOriginalBaselineWithoutChangingPreviewOrDraft(t *testing.T) {
	s, d, tt, _ := editingTestWorkspace(t)
	defer s.CloseAll()
	row := completeValuesRow(d)
	preview := d.page.Rows[0][2]
	s.EditCell(ui.Services{}, d, 0, d.page.Columns[1], contract.Cell{Kind: contract.Text, Text: "local draft"})
	s.RowReader = func(context.Context, int64, string, string, contract.RowRef) (*contract.DataRow, error) {
		return row, nil
	}
	s.beginMySQLCellEdit(ui.Services{}, d, 0, 2, true)
	waitEditing(t, s, d, tt)
	if d.edit.Editor == nil || d.edit.Editor.Text != row.Values[d.page.Columns[2]].Text || d.page.RowSnapshots[0].Original[2].Truncated || d.page.Rows[0][2] != preview {
		t.Fatal("complete read lost original baseline or replaced bounded grid")
	}
	draft := mysqlDataDraftForRow(d, 0)
	if draft == nil || draft.Values[d.page.Columns[1]].Text != "local draft" || draft.Original.Original[2].Truncated {
		t.Fatal("complete baseline discarded or retained truncated draft originals")
	}
	if !dataCompleteRowMatches(d.page.RowSnapshots[0], row) {
		t.Fatal("reopening another complete field rejected the already verified full baseline")
	}
	row.Snapshot.Original[2].Text = "mutated provider result"
	row.PreviewSnapshot.Original[0].Text = "foreign"
	if d.page.RowSnapshots[0].Original[2].Text == "mutated provider result" || draft.Original.Original[2].Text == "mutated provider result" {
		t.Fatal("UI cached a mutable provider snapshot")
	}
}

func TestMalformedCompleteValuesReadIsRejectedWithoutAutomaticReplay(t *testing.T) {
	for _, fault := range []string{"observation", "identity", "values", "missing", "truncated", "budget", "read_error"} {
		t.Run(fault, func(t *testing.T) {
			s, d, tt, _ := editingTestWorkspace(t)
			defer s.CloseAll()
			row := completeValuesRow(d)
			before := cloneSnapshot(d.page.RowSnapshots[0])
			switch fault {
			case "observation":
				row.PreviewSnapshot.Original[1].Text = "external change"
			case "identity":
				row.Snapshot.Ref.Table = "foreign"
			case "values":
				row.Snapshot.Original[2].Text = "unmatched payload"
			case "missing":
				delete(row.Values, d.page.Columns[2])
			case "truncated":
				row.Snapshot.Original[2].Truncated = true
			case "budget":
				row.Snapshot.Original[2].Text = strings.Repeat("x", contract.DataEditMaxBytes+1)
				v := row.Values[d.page.Columns[2]]
				v.Text = row.Snapshot.Original[2].Text
				row.Values[d.page.Columns[2]] = v
			}
			var reads atomic.Int32
			s.RowReader = func(context.Context, int64, string, string, contract.RowRef) (*contract.DataRow, error) {
				reads.Add(1)
				if fault == "read_error" {
					return nil, contract.ErrDataConflict
				}
				return row, nil
			}
			d.toolbar.dock = "文本"
			d.selectedRow = 0
			d.selection.Add(0)
			d.grid.Cells = ui.ColumnRange{Anchor: d.page.Columns[2], Cursor: d.page.Columns[2]}
			s.beginMySQLCellEdit(ui.Services{}, d, 0, 2, true)
			waitEditing(t, s, d, tt)
			for range 5 {
				tt.Frame()
			}
			if d.edit.Editor != nil || d.edit.Err == "" || reads.Load() != 1 || d.HasDraft() || !dataCompleteRowMatches(before, &contract.DataRow{Snapshot: d.page.RowSnapshots[0]}) {
				t.Fatal("malformed read changed baseline or repeated without explicit input", reads.Load())
			}
			row = completeValuesRow(d)
			s.RowReader = func(context.Context, int64, string, string, contract.RowRef) (*contract.DataRow, error) {
				reads.Add(1)
				return row, nil
			}
			s.beginMySQLCellEdit(ui.Services{}, d, 0, 2, true)
			waitEditing(t, s, d, tt)
			if reads.Load() != 2 || d.edit.Editor == nil || d.edit.Err != "" {
				t.Fatal("explicit retry did not recover the complete original editor", reads.Load(), d.edit.Err)
			}
		})
	}
}
