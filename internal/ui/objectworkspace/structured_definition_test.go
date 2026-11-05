package objectworkspace

import (
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"strings"
	"testing"
)

func TestSQLiteGraphIndexStagesQualifiedPreviewWithoutExecuting(t *testing.T) {
	s, d, _, tt, _ := schemaTestState(t)
	if err := tt.Click("索引"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("添加索引"); err != nil {
		t.Fatal(err)
	}
	f := d.schemaDesign.structured
	if f == nil || f.Form.Schema != "main" || f.Form.Table != "t" {
		t.Fatalf("ordinary index did not open graph: %+v", f)
	}
	f.Form.Unique = true
	f.Form.Columns = []schemaddl.BasicIndexColumn{{Name: "value", Descending: true}, {Name: "id"}}
	if err := tt.Click("应用定义修改"); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	if d.DesignBusy() || !strings.Contains(d.schemaDesign.draft.Definitions[0].SQL, `ON "t" ("value" DESC, "id")`) {
		t.Fatalf("wrong draft: %+v", d.schemaDesign.draft.Definitions)
	}
	f.Form.Columns[0].Name = "removed"
	if err := s.applySchemaForms(d); err == nil {
		t.Fatal("removed column was accepted")
	}
	if !d.DesignDirty() {
		t.Fatal("failed graph validation discarded edits")
	}
}
