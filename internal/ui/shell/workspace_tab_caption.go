package shell

import "github.com/nyakang/nyadb/locales"

// Keep tabs compact without losing qualified identity. The full database and
// table remain in each tab's title/tooltip and its close accessibility label.
// Different databases containing the same table get disambiguated visibly.
func (s *State) workspaceTabCaption(t *tab) string {
	if t == nil {
		return ""
	}
	database, table, suffix := "", "", ""
	switch t.kind {
	case tabSQL:
		name := t.query.Name()
		if t.query.Dirty() {
			name += " *"
		}
		return name
	case tabMySQLData:
		database, table = t.data.Database(), t.data.Table()
	case tabStructure:
		database, table, suffix = t.object.Database(), t.object.Table(), " · 设计"
		if t.object.DesignDirty() {
			suffix += " *"
		}
	default:
		return locales.Text(t.title)
	}
	if table == "" {
		return locales.Text(t.title)
	}
	for _, other := range s.tabs {
		if other == t {
			continue
		}
		otherDatabase, otherTable := "", ""
		switch other.kind {
		case tabMySQLData:
			otherDatabase, otherTable = other.data.Database(), other.data.Table()
		case tabStructure:
			otherDatabase, otherTable = other.object.Database(), other.object.Table()
		}
		if otherTable == table && otherDatabase != database {
			return database + "." + table + suffix
		}
	}
	return table + suffix
}
