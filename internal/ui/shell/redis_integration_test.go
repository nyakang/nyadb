package shell

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestRedisSessionKeySharedGridAndNativeCommands(t *testing.T) {
	if os.Getenv("NYADB_REDIS_IT") != "1" {
		t.Skip("requires disposable Redis")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_REDIS_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit loopback port required")
	}
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.Redis)
	p.Name = "Redis live grid"
	p.Host = "127.0.0.1"
	p.Port = uint16(port)
	p.Username = os.Getenv("NYADB_REDIS_USERNAME")
	p.Database = "0"
	if os.Getenv("NYADB_REDIS_TLS") == "1" {
		p.TLSMode = "verify-full"
		p.RedisOptions.CAFile = os.Getenv("NYADB_REDIS_CA")
	}
	p, err = service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = os.Getenv("NYADB_REDIS_PASSWORD")
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("Redis UI operation did not settle")
		}
		tt.Frame()
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" || w.Kind != connections.Redis {
		t.Fatal(w.Status, w.Error)
	}
	bound, _ := s.runtime.Session(p.ID)
	provider, err := contract.Require[contract.QueryProvider](bound)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.ReserveQuery(900)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	ctx := context.Background()
	if _, err = lease.Execute(ctx, p.Database, `["SET", "nyadb_ui_record", "猫"]`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Execute(ctx, p.Database, `["DEL","nyadb_ui_record"]`) })
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, p.Database))
	wait(func() bool { return w.Pending == "" && w.Cache[p.Database] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: p.Database, Object: "keys", Category: "键"})
	dataTab := s.currentTab()
	if dataTab.data == nil || dataTab.data.Object().Kind != "keyspace" || dataTab.data.Kind() != "键" {
		t.Fatal("collection not routed to shared data document")
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.Page() == nil || dataTab.data.Error() != "" {
		t.Fatal(dataTab.data.Error())
	}
	document := `{"string":"c2F2ZWQgYnkgUmVkaXMgZ3JpZA=="}`
	row := -1
	for i, cells := range dataTab.data.Page().Rows {
		if cells[0].Text == "bnlhZGJfdWlfcmVjb3Jk" {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("fixture key not present in grid")
	}
	s.dataWorkspace.EditCell(services, dataTab.data, row, "value", contract.Cell{Kind: contract.JSON, Text: document})
	tt.Frame()
	if err = tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !dataTab.data.Busy() })
	if dataTab.data.HasDraft() || !strings.Contains(dataTab.data.Page().Rows[row][3].Text, "c2F2ZWQgYnkgUmVkaXMgZ3JpZA==") {
		t.Fatal("document grid save", dataTab.data.Error())
	}
	if output := os.Getenv("NYADB_REDIS_CAPTURE_DIR"); output != "" {
		if err = os.MkdirAll(output, 0700); err != nil {
			t.Fatal(err)
		}
		writeDomainCapture(t, output, "redis-live-grid", tt)
	}
	s.openQuery()
	queryTab := s.currentTab()
	command := `["GET","nyadb_ui_record"]`
	queryTab.query.Editor.Buffer.Set(command)
	tt.Frame()
	if err = tt.Click("运行 Redis 命令"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, s, tt, queryTab)
	result := queryTab.query.Result()
	if result == nil || len(result.Rows) != 1 || !strings.Contains(result.Rows[0][0], "saved by Redis grid") {
		t.Fatalf("native command execution: result=%v error=%s", result, queryTab.query.Error())
	}
	s.openMySQLStructure(services, p.Database, "keys")
	structureTab := s.currentTab()
	if structureTab.object == nil || structureTab.object.Object().Kind != "keyspace" {
		t.Fatal("collection structure identity")
	}
	wait(func() bool { return !structureTab.object.Loading() })
	if structureTab.object.Data() == nil || structureTab.object.Error() != "" {
		t.Fatal("collection structure view", structureTab.object.Error())
	}
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !queryTab.query.NeedsRecovery() || queryTab.query.Editor.Buffer.String() != command {
		t.Fatal("disconnect discarded native command")
	}
}
