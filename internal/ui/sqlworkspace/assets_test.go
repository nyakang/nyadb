package sqlworkspace

import (
	"context"
	"errors"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"testing"
)

func assetTestState(t *testing.T) (*State, *queries.Store) {
	t.Helper()
	store, _ := documentStore()
	if e := store.SaveNamed(context.Background(), queries.Named{ID: "q", Name: "Daily", Text: "SELECT 1", ProfileID: 7, Database: "app", Revision: 1}); e != nil {
		t.Fatal(e)
	}
	s := New(Dependencies{})
	t.Cleanup(s.CloseAll)
	s.SetPersistence(store, Files{}, nil)
	waitDocumentIO(t, s)
	s.ActivateAssets(7)
	return s, store
}

func TestQueryAssetPendingRenameFailureRetainsModalAndDirtySQL(t *testing.T) {
	s, _ := assetTestState(t)
	d := s.Open(2, "", 7, "app", false)
	s.OpenNamed(d, s.Named(7)[0])
	d.Editor.Insert(" changed")
	s.beginAsset(7, "q", "rename")
	dialog := s.persistence.assetDialog
	dialog.name = " "
	s.submitAssetDialog(dialog)
	waitDocumentIO(t, s)
	if s.persistence.assetDialog != dialog || dialog.err == "" || s.Named(7)[0].Name != "Daily" || !d.Dirty() {
		t.Fatal("failed rename discarded input or document state")
	}
	dialog.name = "Renamed"
	s.submitAssetDialog(dialog)
	waitDocumentIO(t, s)
	if s.persistence.assetDialog != nil || s.Named(7)[0].Name != "Renamed" {
		t.Fatal("rename retry did not settle")
	}
}
func TestQueryAssetParkedCompletionAndCloseRevokeDialogs(t *testing.T) {
	store, repo := documentStore()
	ctx := context.Background()
	if e := store.SaveNamed(ctx, queries.Named{ID: "q", Name: "Daily", Text: "SELECT 1", ProfileID: 7, Revision: 1}); e != nil {
		t.Fatal(e)
	}
	s := New(Dependencies{})
	t.Cleanup(s.CloseAll)
	s.SetPersistence(store, Files{}, nil)
	waitDocumentIO(t, s)
	s.ActivateAssets(7)
	started, release := make(chan struct{}), make(chan struct{})
	repo.before = func(r queries.Record) {
		if r.Kind == "query_deleted" {
			close(started)
			<-release
		}
	}
	s.beginAsset(7, "q", "delete")
	s.submitAssetDialog(s.persistence.assetDialog)
	awaitSignal(t, started)
	s.ActivateAssets(8)
	close(release)
	waitDocumentIO(t, s)
	if len(s.Named(7)) != 0 || s.persistence.assetScope != 8 || s.persistence.assetDialog != nil {
		t.Fatal("parked completion changed active query scope")
	}
	s.CloseAll()
	s.ConsumeDocuments(ui.Services{})
	if s.persistence.assetDialog != nil || len(s.persistence.assets) != 0 {
		t.Fatal("closed query assets retained UI callbacks")
	}
}
func TestQueryAssetOlderDocumentCannotOverwriteAfterRename(t *testing.T) {
	s, store := assetTestState(t)
	old := s.Named(7)[0]
	current := old
	current.Text = "SELECT 2"
	current.Revision = 2
	if e := store.SaveNamed(context.Background(), current); e != nil {
		t.Fatal(e)
	}
	s.RefreshAssets(7)
	waitDocumentIO(t, s)
	d := s.Open(2, "", 7, "app", false)
	s.OpenNamed(d, old)
	d.Editor.Insert(" edited")
	s.RenameAsset(7, "q", "Latest")
	waitDocumentIO(t, s)
	s.Save(d, false, nil)
	waitDocumentIO(t, s)
	if !d.Dirty() || d.Error() == "" {
		t.Fatal("old SQL body silently overwritten")
	}
	saved, e := store.ListNamed(context.Background(), 7)
	if e != nil || saved[0].Text != "SELECT 2" {
		t.Fatalf("latest body changed: %v %v", saved, e)
	}
	if !errors.Is(store.SaveNamed(context.Background(), old), queries.ErrStaleRevision) {
		t.Fatal("stale revision accepted")
	}
}
func TestQueryAssetRenameAndDeletePreserveOpenDocuments(t *testing.T) {
	s, store := assetTestState(t)
	n := s.Named(7)[0]
	d := s.Open(2, "", 7, "app", false)
	s.OpenNamed(d, n)
	lease := &testLease{}
	d.lease = lease
	d.Editor.Insert(" dirty")
	text := d.Editor.Buffer.String()
	selection := d.Editor.Selection()
	s.RenameAsset(7, "q", " Renamed ")
	waitDocumentIO(t, s)
	if d.Name() != "Renamed" || d.Editor.Buffer.String() != text || !d.Dirty() || d.savedText != "SELECT 1" || d.namedRevision != 2 {
		t.Fatal("rename changed body or draft baseline")
	}
	older := s.Open(3, "", 7, "app", false)
	s.OpenNamed(older, n)
	s.DeleteAsset(7, "q")
	waitDocumentIO(t, s)
	if d.QueryID() != "" || older.QueryID() != "" || d.Editor.Buffer.String() != text || d.Editor.Selection() != selection || d.lease != lease || lease.releases != 0 || !d.Dirty() {
		t.Fatal("delete disposed or changed open SQL")
	}
	if len(s.Named(7)) != 0 {
		t.Fatal("deleted asset retained")
	}
	if e := store.SaveNamed(context.Background(), queries.Named{ID: "q", Name: "late", ProfileID: 7, Revision: 3}); e == nil {
		t.Fatal("deleted asset resurrected")
	}
}
func TestQueryAssetGroupsRestartAndDeletingGroupRetainsQueries(t *testing.T) {
	s, store := assetTestState(t)
	s.ChangeGroup(7, "new_group", "", "Reports")
	waitDocumentIO(t, s)
	g := s.Catalog(7).Groups[0]
	s.ChangeGroup(7, "move", "q", g.ID)
	waitDocumentIO(t, s)
	if s.AssetGroup(7, "q") != g.ID {
		t.Fatal("move not published")
	}
	s.ChangeGroup(7, "rename_group", g.ID, "History")
	waitDocumentIO(t, s)
	restarted := New(Dependencies{})
	t.Cleanup(restarted.CloseAll)
	restarted.SetPersistence(store, Files{}, nil)
	waitDocumentIO(t, restarted)
	if restarted.AssetGroupName(7, g.ID) != "History" || restarted.AssetGroup(8, "q") != "" {
		t.Fatal("groups lost or crossed profiles")
	}
	s.ChangeGroup(7, "delete_group", g.ID, "")
	waitDocumentIO(t, s)
	if s.AssetGroup(7, "q") != "" || len(s.Named(7)) != 1 {
		t.Fatal("group removal deleted query")
	}
}
func TestQueryAssetUISelectionSearchKeyboardAndScope(t *testing.T) {
	s, _ := assetTestState(t)
	opened := 0
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.AssetsView(c, 7, func(n queries.Named) { opened++ })
		s.Modals(c)
	}, 900, 500)
	if e := tt.Click("Daily"); e != nil {
		t.Fatal(e)
	}
	if s.assetBrowser(7).selected != "q" {
		t.Fatal("click did not select")
	}
	tt.Key(0, ui.KeyF2)
	if s.persistence.assetDialog == nil || s.persistence.assetDialog.kind != "rename" {
		t.Fatal("F2 did not rename")
	}
	if e := tt.Click("取消"); e != nil {
		t.Fatal(e)
	}
	if e := tt.Click("Daily"); e != nil {
		t.Fatal(e)
	}
	tt.Key(0, ui.KeyEnter)
	if opened != 1 {
		t.Fatalf("Enter opened %d", opened)
	}
	if e := tt.Click("搜索查询名称"); e != nil {
		t.Fatal(e)
	}
	tt.Type("missing")
	if len(s.filteredAssets(7)) != 0 {
		t.Fatal("search did not filter")
	}
	s.beginAsset(7, "q", "delete")
	s.ActivateAssets(8)
	if s.persistence.assetDialog != nil {
		t.Fatal("cross-profile dialog retained")
	}
	s.ActivateAssets(7)
	if s.assetBrowser(7).search != "missing" {
		t.Fatal("profile search state lost")
	}
}

func TestQueryAssetMoveMenuRejectsChangedGroupDirectory(t *testing.T) {
	s, _ := assetTestState(t)
	s.ChangeGroup(7, "new_group", "", "Reports")
	waitDocumentIO(t, s)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.AssetsView(c, 7, nil)
	}, 900, 500)
	if err := tt.RightClick("Daily"); err != nil {
		t.Fatal(err)
	}
	s.ChangeGroup(7, "rename_group", s.AssetGroups(7)[0].ID, "Reports Updated")
	waitDocumentIO(t, s)
	if err := tt.ChooseMenuItem("移动到查询分组", "Reports"); err != nil {
		t.Fatal(err)
	}
	if len(s.persistence.jobs) != 0 || s.AssetGroup(7, "q") != "" {
		t.Fatal("stale menu moved a query using a changed catalog")
	}
}

func TestQueryAssetFilterReservedNamesRemainSelectable(t *testing.T) {
	s, store := assetTestState(t)
	n := queries.Named{ID: "reserved", Name: "Reserved", Text: "SELECT 2", ProfileID: 7, Database: "全部数据库", Revision: 1}
	if err := store.SaveNamed(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	s.RefreshAssets(7)
	waitDocumentIO(t, s)
	s.ChangeGroup(7, "new_group", "", "未分组")
	waitDocumentIO(t, s)
	group := s.AssetGroups(7)[0]
	s.ChangeGroup(7, "move", n.ID, group.ID)
	waitDocumentIO(t, s)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.AssetsView(c, 7, nil)
	}, 1000, 500)
	if err := tt.Click("查询数据库筛选"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("全部数据库 · 全部数据库"); err != nil {
		t.Fatal(err)
	}
	if got := s.filteredAssets(7); len(got) != 1 || got[0].ID != n.ID {
		t.Fatal("database sentinel hid a legal database name")
	}
	if err := tt.Click("查询分组筛选"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("未分组 · " + group.ID); err != nil {
		t.Fatal(err)
	}
	if s.assetBrowser(7).group != group.ID {
		t.Fatal("ungrouped sentinel hid a legal group name")
	}
}
func TestQueryAssetRestoreDetachesDeletedSavedIdentity(t *testing.T) {
	s, _ := assetTestState(t)
	s.DeleteAsset(7, "q")
	waitDocumentIO(t, s)
	d := s.RestoreDocument(queries.Document{ID: 4, QueryID: "q", Name: "Daily", Text: "SELECT 9", SavedText: "SELECT 9", ProfileID: 7, Database: "app", NamedRevision: 1})
	if d.QueryID() != "" || d.Editor.Buffer.String() != "SELECT 9" || !d.Dirty() || !d.NeedsRecovery() {
		t.Fatal("restored deleted asset could overwrite its original identity")
	}
}
