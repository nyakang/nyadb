package objectworkspace

import (
	"testing"
)

func TestTiDBDesignerPagesRetainFieldControlsAndSkipTriggers(t *testing.T) {
	s, d, _, tt := designTestState(t)
	d.design.original.Engine = "tidb"
	tt.Frame()
	if tt.HasText("触发器") || !tt.HasText("自动递增") || !tt.HasText("字段注释") {
		t.Fatal("TiDB designer advertised triggers or lost shared field controls")
	}
	for _, item := range []struct {
		label string
		page  int
	}{{"表选项", 5}, {"注释", 6}, {"原始 DDL", 7}, {"索引", 1}, {"字段", 0}} {
		if err := tt.Click(item.label); err != nil {
			t.Fatal(err)
		}
		if d.design.page != item.page {
			t.Fatal("incorrect TiDB designer page", item.label, d.design.page)
		}
	}
	s.deps.Engine = func(int64) string { return "tidb" }
	next := s.OpenNewDesign(33, 1, "db", "new_t")
	if next == nil || next.design.original.Engine != "tidb" {
		t.Fatal("new table lost TiDB identity")
	}
}
