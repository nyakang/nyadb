package shell

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
	{"华为云 GaussDB(DWS)", "GaussDB", "华为", "云数据库", "cloud-type", "#c91e35", ""},
	{"华为云 MySQL", "MySQL", "华为", "云数据库", "cloud-type", "#3bcb53", ""},
	{"华为云 SQL Server", "SQL Server", "华为", "云数据库", "cloud-type", "#fab115", ""},
	{"华为云 DDS", "MongoDB", "华为", "云数据库", "cloud-type", "#b25a0d", ""},
	{"华为云 Redis", "Redis", "华为", "云数据库", "cloud-type", "#fa858a", ""},
	{"openGauss", "openGauss", "华为", "云数据库", "database", "#159ac1", ""},
	{"腾讯云 PostgreSQL", "PostgreSQL", "腾讯云", "云数据库", "cloud-type", "#4087f7", ""},
	{"腾讯云 SQL Server", "SQL Server", "腾讯云", "云数据库", "cloud-type", "#f1af14", ""},
	{"腾讯云 MariaDB", "MariaDB", "腾讯云", "云数据库", "cloud-type", "#b98b5e", ""},
	{"腾讯云 MongoDB", "MongoDB", "腾讯云", "云数据库", "cloud-type", "#b2590e", ""},
	{"腾讯云 Redis", "Redis", "腾讯云", "云数据库", "cloud-type", "#f57b7d", ""},
	{"KingbaseES", "KingbaseES", "电科金仓", "云数据库", "database", "#d73337", ""},
	{"TiDB", "TiDB", "亚信星辰", "云数据库", "database", "#e21515", ""},
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
