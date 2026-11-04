package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	"strings"
	"testing"
)

func TestInlineFieldsCommitRetainsIdentityAndInvalidDraft(t *testing.T) {
	s, d, _, tt := designTestState(t)
	id := d.design.draft.Table.Clauses[0].ID
	if err := s.commitMySQLCell(d, id, 0, "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := s.commitMySQLCell(d, id, 1, "varchar(50)"); err != nil {
		t.Fatal(err)
	}
	if err := s.commitMySQLCell(d, id, 3, "'hello'"); err != nil {
		t.Fatal(err)
	}
	if d.design.draft.Table.Clauses[0].ID != id || !strings.Contains(d.design.draft.Table.Clauses[0].SQL, "varchar(50)") {
		t.Fatal("lost field identity or cell commit")
	}
	tt.Frame()
	key := "field/" + id + "/0"
	if err := tt.Click(key); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("")
	tt.Key(0, ui.KeyEnter)
	if !d.inlineDirty() || d.inline[key].Error == "" || s.PrepareDesign(d) == nil {
		t.Fatal("invalid pending cell bypassed validation")
	}
	tt.Key(0, ui.KeyEscape)
	if d.inline[key].Text != d.inline[key].Base {
		t.Fatal("Escape did not restore cell")
	}
	d.removeInlineField(id)
	for key := range d.inline {
		if strings.HasPrefix(key, "field/"+id+"/") {
			t.Fatal("removed field retained callback")
		}
	}
}

func TestInlineMySQLAttributesUpdateDraftOnly(t *testing.T) {
	s, d, _, _ := designTestState(t)
	id := d.design.draft.Table.Clauses[0].ID
	for col, value := range map[int]string{5: "中文字段", 6: "utf8mb4_bin"} {
		if err := s.commitMySQLCell(d, id, col, value); err != nil {
			t.Fatal(err)
		}
	}
	raw := d.design.draft.Table.Clauses[0].SQL
	if !strings.Contains(raw, "中文字段") || !strings.Contains(raw, "utf8mb4_bin") || d.design.busy {
		t.Fatal("attribute draft was lost or executed")
	}
}

func TestInlineFocusAndInspectorFollowDraftAcrossRows(t *testing.T) {
	for _, engine := range []string{"mysql", "sqlite", "postgresql"} {
		t.Run(engine, func(t *testing.T) {
			var d *Document
			var tt *ui.Tester
			switch engine {
			case "mysql":
				_, d, _, tt = designTestState(t)
			case "sqlite":
				_, d, _, tt, _ = schemaTestState(t)
			default:
				_, d, tt, _ = postgresFormState(t)
			}
			ids := []string{}
			if d.design != nil {
				for _, clause := range d.design.draft.Table.Clauses {
					ids = append(ids, clause.ID)
				}
			} else {
				for _, clause := range d.schemaDesign.draft.Clauses {
					if clause.Kind == "column" {
						ids = append(ids, clause.ID)
					}
				}
			}
			// Both directions cover blur commits rendered before and after the
			// newly focused cell. No Apply button or DDL executor is involved.
			for step, row := range []int{1, 0, 1} {
				key := "field/" + ids[row] + "/0"
				if err := tt.Click(key); err != nil {
					t.Fatal(err)
				}
				tt.Key(ui.Ctrl, ui.KeyA)
				value := []string{"draft_second", "draft_first", "draft_last"}[step]
				tt.Type(value)
				tt.Frame()
				field, ok := d.SelectedField()
				if !ok || field.Name != value {
					t.Fatalf("focused draft not reflected: %+v, expected %s", field, value)
				}
				if step == 2 {
					tt.Key(0, ui.KeyEnter)
					tt.Frame()
					field, ok = d.SelectedField()
					if !ok || field.Name != value {
						t.Fatalf("committed draft selected old row: %+v", field)
					}
				}
			}
			if !d.DesignDirty() || d.DesignBusy() {
				t.Fatal("cell edits did not remain unexecuted design drafts")
			}
		})
	}
}
