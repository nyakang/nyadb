package shell

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// Use the existing explicitly enabled disposable edit fixture. Every table
// created by this test is unique and dropped without touching other fixtures.
func TestMySQLDataWholeCellEditingIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_DATA_EDIT_IT") != "1" {
		t.Skip("requires disposable loopback MySQL edit fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_DATA_EDIT_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	database := os.Getenv("NYADB_MYSQL_DATA_EDIT_DATABASE")
	if database == "" {
		database = "table_edit_it"
	}
	manager := mysqlsvc.NewMySQLSessionManager()
	t.Cleanup(func() { _ = manager.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	const profile = 9301
	settings := connections.ActiveSettings{ProfileID: profile, Host: "127.0.0.1", Port: uint16(port), Username: "data_probe", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	if err := manager.Connect(ctx, settings, os.Getenv("NYADB_MYSQL_DATA_EDIT_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	table := fmt.Sprintf("cell_edit_%d", time.Now().UnixNano())
	exec := func(statement string) {
		t.Helper()
		if _, err := manager.ExecuteSQL(ctx, profile, database, statement); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE TABLE " + table + " (id BIGINT PRIMARY KEY, note LONGTEXT, doc JSON, payload VARBINARY(10)) ENGINE=InnoDB")
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := manager.ExecuteSQL(cleanup, profile, database, "DROP TABLE "+table); err != nil {
			t.Error(err)
		}
	})
	exec("INSERT INTO " + table + " VALUES (1,REPEAT(CONCAT('中文😀',CHAR(10),CHAR(9)),700),JSON_OBJECT('text',REPEAT('中文😀',700)),X'000141')")
	page, err := manager.TableDataPage(ctx, profile, database, table, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Rows[0][1].Truncated || !page.Rows[0][2].Truncated || page.RowSnapshots[0] == nil {
		t.Fatal("fixture did not provide truncated editable text/JSON")
	}
	doc := &tab{id: 9301, kind: tabMySQLData, title: "cell edit fixture", dataProfileID: profile, dataDatabase: database, dataTable: table, dataPage: page, dataLimit: 100, dataSelectedRow: -1, dataHiddenColumns: map[string]bool{}}
	for _, name := range page.Columns {
		doc.dataColumns = append(doc.dataColumns, ui.TableColumn{ID: name, Title: name, Width: 150, MinWidth: 65})
	}
	s := New()
	s.inspectorOn = false
	s.mysqlSessionManager = manager
	s.mysqlSessionProfileID, s.mysqlSessionStatus = profile, "已连接"
	s.tabs = append(s.tabs, doc)
	s.activeTab = len(s.tabs) - 1
	before := theme.CurrentMode()
	t.Cleanup(func() { theme.SetMode(before) })
	theme.SetMode(theme.ModeDark)
	tt := ui.NewTester(s.View, 1000, 650)
	tt.Frame()
	read := func() *mysqlsvc.MySQLDataRow {
		t.Helper()
		row, err := manager.ReadMySQLDataRow(ctx, profile, database, table, *doc.dataPage.RowRefs[0])
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	original := read()
	full := strings.Repeat("中文😀\n\t", 700)
	if original.Values["note"].Text != full {
		t.Fatal("server fixture did not retain Unicode/newlines/tabs")
	}
	for _, mode := range []theme.Mode{theme.ModeDark, theme.ModeLight} {
		theme.SetMode(mode)
		tt.Frame()
		cell := editingCellBounds(t, doc, 0, 1)
		tt.ClickAt(cell.X+cell.W-0.5, cell.Y+0.5)
		waitEditing(t, s, doc, tt)
		if e := doc.dataEdit.Editor; e == nil || e.Dedicated || copyFocusedText(tt) != full {
			t.Fatal("live single click did not load/focus complete long text")
		}
		if dir := os.Getenv("NYADB_MYSQL_CELL_EDIT_CAPTURE_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			file, err := os.Create(filepath.Join(dir, string(mode)+"-whole-cell.png"))
			if err != nil {
				t.Fatal(err)
			}
			encodeErr := png.Encode(file, tt.Image())
			closeErr := file.Close()
			if encodeErr != nil || closeErr != nil {
				t.Fatalf("capture: %v %v", encodeErr, closeErr)
			}
		}
		tt.Key(0, ui.KeyEscape)
		if mysqlDataHasDraft(doc) || read().Values["note"].Text != full {
			t.Fatal("cancel modified live long text")
		}
	}
	cell := editingCellBounds(t, doc, 0, 1)
	tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
	waitEditing(t, s, doc, tt)
	tt.Key(0, ui.KeyEnd)
	tt.Type("tail")
	tt.Key(0, ui.KeyEnter)
	s.saveMySQLData(ui.Services{}, doc)
	waitEditing(t, s, doc, tt)
	if doc.dataEdit.Err != "" || read().Values["note"].Text != full+"tail" {
		t.Fatalf("long text save lost complete value: %s", doc.dataEdit.Err)
	}
	tt.Frame()
	cell = editingCellBounds(t, doc, 0, 2)
	tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
	waitEditing(t, s, doc, tt)
	if e := doc.dataEdit.Editor; e == nil || e.Dedicated || copyFocusedText(tt) != original.Values["doc"].Text {
		t.Fatal("live JSON did not enter complete inline edit")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	cell = editingCellBounds(t, doc, 0, 3)
	tt.ClickAt(cell.X+cell.W/2, cell.Y+cell.H/2)
	waitEditing(t, s, doc, tt)
	if e := doc.dataEdit.Editor; e == nil || !e.Dedicated || e.Text != "000141" {
		t.Fatal("live binary did not open complete hex editor")
	}
	doc.dataEdit.Editor.Text = "000141FF00"
	s.finishMySQLCellEdit(doc, true)
	s.saveMySQLData(ui.Services{}, doc)
	waitEditing(t, s, doc, tt)
	if doc.dataEdit.Err != "" || !bytes.Equal(read().Values["payload"].Bytes, []byte{0, 1, 65, 255, 0}) {
		t.Fatalf("live binary save changed bytes: %s", doc.dataEdit.Err)
	}
}
