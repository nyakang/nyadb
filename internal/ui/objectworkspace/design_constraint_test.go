package objectworkspace

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
)

func TestStructuredDesignerPagesRenameKeepsIdentityAndPreviewOnly(t *testing.T) {
	_, d, _, tt := designTestState(t)
	originalFields := append([]mysqlddl.Clause(nil), d.design.draft.Table.Clauses...)
	if err := tt.Click("表设计子页/索引"); err != nil {
		t.Fatal(err)
	}
	if d.design.page != 1 || !tt.HasText("添加索引") || tt.HasText("添加外键") {
		t.Fatal("design page selector did not isolate index actions")
	}
	if err := tt.Click("添加索引"); err != nil {
		t.Fatal(err)
	}
	f := d.design.constraint
	if f == nil || f.data.Name != "idx_1" {
		t.Fatal("index form missing")
	}
	id := d.design.draft.Table.Clauses[f.row].ID
	if err := tt.Click("约束名称"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("renamed_index")
	if !d.DesignDirty() {
		t.Fatal("pending constraint text not dirty")
	}
	if err := tt.Click("应用约束修改"); err != nil {
		t.Fatal(err)
	}
	clause := d.design.draft.Table.Clauses[f.row]
	if clause.ID != id || clause.Name != "renamed_index" {
		t.Fatalf("rename changed identity: %+v", clause)
	}
	for i, field := range originalFields {
		if d.design.draft.Table.Clauses[i] != field {
			t.Fatal("constraint form rewrote an unedited field")
		}
	}
	if err := tt.Click("SQL 预览"); err != nil {
		t.Fatal(err)
	}
	if d.design.plan == nil || len(d.design.plan.Statements) != 1 || !strings.Contains(d.design.plan.Statements[0], "ADD KEY `renamed_index`") || d.DesignBusy() {
		t.Fatal("preview did not stage independent index SQL")
	}
}

func TestStructuredDesignerInvalidPrefixRetainsFormAndRejectsPageSwitch(t *testing.T) {
	s, d, _, tt := designTestState(t)
	if !s.changeDesignPage(d, 1) {
		t.Fatal("index page unavailable")
	}
	s.addDesignClause(d, mysqlddl.Index)
	tt.Frame()
	f := d.design.constraint
	if f == nil {
		t.Fatal("constraint form missing")
	}
	f.columnRow = 0
	tt.Frame()
	if err := tt.Click("索引前缀长度"); err != nil {
		t.Fatal(err)
	}
	tt.Type("invalid")
	before := d.design.draft.Clone()
	if !d.DesignDirty() || s.changeDesignPage(d, 2) || d.design.page != 1 || f.prefix != "invalid" || s.PrepareDesign(d) == nil || designSignature(before) != designSignature(d.design.draft) {
		t.Fatal("invalid prefix was discarded or changed the staged constraint")
	}
	if !s.GuardDesignLeave(d, func(ui.Services) {}) {
		t.Fatal("invalid form bypassed leave guard")
	}
	tt.Frame()
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if f.prefix != "invalid" {
		t.Fatal("cancelled leave discarded raw form text")
	}
}

func TestStructuredDesignerCommentDraftAndStalePreviewAreGuarded(t *testing.T) {
	s, d, _, tt := designTestState(t)
	if !s.changeDesignPage(d, 6) {
		t.Fatal("comment page unavailable")
	}
	tt.Frame()
	if err := tt.Click("表注释"); err != nil {
		t.Fatal(err)
	}
	tt.Type("pending 'comment'")
	if !d.DesignDirty() || s.PrepareDesign(d) != nil || !strings.Contains(strings.Join(d.design.plan.Statements, "\n"), "COMMENT='pending ''comment'''") {
		t.Fatal("unapplied comment bypassed preview or dirty guard")
	}
	d.design.optionValue = "newer"
	called := false
	s.deps.DesignExecutor = func() DesignExecutor { called = true; return nil }
	s.executeDesign(d, ui.Services{})
	if called || !d.DesignDirty() {
		t.Fatal("a preview acquired permission to overwrite a newer form")
	}
}

func TestStructuredDesignerNarrowPaneKeepsListAndApplyReachable(t *testing.T) {
	s, d, _, tt := designTestState(t)
	if !s.changeDesignPage(d, 1) {
		t.Fatal("index page unavailable")
	}
	s.addDesignClause(d, mysqlddl.Index)
	// This is the center pane inside the formal 840x560 window.
	tt.SetSize(530, 430)
	tt.Frame()
	if !tt.HasText("完整定义") || !tt.HasText("idx_1") {
		t.Fatal("constraint form displaced the selectable definition list")
	}
	button, ok := tt.Find("应用约束修改")
	if !ok || button.Y < 0 || button.Y+button.H > 430 || button.X+button.W > 530 {
		t.Fatalf("narrow-pane apply button is clipped: %+v present=%t", button, ok)
	}
	if err := tt.Click("应用约束修改"); err != nil {
		t.Fatal(err)
	}
	d.design.constraint.columnRow = 0
	tt.Frame()
	comment, ok := tt.Find("索引注释")
	if !ok || comment.H == 0 {
		t.Fatal("index form has no visible scroll target")
	}
	tt.Scroll(comment.X+comment.W-5, comment.Y+comment.H/2, 0, 1000)
	field, ok := tt.Find("所选约束字段")
	if !ok || field.H == 0 || field.Y+field.H > 430 {
		t.Fatalf("lower constraint controls cannot be reached by scrolling: %+v present=%t", field, ok)
	}
}
