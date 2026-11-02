// Package favoritesworkspace owns the global favorites directory and dialogs.
package favoritesworkspace

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/favorites"
	"github.com/nyakang/nyadb/locales"
)

type Scope struct {
	ProfileID  int64
	Generation uint64
}
type Item struct {
	Entry                   favorites.Entry
	Profile, Database, Type string
	Reason                  string
	Available               bool
}
type Dependencies struct {
	Items           func([]favorites.Entry) []Item
	Scope           func() Scope
	Open            func(ui.Services, Item)
	Unstar          func(context.Context, int64) error
	ProfilesChanged func()
	Status          func(string)
}
type dialog struct {
	kind, id, name string
	entry          favorites.Entry
	scope          Scope
	revision       uint64
}
type receipt struct {
	snapshot favorites.Snapshot
	err      error
	dialog   *dialog
	kind     string
}
type menuScope struct {
	scope    Scope
	id       string
	revision uint64
}
type State struct {
	deps                         Dependencies
	store                        *favorites.Store
	Invalidate                   func()
	open, loaded, busy, closed   bool
	snapshot                     favorites.Snapshot
	results                      <-chan receipt
	cancel                       context.CancelFunc
	search, group, selected, err string
	row                          int
	list                         ui.ListState
	dialog                       *dialog
	menu                         *menuScope
	pendingAdd                   *dialog
}

func New(deps Dependencies) *State { return &State{deps: deps, row: -1} }
func (s *State) SetStore(store *favorites.Store) {
	s.revoke()
	s.store = store
	s.loaded = false
	s.closed = false
	s.snapshot = favorites.Snapshot{}
}
func (s *State) Available() bool { return !s.closed && s.store != nil }
func (s *State) scope() Scope {
	if s.deps.Scope != nil {
		return s.deps.Scope()
	}
	return Scope{}
}
func (s *State) Open() {
	if !s.Available() {
		return
	}
	s.open = true
	s.Refresh()
}
func (s *State) revoke() {
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel = nil
	s.results = nil
	s.busy = false
	s.dialog = nil
	s.menu = nil
	s.pendingAdd = nil
}
func (s *State) Close()    { s.open = false; s.revoke(); s.loaded = false }
func (s *State) CloseAll() { s.Close(); s.closed = true }
func (s *State) launch(kind string, d *dialog, work func(context.Context) (favorites.Snapshot, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	out := make(chan receipt, 1)
	s.cancel, s.results, s.busy, s.err = cancel, out, true, ""
	invalidate := s.Invalidate
	go func() {
		defer cancel()
		snapshot, err := work(ctx)
		out <- receipt{snapshot: snapshot, err: err, dialog: d, kind: kind}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) Refresh() {
	if !s.Available() || s.busy {
		return
	}
	store := s.store
	s.launch("load", nil, store.Load)
}
func (s *State) Add(e favorites.Entry) {
	if !s.Available() || s.busy {
		return
	}
	s.open = true
	d := &dialog{kind: "add", entry: e, name: e.Name, scope: s.scope()}
	s.pendingAdd = d
	store := s.store
	s.launch("load", nil, store.Load)
}
func (s *State) Consume() {
	if s.closed {
		return
	}
	if d := s.dialog; d != nil && d.scope != s.scope() {
		s.dialog = nil
	}
	if s.results == nil {
		return
	}
	select {
	case r := <-s.results:
		s.results = nil
		s.busy = false
		if s.cancel != nil {
			s.cancel()
		}
		s.cancel = nil
		if r.err != nil {
			s.err = r.err.Error()
			if s.deps.Status != nil {
				s.deps.Status(s.err)
			}
			s.pendingAdd = nil
			return
		}
		if r.kind == "unstar" {
			if s.deps.ProfilesChanged != nil {
				s.deps.ProfilesChanged()
			}
		} else {
			s.snapshot = r.snapshot
			s.loaded = true
		}
		if r.dialog != nil && r.kind == "save" {
			if r.dialog.kind == "add" {
				s.selected = r.dialog.id
			}
			if r.dialog.kind == "delete" && s.selected == r.dialog.id {
				s.selected = ""
			}
			if r.dialog.kind == "delete_group" && s.group == r.dialog.id {
				s.group = ""
			}
		}
		if r.dialog != nil && s.dialog == r.dialog {
			s.dialog = nil
		}
		if d := s.pendingAdd; d != nil {
			s.pendingAdd = nil
			if d.scope == s.scope() {
				duplicate := ""
				for _, e := range s.snapshot.Entries {
					if e.SameTarget(d.entry) {
						duplicate = e.ID
						break
					}
				}
				if duplicate != "" {
					s.selected = duplicate
					s.err = "该对象或查询已在收藏夹中"
				} else {
					d.revision = s.snapshot.Revision
					s.dialog = d
				}
			}
		}
	default:
	}
}
func (s *State) items() []Item {
	if s.deps.Items == nil {
		return nil
	}
	out := []Item{}
	for _, item := range s.deps.Items(append([]favorites.Entry(nil), s.snapshot.Entries...)) {
		e := item.Entry
		if s.group == "ungrouped" && e.GroupID != "" || s.group != "" && s.group != "ungrouped" && e.GroupID != s.group {
			continue
		}
		if s.search != "" && !strings.Contains(strings.ToLower(e.Name+" "+item.Profile+" "+item.Database), strings.ToLower(strings.TrimSpace(s.search))) {
			continue
		}
		out = append(out, item)
	}
	return out
}
func (s *State) selectedItem() (Item, bool) {
	for _, item := range s.items() {
		if item.Entry.ID == s.selected {
			return item, true
		}
	}
	return Item{}, false
}
func (s *State) begin(kind, id string) {
	if !s.loaded || s.busy {
		return
	}
	d := &dialog{kind: kind, id: id, scope: s.scope(), revision: s.snapshot.Revision}
	if kind == "rename" || kind == "delete" {
		item, ok := s.selectedItem()
		if !ok || item.Entry.ID != id {
			return
		}
		d.entry = item.Entry
		d.name = item.Entry.Name
		if kind == "rename" && item.Entry.Kind == "connection" {
			return
		}
	}
	if kind == "rename_group" || kind == "delete_group" {
		for _, g := range s.snapshot.Groups {
			if g.ID == id {
				d.name = g.Name
			}
		}
		if d.name == "" {
			return
		}
	}
	s.dialog = d
}
func (s *State) save(after favorites.Snapshot, d *dialog) {
	before := s.snapshot.Clone()
	store := s.store
	s.launch("save", d, func(ctx context.Context) (favorites.Snapshot, error) { return store.Save(ctx, before, after) })
}
func (s *State) apply() {
	d := s.dialog
	if d == nil || s.busy || d.scope != s.scope() || d.revision != s.snapshot.Revision {
		return
	}
	after := s.snapshot.Clone()
	name := strings.TrimSpace(d.name)
	switch d.kind {
	case "add":
		e := d.entry
		e.ID = favorites.NewID()
		e.Name = name
		d.id = e.ID
		if s.group != "ungrouped" {
			e.GroupID = s.group
		}
		after.Entries = append(after.Entries, e)
	case "rename":
		for i, e := range after.Entries {
			if e.ID == d.id {
				after.Entries[i].Name = name
			}
		}
	case "delete":
		if d.entry.Kind == "connection" {
			if s.deps.Unstar == nil {
				return
			}
			unstar, profile := s.deps.Unstar, d.entry.ProfileID
			s.launch("unstar", d, func(ctx context.Context) (favorites.Snapshot, error) {
				return favorites.Snapshot{}, unstar(ctx, profile)
			})
			return
		}
		entries := after.Entries[:0]
		for _, e := range after.Entries {
			if e.ID != d.id {
				entries = append(entries, e)
			}
		}
		after.Entries = entries
	case "new_group":
		after.Groups = append(after.Groups, favorites.Group{ID: favorites.NewID(), Name: name})
	case "rename_group":
		for i, g := range after.Groups {
			if g.ID == d.id {
				after.Groups[i].Name = name
			}
		}
	case "delete_group":
		groups := after.Groups[:0]
		for _, g := range after.Groups {
			if g.ID != d.id {
				groups = append(groups, g)
			}
		}
		after.Groups = groups
		for i, e := range after.Entries {
			if e.GroupID == d.id {
				after.Entries[i].GroupID = ""
			}
		}

	default:
		return
	}
	s.save(after, d)
}
func (s *State) move(id, group string) {
	if !s.loaded || s.busy {
		return
	}
	after := s.snapshot.Clone()
	for i, e := range after.Entries {
		if e.ID == id {
			after.Entries[i].GroupID = group
			s.save(after, nil)
			return
		}
	}
}
func (s *State) openItem(services ui.Services, item Item) {
	if s.busy || !item.Available || s.deps.Open == nil {
		return
	}
	s.deps.Open(services, item)
	s.open = false
}
func (s *State) contextMenu(services ui.Services, m *ui.Menu, item Item) {
	action, group := "", ""
	if m.Item(locales.Text("打开收藏")).Shortcut(0, ui.KeyEnter).Disabled(s.busy || !item.Available).Chosen() {
		action = "open"
	}
	if m.Item(locales.Text("重命名收藏")).Shortcut(0, ui.KeyF2).Disabled(s.busy || item.Entry.Kind == "connection").Chosen() {
		action = "rename"
	}
	m.Submenu(locales.Text("移动到收藏分组"), func(sub *ui.Menu) {
		if sub.Item(locales.Text("未分组")).Chosen() {
			action = "move"
		}
		for _, g := range s.snapshot.Groups {
			if sub.Item(g.Name).Chosen() {
				action, group = "move", g.ID
			}
		}
	}).Disabled(s.busy || item.Entry.Kind == "connection")
	if m.Item(locales.Text("移除收藏…")).Shortcut(0, ui.KeyDelete).Disabled(s.busy).Chosen() {
		action = "delete"
	}
	if action == "" {
		s.menu = &menuScope{scope: s.scope(), id: item.Entry.ID, revision: s.snapshot.Revision}
		return
	}
	captured := s.menu
	s.menu = nil
	if captured == nil || captured.scope != s.scope() || captured.revision != s.snapshot.Revision || captured.id != item.Entry.ID {
		return
	}
	s.selected = item.Entry.ID
	switch action {
	case "open":
		s.openItem(services, item)
	case "move":
		s.move(item.Entry.ID, group)
	default:
		s.begin(action, item.Entry.ID)
	}
}
