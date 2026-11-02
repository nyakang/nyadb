package shell

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/favorites"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/favoritesworkspace"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
)

func TestShellURIImportUsesEditorAndNativeTitleWithoutAutoConnecting(t *testing.T) {
	s, tt, service, _ := profileTestState(t)
	s.openConnectionDialog()
	tt.Frame()
	if err := tt.Click("通过 URI 新建连接…"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 2 || s.ConnectionWindowTitle() != "通过 URI 新建连接" {
		t.Fatal("URI page not owned by editor/native title")
	}
	if err := tt.Click("连接 URI"); err != nil {
		t.Fatal(err)
	}
	tt.Type("mysql://alice:temporary-fixture@localhost:3307/original")
	if err := tt.Click("导入到连接表单"); err != nil {
		t.Fatal(err)
	}
	if s.connectionStep != 1 || !s.connectionEditor.Active() || s.connectionEditor.Draft().Host != "localhost" || s.connectionEditor.Draft().Database != "original" || s.sessions.HasSessions() {
		t.Fatal("URI failed or automatically connected")
	}
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 0 {
		t.Fatal("URI silently saved profile")
	}
	s.ConnectionWindowClosed()
	if s.connectionEditor.Active() || s.connectionEditor.URIActive() {
		t.Fatal("native close retained URI editor")
	}
}

func TestShellFavoritesOfflineQueryLatestRevisionAndDeletedAsset(t *testing.T) {
	s, tt, profiles, _ := profileTestState(t)
	ctx := context.Background()
	a, err := profiles.Create(ctx, connections.Profile{Name: "Favorite A", Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "a.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := profiles.Create(ctx, connections.Profile{Name: "Favorite B", Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "b.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(b.Name)
	db, err := localdb.Open(filepath.Join(t.TempDir(), "assets.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer s.CloseMySQLSessions()
	store := queries.NewStore(localdb.NewSQLRepository(db), &assetTestKey{})
	n := queries.Named{ID: "fav-query", ProfileID: a.ID, Database: "main", Name: "Original", Text: "SELECT 'old'", Revision: 1}
	if err = store.SaveNamed(ctx, n); err != nil {
		t.Fatal(err)
	}
	s.SetSQLPersistence(store, sqlworkspace.Files{}, nil)
	fs := favorites.NewStore(localdb.NewFavoritesRepository(db))
	before, _ := fs.Load(ctx)
	after := before.Clone()
	after.Entries = []favorites.Entry{{ID: "favorite", ProfileID: a.ID, Name: "Alias", Kind: "query", QueryID: n.ID}}
	if _, err = fs.Save(ctx, before, after); err != nil {
		t.Fatal(err)
	}
	s.SetFavoritesStore(fs)
	until := time.Now().Add(time.Second)
	for s.sqlWorkspace.PersistenceLoading() && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if err = tt.Click("收藏夹"); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem("管理收藏夹…"); err != nil {
		t.Fatal(err)
	}
	// Public reference resolves new encrypted text rather than the cached snapshot.
	n.Revision = 2
	n.Name = "Latest"
	n.Text = "SELECT 'latest'"
	if err = store.SaveNamed(ctx, n); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(time.Second)
	for !tt.HasText("Alias") && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if err = tt.Click("Alias"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnter)
	until = time.Now().Add(time.Second)
	for len(s.tabs) == 1 && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	q := s.currentTab().query
	if q == nil || q.QueryID() != n.ID || q.Editor.Buffer.String() != n.Text || !q.NeedsRecovery() || len(s.tabs) != 2 || s.sessions.HasSessions() {
		t.Fatal("offline favorite did not restore latest original query safely")
	}
	if err = store.DeleteNamedRevision(ctx, a.ID, n.ID, n.Revision); err != nil {
		t.Fatal(err)
	}
	s.openFavorite(ui.Services{}, favoritesworkspace.Item{Entry: after.Entries[0], Available: true})
	until = time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if len(s.tabs) != 2 || q.Editor.Buffer.String() != n.Text {
		t.Fatal("deleted favorite resurrected query or changed open text")
	}
}

func TestObjectSearchRealSQLiteOpensCompleteMetadataAndReusesQualifiedView(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	ctx := context.Background()
	p, err := service.Create(ctx, connections.Profile{Name: "Search fixture", Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "user.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	tt.Frame()
	s.startMySQLSession(services)
	until := time.Now().Add(3 * time.Second)
	for s.sessions.StatusFor(p.ID) == "连接中" && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	session, err := s.engineSession(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := session.Reserve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lease.Execute(ctx, "CREATE TABLE records(id INTEGER PRIMARY KEY,invoice TEXT); CREATE TABLE unrelated(id INTEGER); CREATE VIEW records_view AS SELECT * FROM records;")
	lease.Close()
	if err != nil {
		t.Fatal(err)
	}
	s.openObjectSearch()
	tt.Frame()
	if err = tt.Click("查找对象名称、注释或定义"); err != nil {
		t.Fatal(err)
	}
	tt.Type("records_view")
	if err = tt.Click("开始查找"); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(3 * time.Second)
	for !tt.HasText("records_view") && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if err = tt.Click("records_view"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnter)
	until = time.Now().Add(3 * time.Second)
	for (s.currentTab().data == nil || s.currentTab().data.Busy() || s.sessions.Current().Pending != "") && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	tab := s.currentTab()
	if tab.data == nil || tab.data.Object().Kind != "view" {
		t.Fatal("search view downgraded to table")
	}
	if len(s.sessions.CacheFor(p.ID)["main"]) != 3 {
		t.Fatal("search result replaced full session metadata cache")
	}
	ref := tab.data.Object()
	generation := s.sessions.Current().Generation
	s.objectWorkspace.ResolveObjectReference(services, p.ID, generation, ref, "main")
	until = time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		tt.Frame()
		if s.currentTab() == tab && len(s.tabs) == 2 {
			time.Sleep(20 * time.Millisecond)
			tt.Frame()
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(s.tabs) != 2 || s.currentTab() != tab {
		t.Fatal("fresh favorite reference duplicated qualified view document")
	}
}
