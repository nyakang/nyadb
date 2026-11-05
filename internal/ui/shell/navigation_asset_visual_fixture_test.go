package shell

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// A local-only fixture with the same hierarchy as the navigation reference.
// No MySQL server or original application is launched.
func TestNavigationAssetVisualFixture(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	if err := service.CreateGroup(context.Background(), "开发库"); err != nil {
		t.Fatal(err)
	}
	offline, err := service.Create(context.Background(), connections.Profile{
		Name: "192.168.142.76", GroupName: "开发库", Kind: connections.MySQL,
		Host: "127.0.0.1", Port: 3306,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := service.Create(context.Background(), connections.Profile{
		Name: "192.168.142.77", GroupName: "开发库", Kind: connections.MySQL,
		Host: "127.0.0.1", Port: 3306,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	if children := s.navigation.Children(navigation.ConnectionKey(offline.ID)); children != nil {
		t.Fatalf("offline connection has disclosure children: %v", children)
	}
	s.sessions.Current().ProfileID = active.ID
	s.sessions.Current().Status = "已连接"
	publishRuntimeFixture(t, s)
	s.sessions.Current().Databases = []string{"0_mysql_monitor", "aigc", "alg_mark_platform", "alg_train", "angular", "audit_platform", "audit_platform_tenant", "audit_platform_test", "audit_platform_web", "c2c", "dev_audit_platform"}
	s.sessions.Current().Cache = make(map[string][]mysqlsvc.MySQLDBObject)
	s.sessions.Current().Cache["dev_audit_platform"] = []mysqlsvc.MySQLDBObject{
		{Name: "orders", Kind: "表"}, {Name: "daily", Kind: "视图"},
	}
	s.selectedProfileID = active.ID
	s.navigation.Key = navigation.DatabaseNode(active.ID, "dev_audit_platform").Key()
	s.navigation.Row = -1
	s.sessions.Current().Database = "dev_audit_platform"
	s.sessions.MarkOpened(active.ID, "dev_audit_platform")
	s.navigation.Tree.Open.Add(navigation.GroupKey("开发库"))
	s.navigation.Tree.Open.Add(navigation.ConnectionKey(active.ID))
	s.navigation.Tree.Open.Add(navigation.DatabaseNode(active.ID, "dev_audit_platform").Key())
	previousMode := theme.CurrentMode()
	defer theme.SetMode(previousMode)
	for _, mode := range []theme.Mode{theme.ModeDark, theme.ModeLight} {
		for _, scale := range []float32{1, 2} {
			t.Run(fmt.Sprintf("%s-%gx", mode, scale), func(t *testing.T) {
				theme.SetMode(mode)
				// 1600 DIP gives a 320-DIP sidebar, matching the reference crop.
				tt := ui.NewTester(s.View, 1600, 900)
				tt.SetScale(scale)
				verifyNavigationAssetFixture(t, s, tt, scale)
				if output := os.Getenv("NYADB_NAV_ASSET_CAPTURE"); output != "" {
					if mode != theme.ModeDark || scale != 1 {
						ext := filepath.Ext(output)
						output = fmt.Sprintf("%s-%s-%gx%s", strings.TrimSuffix(output, ext), mode, scale, ext)
					}
					f, err := os.Create(output)
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
			})
		}
	}
}

func verifyNavigationAssetFixture(t *testing.T, s *State, tt *ui.Tester, scale float32) {
	t.Helper()
	tt.Frame()
	for _, name := range []string{"开发库", "192.168.142.76", "192.168.142.77", "0_mysql_monitor", "aigc", "dev_audit_platform", "表", "视图"} {
		r, ok := tt.Find(name)
		if !ok {
			t.Errorf("missing visible navigation node %q", name)
			continue
		}
		t.Logf("%s x=%.1f y=%.1f w=%.1f h=%.1f", name, r.X, r.Y, r.W, r.H)
	}
	group, groupFound := tt.Find("开发库")
	connection, connectionFound := tt.Find("192.168.142.77")
	database, databaseFound := tt.Find("0_mysql_monitor")
	if !groupFound || !connectionFound || !databaseFound {
		t.Fatal("incomplete navigation hierarchy in visual fixture")
	}
	if connection.X-group.X < 10 || database.X-connection.X < 10 {
		t.Fatalf("hierarchy lacks visible nesting: group %.1f, connection %.1f, database %.1f", group.X, connection.X, database.X)
	}
	if pitch := database.Y - connection.Y; pitch < 20 || pitch > 22 {
		t.Fatalf("database tree row pitch %.1f out of compact range", pitch)
	}
	if s.navigation.Children(navigation.DatabaseNode(s.selectedProfileID, "0_mysql_monitor").Key()) != nil {
		t.Fatal("unopened database shows an expansion indicator")
	}
	// Only assert its tree identity and hierarchy, not unverifiable
	// cross-application pixel equivalence.
	if !s.navigation.Tree.Open.Has(navigation.ConnectionKey(s.selectedProfileID)) ||
		!s.navigation.Tree.Open.Has(navigation.DatabaseNode(s.selectedProfileID, "dev_audit_platform").Key()) {
		t.Fatal("fixture lost tree expansion")
	}
	palette := theme.PaletteFor(theme.CurrentMode(), true)
	objects, ok := tt.Find("对象")
	if !ok {
		t.Fatal("objects tab missing")
	}
	assertThemePixel(t, tt, int((objects.X+4)*scale), int((objects.Y+4)*scale), palette.Header)
	assertThemePixel(t, tt, int((objects.X+objects.W+8)*scale), int((objects.Y+4)*scale), palette.Panel)
	offline, ok := tt.Find("192.168.142.76 · 未连接")
	if !ok {
		t.Fatal("offline connection row missing")
	}
	// The native disclosure gutter and connection's leading padding are empty.
	for _, x := range []float32{offline.X - 6, offline.X + 1} {
		assertThemePixel(t, tt, int(x*scale), int((offline.Y+offline.H/2)*scale), palette.Sidebar)
	}
	query, queryFound := tt.Find("新建查询")
	table, tableFound := tt.Find("表")
	if !queryFound || !tableFound {
		t.Fatal("toolbar group boundary missing")
	}
	borderPixels := 0
	for x := int((query.X + query.W) * scale); x < int(table.X*scale); x++ {
		pixel := tt.Image().RGBAAt(x, int((table.Y+table.H/2)*scale))
		if pixel.R == palette.Border.R && pixel.G == palette.Border.G && pixel.B == palette.Border.B {
			borderPixels++
		}
	}
	if borderPixels != int(scale) {
		t.Fatalf("toolbar divider occupies %d pixels at %gx, want %g", borderPixels, scale, scale)
	}
}
