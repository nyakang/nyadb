package shell

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/storage/localdb"
)

func profileTestState(t *testing.T) (*State, *ui.Tester, *connections.Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	service := connections.NewService(connections.NewStore(db))
	s := NewWithProfiles(service)
	return s, ui.NewTester(s.View, 1280, 800), service, path
}

func TestProfileUICreateAndRefresh(t *testing.T) {
	s, tt, service, _ := profileTestState(t)
	if len(s.profileEntries) != 0 || tt.HasText("测试库") {
		t.Fatal("real UI should start without mock connections")
	}
	if err := tt.Click("添加连接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("选择 MySQL"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "Development"
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog || len(s.profileEntries) != 1 || s.profileEntries[0].Name != "Development" {
		t.Fatalf("profile not created: open=%t entries=%+v error=%q", s.connectionDialog, s.profileEntries, s.connectionError)
	}
	if !tt.HasText("Development") {
		t.Fatal("new connection missing from explorer")
	}
	if err := tt.Click("刷新"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Development") {
		t.Fatal("refresh hid persisted connection")
	}
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("profile was not saved: %+v %v", list, err)
	}
}

func TestProfileUIEditDuplicateDelete(t *testing.T) {
	s, tt, service, _ := profileTestState(t)
	p, err := service.Create(context.Background(), connections.Profile{
		Name: "Local", Kind: connections.SQLite, Database: "/tmp/nyadb-test.sqlite",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = p.ID
	s.loadProfiles()
	tt.SetSize(1280, 800)
	if !tt.HasText("Local") {
		t.Fatal("saved connection not displayed")
	}
	if err := tt.Click("编辑连接"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionDialog || s.connectionDraft.ID != p.ID || !tt.HasText("编辑连接 · SQLite") {
		t.Fatalf("edit form not prefilled: modal=%v draft=%+v selected=%d status=%q", s.connectionDialog, s.connectionDraft, s.selectedProfileID, s.status)
	}
	s.connectionDraft.Name = "Renamed"
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	p, err = service.Get(context.Background(), p.ID)
	if err != nil || p.Name != "Renamed" {
		t.Fatalf("edit not saved: %+v %v", p, err)
	}
	if err := tt.Click("复制连接"); err != nil {
		t.Fatal(err)
	}
	if len(s.profileEntries) != 2 || !tt.HasText("Renamed 副本") {
		t.Fatalf("duplicate not reflected in explorer: %+v", s.profileEntries)
	}
	if err := tt.Click("删除连接"); err != nil {
		t.Fatal(err)
	}
	if !s.deleteConfirm || !tt.HasText("删除连接档案？") {
		t.Fatal("missing delete confirmation")
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if len(s.profileEntries) != 2 {
		t.Fatal("cancel deleted a connection")
	}
	if err := tt.Click("删除连接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	if len(s.profileEntries) != 1 {
		t.Fatalf("delete failed: %+v", s.profileEntries)
	}
	if _, err := service.Get(context.Background(), s.selectedProfileID); s.selectedProfileID != 0 && errors.Is(err, connections.ErrNotFound) {
		t.Fatal("selected profile ID points at deleted entry")
	}
	if strings.Contains(s.status, "数据库已删除") {
		t.Fatal("status implies deletion of user DB")
	}
}

func TestProfileDuplicateNameError(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, err := service.Create(context.Background(), connections.Profile{Name: "one", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = p.ID
	s.loadProfiles()
	s.startEditProfile()
	s.connectionDraft.Name = "one"
	s.saveConnectionDraft()
	if s.connectionDialog {
		t.Fatal("updating same profile name should succeed")
	}
	_, err = service.Create(context.Background(), connections.Profile{Name: "taken", Kind: connections.SQLite, Database: "/tmp/test.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	s.startEditProfile()
	s.connectionDraft.Name = "taken"
	s.saveConnectionDraft()
	if !s.connectionDialog || s.connectionError == "" {
		t.Fatal("duplicate name should display error without closing dialog")
	}
}
