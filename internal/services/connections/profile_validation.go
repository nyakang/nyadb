package connections

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// NormalizeProfile validates public settings and applies their canonical values.
// Variants are normalized in place; callers retaining them must clone the slice.
func NormalizeProfile(p Profile) (Profile, error) {
	if p.Kind != Qdrant && p.QdrantOptions != (QdrantOptions{}) {
		return p, fmt.Errorf("%w: Qdrant options on another engine", ErrInvalid)
	}
	if p.Kind != Meilisearch && p.MeilisearchOptions != (MeilisearchOptions{}) {
		return p, fmt.Errorf("%w: Meilisearch options on another engine", ErrInvalid)
	}
	if p.Kind != CouchDB && p.CouchDBOptions != (CouchDBOptions{}) {
		return p, fmt.Errorf("%w: CouchDB options on another engine", ErrInvalid)
	}
	if p.Kind != Cassandra && p.CassandraOptions != (CassandraOptions{}) {
		return p, fmt.Errorf("%w: Cassandra options on another engine", ErrInvalid)
	}
	if p.Kind != Solr && p.SolrOptions != (SolrOptions{}) {
		return p, fmt.Errorf("%w: Solr options on another engine", ErrInvalid)
	}
	if p.Kind != Easysearch && p.EasysearchOptions != (EasysearchOptions{}) {
		return p, fmt.Errorf("%w: Easysearch options on another engine", ErrInvalid)
	}
	if p.Kind != Elasticsearch && p.ElasticsearchOptions != (ElasticsearchOptions{}) {
		return p, fmt.Errorf("%w: Elasticsearch options on another engine", ErrInvalid)
	}
	if p.Kind != Trino && p.TrinoOptions != (TrinoOptions{}) {
		return p, fmt.Errorf("%w: Trino options on another engine", ErrInvalid)
	}
	if p.Kind != StarRocks && p.StarRocksOptions != (StarRocksOptions{}) {
		return p, fmt.Errorf("%w: StarRocks options on another engine", ErrInvalid)
	}
	if p.Kind != Doris && p.DorisOptions != (DorisOptions{}) {
		return p, fmt.Errorf("%w: Doris options on another engine", ErrInvalid)
	}
	if !ValidProfileColor(p.Color) {
		return p, fmt.Errorf("%w: invalid connection color", ErrInvalid)
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Kind != Dameng && p.DamengOptions != (DamengOptions{}) {
		return p, fmt.Errorf("%w: DM8 options on another engine", ErrInvalid)
	}
	if p.Kind != GaussDB && p.GaussDBOptions != (GaussDBOptions{}) {
		return p, fmt.Errorf("%w: GaussDB options on another engine", ErrInvalid)
	}
	if p.Kind != Redis && p.RedisOptions != (RedisOptions{}) {
		return p, fmt.Errorf("%w: Redis options on another engine", ErrInvalid)
	}
	if p.Kind != MongoDB && p.MongoDBOptions != (MongoDBOptions{}) {
		return p, fmt.Errorf("%w: MongoDB options on another engine", ErrInvalid)
	}
	if p.Kind != Oracle && p.OracleOptions != (OracleOptions{}) {
		return p, fmt.Errorf("%w: Oracle options on another engine", ErrInvalid)
	}
	if p.Kind != DuckDB && p.DuckDBOptions != (DuckDBOptions{}) {
		return p, fmt.Errorf("%w: DuckDB options on another engine", ErrInvalid)
	}
	if p.Kind != SQLServer && p.SQLServerOptions != (SQLServerOptions{}) {
		return p, fmt.Errorf("%w: SQL Server options on another engine", ErrInvalid)
	}
	p.GroupName = strings.TrimSpace(p.GroupName)
	if p.GroupName != "" {
		if err := ValidateGroupName(p.GroupName); err != nil {
			return p, err
		}
	}
	if !p.Kind.IsPostgreSQLFamily() && p.PostgreSQLOptions != (PostgreSQLOptions{}) {
		return p, fmt.Errorf("%w: PostgreSQL options on another engine", ErrInvalid)
	}
	if p.Kind != SQLite && (p.SQLiteOptions.Version != 0 || p.SQLiteOptions.MaxConnections != 0 || p.SQLiteOptions.SQLMaxConnections != 0 || p.SQLiteOptions.ReadOnly || p.SQLiteOptions.RequireExisting || p.SQLiteOptions.BusyTimeoutMS != 0 || p.SQLiteOptions.ForeignKeys || len(p.SQLiteOptions.Attachments) > 0) {
		return p, fmt.Errorf("%w: SQLite options on another engine", ErrInvalid)
	}
	p.ActiveVariant = strings.TrimSpace(p.ActiveVariant)
	if len(p.Variants) > 16 {
		return p, fmt.Errorf("%w: too many connection configurations", ErrInvalid)
	}
	for i, variant := range p.Variants {
		variant.Name = strings.TrimSpace(variant.Name)
		if variant.Name == "" || utf8.RuneCountInString(variant.Name) > 120 ||
			strings.ContainsRune(variant.Name, 0) {
			return p, fmt.Errorf("%w: invalid configuration name", ErrInvalid)
		}
		for j := 0; j < i; j++ {
			if strings.EqualFold(p.Variants[j].Name, variant.Name) {
				return p, fmt.Errorf("%w: duplicate configuration name", ErrInvalid)
			}
		}
		if !p.Kind.IsMySQLFamily() {
			return p, fmt.Errorf("%w: alternative configurations require MySQL", ErrInvalid)
		}
		checked, err := NormalizeProfile(Profile{Name: "configuration", Kind: p.Kind,
			Host: variant.Host, Port: variant.Port, Database: variant.Database,
			Username: variant.Username, TLSMode: variant.TLSMode,
			MySQLAdvanced: variant.MySQLAdvanced})
		if err != nil {
			return p, fmt.Errorf("%w: configuration %s: %v", ErrInvalid, variant.Name, err)
		}
		// The host is a hostname/IP, never a URL or userinfo-bearing DSN.
		if strings.ContainsAny(checked.Host, "@/?!#") {
			return p, fmt.Errorf("%w: invalid configuration host", ErrInvalid)
		}
		variant.Host, variant.Port, variant.Database = checked.Host, checked.Port, checked.Database
		variant.Username, variant.TLSMode = checked.Username, checked.TLSMode
		variant.MySQLAdvanced = checked.MySQLAdvanced
		p.Variants[i] = variant
	}
	if p.ActiveVariant != "" {
		if !p.Kind.IsMySQLFamily() {
			return p, fmt.Errorf("%w: active alternatives require MySQL", ErrInvalid)
		}
		found := false
		for _, variant := range p.Variants {
			if strings.EqualFold(variant.Name, p.ActiveVariant) {
				p.ActiveVariant, found = variant.Name, true
				break
			}
		}
		if !found {
			return p, fmt.Errorf("%w: active configuration does not exist", ErrInvalid)
		}
	}
	p.Host = strings.TrimSpace(p.Host)
	if p.Kind != Dameng {
		if p.Kind != Cassandra && p.Kind != Qdrant {
			p.Database = strings.TrimSpace(p.Database)
		}
		if p.Kind != Qdrant && p.Kind != Meilisearch && p.Kind != CouchDB && p.Kind != Solr && p.Kind != Cassandra {
			p.Username = strings.TrimSpace(p.Username)
		}
	}
	p.TLSMode = strings.ToLower(strings.TrimSpace(p.TLSMode))
	// Notes are user-authored multiline profile metadata, not credentials.
	if !utf8.ValidString(p.Notes) || strings.ContainsRune(p.Notes, 0) || len(p.Notes) > 64*1024 {
		return p, fmt.Errorf("%w: connection notes must be valid UTF-8, NUL-free, and at most 64 KiB", ErrInvalid)
	}
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 120 || strings.ContainsRune(p.Name, 0) {
		return p, fmt.Errorf("%w: name must contain 1 to 120 characters", ErrInvalid)
	}
	for _, v := range []string{p.Host, p.Database, p.Username, p.TLSMode} {
		if strings.ContainsRune(v, 0) {
			return p, fmt.Errorf("%w: NUL byte in field", ErrInvalid)
		}
	}
	switch p.Kind {
	case Qdrant:
		return normalizeQdrantProfile(p)
	case Meilisearch:
		return normalizeMeilisearchProfile(p)
	case CouchDB:
		return normalizeCouchDBProfile(p)
	case Cassandra:
		return normalizeCassandraProfile(p)
	case Solr:
		return normalizeSolrProfile(p)
	case Easysearch:
		return normalizeEasysearchProfile(p)
	case Elasticsearch:
		return normalizeElasticsearchProfile(p)
	case Trino:
		return normalizeTrinoProfile(p)
	case StarRocks:
		return normalizeStarRocksProfile(p)
	case Doris:
		return normalizeDorisProfile(p)
	case Dameng:
		if p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: MySQL options on DM8", ErrInvalid)
		}
		cfg, err := DamengProfileConfig(p)
		if err != nil {
			return p, err
		}
		p.Host, p.Port, p.TLSMode, p.DamengOptions = cfg.Host, cfg.Port, cfg.TLSMode, cfg.Options
		return p, nil
	case Kingbase:
		if p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: MySQL options on Kingbase", ErrInvalid)
		}
		cfg, err := KingbaseProfileConfig(p)
		if err != nil {
			return p, err
		}
		p.Host, p.Port, p.TLSMode, p.PostgreSQLOptions = cfg.Host, cfg.Port, cfg.TLSMode, cfg.Options
		return p, nil
	case GaussDB:
		if p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: MySQL options on GaussDB", ErrInvalid)
		}
		return normalizeGaussDBProfile(p)
	case Redis:
		return normalizeRedisProfile(p)
	case MongoDB:
		return normalizeMongoDBProfile(p)
	case DuckDB:
		var err error
		p.DuckDBOptions, err = NormalizeDuckDBOptions(p.DuckDBOptions)
		if err != nil {
			return p, err
		}
		if p.Database == "" || p.Host != "" || p.Port != 0 || p.Username != "" || p.TLSMode != "" || p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: DuckDB requires a file path or :memory: and no server settings", ErrInvalid)
		}
		if p.Database == ":memory:" && p.DuckDBOptions.ReadOnly {
			return p, fmt.Errorf("%w: an in-memory DuckDB database cannot be read-only", ErrInvalid)
		}
	case MySQL, MariaDB, TiDB, OceanBase, PostgreSQL, CockroachDB, OpenGauss, ClickHouse, SQLServer, Oracle:
		if p.Kind == Oracle {
			var err error
			p.OracleOptions, err = NormalizeOracleOptions(p.OracleOptions)
			if err != nil {
				return p, err
			}
			if p.Database == "" || p.Username == "" || !validEngineText(p.Username, 128) || strings.ContainsAny(p.Host, "@/?!#\\()= \t\r\n") || !validEngineText(p.Database, 4096) || strings.ContainsAny(p.Database, "()=\\ \t\r\n") {
				return p, fmt.Errorf("%w: Oracle requires a hostname, service/SID and username", ErrInvalid)
			}
			if p.OracleOptions.DefaultSchema == "" {
				p.OracleOptions.DefaultSchema = strings.ToUpper(p.Username)
			}
			if p.TLSMode == "" {
				p.TLSMode = "disable"
			}
			if p.TLSMode != "disable" && p.TLSMode != "require" && p.TLSMode != "verify-full" {
				return p, fmt.Errorf("%w: Oracle TLS must be disable, require or verify-full", ErrInvalid)
			}
			if p.TLSMode == "disable" && (p.OracleOptions.CAFile != "" || p.OracleOptions.ServerName != "") {
				return p, fmt.Errorf("%w: Oracle certificate settings require TCPS", ErrInvalid)
			}
		}
		if p.Kind == SQLServer {
			var err error
			p.SQLServerOptions, err = NormalizeSQLServerOptions(p.SQLServerOptions)
			if err != nil {
				return p, err
			}
			if p.Database == "" || p.Username == "" || len([]rune(p.Database)) > 128 || strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n") {
				return p, fmt.Errorf("%w: SQL Server requires a hostname, database and SQL username", ErrInvalid)
			}
			if p.TLSMode == "" {
				p.TLSMode = "verify-full"
			}
			if p.TLSMode != "disable" && p.TLSMode != "require" && p.TLSMode != "verify-full" {
				return p, fmt.Errorf("%w: SQL Server TLS must be disable, require or verify-full", ErrInvalid)
			}
			if p.TLSMode == "disable" && (p.SQLServerOptions.CAFile != "" || p.SQLServerOptions.ServerName != "") {
				return p, fmt.Errorf("%w: SQL Server certificate settings require TLS", ErrInvalid)
			}
		}
		if p.Kind.IsMySQLFamily() {
			var err error
			p.MySQLAdvanced, err = NormalizeMySQLAdvanced(p.MySQLAdvanced)
			if err != nil {
				return p, err
			}
		} else if p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: MySQL options on another engine", ErrInvalid)
		}
		if p.Kind == CockroachDB {
			if p.Database == "" || p.Username == "" || strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n") {
				return p, fmt.Errorf("%w: CockroachDB requires a hostname, database and username", ErrInvalid)
			}
			if p.TLSMode == "" {
				p.TLSMode = "verify-full"
			}
			if p.PostgreSQLOptions.ToolsDirectory != "" {
				return p, fmt.Errorf("%w: CockroachDB does not use PostgreSQL backup tools", ErrInvalid)
			}
		}
		if p.Kind == OpenGauss {
			if p.PostgreSQLOptions.LockTimeoutMS != 0 {
				return p, fmt.Errorf("%w: openGauss per-connection lock timeout is unavailable", ErrInvalid)
			}
			if p.Database == "" || p.Username == "" || strings.ContainsAny(p.Host, "@/?!#\\ \t\r\n") {
				return p, fmt.Errorf("%w: openGauss requires a hostname, database and username", ErrInvalid)
			}
			if p.TLSMode == "" {
				p.TLSMode = "verify-full"
			}
			if p.PostgreSQLOptions.ToolsDirectory != "" {
				return p, fmt.Errorf("%w: openGauss requires its own backup tools", ErrInvalid)
			}
		}
		if p.Kind.IsPostgreSQLFamily() {
			var err error
			p.PostgreSQLOptions, err = NormalizePostgreSQLOptions(p.PostgreSQLOptions)
			if err != nil {
				return p, err
			}
		}
		if p.Host == "" || p.Port == 0 {
			return p, fmt.Errorf("%w: server host and port are required", ErrInvalid)
		}
		if p.Kind == ClickHouse {
			if strings.ContainsAny(p.Host, "@/?!#\\") || strings.ContainsAny(p.Host, " \t\r\n") || p.Database == "" || p.Username == "" {
				return p, fmt.Errorf("%w: ClickHouse requires a hostname, database and username", ErrInvalid)
			}
			if p.TLSMode == "" {
				p.TLSMode = "disable"
			}
			if p.TLSMode != "disable" && p.TLSMode != "verify-full" {
				return p, fmt.Errorf("%w: ClickHouse TLS must be disable or verify-full", ErrInvalid)
			}
		}
		switch p.TLSMode {
		case "", "disable", "prefer", "require", "verify-ca", "verify-full":
		default:
			return p, fmt.Errorf("%w: unsupported TLS mode", ErrInvalid)
		}
	case SQLite:
		var sqliteErr error
		p.SQLiteOptions, sqliteErr = NormalizeSQLiteOptions(p.SQLiteOptions)
		if sqliteErr != nil {
			return p, sqliteErr
		}
		if p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: MySQL options on SQLite profile", ErrInvalid)
		}
		if p.Database == "" || p.Host != "" || p.Port != 0 || p.Username != "" || p.TLSMode != "" {
			return p, fmt.Errorf("%w: SQLite requires a file path and no server settings", ErrInvalid)
		}
	default:
		return p, fmt.Errorf("%w: unsupported database kind", ErrInvalid)
	}
	return p, nil
}
