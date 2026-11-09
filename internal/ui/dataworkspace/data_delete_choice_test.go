package dataworkspace

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestDataNodeDeleteChoiceCancelKeepsMixedOriginalDraftsAndUndo(t *testing.T) {
	for _, escape := range []bool{false, true} {
		s, d, _ := mutationWorkspace(t)
		s.addMySQLDataRecord(ui.Services{}, d)
		s.finishMySQLCellEdit(d, false)
		s.setMySQLDataCell(ui.Services{}, d, 0, 1, contract.DataValue{Text: "original draft"})
		d.selection.Clear()
		d.selection.Add(0)
		d.selection.Add(3)
		before := captureDraft(d)
		undo := len(d.edit.Undo)
		s.deleteMySQLDataRecords(ui.Services{}, d, 0)
		if d.deleteChoice == nil || !d.edit.Busy || !d.LeavePending() || s.CanSave(d) || !s.GuardLeave(d, nil) || !reflect.DeepEqual(before, captureDraft(d)) {
			t.Fatal("deletion choice changed original drafts or lost its guard")
		}
		tt := ui.NewTester(func(c *ui.Context) { s.Modals(c) }, 720, 520)
		tt.Frame()
		if !tt.HasText(locales.Text("选择节点删除方式")) {
			t.Fatal("explicit deletion decision missing")
		}
		if escape {
			tt.Key(0, ui.KeyEscape)
			tt.Frame()
		} else {
			if err := tt.Click(locales.Text("取消")); err != nil {
				t.Fatal(err)
			}
		}
		if d.deleteChoice != nil || d.edit.Busy || !reflect.DeepEqual(before, captureDraft(d)) || len(d.edit.Undo) != undo {
			t.Fatal("cancel removed local insert or changed original updates")
		}
		s.CloseAll()
	}
}
func TestDataNodeDeleteChoiceStagesOnlyCapturedRowsAndExplicitKind(t *testing.T) {
	for _, kind := range []string{"delete", "detach_delete"} {
		s, d, _ := mutationWorkspace(t)
		d.selection.Clear()
		d.selection.Add(0)
		d.selection.Add(1)
		s.deleteMySQLDataRecords(ui.Services{}, d, 0)
		if d.deleteChoice == nil {
			t.Fatal("node delete bypassed explicit decision")
		}
		// A later UI selection cannot redirect the captured operation.
		d.selectedRow = 2
		d.selection.Clear()
		d.selection.Add(2)
		s.chooseDataDeleteKind(ui.Services{}, d, kind)
		if d.deleteChoice != nil || d.edit.Busy || len(d.edit.Drafts) != 2 || mysqlDataDraftForRow(d, 2) != nil {
			t.Fatal("choice adopted later selection")
		}
		for _, draft := range d.edit.Drafts {
			if draft.Kind != kind || len(draft.Values) != 0 {
				t.Fatal("explicit kind or original selection lost")
			}
		}
		if mysqlDataCanEdit(d, 0, 1) || mysqlDataCanDeleteSelection(d, 0) {
			t.Fatal("deletion draft still editable")
		}
		if !s.undoDraft(d, false) || len(d.edit.Drafts) != 0 {
			t.Fatal("deletion decision not in draft history")
		}
		if !s.undoDraft(d, true) || len(d.edit.Drafts) != 2 {
			t.Fatal("redo lost captured deletion rows")
		}
		s.CloseAll()
	}
}
func TestDataNodeDeleteChoiceOnlyNewRowsAreDiscardedLocally(t *testing.T) {
	s, d, _ := mutationWorkspace(t)
	s.addMySQLDataRecord(ui.Services{}, d)
	s.finishMySQLCellEdit(d, false)
	s.deleteMySQLDataRecords(ui.Services{}, d, 3)
	if d.deleteChoice != nil || d.edit.Busy || len(d.page.Rows) != 3 || d.HasDraft() {
		t.Fatal("local insert discard opened native deletion or retained draft")
	}
}
func TestDataNodeDeleteChoiceParkGenerationDraftChangesStopAndClose(t *testing.T) {
	for _, change := range []string{"park", "generation", "values", "fingerprints", "stop", "close"} {
		t.Run(change, func(t *testing.T) {
			s, d, active := mutationWorkspace(t)
			s.setMySQLDataCell(ui.Services{}, d, 0, 2, contract.DataValue{Binary: true, Bytes: []byte("original")})
			s.deleteMySQLDataRecords(ui.Services{}, d, 0)
			if d.deleteChoice == nil {
				t.Fatal("choice missing")
			}
			switch change {
			case "park":
				active.ProfileID = 99
				s.Consume(ui.Services{})
				if d.deleteChoice == nil || !d.edit.Busy {
					t.Fatal("parked choice consumed")
				}
				active.ProfileID = d.profileID
			case "generation":
				active.Generation++
			case "values":
				for _, draft := range d.edit.Drafts {
					draft.Values["extra"].Bytes[0] = 'X'
				}
			case "fingerprints":
				*d.page.RowSnapshots[0].Fingerprints[0] = "changed"
			case "stop":
				s.stopMySQLDataEdit(d)
			case "close":
				s.Close(d)
				s.Close(d)
				if d.deleteChoice != nil || d.page != nil {
					t.Fatal("close retained original choice")
				}
				return
			}
			if change == "park" {
				s.cancelDataDeleteChoice(d)
			} else if change != "stop" {
				s.chooseDataDeleteKind(ui.Services{}, d, "detach_delete")
			}
			if d.deleteChoice != nil || d.edit.Busy || d.edit.Uncertain != nil || len(d.edit.Drafts) != 1 || mysqlDataDraftForRow(d, 0).Kind != "update" {
				t.Fatal("invalid/canceled choice authorized deletion or lost drafts")
			}
		})
	}
}
func TestDataDefaultKindsNeverOfferDetachedDeletionToSQLProviders(t *testing.T) {
	s, d, _ := mutationWorkspace(t)
	d.boundRuntime.EditKinds = nil
	if d.allowsDataEdit("detach_delete") || d.allowsDataEdit("unknown_delete") {
		t.Fatal("default SQL kinds invented detached deletion")
	}
	s.deleteMySQLDataRecords(ui.Services{}, d, 0)
	if d.deleteChoice != nil || mysqlDataDraftForRow(d, 0).Kind != "delete" {
		t.Fatal("default SQL delete behavior changed")
	}
}
