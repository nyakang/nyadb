package shell

import (
	"sort"

	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *State) sqlCompletionCatalog(id int64, namespace string) sqltext.CompletionCatalog {
	catalog := sqltext.CompletionCatalog{Namespace: namespace, Dialect: "mysql"}
	known := false
	for _, p := range s.navigation.Profiles() {
		if p.ID == id {
			d, ok := connections.Describe(p.Kind)
			if !ok {
				return catalog
			}
			catalog.Dialect, known = d.Dialect, true
			break
		}
	}
	if !known {
		return catalog
	}
	type key struct{ namespace, name string }
	objects := map[key]*sqltext.CompletionObject{}
	add := func(db, name string, columns []string) {
		k := key{db, name}
		if objects[k] == nil {
			objects[k] = &sqltext.CompletionObject{Namespace: db, Name: name}
		}
		if columns != nil {
			objects[k].Columns = append([]string(nil), columns...)
		}
	}
	for db, list := range s.sessions.CacheFor(id) {
		catalog.Namespaces = append(catalog.Namespaces, db)
		for _, obj := range list {
			if obj.Kind == "表" || obj.Kind == "视图" {
				add(db, obj.Name, nil)
			}
		}
	}
	for _, t := range s.tabs {
		if t.data != nil && t.data.ProfileID() == id && t.data.Page() != nil {
			add(t.data.Database(), t.data.Table(), t.data.Page().Columns)
		}
		if t.object != nil && t.object.ProfileID() == id && t.object.Data() != nil {
			var columns []string
			for _, column := range t.object.Data().Columns {
				columns = append(columns, column.Name)
			}
			add(t.object.Database(), t.object.Table(), columns)
		}
	}
	for _, object := range objects {
		catalog.Objects = append(catalog.Objects, *object)
	}
	sort.Strings(catalog.Namespaces)
	sort.Slice(catalog.Objects, func(i, j int) bool {
		a, b := catalog.Objects[i], catalog.Objects[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return catalog
}
