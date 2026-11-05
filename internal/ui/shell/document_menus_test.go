package shell

import (
	"context"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"testing"
	"time"
)

func TestDocumentMenusRejectSwitchedTabAndKeepCloseGuards(t *testing.T) {
	s, tt, _, _ := profileTestState(t)
	s.openQuery()
	original := s.currentTab()
	original.query.Editor.Buffer.Set("SELECT original")
	tt.Frame()
	if err := tt.Click("编辑"); err != nil {
		t.Fatal(err)
	}
	s.openQuery()
	other := s.currentTab()
	tt.Frame()
	if err := tt.ChooseMenuItem("全选"); err != nil {
		t.Fatal(err)
	}
	if other.query.Editor.SelectedText() != "" {
		t.Fatal("stale edit menu selected text in a new document")
	}
	if err := tt.Click("窗口"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("关闭所有页签"); err != nil {
		t.Fatal(err)
	}
	if len(s.tabs) != 3 || !tt.HasText("保存查询更改？") || original.query.Editor.Buffer.String() != "SELECT original" {
		t.Fatal("window menu bypassed dirty-document leave guard")
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if len(s.tabs) != 3 || s.currentTab() != other {
		t.Fatal("cancelled batch close changed active document")
	}
	if err := tt.Click("SQL 编辑器"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("编辑"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("全选"); err != nil {
		t.Fatal(err)
	}
	if other.query.Editor.SelectedText() == "" {
		t.Fatal("current document editing menu was not connected")
	}
	if err := tt.Click("SQL 编辑器"); err != nil {
		t.Fatal(err)
	}
	tt.Key(primaryModifier(), ui.KeyN)
	if len(s.tabs) != 4 {
		t.Fatal("new-query shortcut was consumed by the editor")
	}
}

func TestCapturedEditMenuRejectsInvalidTargetsAndOtherWindows(t *testing.T) {
	s := New()
	defer s.CloseMySQLSessions()
	var entries []theme.EditMenuEntry
	s.SetEditMenuHost(func(value []theme.EditMenuEntry) { entries = value })
	calls, alive := 0, true
	target := theme.EditTarget{ID: "main-result", Valid: func() bool { return alive }, Enabled: func(command string) bool { return command == "copy" }, Run: func(ui.Services, string) bool { calls++; return true }}
	s.editFocus = target.ID
	s.registerEditTarget(target)
	s.openEditMenu()
	var copyEntry theme.EditMenuEntry
	for _, entry := range entries {
		if entry.Command == "copy" {
			copyEntry = entry
		}
		if entry.Command == "paste" && entry.Enabled {
			t.Fatal("read-only result exposed a write command")
		}
	}
	if !copyEntry.Enabled {
		t.Fatal("focused result did not enable copy")
	}
	copyEntry.Run()
	if calls != 1 {
		t.Fatal("copy did not reach the captured target")
	}
	alive = false
	copyEntry.Run()
	alive = true
	s.connectionDialog = true
	copyEntry.Run()
	s.connectionDialog = false
	s.openQuery()
	copyEntry.Run()
	if calls != 1 {
		t.Fatal("stale menu dispatched after destruction, another window or another document")
	}
}

func TestDocumentShortcutFromConnectionNavigationCreatesQuery(t *testing.T) {
	s, tt, profiles, _ := profileTestState(t)
	p, err := profiles.Create(context.Background(), connections.Profile{Name: "Shortcut Profile", Kind: connections.SQLite, Database: "/tmp/nyadb-shortcut-fixture.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	tt.Frame()
	if err := tt.Click(p.Name); err != nil {
		t.Fatal(err)
	}
	count := len(s.tabs)
	tt.Key(primaryModifier(), ui.KeyN)
	if len(s.tabs) != count+1 || s.currentTab().query == nil || s.currentTab().query.ProfileID() != p.ID {
		t.Fatal("navigator consumed new-query shortcut without creating the original-profile document")
	}
}

func TestDataSaveMenuAndShortcutKeepOriginalActiveDocument(t *testing.T) {
	s := New()
	doc := fixtureDataTab(t, s, editableHostPage())
	tt := ui.NewTester(s.View, 1100, 700)
	staged := make(chan string, 2)
	s.dataWorkspace.Saver = func(_ context.Context, id int64, db, table string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		staged <- fmt.Sprintf("%d/%s/%s", id, db, table)
		out := &mysqlsvc.MySQLDataEditResult{}
		for _, e := range edits {
			out.Outcomes = append(out.Outcomes, mysqlsvc.MySQLDataEditOutcome{Kind: e.Kind, Original: e.Original, Saved: e.Original})
		}
		return out, nil
	}
	stageHostDraft(t, tt, "pending")
	if err := tt.Click("文件"); err != nil {
		t.Fatal(err)
	}
	s.openQuery()
	tt.Frame()
	if err := tt.ChooseMenuItem("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-staged:
		t.Fatal("stale file menu saved", got)
	default:
	}
	if !doc.data.HasDraft() {
		t.Fatal("stale command discarded original draft")
	}
	s.activeTab = 1
	tt.Frame()
	tt.Key(primaryModifier(), ui.KeyS)
	deadline := time.Now().Add(2 * time.Second)
	for doc.data.HasDraft() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	select {
	case got := <-staged:
		if got != "19/edit_test/records" {
			t.Fatal("shortcut saved different identity", got)
		}
	default:
		t.Fatal("data Ctrl+S did not save")
	}
	if doc.data.HasDraft() {
		t.Fatal("shortcut did not consume saved result")
	}
}
