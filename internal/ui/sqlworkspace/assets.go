package sqlworkspace

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/queries"
)

type assetBrowser struct {
	search, database, group, selected, err string
	row                                    int
	list                                   ui.ListState
}
type assetDialog struct {
	profile             int64
	id, kind, name, err string
	revision            uint64
	token               uint64
}
type assetMenuScope struct {
	profile         int64
	id              string
	revision        uint64
	catalogRevision uint64
}

func (s *State) ActivateAssets(profile int64) {
	p := &s.persistence
	if p.assetScope != profile {
		p.assetScope = profile
		p.assetDialog = nil
		p.assetMenu = nil
		p.assetGroupMenu = nil
	}
}
func (s *State) assetBrowser(profile int64) *assetBrowser {
	p := &s.persistence
	if p.assets == nil {
		p.assets = map[int64]*assetBrowser{}
	}
	if p.assets[profile] == nil {
		p.assets[profile] = &assetBrowser{row: -1}
	}
	return p.assets[profile]
}
func (s *State) Catalog(profile int64) queries.Catalog {
	c := s.persistence.catalogs[profile]
	c.ProfileID = profile
	raw, _ := json.Marshal(c)
	var owned queries.Catalog
	_ = json.Unmarshal(raw, &owned)
	if owned.Membership == nil {
		owned.Membership = map[string]string{}
	}
	return owned
}
func (s *State) AssetGroups(profile int64) []queries.Group {
	return append([]queries.Group(nil), s.persistence.catalogs[profile].Groups...)
}
func (s *State) AssetGroup(profile int64, id string) string {
	return s.persistence.catalogs[profile].Membership[id]
}
func (s *State) AssetGroupName(profile int64, id string) string {
	for _, g := range s.persistence.catalogs[profile].Groups {
		if g.ID == id {
			return g.Name
		}
	}
	return ""
}
func (s *State) asset(profile int64, id string) (queries.Named, bool) {
	for _, n := range s.Named(profile) {
		if n.ID == id {
			return n, true
		}
	}
	return queries.Named{}, false
}
func (s *State) assetWritable(profile int64) bool {
	p := &s.persistence
	return !p.closed && !p.loading && !p.blocked && p.store != nil && !p.catalogBusy[profile] && !p.assetsRefreshing
}
func (s *State) assetPending(id string) bool {
	if s.persistence.assetBusy[id] {
		return true
	}
	for _, d := range s.documents {
		if !d.closed && d.queryID == id && d.saving {
			return true
		}
	}
	return false
}
func (s *State) beginAsset(profile int64, id, kind string) {
	if profile != s.persistence.assetScope || !s.assetWritable(profile) || s.assetPending(id) {
		return
	}
	d := &assetDialog{profile: profile, id: id, kind: kind}
	s.persistence.assetSequence++
	d.token = s.persistence.assetSequence
	if kind == "rename" || kind == "delete" {
		n, ok := s.asset(profile, id)
		if !ok {
			return
		}
		d.name, d.revision = n.Name, n.Revision
	}
	if kind == "rename_group" || kind == "delete_group" {
		d.name = s.AssetGroupName(profile, id)
		if d.name == "" {
			return
		}
	}
	if strings.Contains(kind, "group") {
		d.revision = s.Catalog(profile).Revision
	}
	s.persistence.assetDialog = d
}
func (s *State) RenameAsset(profile int64, id, name string) {
	n, ok := s.asset(profile, id)
	if !ok || profile != s.persistence.assetScope || !s.assetWritable(profile) || s.assetPending(id) {
		return
	}
	if s.persistence.assetBusy == nil {
		s.persistence.assetBusy = map[string]bool{}
	}
	s.persistence.assetBusy[id] = true
	store := s.persistence.store
	token := s.assetOperationToken(profile, id)
	s.startIO(nil, nil, func() ioResult {
		v, e := store.RenameNamed(context.Background(), profile, id, n.Revision, name)
		return ioResult{kind: "rename_asset", named: v, before: n, profile: profile, err: e, assetToken: token}
	})
}
func (s *State) DeleteAsset(profile int64, id string) {
	n, ok := s.asset(profile, id)
	if !ok || profile != s.persistence.assetScope || !s.assetWritable(profile) || s.assetPending(id) {
		return
	}
	if s.persistence.assetBusy == nil {
		s.persistence.assetBusy = map[string]bool{}
	}
	s.persistence.assetBusy[id] = true
	store := s.persistence.store
	token := s.assetOperationToken(profile, id)
	s.startIO(nil, nil, func() ioResult {
		e := store.DeleteNamedRevision(context.Background(), profile, id, n.Revision)
		return ioResult{kind: "delete_asset", deleted: id, before: n, profile: profile, err: e, assetToken: token}
	})
}
func (s *State) ChangeGroup(profile int64, kind, id, name string) {
	if profile != s.persistence.assetScope || !s.assetWritable(profile) {
		return
	}
	c := s.Catalog(profile)
	name = strings.TrimSpace(name)
	switch kind {
	case "new_group":
		c.Groups = append(c.Groups, queries.Group{ID: queries.NewID(), Name: name})
	case "rename_group":
		for i, g := range c.Groups {
			if g.ID == id {
				c.Groups[i].Name = name
			}
		}
	case "delete_group":
		var groups []queries.Group
		for _, g := range c.Groups {
			if g.ID != id {
				groups = append(groups, g)
			}
		}
		c.Groups = groups
		for q, g := range c.Membership {
			if g == id {
				delete(c.Membership, q)
			}
		}
	case "move":
		if _, ok := s.asset(profile, id); !ok {
			return
		}
		if name == "" {
			delete(c.Membership, id)
		} else {
			c.Membership[id] = name
		}
	default:
		return
	}
	c.Revision++
	if s.persistence.catalogBusy == nil {
		s.persistence.catalogBusy = map[int64]bool{}
	}
	s.persistence.catalogBusy[profile] = true
	store := s.persistence.store
	token := s.assetOperationToken(profile, id)
	s.startIO(nil, nil, func() ioResult {
		return ioResult{kind: "catalog", profile: profile, catalogs: []queries.Catalog{c}, err: store.SaveCatalog(context.Background(), c), assetToken: token}
	})
}
func (s *State) consumeAssetIO(r ioResult) {
	p := &s.persistence
	switch r.kind {
	case "rename_asset", "delete_asset":
		delete(p.assetBusy, r.before.ID)
	case "catalog":
		delete(p.catalogBusy, r.profile)
	case "refresh_assets":
		p.assetsRefreshing = false
	default:
		return
	}
	b := s.assetBrowser(r.profile)
	if r.err != nil {
		b.err = r.err.Error()
		if d := p.assetDialog; d != nil && d.token == r.assetToken {
			d.err = b.err
		}
		return
	}
	b.err = ""
	if d := p.assetDialog; d != nil && d.token == r.assetToken {
		p.assetDialog = nil
	}
	switch r.kind {
	case "refresh_assets":
		p.names = r.names
		p.catalogs = make(map[int64]queries.Catalog)
		for _, catalog := range r.catalogs {
			p.catalogs[catalog.ProfileID] = catalog
		}
	case "catalog":
		if p.catalogs == nil {
			p.catalogs = map[int64]queries.Catalog{}
		}
		p.catalogs[r.profile] = r.catalogs[0]
	case "rename_asset":
		for i, n := range p.names {
			if n.ID == r.named.ID {
				p.names[i] = r.named
			}
		}
		for _, d := range s.documents {
			if !d.closed && d.queryID == r.named.ID {
				d.name = r.named.Name
				if d.namedRevision == r.before.Revision && d.savedText == r.before.Text {
					d.namedRevision = r.named.Revision
				}
				d.revision++
			}
		}
	case "delete_asset":
		var names []queries.Named
		for _, n := range p.names {
			if n.ID != r.deleted {
				names = append(names, n)
			}
		}
		p.names = names
		for _, d := range s.documents {
			if !d.closed && d.queryID == r.deleted {
				d.queryID, d.name, d.savedText = "", "", ""
				d.namedRevision = 0
				d.revision++
			}
		}
		if b.selected == r.deleted {
			b.selected = ""
			b.row = -1
		}
	}
	if s.deps.AssetsChanged != nil {
		s.deps.AssetsChanged(r.profile, r.before.ID, r.kind == "delete_asset")
	}
}

func (s *State) assetOperationToken(profile int64, id string) uint64 {
	if d := s.persistence.assetDialog; d != nil && d.profile == profile && d.id == id {
		return d.token
	}
	return 0
}
func (s *State) RefreshAssets(profile int64) {
	if !s.assetWritable(profile) || len(s.persistence.assetBusy) > 0 || len(s.persistence.catalogBusy) > 0 {
		return
	}
	for _, d := range s.documents {
		if d.saving {
			return
		}
	}
	s.persistence.assetsRefreshing = true
	store := s.persistence.store
	s.startIO(nil, nil, func() ioResult {
		names, e := store.ListNamed(context.Background(), 0)
		if e != nil {
			return ioResult{kind: "refresh_assets", profile: profile, err: e}
		}
		catalogs, e := store.ListCatalogs(context.Background())
		return ioResult{kind: "refresh_assets", profile: profile, names: names, catalogs: catalogs, err: e}
	})
}
func (s *State) filteredAssets(profile int64) []queries.Named {
	b := s.assetBrowser(profile)
	var out []queries.Named
	for _, n := range s.Named(profile) {
		if b.search != "" && !strings.Contains(strings.ToLower(n.Name), strings.ToLower(b.search)) {
			continue
		}
		if b.database != "" && n.Database != b.database {
			continue
		}
		group := s.AssetGroup(profile, n.ID)
		if b.group == "ungrouped" && group != "" {
			continue
		}
		if b.group != "" && b.group != "ungrouped" && group != b.group {
			continue
		}
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Database != out[j].Database {
			return out[i].Database < out[j].Database
		}
		return out[i].ID < out[j].ID
	})
	return out
}
