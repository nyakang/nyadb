package shell

import (
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// Catalog labels and colors are newly authored for NyaDB, not extracted
// from the Navicat executable. Unsupported drivers are discovery-only.
type catalogEntry struct {
	name, engine, vendor, section, symbol, color string
	kind                                         connections.Kind
}

var databaseCatalog = []catalogEntry{
	{"MySQL", "MySQL", "", "全部", "mysql-type", "#24bf65", connections.MySQL},
	{"PostgreSQL", "PostgreSQL", "", "全部", "postgres-type", "#397cdf", connections.PostgreSQL},
	{"SQL Server", "SQL Server", "", "全部", "mssql-type", "#edab24", ""},
	{"Oracle", "Oracle", "", "全部", "oracle-type", "#ef3b5c", ""},
	{"SQLite", "SQLite", "", "全部", "sqlite-type", "#4cbfc2", connections.SQLite},
	{"MariaDB", "MariaDB", "", "全部", "maria-type", "#b58a61", ""},
	{"MongoDB", "MongoDB", "", "全部", "mongo-type", "#bb691d", ""},
	{"Redis", "Redis", "", "全部", "redis-type", "#e77679", ""},
	{"Snowflake", "Snowflake", "", "全部", "snowflake-type", "#3daac7", ""},
	{"达梦", "达梦", "", "全部", "dm-type", "#7e63cc", ""},
	{"OceanBase", "OceanBase", "", "全部", "ocean-type", "#3486d1", ""},
	{"阿里云 RDS MySQL 版", "MySQL", "阿里云", "云数据库", "database", "#32c963", ""},
	{"阿里云 RDS PostgreSQL 版", "PostgreSQL", "阿里云", "云数据库", "database", "#4b86e1", ""},
	{"阿里云 RDS SQL Server 版", "SQL Server", "阿里云", "云数据库", "table", "#f0a90d", ""},
	{"阿里云 MongoDB 版", "MongoDB", "阿里云", "云数据库", "database", "#b76115", ""},
	{"阿里云 Redis 版", "Redis", "阿里云", "云数据库", "database", "#ee8686", ""},
	{"腾讯云 MySQL", "MySQL", "腾讯云", "云数据库", "database", "#44aee0", ""},
	{"华为云 PostgreSQL", "PostgreSQL", "华为", "云数据库", "database", "#d97777", ""},
	{"AWS RDS MySQL", "MySQL", "AWS", "云数据库", "database", "#dd9933", ""},
	{"Azure SQL", "SQL Server", "Microsoft", "云数据库", "table", "#378de0", ""},
}

var catalogEngines = []string{"MySQL", "PostgreSQL", "SQL Server", "Oracle", "SQLite", "MariaDB", "MongoDB", "Redis", "Snowflake"}
var catalogVendors = catalogTreeVendorOrder

func catalogMatch(e catalogEntry, query string, engines, vendors map[string]bool) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q != "" && !strings.Contains(strings.ToLower(e.name+" "+e.engine+" "+e.vendor), q) {
		return false
	}
	if len(engines) != 0 && !engines[e.engine] {
		return false
	}
	if len(vendors) != 0 && !vendors[e.vendor] && !(e.section == "全部" && e.engine == "达梦" && vendors["达梦"]) {
		return false
	}
	return true
}

func (s *State) catalogItems(section string) []catalogEntry {
	items := make([]catalogEntry, 0)
	for _, e := range databaseCatalog {
		if e.section == section && catalogMatch(e, s.connectionSearch, s.connectionEngineFilters, s.connectionVendorFilters) {
			items = append(items, e)
		}
	}
	return items
}

func (s *State) catalogRecents() []catalogEntry {
	result := make([]catalogEntry, 0, len(s.connectionRecent))
	for _, name := range s.connectionRecent {
		for _, e := range databaseCatalog {
			if e.name == name && catalogMatch(e, s.connectionSearch, s.connectionEngineFilters, s.connectionVendorFilters) {
				result = append(result, e)
				break
			}
		}
	}
	return result
}

func (s *State) chooseCatalogEntry(e catalogEntry) {
	s.connectionSelectedName = e.name
	if e.kind == "" {
		s.connectionKind = ""
		s.connectionError = e.name + " 的连接驱动尚未支持"
		return
	}
	s.chooseConnectionKind(e.kind)
}

func (s *State) recordRecentConnection(name string) {
	unique := []string{name}
	for _, candidate := range s.connectionRecent {
		if candidate != name && len(unique) < 5 {
			unique = append(unique, candidate)
		}
	}
	s.connectionRecent = unique
}

func (s *State) clearCatalogFilters() {
	s.connectionEngineFilters = map[string]bool{}
	s.connectionVendorFilters = map[string]bool{}
	s.connectionSearch = ""
}

func setCatalogFilter(filters map[string]bool, label string, enabled bool) {
	if enabled {
		filters[label] = true
	} else {
		delete(filters, label)
	}
}
