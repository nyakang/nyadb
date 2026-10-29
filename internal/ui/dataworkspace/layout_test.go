package dataworkspace

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func layoutTestTab() *Document {
	t := alignedDataTestTab()
	t.profileID, t.database, t.table = 7, "sample", "entries"
	t.showFieldTypes = true
	return t
}

func memoryLayoutPreferences(s *State, saved map[string]string) {
	s.SetDataLayoutPreferences(func(key string) (string, error) { return saved[key], nil },
		func(key, value string) error { saved[key] = value; return nil })
}

func TestMySQLDataLayoutRestoresAcrossInstancesAndIsolatesIdentity(t *testing.T) {
	saved := map[string]string{}
	s := New(Dependencies{})
	memoryLayoutPreferences(s, saved)
	doc := layoutTestTab()
	s.restoreMySQLDataLayout(doc)
	doc.grid.Columns = ui.TableLayout{Order: []string{"label", "id"}, Widths: map[string]float32{"label": 290, "id": 96}}
	doc.hiddenColumns = map[string]bool{"label": true}
	doc.showFieldTypes = false
	doc.layout.showRowNumbers = true
	doc.page.Rows[0][1].Text = "private cell value must never be persisted"
	s.flushMySQLDataLayout(doc)
	if len(saved) != 1 || strings.Contains(saved[mysqlDataLayoutKey(doc)], "private cell") {
		t.Fatalf("layout either wasn't saved or included row data: %v", saved)
	}
	restarted := New(Dependencies{})
	memoryLayoutPreferences(restarted, saved)
	reopened := layoutTestTab()
	restarted.restoreMySQLDataLayout(reopened)
	if !reflect.DeepEqual(reopened.grid.Columns, doc.grid.Columns) ||
		!reopened.hiddenColumns["label"] || reopened.showFieldTypes || !reopened.layout.showRowNumbers {
		t.Fatalf("layout did not survive a new application state: %+v", reopened.layout)
	}
	for _, identity := range []struct {
		profile  int64
		db, name string
	}{{8, "sample", "entries"}, {7, "different", "entries"}, {7, "sample", "different"}} {
		other := layoutTestTab()
		other.profileID, other.database, other.table = identity.profile, identity.db, identity.name
		restarted.restoreMySQLDataLayout(other)
		if len(other.grid.Columns.Widths) != 0 || len(other.hiddenColumns) != 0 || other.layout.showRowNumbers {
			t.Fatalf("another table inherited layout: %+v", identity)
		}
	}
}

func TestMySQLDataLayoutReconcilesFieldChangesAndBadPreferences(t *testing.T) {
	for _, value := range []string{"not-json", `{"version":9,"widths":{"id":999}}`} {
		s := New(Dependencies{})
		doc := layoutTestTab()
		memoryLayoutPreferences(s, map[string]string{mysqlDataLayoutKey(doc): value})
		s.restoreMySQLDataLayout(doc)
		if len(doc.grid.Columns.Widths) != 0 || doc.layout.showRowNumbers || !doc.showFieldTypes {
			t.Fatalf("bad preference changed the defaults: %q", value)
		}
	}
	s := New(Dependencies{})
	doc := layoutTestTab()
	memoryLayoutPreferences(s, map[string]string{mysqlDataLayoutKey(doc): `{"version":1,"order":["removed","label","label","id"],"widths":{"removed":900,"label":250,"id":-10},"hiddenColumns":{"id":true,"label":true,"removed":true},"showFieldTypes":true}`})
	s.restoreMySQLDataLayout(doc)
	if !reflect.DeepEqual(doc.grid.Columns.Order, []string{"label", "id"}) ||
		len(doc.grid.Columns.Widths) != 1 || len(doc.hiddenColumns) != 0 {
		t.Fatalf("restored nonexistent or invalid columns: %+v hidden=%v", doc.grid.Columns, doc.hiddenColumns)
	}
	doc.page.Columns = []string{"id", "new_field"}
	s.restoreMySQLDataLayout(doc)
	if !reflect.DeepEqual(doc.grid.Columns.Order, []string{"id", "new_field"}) || len(doc.grid.Columns.Widths) != 0 {
		t.Fatalf("refreshed schema kept deleted fields or lost a new field: %+v", doc.grid.Columns)
	}
}

func TestMySQLDataLayoutWaitsForMetadataAndKeepsRowSelectionColumn(t *testing.T) {
	s := New(Dependencies{})
	doc := layoutTestTab()
	page := doc.page
	doc.page = nil
	reads := 0
	s.SetDataLayoutPreferences(func(string) (string, error) { reads++; return "", nil }, nil)
	s.restoreMySQLDataLayout(doc)
	if reads != 0 || doc.layout.restored {
		t.Fatal("layout was consumed before field identities arrived")
	}
	doc.page = page
	s.restoreMySQLDataLayout(doc)
	s.restoreMySQLDataLayout(doc)
	if reads != 1 {
		t.Fatalf("layout read %d times", reads)
	}
	withoutNumbers := mysqlDataRowHeaderColumn(doc)
	if !withoutNumbers.RowHeader || !withoutNumbers.Fixed || withoutNumbers.Title != "" || mysqlDataRowHeaderText(doc, 0) != "" {
		t.Fatal("hiding row numbers removed the native whole-row selector")
	}
	doc.layout.showRowNumbers = true
	doc.offset = 100
	withNumbers := mysqlDataRowHeaderColumn(doc)
	if withNumbers.ID != withoutNumbers.ID || !withNumbers.RowHeader || withNumbers.Title != "#" || mysqlDataRowHeaderText(doc, 2) != "103" {
		t.Fatal("row-number display changed the row-header identity or offset")
	}
}

func TestMySQLDataLayoutDebouncesNativeColumnResize(t *testing.T) {
	s := New(Dependencies{})
	doc := layoutTestTab()
	writes := 0
	s.SetDataLayoutPreferences(func(string) (string, error) { return "", nil }, func(string, string) error { writes++; return nil })
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.restoreMySQLDataLayout(doc)
		ui.Column(c).Fill().Children(func() {
			columns := append([]ui.TableColumn{mysqlDataRowHeaderColumn(doc)}, doc.columns...)
			ui.Table(c, &doc.grid, columns, 1, func(row, col int) {
				if col > 0 {
					ui.Text(c, doc.page.Rows[row][col-1].Text).SingleLine()
				}
			}).Grow(1)
		})
		s.trackMySQLDataLayout(c, doc)
	}, 500, 300)
	header, ok := tt.Find("id")
	if !ok {
		t.Fatal("native header missing")
	}
	edge, y := float32(118), header.Y+header.H/2
	tt.Press(edge, y)
	tt.Move(edge+35, y)
	tt.Release(edge+35, y)
	tt.Frame()
	if doc.grid.Columns.Widths["id"] <= 100 {
		t.Fatalf("native resize did not change the layout: %+v", doc.grid.Columns)
	}
	tt.Advance(200 * time.Millisecond)
	if writes != 0 {
		t.Fatal("resize saved before debounce elapsed")
	}
	doc.grid.Columns.Widths["id"] += 10
	tt.Frame()
	tt.Advance(200 * time.Millisecond)
	if writes != 0 {
		t.Fatal("a continuing drag did not reset the debounce")
	}
	tt.Advance(110 * time.Millisecond)
	if writes != 1 {
		t.Fatalf("settled resize saved %d times", writes)
	}
	tt.Frame()
	tt.Advance(time.Second)
	if writes != 1 {
		t.Fatal("unchanged layout kept being written")
	}
}

func TestMySQLDataLayoutSaveFailureKeepsMemoryAndDoesNotLoop(t *testing.T) {
	var status string
	s := New(Dependencies{Status: func(message string) { status = message }})
	doc := layoutTestTab()
	writes := 0
	s.SetDataLayoutPreferences(func(string) (string, error) { return "", nil }, func(string, string) error {
		writes++
		return errors.New("disk full")
	})
	s.restoreMySQLDataLayout(doc)
	doc.grid.Columns.Widths = map[string]float32{"id": 280}
	s.flushMySQLDataLayout(doc)
	s.flushMySQLDataLayout(doc)
	if writes != 1 || doc.grid.Columns.Widths["id"] != 280 || !strings.Contains(status, "保存表格布局失败") {
		t.Fatalf("failed persistence lost memory or retried: writes=%d status=%q", writes, status)
	}
	doc.grid.Columns.Widths["id"] = 300
	s.flushMySQLDataLayout(doc)
	if writes != 2 {
		t.Fatal("new user change could not attempt saving again")
	}
}

func TestMySQLDataNumericColumnsAlignWithoutChangingColumnIdentity(t *testing.T) {
	doc := layoutTestTab()
	doc.page.ColumnKinds = []string{"bigint", "longtext"}
	indices, columns := mysqlVisibleDataColumns(doc, doc.page)
	if !reflect.DeepEqual(indices, []int{0, 1}) || columns[0].ID != "id" || columns[0].Align != ui.End || columns[1].Align == ui.End {
		t.Fatalf("numeric alignment changed field identity: %+v", columns)
	}
	doc.page.ColumnMetadata = []mysqlsvc.MySQLStructureColumn{{Name: "id", DataType: "varchar", Type: "varchar(12)"}}
	_, columns = mysqlVisibleDataColumns(doc, doc.page)
	if columns[0].Align == ui.End {
		t.Fatal("alignment ignored authoritative server metadata")
	}
}
