package shell

import (
	"context"
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/favorites"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/favoritesworkspace"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) SetFavoritesStore(store *favorites.Store) { s.favorites.SetStore(store) }
func (s *State) favoritesScope() favoritesworkspace.Scope {
	w := s.sessions.Current()
	return favoritesworkspace.Scope{ProfileID: s.selectedProfileID, Generation: w.Generation}
}
func (s *State) favoritesDependencies() favoritesworkspace.Dependencies {
	return favoritesworkspace.Dependencies{Items: s.favoriteItems, Scope: s.favoritesScope, Open: s.openFavorite, ProfilesChanged: s.loadProfiles, Status: func(text string) { s.status = text }, Unstar: func(ctx context.Context, profile int64) error {
		return s.profileService.SetStarred(ctx, profile, false)
	}}
}
func (s *State) favoriteItems(entries []favorites.Entry) []favoritesworkspace.Item {
	profiles := s.navigation.Profiles()
	names := map[int64]string{}
	out := []favoritesworkspace.Item{}
	for _, p := range profiles {
		names[p.ID] = p.Name
		if p.Starred {
			out = append(out, favoritesworkspace.Item{Entry: favorites.Entry{ID: fmt.Sprintf("connection:%d", p.ID), Kind: "connection", ProfileID: p.ID, Name: p.Name}, Profile: p.Name, Type: "连接", Available: true})
		}
	}
	for _, e := range entries {
		item := favoritesworkspace.Item{Entry: e, Profile: names[e.ProfileID], Available: names[e.ProfileID] != ""}
		if e.Kind == "query" {
			item.Type = "查询"
			found := false
			if s.sqlWorkspace.PersistenceLoading() {
				item.Available = false
			}
			for _, n := range s.sqlWorkspace.Named(e.ProfileID) {
				if n.ID == e.QueryID {
					found = true
					item.Database = n.Database
				}
			}
			item.Available = item.Available && found
			if !found {
				item.Reason = "已失效"
			}
		} else {
			item.Database = e.NamespaceLabel
			item.Type = favoriteObjectKind(e.Object.Kind)
			item.Available = item.Available && s.sessions.StatusFor(e.ProfileID) == "已连接" && s.runtime.Namespace(e.ProfileID, e.NamespaceLabel) == e.Object.Namespace()
			if s.sessions.StatusFor(e.ProfileID) != "已连接" {
				item.Reason = "离线"
			}
		}
		out = append(out, item)
	}
	return out
}
func favoriteObjectKind(kind string) string {
	switch kind {
	case "table":
		return "表"
	case "view":
		return "视图"
	case "function":
		return "函数"
	case "procedure":
		return "存储过程"
	case "event":
		return "事件"
	case "sequence":
		return "序列"
	}
	return kind
}
func (s *State) openFavorite(services ui.Services, item favoritesworkspace.Item) {
	e := item.Entry
	switch e.Kind {
	case "connection":
		for _, p := range s.navigation.Profiles() {
			if p.ID == e.ProfileID {
				s.selectProfileByName(p.Name)
				return
			}
		}
	case "query":
		scope := s.favoritesScope()
		s.sqlWorkspace.OpenAssetReference(e.ProfileID, e.QueryID, func(n queries.Named) {
			if s.favoritesScope() == scope {
				s.openNamedQuery(n)
			}
		})
	case "object":
		w := s.sessions.Workspace(e.ProfileID)
		if w == nil || w.Status != "已连接" || s.runtime.Namespace(e.ProfileID, e.NamespaceLabel) != e.Object.Namespace() {
			s.status = "原连接或命名空间已失效，请显式重连后打开收藏"
			return
		}
		s.objectWorkspace.ResolveObjectReference(services, e.ProfileID, w.Generation, e.Object, e.NamespaceLabel)
	}
}
func (s *State) favoriteQuery(n queries.Named) {
	s.favorites.Add(favorites.Entry{ProfileID: n.ProfileID, Kind: "query", QueryID: n.ID, Name: n.Name})
}
func (s *State) favoriteObject(_ ui.Services, profile int64, database, name, kind string) {
	if !s.favorites.Available() {
		return
	}
	ref := s.runtime.Namespace(profile, database).Object(searchFavoriteKind(kind), name)
	if ref.Catalog == "" && ref.Schema == "" {
		return
	}
	s.favorites.Add(favorites.Entry{ProfileID: profile, Kind: "object", Object: ref, NamespaceLabel: database, Name: name})
}
func searchFavoriteKind(kind string) string {
	switch kind {
	case "表":
		return "table"
	case "视图":
		return "view"
	case "函数":
		return "function"
	case "存储过程":
		return "procedure"
	case "事件":
		return "event"
	case "序列":
		return "sequence"
	}
	return kind
}
func (s *State) currentFavorite() (favorites.Entry, bool) {
	t := s.currentTab()
	if t.query != nil && t.query.QueryID() != "" {
		for _, n := range s.sqlWorkspace.Named(0) {
			if n.ID == t.query.QueryID() {
				return favorites.Entry{ProfileID: n.ProfileID, Kind: "query", QueryID: n.ID, Name: n.Name}, true
			}
		}
	}
	if t.data != nil {
		ref := t.data.Object()
		return favorites.Entry{ProfileID: ref.ProfileID, Kind: "object", Name: ref.Name, Object: ref, NamespaceLabel: t.data.Database()}, ref.ProfileID > 0
	}
	if t.object != nil {
		ref := t.object.Object()
		if t.kind == tabProgram {
			ref = t.object.ProgramData().Ref
		}
		return favorites.Entry{ProfileID: ref.ProfileID, Kind: "object", Name: ref.Name, Object: ref, NamespaceLabel: t.object.Database()}, ref.ProfileID > 0
	}
	return favorites.Entry{}, false
}
func (s *State) favoritesMenu(m *ui.Menu) {
	original := s.favoriteMenuScope
	scope := s.favoritesScope()
	entry, canAdd := s.currentFavorite()
	action := ""
	if m.Item(locales.Text("管理收藏夹…")).Disabled(!s.favorites.Available()).Chosen() {
		action = "manage"
	}
	if m.Item(locales.Text("添加当前对象或查询到收藏夹…")).Disabled(!s.favorites.Available() || !canAdd).Chosen() {
		action = "add"
	}
	if action == "" {
		s.favoriteMenuScope = &favoriteMenuScope{scope: scope, tab: s.currentTab().id, entry: entry}
		return
	}
	s.favoriteMenuScope = nil
	if original == nil || original.scope != scope || original.tab != s.currentTab().id {
		return
	}
	if action == "manage" {
		s.favorites.Open()
	} else if original.entry == entry && canAdd {
		s.favorites.Add(entry)
	}
}

type favoriteMenuScope struct {
	scope favoritesworkspace.Scope
	tab   uint64
	entry favorites.Entry
}
