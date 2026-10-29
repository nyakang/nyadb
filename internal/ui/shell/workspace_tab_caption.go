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
	case tabMySQLData:
		database, table = t.dataDatabase, t.dataTable
	case tabStructure:
		database, table, suffix = t.design.Database(), t.design.Table(), " · 设计"
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
			otherDatabase, otherTable = other.dataDatabase, other.dataTable
		case tabStructure:
			otherDatabase, otherTable = other.design.Database(), other.design.Table()
		}
		if otherTable == table && otherDatabase != database {
			return database + "." + table + suffix
		}
	}
	return table + suffix
}
