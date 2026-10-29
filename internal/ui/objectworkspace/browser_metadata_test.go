package objectworkspace

import (
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func objectInt64(n int64) *int64 { return &n }

func TestMySQLObjectMetadataColumnsRespectServerNullsAndEstimates(t *testing.T) {
	table := mysqlsvc.MySQLDBObject{
		Name: "orders", Kind: "表", Engine: "InnoDB", Comment: "sales",
		AutoIncrement: objectInt64(71),
		ModifiedAt:    "2026-10-20 12:34:56",
		DataLength:    objectInt64(16384),
		RowsEstimate:  objectInt64(0),
	}
	expected := []string{"orders", "71", "2026-10-20 12:34:56", "16384", "InnoDB", "≈0", "sales"}
	for col, want := range expected {
		if got := mysqlObjectCellText(table, col); got != want {
			t.Fatalf("metadata column %d: want %q got %q", col, want, got)
		}
	}
	view := mysqlsvc.MySQLDBObject{Name: "report_view", Kind: "视图", Comment: "report"}
	for _, col := range []int{1, 2, 3, 4, 5} {
		if got := mysqlObjectCellText(view, col); got != "" {
			t.Fatalf("unknown view metadata was invented: column %d %q", col, got)
		}
	}
}
