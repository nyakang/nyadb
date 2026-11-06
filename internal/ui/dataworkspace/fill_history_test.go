package dataworkspace

import (
	"testing"
)

func TestFillIsOneUndoAndRedoWithExistingDraft(t *testing.T) {
	s, d, _, _ := editingTestWorkspace(t)
	d.selectedRow = 0
	d.selection.Add(0)
	d.grid.ColumnSelection = []string{"note"}
	d.fill = mysqlDataFillState{Mode: "值", Text: "prior draft"}
	if err := s.fillSelectedData(d); err != nil {
		t.Fatal(err)
	}
	d.grid.ColumnSelection = []string{"note", "extra"}
	d.fill.Text = "filled"
	if err := s.fillSelectedData(d); err != nil {
		t.Fatal(err)
	}
	if len(d.edit.Undo) != 2 {
		t.Fatal("fill did not form one history group")
	}
	if !s.undoDraft(d, false) || d.page.Rows[0][1].Text != "prior draft" || d.page.Rows[0][2].Text != "same-row" {
		t.Fatal("one undo did not restore entire fill and prior draft")
	}
	if !s.undoDraft(d, true) || d.page.Rows[0][1].Text != "filled" || d.page.Rows[0][2].Text != "filled" {
		t.Fatal("one redo did not restore entire fill")
	}
}

func TestFillSequenceAllowsFinalIntegerBoundaryAndRejectsRealOverflow(t *testing.T) {
	for _, tc := range []struct {
		start, step string
		rows        int
		valid       bool
	}{
		{"9223372036854775807", "1", 1, true},
		{"-9223372036854775808", "-1", 1, true},
		{"9223372036854775807", "0", 2, true},
		{"9223372036854775807", "1", 2, false},
		{"-9223372036854775808", "-1", 2, false},
	} {
		s, d, _, _ := editingTestWorkspace(t)
		d.selectedRow = 0
		for row := 0; row < tc.rows; row++ {
			d.selection.Add(row)
		}
		d.grid.ColumnSelection = []string{"id"}
		d.fill = mysqlDataFillState{Mode: "序列", Start: tc.start, Step: tc.step}
		err := s.fillSelectedData(d)
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
		if !tc.valid && (d.page.Rows[0][0].Text != "41" || len(d.edit.Undo) != 0 || len(d.edit.Drafts) != 0) {
			t.Fatal("overflow left partial fill")
		}
	}
}
