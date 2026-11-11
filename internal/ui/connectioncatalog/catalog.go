package connectioncatalog

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// Catalog labels and colors are newly authored for NyaDB, not extracted
// from the Navicat executable. Unsupported drivers are discovery-only.
type catalogEntry struct {
	name, engine, vendor, section, symbol, color string
	kind                                         connections.Kind
}

var databaseCatalog = []catalogEntry{
	{"Apache Solr", "Apache Solr", "", "全部", "database", "#d9411e", connections.Solr},
	{"Elasticsearch", "Elasticsearch", "", "全部", "database", "#00bfb3", connections.Elasticsearch},
	{"Easysearch", "Easysearch", "", "全部", "database", "#4066d5", connections.Easysearch},
	{"MySQL", "MySQL", "", "全部", "mysql-type", "#24bf65", connections.MySQL},
	{"PostgreSQL", "PostgreSQL", "", "全部", "postgres-type", "#397cdf", connections.PostgreSQL},
	{"CockroachDB", "CockroachDB", "", "全部", "database", "#6933a6", connections.CockroachDB},
	{"openGauss", "openGauss", "", "全部", "database", "#1c77c3", connections.OpenGauss},
	{"GaussDB", "GaussDB", "", "全部", "database", "#1c77c3", connections.GaussDB},
	{"KingbaseES", "KingbaseES", "", "全部", "database", "#c9272e", connections.Kingbase},
	{"SQL Server", "SQL Server", "", "全部", "mssql-type", "#edab24", connections.SQLServer},
	{"Oracle", "Oracle", "", "全部", "oracle-type", "#ef3b5c", connections.Oracle},
	{"SQLite", "SQLite", "", "全部", "sqlite-type", "#4cbfc2", connections.SQLite},
	{"DuckDB", "DuckDB", "", "全部", "duckdb-type", "#d6ad00", connections.DuckDB},
	{"MariaDB", "MariaDB", "", "全部", "maria-type", "#b58a61", connections.MariaDB},
	{"TiDB", "TiDB", "", "全部", "database", "#e21515", connections.TiDB},
	{"MongoDB", "MongoDB", "", "全部", "mongo-type", "#bb691d", connections.MongoDB},
	{"Redis", "Redis", "", "全部", "redis-type", "#e77679", connections.Redis},
	{"Snowflake", "Snowflake", "", "全部", "snowflake-type", "#3daac7", ""},
	{"达梦", "达梦", "", "全部", "dm-type", "#7e63cc", connections.Dameng},
	{"OceanBase", "OceanBase", "", "全部", "ocean-type", "#3486d1", connections.OceanBase},
	{"阿里云 RDS MySQL 版", "MySQL", "阿里云", "云数据库", "database", "#32c963", ""},
	{"阿里云 RDS PostgreSQL 版", "PostgreSQL", "阿里云", "云数据库", "database", "#4b86e1", ""},
	{"阿里云 RDS SQL Server 版", "SQL Server", "阿里云", "云数据库", "table", "#f0a90d", ""},
	{"阿里云 MongoDB 版", "MongoDB", "阿里云", "云数据库", "database", "#b76115", ""},
	{"阿里云 Redis 版", "Redis", "阿里云", "云数据库", "database", "#ee8686", ""},
	{"腾讯云 MySQL", "MySQL", "腾讯云", "云数据库", "database", "#44aee0", ""},
	{"华为云 PostgreSQL", "PostgreSQL", "华为", "云数据库", "database", "#d97777", ""},
	{"AWS RDS MySQL", "MySQL", "AWS", "云数据库", "database", "#dd9933", ""},
	{"Azure SQL", "SQL Server", "Microsoft", "云数据库", "table", "#378de0", ""},
	{"ClickHouse", "ClickHouse", "", "全部", "database", "#e7b632", connections.ClickHouse},
	{"Trino", "Trino", "", "全部", "database", "#dd5a9b", connections.Trino},
	{"StarRocks", "StarRocks", "", "全部", "database", "#e65f25", connections.StarRocks},
	{"Apache Doris", "Apache Doris", "", "全部", "database", "#259aca", connections.Doris},
	{"Apache Cassandra", "Apache Cassandra", "", "全部", "database", "#1b9aaa", connections.Cassandra},
	{"Apache CouchDB", "Apache CouchDB", "", "全部", "database", "#e42528", connections.CouchDB},
	{"Meilisearch", "Meilisearch", "", "全部", "database", "#ff5c8d", connections.Meilisearch},
	{"Qdrant", "Qdrant", "", "全部", "database", "#dc244c", connections.Qdrant},
	{"Neo4j", "Neo4j", "", "全部", "database", "#008cc1", connections.Neo4j},
	{"etcd", "etcd", "", "全部", "database", "#419eda", connections.Etcd},
	{"Consul", "Consul", "", "全部", "database", "#ca2171", connections.Consul},
}

var catalogEngines = []string{"MySQL", "PostgreSQL", "SQL Server", "Oracle", "SQLite", "DuckDB", "ClickHouse", "Apache Doris", "StarRocks", "Trino", "MariaDB", "TiDB", "OceanBase", "CockroachDB", "openGauss", "GaussDB", "KingbaseES", "达梦", "MongoDB", "Redis", "Elasticsearch", "Easysearch", "Apache Solr", "Apache Cassandra", "Apache CouchDB", "Meilisearch", "Qdrant", "Neo4j", "etcd", "Consul", "Snowflake"}
var catalogVendors = catalogTreeVendorOrder

func catalogMatch(e catalogEntry, query string, engines, vendors map[string]bool) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	names := e.name + " " + e.engine + " " + e.vendor
	if e.kind == connections.Dameng {
		names += " Dameng DM8"
	}
	if q != "" && !strings.Contains(strings.ToLower(names), q) {
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
		if e.section == section && catalogMatch(e, s.search, s.engineFilters, s.vendorFilters) {
			items = append(items, e)
		}
	}
	return items
}

func (s *State) catalogRecents() []catalogEntry {
	result := make([]catalogEntry, 0, len(s.recent))
	for _, name := range s.recent {
		for _, e := range databaseCatalog {
			if e.name == name && catalogMatch(e, s.search, s.engineFilters, s.vendorFilters) {
				result = append(result, e)
				break
			}
		}
	}
	return result
}

func (s *State) clearCatalogFilters() {
	s.engineFilters = map[string]bool{}
	s.vendorFilters = map[string]bool{}
	s.search = ""
}

func setCatalogFilter(filters map[string]bool, label string, enabled bool) {
	if enabled {
		filters[label] = true
	} else {
		delete(filters, label)
	}
}

// Extended type catalog reconstructed from observed Navicat 18 chooser labels
// (O: GUI screenshots). These entries are for browsing/filtering only; they
// deliberately do NOT claim that a database driver is implemented in NyaDB.
var additionalCatalog = []catalogEntry{
	{"阿里云 OceanBase（MySQL 模式）", "OceanBase", "阿里云", "云数据库", "cloud-type", "#167dfa", ""},
	{"阿里云 OceanBase（Oracle 模式）", "OceanBase", "阿里云", "云数据库", "cloud-type", "#197bf0", ""},
	{"阿里云 PolarDB MySQL 版", "MySQL", "阿里云", "云数据库", "cloud-type", "#33c84c", ""},
	{"阿里云 PolarDB PostgreSQL 版", "PostgreSQL", "阿里云", "云数据库", "cloud-type", "#4d83e8", ""},
	{"阿里云 PolarDB 分布式版", "MySQL", "阿里云", "云数据库", "cloud-type", "#34c66b", ""},
	{"华为云 GaussDB 主备版", "GaussDB", "华为", "云数据库", "cloud-type", "#d20716", ""},
	{"华为云 GaussDB 分布式", "GaussDB", "华为", "云数据库", "cloud-type", "#d20716", ""},
	{"电科金仓 KingbaseES", "KingbaseES", "电科金仓", "云数据库", "database", "#c9272e", connections.Kingbase},
	{"华为云 GaussDB(DWS)", "GaussDB", "华为", "云数据库", "cloud-type", "#c91e35", ""},
	{"华为云 MySQL", "MySQL", "华为", "云数据库", "cloud-type", "#3bcb53", ""},
	{"华为云 SQL Server", "SQL Server", "华为", "云数据库", "cloud-type", "#fab115", ""},
	{"华为云 DDS", "MongoDB", "华为", "云数据库", "cloud-type", "#b25a0d", ""},
	{"华为云 Redis", "Redis", "华为", "云数据库", "cloud-type", "#fa858a", ""},
	{"腾讯云 PostgreSQL", "PostgreSQL", "腾讯云", "云数据库", "cloud-type", "#4087f7", ""},
	{"腾讯云 SQL Server", "SQL Server", "腾讯云", "云数据库", "cloud-type", "#f1af14", ""},
	{"腾讯云 MariaDB", "MariaDB", "腾讯云", "云数据库", "cloud-type", "#b98b5e", ""},
	{"腾讯云 MongoDB", "MongoDB", "腾讯云", "云数据库", "cloud-type", "#b2590e", ""},
	{"腾讯云 Redis", "Redis", "腾讯云", "云数据库", "cloud-type", "#f57b7d", ""},
	{"IvorySQL", "IvorySQL", "瀚高", "云数据库", "database", "#ffa73c", ""},
	{"OceanBase 社区版", "OceanBase", "OceanBase", "云数据库", "ocean-type", "#147dff", ""},
	{"OceanBase 企业版（MySQL 模式）", "OceanBase", "OceanBase", "云数据库", "ocean-type", "#147dff", ""},
	{"OceanBase 企业版（Oracle 模式）", "OceanBase", "OceanBase", "云数据库", "ocean-type", "#167afa", ""},
	{"Amazon RDS for MySQL", "MySQL", "AWS", "云数据库", "cloud-type", "#29ca53", ""},
	{"Amazon RDS for PostgreSQL", "PostgreSQL", "AWS", "云数据库", "cloud-type", "#4483e8", ""},
	{"Amazon RDS for SQL Server", "SQL Server", "AWS", "云数据库", "cloud-type", "#f3aa1a", ""},
	{"Amazon RDS for Oracle", "Oracle", "AWS", "云数据库", "cloud-type", "#f12f58", ""},
	{"Amazon RDS for MariaDB", "MariaDB", "AWS", "云数据库", "cloud-type", "#b88a62", ""},
	{"Amazon Aurora for MySQL", "MySQL", "AWS", "云数据库", "cloud-type", "#2ccc4e", ""},
	{"Amazon Aurora for PostgreSQL", "PostgreSQL", "AWS", "云数据库", "cloud-type", "#5188ea", ""},
	{"Amazon Redshift", "Redshift", "AWS", "云数据库", "cloud-type", "#8761df", ""},
	{"Amazon DocumentDB", "MongoDB", "AWS", "云数据库", "cloud-type", "#b45a0d", ""},
	{"Amazon ElastiCache for Redis", "Redis", "AWS", "云数据库", "cloud-type", "#f4787d", ""},
	{"Fujitsu Enterprise Postgres", "PostgreSQL", "Fujitsu", "云数据库", "database", "#ee242b", ""},
	{"Google Cloud SQL for MySQL", "MySQL", "Google Cloud", "云数据库", "cloud-type", "#37c94e", ""},
	{"Google Cloud SQL for PostgreSQL", "PostgreSQL", "Google Cloud", "云数据库", "cloud-type", "#4987f3", ""},
	{"Google Cloud SQL for SQL Server", "SQL Server", "Google Cloud", "云数据库", "cloud-type", "#f5ab12", ""},
	{"Google Memorystore (Redis)", "Redis", "Google Cloud", "云数据库", "cloud-type", "#fa757a", ""},
	{"Microsoft Azure Database for MySQL", "MySQL", "Microsoft", "云数据库", "cloud-type", "#31c651", ""},
	{"Microsoft Azure Database for PostgreSQL", "PostgreSQL", "Microsoft", "云数据库", "cloud-type", "#4783ed", ""},
	{"Microsoft Azure Database for MariaDB", "MariaDB", "Microsoft", "云数据库", "cloud-type", "#b99168", ""},
	{"Azure Cosmos DB for MongoDB", "MongoDB", "Microsoft", "云数据库", "cloud-type", "#b7610c", ""},
	{"Microsoft Azure Cache for Redis", "Redis", "Microsoft", "云数据库", "cloud-type", "#ef787c", ""},
	{"Microsoft Garnet", "Redis", "Microsoft", "云数据库", "cloud-type", "#c70e19", ""},
	{"MongoDB Atlas", "MongoDB", "MongoDB", "云数据库", "mongo-type", "#b45c0c", ""},
	{"Oracle Database Cloud Services", "Oracle", "Oracle", "云数据库", "oracle-type", "#ef3255", ""},
	{"Oracle MySQL Cloud Service", "MySQL", "Oracle", "云数据库", "mysql-type", "#36c851", ""},
	{"Redis Enterprise Cloud", "Redis", "Redis", "云数据库", "redis-type", "#f4787e", ""},
}

func init() {
	databaseCatalog = append(databaseCatalog, additionalCatalog...)
}

// Prefer an embedded catalog icon already tracked in the project. Additional
// entries still use a neutral original SVG when no branded asset is available.
func catalogFilterIcon(name string, vendor bool) (catalogEntry, bool) {
	for _, item := range databaseCatalog {
		if vendor && item.vendor == name {
			return item, true
		}
		if !vendor && item.section == "全部" && item.engine == name {
			return item, true
		}
	}
	return catalogEntry{}, false
}

// Choice identifies a database type. An empty Kind denotes a discovery-only
// entry whose driver is not supported.
type Choice struct {
	Name string
	Kind connections.Kind
}

// State is chooser state owned by the UI thread. Create it with New and reuse
// it when reopening the chooser. Never retain ui.Element values.
type State struct {
	editFocus     *string
	editTarget    func(theme.EditTarget)
	editValid     func() bool
	selected      Choice
	search        string
	gridMode      bool
	treeOpen      map[string]bool
	tree          ui.OutlineState[catalogNodeKey]
	treeRow       int
	treeSearching bool
	recent        []string
	engineFilters map[string]bool
	vendorFilters map[string]bool
	bundledIcons  map[string]*ui.Bitmap
	providerIcons map[string]*ui.Bitmap
	researchIcons map[string]*ui.Bitmap
}

func (s *State) SetEditing(focus *string, register func(theme.EditTarget), valid func() bool) {
	s.editFocus, s.editTarget, s.editValid = focus, register, valid
}

func New() *State {
	s := &State{gridMode: true, treeOpen: map[string]bool{}, treeRow: -1}
	s.Reset()
	s.bundledIcons, _ = theme.BundledConnectionIcons()
	s.providerIcons, _ = theme.BundledProviderIcons()
	return s
}

// Reset clears the current selection, search and filters. Presentation,
// expansion state, in-session recents and loaded icons remain available.
func (s *State) Reset() {
	s.selected = Choice{}
	s.treeRow = -1
	s.clearCatalogFilters()
}

func (s *State) Selected() Choice {
	return s.selected
}

// RecordRecent records actual use, after the host successfully opens the
// connection editor. Selecting a card alone must not move the catalog.
func (s *State) RecordRecent(name string) {
	unique := []string{name}
	for _, candidate := range s.recent {
		if candidate != name && len(unique) < 5 {
			unique = append(unique, candidate)
		}
	}
	s.recent = unique
}

func (s *State) chooseCatalogEntry(e catalogEntry, onSelect func(Choice)) {
	s.selected = Choice{Name: e.name, Kind: e.kind}
	if onSelect != nil {
		onSelect(s.selected)
	}
}
