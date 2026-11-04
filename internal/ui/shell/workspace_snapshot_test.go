package shell

import (
	"context"
	"encoding/json"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/queries"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkbenchRestorationKeepsTabsPinnedAndNeverExecutes(t *testing.T) {
	s := New()
	s.profileMode = true
	defer s.CloseMySQLSessions()
	s.openQuery()
	sql := s.currentTab()
	sql.query.Editor.Insert("SELECT private_draft;")
	s.pinTab(sql.id)
	v := queries.WorkspaceTab{ID: 20, Kind: uint8(tabMySQLData), Title: "missing.t", ProfileID: 77, Catalog: "db", Schema: "main", Database: "main", ObjectKind: "表", ObjectName: "t"}
	s.tabs = append(s.tabs, &tab{id: 20, kind: tabMySQLData, title: v.Title, placeholder: &v})
	s.selectTab(20)
	w := s.workspaceSnapshot()
	if w.SnapshotVersion != 2 || len(w.Tabs) != 3 {
		t.Fatal("incomplete snapshot")
	}
	restarted := New()
	defer restarted.CloseMySQLSessions()
	restarted.restoreWorkspace(w, false)
	if len(restarted.tabs) != 3 || !restarted.tabs[1].pinned || restarted.currentTab().id != 20 || restarted.currentTab().placeholder == nil || !restarted.tabs[1].query.NeedsRecovery() && restarted.tabs[1].query.ProfileID() != 0 {
		t.Fatal("restored scene not lazy")
	}
	active := restarted.currentTab().id
	restarted.restoreWorkspace(w, true)
	if len(restarted.tabs) != 3 || restarted.currentTab().id != active {
		t.Fatal("retry duplicated or replaced current scene")
	}
	b, _ := json.Marshal(w)
	if strings.Contains(string(b), "Rows") || strings.Contains(string(b), "designDraft") {
		t.Fatal("persisted results or write drafts")
	}
}

func TestRestoredConnectionEntryConnectsOriginalWithoutExecutingSQL(t *testing.T) {
	s, tt, service, _ := profileTestState(t)
	p, err := service.Create(context.Background(), connections.Profile{Name: "Original", Kind: connections.MySQL, Host: "127.0.0.1", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Create(context.Background(), connections.Profile{Name: "Other", Kind: connections.MySQL, Host: "127.0.0.1", Port: 3307})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	var opened atomic.Int64
	s.sessions.Opener = func(_ context.Context, settings connections.ActiveSettings, _ string) error {
		opened.Store(settings.ProfileID)
		return nil
	}
	s.restoreWorkspace(queries.Workspace{SnapshotVersion: 2, ActiveID: 20,
		Documents: []queries.Document{{ID: 20, ProfileID: p.ID, Database: "db", Text: "SELECT must_not_replay;"}},
		Tabs:      []queries.WorkspaceTab{{ID: 20, Kind: uint8(tabSQL), ProfileID: p.ID}}}, false)
	s.sessions.Activate(other.ID)
	s.selectedProfileID = other.ID
	tt.Frame()
	if err := tt.Click("恢复原连接"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.sessions.StatusFor(p.ID) == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	d := s.currentTab().query
	if opened.Load() != p.ID || s.sessions.StatusFor(p.ID) != "已连接" || !d.NeedsRecovery() || d.Loading() || d.Editor.Buffer.String() != "SELECT must_not_replay;" {
		t.Fatal("reconnect entry failed to connect the original profile without replaying SQL")
	}
}

func TestWorkspaceSwitchExposesDesignGuardAndCancelKeepsScene(t *testing.T) {
	s, tt, _, _ := profileTestState(t)
	d := s.objectWorkspace.OpenNewDesign(20, 1, "db", "unsaved_table")
	if d == nil || !d.DesignDirty() {
		t.Fatal("missing unsaved design fixture")
	}
	s.tabs = append(s.tabs, &tab{id: 20, kind: tabStructure, title: "unsaved_table", object: d})
	s.selectTab(20)
	s.workspaces.open = true
	s.switchWorkspace(queries.Workspace{SnapshotVersion: 2, ActiveID: 1})
	tt.Frame()
	if s.workspaces.open || !tt.HasText("保存表设计更改？") || len(s.tabs) != 2 {
		t.Fatal("workspace dialog covered the design leave guard or replaced the scene early")
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if len(s.tabs) != 2 || s.currentTab().id != 20 || !d.DesignDirty() {
		t.Fatal("cancelled workspace switch changed the original scene")
	}
}
func TestTabOperationsUseStableIDsAndSkipPinnedBatch(t *testing.T) {
	s := New()
	defer s.CloseMySQLSessions()
	s.openQuery()
	a := s.currentTab()
	s.openQuery()
	b := s.currentTab()
	s.openQuery()
	c := s.currentTab()
	s.pinTab(b.id)
	if s.tabs[1] != b {
		t.Fatal("pinned region")
	}
	s.selectTab(c.id)
	s.moveTab(c.id, a.id)
	if s.currentTab() != c || s.tabs[2] != c {
		t.Fatal("reorder lost active ID")
	}
	s.cycleTabs(true)
	if s.currentTab() != b {
		t.Fatal("reverse visible order")
	}
	s.closeRight(b.id)
	if len(s.tabs) != 2 || s.currentTab() != b {
		t.Fatal("close right lost pinned target")
	}
	s.closeTabID(b.id)
	if len(s.tabs) != 1 {
		t.Fatal("explicit pinned close failed")
	}
}

func TestWorkspaceRetryMergesCollidingSQLWithoutOverwritingLiveDraft(t *testing.T) {
	s := New()
	s.profileMode = true
	defer s.CloseMySQLSessions()
	s.openQuery()
	d := s.currentTab()
	d.query.Editor.Insert("new live draft")
	w := queries.Workspace{Documents: []queries.Document{{ID: d.id, Text: "old encrypted draft"}}}
	s.restoreWorkspace(w, true)
	if len(s.tabs) != 3 || s.currentTab() != d || s.tabs[2].query.Editor.Buffer.String() != "old encrypted draft" {
		t.Fatal("retry lost a colliding draft")
	}
}
