package shell

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/storage/localdb"
)

func TestDictionaryEntryAcceptanceReadOnlyMySQL(t *testing.T) {
	if os.Getenv("NYADB_ENTRY_IT") != "1" {
		t.Skip("requires owned entry-acceptance fixture")
	}
	root := os.Getenv("NYADB_ENTRY_ROOT")
	marker, err := os.ReadFile(filepath.Join(root, ".owned"))
	port, e := strconv.Atoi(os.Getenv("NYADB_ENTRY_PORT"))
	if err != nil || len(strings.TrimSpace(string(marker))) != 12 || e != nil || port < 1024 || port > 65535 || os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "unix:path="+root+"/runtime/bus" {
		t.Fatal("owned isolated fixture required")
	}
	db, err := localdb.Open(filepath.Join(t.TempDir(), "profiles.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profiles := connections.NewService(localdb.NewProfileStore(db))
	s := NewWithProfiles(profiles)
	defer s.CloseMySQLSessions()
	vault := credentials.NewOSVault()
	s.SetCredentialVault(vault)
	p, err := profiles.Create(context.Background(), connections.Profile{Name: "Dictionary read-only MySQL", Kind: connections.MySQL, Host: "127.0.0.1", Port: uint16(port), Username: "entry_read", MySQLAdvanced: connections.DefaultMySQLAdvanced()})
	if err != nil {
		t.Fatal(err)
	}
	if err = vault.Set(p.ID, "", os.Getenv("NYADB_ENTRY_READ_PASSWORD")); err != nil {
		t.Fatal("isolated real vault write failed")
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	tt.Frame()
	s.startMySQLSession(services)
	until := time.Now().Add(15 * time.Second)
	for s.sessions.Current().Status == "连接中" && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if s.sessions.Current().Status != "已连接" {
		t.Fatal("real readonly fixture connect failed")
	}
	s.openMySQLDatabase(services, "entry_source", false)
	until = time.Now().Add(15 * time.Second)
	for (s.sessions.Current().Pending != "" || len(s.sessions.Current().Cache["entry_source"]) == 0) && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	path := filepath.Join(root, "evidence", "readonly-dictionary.md")
	s.SetDictionaryFilePicker(func(string) (string, error) { return path, nil })
	if err = tt.Click("工具"); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem("数据字典…"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("读取字典"); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(15 * time.Second)
	for !tt.HasText("字典包含 1 个对象；导出完整已读字典，搜索仅过滤预览。") && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if !tt.HasText("字典包含 1 个对象；导出完整已读字典，搜索仅过滤预览。") {
		t.Fatal("read-only dictionary did not finish")
	}
	if err = tt.Click("导出字典…"); err != nil {
		t.Fatal(err)
	}
	var data []byte
	until = time.Now().Add(15 * time.Second)
	for time.Now().Before(until) {
		tt.Frame()
		data, err = os.ReadFile(path)
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil || !strings.Contains(string(data), "samples") || !strings.Contains(string(data), "decimal(40,20)") || !strings.Contains(string(data), "PRIMARY") || strings.Contains(string(data), "12345678901234567890.12345678901234567890") {
		t.Fatal("read-only MySQL dictionary lost metadata or exported rows", err)
	}
}
