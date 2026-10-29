package shell

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// Opt-in artifact capture uses synthetic service replies and isolated storage.
// It covers full host layout; functional tests live in their owning packages.
func TestUIFunctionalDomainVisualMatrix(t *testing.T) {
	output := os.Getenv("NYADB_UI_DOMAIN_CAPTURE")
	if output == "" {
		t.Skip("set NYADB_UI_DOMAIN_CAPTURE for the visual matrix")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	mode := theme.CurrentMode()
	defer theme.SetMode(mode)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, mode := range []theme.Mode{theme.ModeDark, theme.ModeLight} {
			theme.SetMode(mode)
			for _, size := range [][2]int{{1280, 800}, {840, 560}} {
				for _, panel := range []bool{false, true} {
					for _, view := range []string{"objects", "data", "structure", "program"} {
						s, _, service, _ := profileTestState(t)
						profile, err := service.Create(context.Background(), connections.Profile{Name: "Visual fixture", Kind: connections.MySQL, Host: "localhost", Port: 3306})
						if err != nil {
							t.Fatal(err)
						}
						s.loadProfiles()
						s.selectedProfileID = profile.ID
						w := s.sessions.Current()
						w.ProfileID, w.Status, w.Database = profile.ID, "已连接", "app_db"
						w.Databases = []string{"app_db"}
						w.Cache = map[string][]mysqlsvc.MySQLDBObject{"app_db": {{Name: "orders", Kind: "表", Engine: "InnoDB", Comment: "订单 / orders"}, {Name: "sales", Kind: "视图"}, {Name: "discount", Kind: "函数", Definition: "CREATE FUNCTION discount(amount DECIMAL(10,2)) RETURNS DECIMAL(10,2)\nRETURN amount * 0.9", ReturnType: "DECIMAL(10,2)", Definer: "fixture@localhost"}}}
						s.sessions.MarkOpened(profile.ID, "app_db")
						s.navigation.Key = navigation.DatabaseNode(profile.ID, "app_db").Key()
						s.navigation.Tree.Open.Add(navigation.ConnectionKey(profile.ID))
						s.navigation.Tree.Open.Add(navigation.DatabaseNode(profile.ID, "app_db").Key())
						s.inspectorOn = panel
						s.dataWorkspace.Loader = func(_ context.Context, _ int64, db, table string, offset int64, limit int) (*mysqlsvc.MySQLDataPage, error) {
							page := editableHostPage()
							page.Database, page.Table, page.Offset, page.Limit = db, table, offset, limit
							page.Rows[0][1].Text = "订单 / sample"
							return page, nil
						}
						s.objectWorkspace.Loader = func(_ context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableStructure, error) {
							return fakeStructure(db, table), nil
						}
						switch view {
						case "data":
							s.openMySQLData(ui.Services{}, "app_db", "orders")
						case "structure":
							s.openMySQLStructure(ui.Services{}, "app_db", "orders")
						case "program":
							s.openMySQLProgram("app_db", "discount", "函数")
						}
						tt := ui.NewTester(s.View, size[0], size[1])
						deadline := time.Now().Add(3 * time.Second)
						for time.Now().Before(deadline) {
							tt.Frame()
							loading := false
							if s.currentTab().data != nil {
								loading = s.currentTab().data.Loading()
							}
							if s.currentTab().object != nil {
								loading = s.currentTab().object.Loading()
							}
							if !loading {
								break
							}
							time.Sleep(time.Millisecond)
						}
						tt.Frame()
						writeDomainCapture(t, output, fmt.Sprintf("%s-%s-%dx%d-panel%t-%s", language, mode, size[0], size[1], panel, view), tt)
					}
				}
			}
			s := New()
			s.SetConnectionWindowHost(func() {}, func() {})
			s.openConnectionDialog()
			child := ui.NewTester(s.ConnectionWindowView, 900, 700)
			writeDomainCapture(t, output, language+"-"+string(mode)+"-native-chooser", child)
			s.chooseConnectionKind(connections.MySQL)
			s.nextConnectionStep()
			child.Frame()
			writeDomainCapture(t, output, language+"-"+string(mode)+"-native-editor", child)
			s.ConnectionWindowClosed()
		}
	}
}
func writeDomainCapture(t *testing.T, output, name string, tt *ui.Tester) {
	t.Helper()
	f, err := os.Create(filepath.Join(output, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, tt.Image())
	closeErr := f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}
