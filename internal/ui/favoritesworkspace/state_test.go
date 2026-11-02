package favoritesworkspace

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/favorites"
)

type testRepository struct {
	mu               sync.Mutex
	raw              string
	started, release chan struct{}
}

func (r *testRepository) Read(context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.raw, nil
}
func (r *testRepository) Publish(_ context.Context, before, after string) (bool, error) {
	if r.started != nil {
		close(r.started)
		<-r.release
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.raw != before {
		return false, nil
	}
	r.raw = after
	return true, nil
}
func favoritesTestState(t *testing.T) (*State, *testRepository, *Scope, *ui.Tester) {
	t.Helper()
	repo := &testRepository{}
	scope := &Scope{ProfileID: 7, Generation: 3}
	s := New(Dependencies{Scope: func() Scope { return *scope }, Items: func(entries []favorites.Entry) []Item {
		out := []Item{}
		for _, e := range entries {
			out = append(out, Item{Entry: e, Profile: "Fixture", Type: "查询", Available: true})
		}
		return out
	}})
	s.SetStore(favorites.NewStore(repo))
	t.Cleanup(s.CloseAll)
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(); s.View(c) }, 1280, 800)
	return s, repo, scope, tt
}
func settle(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for s.busy && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	tt.Frame()
	if s.busy {
		t.Fatal("favorites receipt did not settle")
	}
}
func addFavorite(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	s.Add(favorites.Entry{ProfileID: 7, Kind: "query", QueryID: "q", Name: "Original"})
	settle(t, s, tt)
	if s.dialog == nil {
		t.Fatal("add dialog missing")
	}
	if err := tt.Click("确认收藏操作"); err != nil {
		t.Fatal(err)
	}
	settle(t, s, tt)
}
func TestFavoritesTesterSearchRenameMoveDeleteGroupAndRestart(t *testing.T) {
	s, repo, _, tt := favoritesTestState(t)
	addFavorite(t, s, tt)
	id := s.snapshot.Entries[0].ID
	if s.selected != id {
		t.Fatal("new stable ID not selected")
	}
	if err := tt.Click("Original"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyF2)
	if s.dialog == nil || s.dialog.kind != "rename" {
		t.Fatal("F2 did not rename selection")
	}
	if err := tt.Click("收藏名称"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("Renamed")
	if err := tt.Click("确认收藏操作"); err != nil {
		t.Fatal(err)
	}
	settle(t, s, tt)
	if s.snapshot.Entries[0].ID != id || s.selected != id {
		t.Fatal("rename lost stable selection")
	}
	if err := tt.Click("新建收藏分组"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("收藏名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Group")
	if err := tt.Click("确认收藏操作"); err != nil {
		t.Fatal(err)
	}
	settle(t, s, tt)
	group := s.snapshot.Groups[0].ID
	if err := tt.RightClick("Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("移动到收藏分组", "Group"); err != nil {
		t.Fatal(err)
	}
	settle(t, s, tt)
	restored, err := favorites.NewStore(repo).Load(context.Background())
	if err != nil || restored.Entries[0].GroupID != group {
		t.Fatal("restart lost membership")
	}
	s.begin("delete_group", group)
	tt.Frame()
	if err := tt.Click("确认收藏操作"); err != nil {
		t.Fatal(err)
	}
	settle(t, s, tt)
	if len(s.snapshot.Groups) != 0 || s.snapshot.Entries[0].GroupID != "" {
		t.Fatal("group deletion removed or stranded query")
	}
	if err := tt.Click("搜索收藏"); err != nil {
		t.Fatal(err)
	}
	tt.Type("missing")
	if len(s.items()) != 0 {
		t.Fatal("search ignored text")
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("renamed")
	if err := tt.Click("Renamed"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyDelete)
	if s.dialog == nil || s.dialog.kind != "delete" || len(s.snapshot.Entries) != 1 {
		t.Fatal("Delete bypassed confirmation")
	}
	if err := tt.Click("确认收藏操作"); err != nil {
		t.Fatal(err)
	}
	settle(t, s, tt)
	if len(s.snapshot.Entries) != 0 {
		t.Fatal("confirmed removal did not persist")
	}
}
func TestFavoritesConflictRetainsDialogAndOriginalList(t *testing.T) {
	s, repo, _, tt := favoritesTestState(t)
	addFavorite(t, s, tt)
	id := s.selected
	s.begin("rename", id)
	s.dialog.name = "Local"
	external := favorites.NewStore(repo)
	before, err := external.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after := before.Clone()
	after.Entries[0].Name = "External"
	if _, err = external.Save(context.Background(), before, after); err != nil {
		t.Fatal(err)
	}
	s.apply()
	settle(t, s, tt)
	if s.dialog == nil || s.dialog.name != "Local" || s.snapshot.Entries[0].Name != "Original" || s.err == "" {
		t.Fatal("conflict discarded editor or overwrote displayed original")
	}
	latest, _ := external.Load(context.Background())
	if latest.Entries[0].Name != "External" {
		t.Fatal("conflict overwrote external version")
	}
}
func TestFavoritesClosingAndScopeChangeRevokeReceiptsAndMenus(t *testing.T) {
	s, repo, scope, tt := favoritesTestState(t)
	addFavorite(t, s, tt)
	if err := tt.RightClick("Original"); err != nil {
		t.Fatal(err)
	}
	scope.Generation++
	if err := tt.ChooseMenuItem("重命名收藏"); err != nil {
		t.Fatal(err)
	}
	if s.dialog != nil {
		t.Fatal("stale generation menu opened new dialog")
	}
	s.begin("rename", s.selected)
	s.dialog.name = "Saved before close"
	repo.started, repo.release = make(chan struct{}), make(chan struct{})
	s.apply()
	<-repo.started
	receipt := s.results
	s.Close()
	s.SetStore(favorites.NewStore(&testRepository{}))
	s.Open()
	settle(t, s, tt)
	close(repo.release)
	select {
	case <-receipt:
	case <-time.After(time.Second):
		t.Fatal("late worker did not settle")
	}
	s.Consume()
	if len(s.snapshot.Entries) != 0 || s.dialog != nil || s.err != "" {
		t.Fatal("closed receipt changed replacement store scope")
	}
}

func TestFavoritesReservedGroupNameRemainsSelectable(t *testing.T) {
	s, _, _, tt := favoritesTestState(t)
	addFavorite(t, s, tt)
	s.begin("new_group", "")
	s.dialog.name = "未分组"
	s.apply()
	settle(t, s, tt)
	id := s.snapshot.Groups[0].ID
	if err := tt.Click("收藏分组"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	if s.group != id {
		t.Fatal("a group named like the built-in filter cannot be selected")
	}
}
