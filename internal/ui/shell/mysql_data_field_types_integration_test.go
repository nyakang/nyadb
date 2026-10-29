package shell

import (
	"context"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// Real MySQL data are read from the disposable fixture, not synthesized.
// ui.NewTester validates native rendering, not a desktop compositor window.
func TestMySQLEnumJSONNativeViewer84(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_TYPES_IT") != "1" {
		t.Skip("requires disposable field-types fixture on 127.0.0.1:34306")
	}
	m := mysqlsvc.NewMySQLSessionManager()
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	settings := connections.ActiveSettings{ProfileID: 84310, Host: "127.0.0.1", Port: 34306,
		Username: "nyadb_it_reader", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	if err := m.Connect(ctx, settings, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	page, err := m.TableDataPageWithOptions(ctx, settings.ProfileID, "nyadb_types", "field_types",
		mysqlsvc.MySQLDataOptions{UseKeyset: true, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	tab := &tab{dataPage: page, dataDatabase: "nyadb_types", dataTable: "field_types",
		dataLimit: 3, dataShowFieldTypes: true, dataSelectedRow: -1,
		dataHiddenColumns: map[string]bool{}}
	for _, name := range page.Columns {
		tab.dataColumns = append(tab.dataColumns, ui.TableColumn{ID: name, Title: name, Width: 210})
	}
	indices, cols := mysqlVisibleDataColumns(tab, page)
	if cols[1].Subtitle != "enum('active','disabled','待审核')" ||
		cols[1].SubtitleIcon != "A" || cols[2].Subtitle != "json" || cols[2].SubtitleIcon != "{}" {
		t.Fatalf("server-derived MyGo native headers: %+v", cols)
	}
	s := &State{}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Fill().Children(func() {
			ui.Table(c, &tab.dataState, cols, len(page.Rows), func(r, i int) {
				cell := page.Rows[r][indices[i]]
				if cell.Null {
					ui.Text(c, "NULL")
				} else {
					ui.Text(c, cell.Text).SingleLine()
				}
			}).Grow(1)
			ui.Box(c).Width(210).Children(func() { s.mysqlDataInspector(c, tab) })
		})
	}, 1280, 800)
	tt.Frame()
	if !tt.HasText("enum('active','disabled','待审核')") || !tt.HasText("json") ||
		!tt.HasText("主键") {
		t.Fatalf("live server fields not rendered in native table: %v", tt.Texts())
	}
	tab.dataState.Cells = ui.ColumnRange{Cursor: "status", Anchor: "status"}
	tt.Frame()
	if !tt.HasText("状态枚举") || !tt.HasText("enum('active','disabled','待审核')") {
		t.Fatalf("live enum metadata not in inspector: %v", tt.Texts())
	}
	tab.dataState.Cells = ui.ColumnRange{Cursor: "attributes", Anchor: "attributes"}
	tt.Frame()
	if !tt.HasText("json") {
		t.Fatal("live JSON field not inspected")
	}
	if path := os.Getenv("NYADB_MYSQL_TYPES_CAPTURE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
