package dataworkspace

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func sampleSelection() *Selection {
	literalNull := "NULL"
	length := int64(80)
	precision, scale := int64(10), int64(2)
	charset, collation := "utf8mb4", "utf8mb4_0900_ai_ci"
	return &Selection{
		Row: -1,
		Page: &mysqlsvc.MySQLDataPage{
			Columns:     []string{"id", "label", "price"},
			ColumnKinds: []string{"bigint", "varchar", "decimal"},
			ColumnMetadata: []mysqlsvc.MySQLStructureColumn{
				{Name: "id", DataType: "bigint", Type: "bigint unsigned", Key: "PRI", Extra: "auto_increment"},
				{Name: "label", DataType: "varchar", Type: "varchar(80)", Nullable: true,
					Default: &literalNull, CharacterMaxLength: &length, CharacterSet: &charset,
					Collation: &collation, Comment: "reviewed"},
				{Name: "price", DataType: "decimal", Type: "decimal(10,2)", NumericPrecision: &precision,
					NumericScale: &scale, Nullable: true},
			},
			Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Text: "sample"}, {Text: "12.50"}}},
		},
	}
}

func testInspector(selection *Selection) (*inspectorState, *Panel) {
	s := newInspector(InspectorDependencies{})
	p := s.Open(8, "test_schema", "entries", func() Selection { return *selection })
	p.info.fieldMode = true
	return s, p
}

func TestMySQLInspectorUsesExactFieldMetadataAndLifecycle(t *testing.T) {
	tab := sampleSelection()
	field, found, complete := tab.Field()
	if !found || !complete || field.Name != "id" || field.Type != "bigint unsigned" {
		t.Fatalf("default field: %+v %v %v", field, found, complete)
	}
	s, p := testInspector(tab)
	tt := ui.NewTester(func(c *ui.Context) { s.FieldView(c, p) }, 330, 780)
	tt.Frame()
	if !tt.HasText("bigint unsigned") || !tt.HasText("主键") || !tt.HasText("auto_increment") {
		t.Fatalf("initial field attributes missing: %v", tt.Texts())
	}
	tab.Column = "label"
	tt.Frame()
	field, found, complete = tab.Field()
	if !found || !complete || field.Name != "label" || field.Default == nil || *field.Default != "NULL" {
		t.Fatalf("selected field metadata: %+v %v %v", field, found, complete)
	}
	if !tt.HasText("varchar(80)") || !tt.HasText("\"NULL\"") || !tt.HasText("utf8mb4") {
		t.Fatalf("literal NULL or charset missing: %v", tt.Texts())
	}
	tab.Row = 0
	tt.Frame()
	if name, cell, ok := tab.Cell(); !ok || name != "label" || cell.Text != "sample" {
		t.Fatal("cell selection and field metadata diverged")
	}
	tab.Column = "price"
	tt.Frame()
	if !tt.HasText("decimal(10,2)") {
		t.Fatal("cell changed but metadata did not")
	}
	tab.Hidden = map[string]bool{"price": true}
	if _, found, _ := tab.Field(); found {
		t.Fatal("hidden field leaked into inspector")
	}
	tab.Page = nil
	tab.Loading = false
	tab.Error = "disconnected"
	tt.Frame()
	if !tt.HasText("字段元数据不可用，请重试读取数据") || tt.HasText("decimal(10,2)") {
		t.Fatal("stale metadata survived page failure")
	}
}

func TestMySQLInspectorFallbackHasNoFabricatedAttributes(t *testing.T) {
	tab := &Selection{Page: &mysqlsvc.MySQLDataPage{
		Columns: []string{"id"}, ColumnKinds: []string{"bigint"},
	}}
	field, found, complete := tab.Field()
	if !found || complete || field.Type != "bigint" || field.Key != "" {
		t.Fatalf("legacy fields must not fake metadata: %+v", field)
	}
	s, p := testInspector(tab)
	tt := ui.NewTester(func(c *ui.Context) { s.FieldView(c, p) }, 330, 700)
	tt.Frame()
	if !tt.HasText("仅有基础字段类型，完整元数据尚未加载") || tt.HasText("主键") {
		t.Fatal("missing metadata rendered as confirmed attributes")
	}
}

func waitInspectorSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("inspector worker did not signal")
	}
}
func fieldSelection() *Selection {
	selection := sampleSelection()
	selection.Row, selection.Column = 0, "label"
	selection.Page.RowRefs = []*mysqlsvc.MySQLRowRef{{Database: "test_schema", Table: "entries", Columns: []string{"id"}, Values: [][]byte{[]byte("1")}}}
	return selection
}

func TestInspectorChunkCopiesIdentityAndDiscardsSameCellOnAnotherPage(t *testing.T) {
	selection := fieldSelection()
	s, p, _ := tableInfoTestState(selection)
	done := make(chan struct{}, 1)
	var calls atomic.Int32
	s.ChunkLoader = func(_ context.Context, id int64, db, table, field string, ref mysqlsvc.MySQLRowRef, offset int64, units int) (*mysqlsvc.MySQLFieldChunk, error) {
		calls.Add(1)
		if id != 8 || db != "test_schema" || table != "entries" || field != "label" || ref.Columns[0] != "id" || string(ref.Values[0]) != "1" || units != 1024 {
			t.Errorf("chunk identity/bound changed: %d %s %s %s %+v %d", id, db, table, field, ref, units)
		}
		return &mysqlsvc.MySQLFieldChunk{Text: "first", NextOffset: 1024, TotalUnits: 8192, HasMore: true}, nil
	}
	s.LoadChunk(p, func() { done <- struct{}{} })
	waitInspectorSignal(t, done)
	s.Consume()
	if calls.Load() != 1 || p.ChunkNext() != 1024 || p.ChunkText() != "first" {
		t.Fatal("explicit chunk did not render")
	}
	started, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.ChunkLoader = func(ctx context.Context, _ int64, _, _, _ string, ref mysqlsvc.MySQLRowRef, offset int64, units int) (*mysqlsvc.MySQLFieldChunk, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		if ref.Columns[0] != "id" || string(ref.Values[0]) != "1" || offset != 1024 || units != 1024 {
			t.Errorf("captured request was changed by the grid: %+v/%d/%d", ref, offset, units)
		}
		return &mysqlsvc.MySQLFieldChunk{Text: "obsolete", NextOffset: 2048, TotalUnits: 8192}, nil
	}
	s.LoadChunk(p, func() { done <- struct{}{} })
	waitInspectorSignal(t, started)
	// Mutation of a row reference cannot alter the worker's captured key bytes.
	selection.Page.RowRefs[0].Columns[0] = "changed"
	selection.Page.RowRefs[0].Values[0][0] = '9'
	replacement := *selection.Page
	selection.Page = &replacement
	s.Consume()
	waitInspectorSignal(t, canceled)
	close(release)
	waitInspectorSignal(t, done)
	s.Consume()
	if p.ChunkLoading() || p.ChunkNext() != 0 || p.ChunkText() != "" {
		t.Fatal("same row/field on a new page received an obsolete chunk")
	}
	s.CloseAll()
}

func TestInspectorSwitchingToTableModeCancelsOnlyFieldRead(t *testing.T) {
	selection := fieldSelection()
	s, p, _ := tableInfoTestState(selection)
	p.info.attempted = true
	s.ShowField(p)
	started, canceled, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	s.ChunkLoader = func(ctx context.Context, _ int64, _, _, _ string, _ mysqlsvc.MySQLRowRef, _ int64, _ int) (*mysqlsvc.MySQLFieldChunk, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return &mysqlsvc.MySQLFieldChunk{Text: "obsolete"}, nil
	}
	s.LoadChunk(p, func() { done <- struct{}{} })
	waitInspectorSignal(t, started)
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(); s.View(c, p) }, 330, 700)
	if err := tt.Click("表信息"); err != nil {
		t.Fatal(err)
	}
	waitInspectorSignal(t, canceled)
	close(release)
	waitInspectorSignal(t, done)
	tt.Frame()
	if p.FieldMode() || p.ChunkLoading() || p.ChunkText() != "" || !tt.HasText("估算行数") {
		t.Fatal("table mode retained a field read or accepted late preview")
	}
	s.CloseAll()
}

func TestInspectorChunksNeedExplicitVisibleNonNullSelection(t *testing.T) {
	for _, kind := range []string{"null", "no-row", "hidden", "no-identity", "done"} {
		t.Run(kind, func(t *testing.T) {
			selection := fieldSelection()
			s, p, _ := tableInfoTestState(selection)
			switch kind {
			case "null":
				selection.Page.Rows[0][1].Null = true
			case "no-row":
				selection.Row = -1
			case "hidden":
				selection.Hidden = map[string]bool{"label": true}
			case "no-identity":
				selection.Page.RowRefs = nil
			case "done":
				p.chunkField, p.chunkRow, p.chunkPage, p.chunkDone = "label", 0, selection.Page, true
			}
			var calls atomic.Int32
			s.ChunkLoader = func(context.Context, int64, string, string, string, mysqlsvc.MySQLRowRef, int64, int) (*mysqlsvc.MySQLFieldChunk, error) {
				calls.Add(1)
				return nil, nil
			}
			s.LoadChunk(p, nil)
			if p.ChunkLoading() || calls.Load() != 0 {
				t.Fatal("invalid or completed preview launched a field read")
			}
			s.CloseAll()
		})
	}
}
