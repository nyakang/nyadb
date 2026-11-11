// Package connections manages non-secret connection profiles and configuration
// services independently of UI, persistence, and database drivers.
package connections

import (
	"errors"
	"time"
)

type Kind string

const (
	Consul        Kind = "consul"
	Etcd          Kind = "etcd"
	MySQL         Kind = "mysql"
	MariaDB       Kind = "mariadb"
	TiDB          Kind = "tidb"
	OceanBase     Kind = "oceanbase"
	PostgreSQL    Kind = "postgresql"
	CockroachDB   Kind = "cockroachdb"
	OpenGauss     Kind = "opengauss"
	GaussDB       Kind = "gaussdb"
	Kingbase      Kind = "kingbase"
	Dameng        Kind = "dameng"
	SQLite        Kind = "sqlite"
	ClickHouse    Kind = "clickhouse"
	Doris         Kind = "doris"
	StarRocks     Kind = "starrocks"
	Trino         Kind = "trino"
	Elasticsearch Kind = "elasticsearch"
	Easysearch    Kind = "easysearch"
	Solr          Kind = "solr"
	Cassandra     Kind = "cassandra"
	CouchDB       Kind = "couchdb"
	Meilisearch   Kind = "meilisearch"
	Qdrant        Kind = "qdrant"
	Neo4j         Kind = "neo4j"
	SQLServer     Kind = "sqlserver"
	DuckDB        Kind = "duckdb"
	Oracle        Kind = "oracle"
	MongoDB       Kind = "mongodb"
	Redis         Kind = "redis"
)

// IsMySQLFamily identifies products sharing the MySQL client and connection
// settings. Product identity and capabilities remain separate.
func (k Kind) IsMySQLFamily() bool { return k == MySQL || k == MariaDB || k == TiDB || k == OceanBase }

// IsNativeKV selects the native request document rather than a SQL editor.
func (k Kind) IsNativeKV() bool { return k == Etcd || k == Consul }

// IsPostgreSQLFamily identifies products sharing PostgreSQL wire transport and options.
func (k Kind) IsPostgreSQLFamily() bool {
	return k == PostgreSQL || k == CockroachDB || k == OpenGauss || k == GaussDB || k == Kingbase
}

// IsGaussFamily identifies native Gauss syntax and catalog rules. Each product
// retains its own connector, server identity, configuration and capabilities.
func (k Kind) IsGaussFamily() bool { return k == OpenGauss || k == GaussDB }

// IsRESTSearch identifies search products with native REST interfaces.
// Each product retains an independent client, identity and paging strategy.
func (k Kind) IsRESTSearch() bool { return k == Elasticsearch || k == Easysearch || k == Solr }

// IsREST selects native request presentation while preserving product families.
func (k Kind) IsREST() bool {
	return k.IsRESTSearch() || k == CouchDB || k == Meilisearch || k == Qdrant
}

var (
	ErrNotFound = errors.New("connection profile not found")
	ErrInvalid  = errors.New("invalid connection profile")
)

// Profile stores only public connection settings. Credentials are provided
// separately at connect time, never persisted in this record.
// ConnectionVariant is a named alternative settings set under one MySQL
// connection, not another top-level connection. Only non-secret fields persist.
type ConnectionVariant struct {
	Name          string
	Host          string
	Port          uint16
	Database      string
	Username      string
	TLSMode       string
	MySQLAdvanced MySQLAdvanced
}

type Profile struct {
	ConsulOptions        ConsulOptions
	EtcdOptions          EtcdOptions
	Neo4jOptions         Neo4jOptions
	QdrantOptions        QdrantOptions
	MeilisearchOptions   MeilisearchOptions
	CouchDBOptions       CouchDBOptions
	CassandraOptions     CassandraOptions
	SolrOptions          SolrOptions
	EasysearchOptions    EasysearchOptions
	ElasticsearchOptions ElasticsearchOptions
	TrinoOptions         TrinoOptions
	StarRocksOptions     StarRocksOptions
	DorisOptions         DorisOptions
	DamengOptions        DamengOptions
	GaussDBOptions       GaussDBOptions
	RedisOptions         RedisOptions
	MongoDBOptions       MongoDBOptions
	OracleOptions        OracleOptions
	SQLServerOptions     SQLServerOptions
	DuckDBOptions        DuckDBOptions
	PostgreSQLOptions    PostgreSQLOptions
	SQLiteOptions        SQLiteOptions
	ID                   int64
	Name                 string
	GroupName            string // Optional virtual navigation group; never a database name.
	Color                string // Local fixed-palette accent; never sent to a database.
	Starred              bool   // Local navigation favorite; never sent to a database.
	Kind                 Kind
	Host                 string
	Port                 uint16
	Database             string
	Username             string
	TLSMode              string
	MySQLAdvanced        MySQLAdvanced
	Notes                string
	Variants             []ConnectionVariant
	ActiveVariant        string // Empty selects the primary configuration.
	CreatedAt            time.Time
	UpdatedAt            time.Time
}
