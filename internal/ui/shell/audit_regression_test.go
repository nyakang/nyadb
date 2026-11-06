package shell

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
)

func TestDeleteProfileCancelPreservesConfigurationAndCredential(t *testing.T) {
	s, tt, service, _ := profileTestState(t)
	p, err := service.Create(context.Background(), connections.Profile{Name: "guarded", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	_ = vault.Set(p.ID, "", "retained")
	s.SetCredentialVault(vault)
	d := s.objectWorkspace.OpenNewDesign(20, p.ID, "db", "unsaved_table")
	s.tabs = append(s.tabs, &tab{id: 20, kind: tabStructure, object: d})
	s.selectTab(20)
	s.pendingDeleteID = p.ID
	s.deleteProfile()
	tt.Frame()
	if !tt.HasText("保存表设计更改？") {
		t.Fatal("missing leave guard")
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), p.ID); err != nil {
		t.Fatal("cancel deleted configuration", err)
	}
	if _, found, _ := vault.Get(p.ID, ""); !found || !d.DesignDirty() || len(s.tabs) != 2 {
		t.Fatal("cancel changed the scene or credential")
	}
}

type deleteFailProfiles struct{ *connections.Service }

func (r deleteFailProfiles) Delete(context.Context, int64) error {
	return errors.New("injected delete failure")
}

func TestDeleteProfileStorageFailurePreservesSessionAndTabs(t *testing.T) {
	s, _, svc, _ := profileTestState(t)
	p, err := svc.Create(context.Background(), connections.Profile{Name: "retained", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.sessions.Current().ProfileID = p.ID
	s.sessions.Current().Status = "已连接"
	s.profileService = connections.NewService(deleteFailProfiles{svc})
	s.pendingDeleteID = p.ID
	s.deleteProfile()
	if _, err := svc.Get(context.Background(), p.ID); err != nil || s.sessions.StatusFor(p.ID) != "已连接" {
		t.Fatal("failed delete disconnected profile")
	}
}

func TestWorkspaceInvalidTargetPreservesCurrentDocument(t *testing.T) {
	targets := []queries.Workspace{
		{SnapshotVersion: 3},
		{SnapshotVersion: 2, Documents: []queries.Document{{ID: 2}, {ID: 2}}},
		{SnapshotVersion: 2, Tabs: []queries.WorkspaceTab{{ID: 2, Kind: uint8(tabSQL)}}},
		{SnapshotVersion: 2, Tabs: []queries.WorkspaceTab{{ID: 2, Kind: uint8(tabSettings)}, {ID: 2, Kind: uint8(tabSettings)}}},
		{SnapshotVersion: 2, Shell: []byte("invalid JSON")},
	}
	for _, target := range targets {
		s := New()
		s.openQuery()
		d := s.currentTab()
		d.query.Editor.Insert("SELECT retained_draft;")
		s.switchWorkspace(target)
		if s.currentTab() != d || d.query.Editor == nil || len(s.tabs) != 2 || s.workspaces.err == "" {
			t.Fatal("invalid target replaced the scene")
		}
		s.CloseMySQLSessions()
	}
}

func TestBrowserResetPreservesOtherProfileNavigation(t *testing.T) {
	s := New()
	defer s.CloseMySQLSessions()
	s.selectedProfileID = 2
	other := navigation.Node{Kind: "database", ProfileID: 2, Database: "otherdb"}.Key()
	s.navigation.Key = other
	s.navigation.Tree.Open.Add(other)
	s.sessionBrowserDependencies().BrowserReset(1)
	if s.navigation.Key != other || !s.navigation.Tree.Open.Has(other) {
		t.Fatal("parked reset changed another profile")
	}
}

func TestDeleteContinuationKeepsOriginalProfileAndSQLText(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	ctx := context.Background()
	p, err := svc.Create(ctx, connections.Profile{Name: "original", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.Duplicate(ctx, p.ID, "other")
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	design := s.objectWorkspace.OpenNewDesign(20, p.ID, "db", "unsaved")
	s.tabs = append(s.tabs, &tab{id: 20, kind: tabStructure, object: design})
	query := s.sqlWorkspace.Open(21, "SELECT preserved", p.ID, "db", false)
	s.tabs = append(s.tabs, &tab{id: 21, kind: tabSQL, query: query})
	s.selectTab(20)
	s.pendingDeleteID = p.ID
	s.deleteProfile()
	tt.Frame()
	s.pendingDeleteID = other.ID
	if err := tt.Click("放弃"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, p.ID); err == nil {
		t.Fatal("original profile not deleted after explicit discard")
	}
	if _, err := svc.Get(ctx, other.ID); err != nil {
		t.Fatal("continuation deleted later selection", err)
	}
	if query.Editor == nil || query.Editor.Buffer.String() != "SELECT preserved" || !query.NeedsRecovery() {
		t.Fatal("deletion lost SQL text or recovery")
	}
}

func TestCredentialDeletionCoversEnginesAndContinuesAfterErrors(t *testing.T) {
	for _, kind := range []connections.Kind{connections.MySQL, connections.PostgreSQL, connections.ClickHouse, connections.SQLite} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, svc, _ := profileTestState(t)
			p, err := connections.Defaults(kind)
			if err != nil {
				t.Fatal(err)
			}
			p.Name = "delete credentials"
			if kind == connections.SQLite {
				p.Database = filepath.Join(t.TempDir(), "user.sqlite")
			}
			if kind == connections.MySQL {
				p.Variants = []connections.ConnectionVariant{{Name: "Other", Host: "localhost", Port: 3306, MySQLAdvanced: connections.DefaultMySQLAdvanced()}}
			}
			p, err = svc.Create(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			v := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "primary", fmt.Sprintf("%d/Other", p.ID): "other"}}
			if kind == connections.MySQL {
				v.deleteErrors = map[string]error{fmt.Sprintf("%d/", p.ID): errors.New("primary cleanup rejected")}
			}
			s.SetCredentialVault(v)
			s.pendingDeleteID = p.ID
			s.deleteProfile()
			switch kind {
			case connections.SQLite:
				if len(v.deleteCalls) != 0 {
					t.Fatal("SQLite invoked database vault")
				}
			case connections.MySQL:
				if len(v.deleteCalls) != 2 || strings.Contains(s.status, "已删除连接档案") {
					t.Fatal("cleanup stopped after first error or hid failure")
				}
				if _, found, _ := v.Get(p.ID, "Other"); found {
					t.Fatal("later credential not cleaned")
				}
			default:
				if len(v.deleteCalls) != 1 {
					t.Fatal("engine credential omitted")
				}
				if _, found, _ := v.Get(p.ID, ""); found {
					t.Fatal("primary credential retained")
				}
			}
		})
	}
}

type failingWorkspaceRepository struct {
	queries.Repository
	fail atomic.Bool
}

func (r *failingWorkspaceRepository) Put(ctx context.Context, v queries.Record) error {
	if r.fail.Load() && v.Kind == "workspace" {
		return errors.New("injected workspace save failure")
	}
	return r.Repository.Put(ctx, v)
}

func TestWorkspaceSaveFailurePreservesTabsAndStoredRecovery(t *testing.T) {
	s, tt, _, _ := profileTestState(t)
	db, err := localdb.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &failingWorkspaceRepository{Repository: localdb.NewSQLRepository(db)}
	store := queries.NewStore(repo, &assetTestKey{})
	baseline := queries.Workspace{SnapshotVersion: 2, Revision: 1, ActiveID: 1}
	if err := store.SaveWorkspace(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	s.SetSQLPersistence(store, sqlworkspace.Files{}, nil)
	deadline := time.Now().Add(time.Second)
	for s.sqlWorkspace.PersistenceLoading() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	s.openQuery()
	current := s.currentTab()
	current.query.Editor.Insert("SELECT unsaved_recovery")
	repo.fail.Store(true)
	s.switchWorkspace(queries.Workspace{SnapshotVersion: 2, ActiveID: 1})
	for time.Now().Before(deadline) {
		tt.Frame()
		if strings.Contains(s.status, "workspace save failure") {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if s.currentTab() != current || current.query.Editor == nil || len(s.tabs) != 2 {
		t.Fatal("failed flush replaced current tabs")
	}
	w, err := store.LoadWorkspace(context.Background())
	if err != nil || w.Revision != 1 {
		t.Fatal("failed flush replaced original recovery", err)
	}
}

func TestUnsupportedLoadedWorkspaceCannotBeOverwrittenByAutosave(t *testing.T) {
	s, tt, _, _ := profileTestState(t)
	db, err := localdb.Open(filepath.Join(t.TempDir(), "unsupported.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := localdb.NewSQLRepository(db)
	store := queries.NewStore(repo, &assetTestKey{})
	original := queries.Workspace{SnapshotVersion: 3, Revision: 1, Documents: []queries.Document{{ID: 2, Text: "retained future draft"}}}
	if err := store.SaveWorkspace(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	s.SetSQLPersistence(store, sqlworkspace.Files{}, nil)
	deadline := time.Now().Add(time.Second)
	for s.sqlWorkspace.PersistenceLoading() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	s.openQuery()
	s.currentTab().query.Editor.Insert("new local draft")
	s.switchWorkspace(queries.Workspace{SnapshotVersion: 2})
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	w, err := store.LoadWorkspace(context.Background())
	if err != nil || w.SnapshotVersion != 3 || w.Documents[0].Text != "retained future draft" || len(s.tabs) != 2 {
		t.Fatal("unsupported recovery was overwritten", err)
	}
}

func TestWorkspaceSwitchCapturesTargetBeforeDeferredLeave(t *testing.T) {
	s, tt, _, _ := profileTestState(t)
	d := s.objectWorkspace.OpenNewDesign(20, 1, "db", "unsaved")
	s.tabs = append(s.tabs, &tab{id: 20, kind: tabStructure, object: d})
	s.selectTab(20)
	target := queries.Workspace{SnapshotVersion: 2, ActiveID: 22, Documents: []queries.Document{{ID: 22, Text: "SELECT captured"}}, Tabs: []queries.WorkspaceTab{{ID: 22, Kind: uint8(tabSQL)}}}
	s.switchWorkspace(target)
	tt.Frame()
	target.Documents[0].Text = "caller changed target"
	target.Tabs[0].ID = 99
	if err := tt.Click("放弃"); err != nil {
		t.Fatal(err)
	}
	if s.currentTab().query == nil || s.currentTab().query.Editor.Buffer.String() != "SELECT captured" {
		t.Fatal("deferred switch observed mutable target")
	}
}
