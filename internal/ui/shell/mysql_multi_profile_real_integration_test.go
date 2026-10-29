package shell

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

// Opt-in only: requires a disposable, localhost-only MySQL fixture with two
// users granted SELECT on different tables within the same schema. This test
// never discovers or contacts a shared/production database.
func TestMySQLTwoRealProfilesIsolateSameSchemaMetadata(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_MULTI_IT") != "1" {
		t.Skip("requires explicitly created localhost-only disposable MySQL fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_MULTI_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("NYADB_MYSQL_MULTI_PORT must be a dedicated unprivileged localhost port")
	}

	s, tt, svc, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	ctx := context.Background()
	a, err := svc.Create(ctx, connections.Profile{
		Name: "Fixture A", Kind: connections.MySQL, Host: "127.0.0.1",
		Port: uint16(port), Username: "nyadb_multi_a",
		MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Create(ctx, connections.Profile{
		Name: "Fixture B", Kind: connections.MySQL, Host: "127.0.0.1",
		Port: uint16(port), Username: "nyadb_multi_b",
		MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()

	connectAndRead := func(p connections.Profile, want string) {
		t.Helper()
		s.selectProfileByName(p.Name)
		tt.Frame()
		if err := tt.Click("连接所选 MySQL"); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(12 * time.Second)
		for s.sessions.Current().Status == "连接中" && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(20 * time.Millisecond)
		}
		if s.sessions.Current().Status != "已连接" {
			t.Fatalf("real profile %q connection failed: %s %s", p.Name, s.sessions.Current().Status, s.sessions.Current().Error)
		}
		if !s.sessions.Manager.IsConnected(p.ID) {
			t.Fatalf("session manager lost authenticated profile %q", p.Name)
		}
		s.navigation.Tree.Open.Add(navigation.ConnectionKey(p.ID))
		// Both connection trees show the same schema name. Target its
		// qualified identity rather than Tester's first matching label.
		s.selectNavigationNode(ui.Services{}, navigation.DatabaseNode(p.ID, "nyadb_multi_shared").Key())
		tt.Frame()
		waitMySQLObjects(t, s, tt)
		items := s.sessions.Current().Cache["nyadb_multi_shared"]
		if len(items) != 1 || items[0].Name != want || items[0].Kind != "表" {
			t.Fatalf("profile %q metadata mismatch: %+v, want %q", p.Name, items, want)
		}
	}
	connectAndRead(a, "a_orders")
	connectAndRead(b, "b_orders")
	if !s.sessions.Manager.IsConnected(a.ID) || !s.sessions.Manager.IsConnected(b.ID) {
		t.Fatal("starting B disconnected A on the real MySQL session manager")
	}
	s.selectProfileByName(a.Name)
	if objects := s.sessions.Current().Cache["nyadb_multi_shared"]; len(objects) != 1 || objects[0].Name != "a_orders" {
		t.Fatalf("same schema metadata leaked from B into A: %+v", objects)
	}
	s.disconnectMySQLProfile(a.ID)
	if s.sessions.Manager.IsConnected(a.ID) || !s.sessions.Manager.IsConnected(b.ID) {
		t.Fatal("disconnecting A closed B or left A connected")
	}
	s.selectProfileByName(b.Name)
	if objects := s.sessions.Current().Cache["nyadb_multi_shared"]; len(objects) != 1 || objects[0].Name != "b_orders" {
		t.Fatalf("B metadata was lost after A disconnect: %+v", objects)
	}
}
