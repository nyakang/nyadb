package connections

import "github.com/nyakang/nyadb/internal/db/contract"

// Descriptor is a value snapshot. It contains no clients, credentials or maps.
type Descriptor struct {
	Kind                                   Kind
	Label, ClientFamily, FormKind, Dialect string
	DefaultPort                            uint16
	Capabilities                           contract.Capabilities
}

func Describe(kind Kind) (Descriptor, bool) {
	switch kind {
	case CouchDB:
		return Descriptor{kind, "Apache CouchDB", "couchdb", "server", "couchdb", 5984, contract.Capabilities{Query: true, Metadata: true}}, true
	case Cassandra:
		return Descriptor{kind, "Apache Cassandra", "cassandra", "server", "cassandra", 9042, contract.Capabilities{Objects: true, Query: true, Metadata: true, Keyset: true, Count: true, FieldChunks: true, Export: true, JSONL: true}}, true
	case Solr:
		return Descriptor{kind, "Apache Solr", "solr", "server", "solr", 8983, contract.Capabilities{Objects: true, Documents: true, Query: true, Metadata: true, EditableTables: true, Keyset: true, Count: true, FieldChunks: true, Export: true, JSONL: true}}, true
	case Easysearch:
		return Descriptor{kind, "Easysearch", "easysearch", "server", "easysearch", 9200, contract.Capabilities{Documents: true, Query: true, Metadata: true, EditableTables: true, Keyset: true, Count: true, FieldChunks: true, Export: true, JSONL: true}}, true
	case Elasticsearch:
		return Descriptor{kind, "Elasticsearch", "elasticsearch", "server", "elasticsearch", 9200, contract.Capabilities{Documents: true, Query: true, Metadata: true, EditableTables: true, Keyset: true, Count: true, FieldChunks: true, Export: true, JSONL: true}}, true
	case Trino:
		return Descriptor{kind, "Trino", "trino", "server", "trino", 8080, contract.Capabilities{Query: true, Metadata: true, Schemas: true, Count: true, Export: true, JSONL: true, Explain: true}}, true
	case StarRocks:
		return Descriptor{kind, "StarRocks", "starrocks", "server", "mysql", 9030, contract.Capabilities{Query: true, Metadata: true, Schemas: true, Count: true, Export: true, JSONL: true, Explain: true}}, true
	case Doris:
		return Descriptor{kind, "Apache Doris", "doris", "server", "mysql", 9030, contract.Capabilities{Query: true, Metadata: true, Schemas: true, Count: true, Export: true, JSONL: true, Explain: true}}, true
	case Dameng:
		return Descriptor{kind, "达梦", "dameng", "server", "dameng", 5236, contract.Capabilities{Query: true, Metadata: true, Schemas: true, Transactions: true, EditableTables: true, Count: true, Keyset: true, FieldChunks: true, Export: true, JSONL: true, Import: true}}, true
	case Redis:
		return Descriptor{kind, "Redis", "redis", "server", "redis", 6379, contract.Capabilities{KeyValues: true, Query: true, Metadata: true, EditableTables: true, Count: true, FieldChunks: true, Export: true, JSONL: true}}, true
	case MongoDB:
		return Descriptor{kind, "MongoDB", "mongodb", "server", "mongodb", 27017, contract.Capabilities{Documents: true, Query: true, Metadata: true, EditableTables: true, Count: true, FieldChunks: true, Export: true, JSONL: true}}, true
	case Oracle:
		return Descriptor{kind, "Oracle", "oracle", "server", "oracle", 1521, contract.Capabilities{Query: true, Metadata: true, Schemas: true, Transactions: true, EditableTables: true, Explain: true, Count: true, FieldChunks: true, Analysis: true, Export: true, JSONL: true, Import: true}}, true
	case DuckDB:
		return Descriptor{kind, "DuckDB", "duckdb", "file", "duckdb", 0, contract.Capabilities{Query: true, Metadata: true, Schemas: true, Transactions: true, EditableTables: true, Explain: true, Count: true, FieldChunks: true, Analysis: true, Export: true, JSONL: true, Import: true}}, true
	case SQLServer:
		return Descriptor{kind, "SQL Server", "sqlserver", "server", "tsql", 1433, contract.Capabilities{Query: true, Metadata: true, Schemas: true, Transactions: true, EditableTables: true, Explain: true, Count: true, FieldChunks: true, Analysis: true, Export: true, JSONL: true, Import: true}}, true
	case MySQL:
		return Descriptor{kind, "MySQL", "mysql", "server", "mysql", 3306, contract.Capabilities{Objects: true, Maintenance: true, Accounts: true, Restore: true, DesignViews: true, Query: true, Metadata: true, Transactions: true, EditableTables: true, Explain: true, DesignTables: true, Import: true, Backup: true, Keyset: true, Count: true, FieldChunks: true, Analysis: true, Export: true}}, true
	case MariaDB:
		d, _ := Describe(MySQL)
		d.Kind, d.Label = kind, "MariaDB"
		// Account administration currently targets MySQL's system tables and
		// authentication plugins; MariaDB does not share that contract.
		d.Capabilities.Accounts = false
		return d, true
	case TiDB:
		d, _ := Describe(MySQL)
		d.Kind, d.Label, d.DefaultPort = kind, "TiDB", 4000
		return d, true
	case OceanBase:
		return Descriptor{kind, "OceanBase", "mysql", "server", "mysql", 2881, contract.Capabilities{Objects: true, DesignViews: true, Maintenance: true, Explain: true, Query: true, Metadata: true, Transactions: true, EditableTables: true, Count: true, FieldChunks: true, Analysis: true, Export: true, Import: true, Keyset: true}}, true
	case CockroachDB:
		return Descriptor{kind, "CockroachDB", "postgres", "server", "postgres", 26257, contract.Capabilities{Query: true, Metadata: true, Schemas: true, Transactions: true, EditableTables: true, Explain: true, Import: true, Keyset: true, Count: true, FieldChunks: true, Analysis: true, Export: true, JSONL: true}}, true
	case OpenGauss:
		return Descriptor{kind, "openGauss", "postgres", "server", "postgres", 5432, contract.Capabilities{Objects: true, DesignViews: true, Query: true, Metadata: true, Schemas: true, Transactions: true, EditableTables: true, Explain: true, Import: true, Keyset: true, Count: true, FieldChunks: true, Analysis: true, Export: true, JSONL: true}}, true
	case GaussDB:
		return Descriptor{kind, "GaussDB", "postgres", "server", "postgres", 5432, contract.Capabilities{Query: true, Metadata: true, Schemas: true, Transactions: true, EditableTables: true, Explain: true, Import: true, Keyset: true, Count: true, FieldChunks: true, Analysis: true, Export: true, JSONL: true}}, true
	case Kingbase:
		return Descriptor{kind, "KingbaseES", "postgres", "server", "postgres", 54321, contract.Capabilities{Query: true, Metadata: true, Schemas: true, Transactions: true, EditableTables: true, Explain: true, Import: true, Keyset: true, Count: true, FieldChunks: true, Analysis: true, Export: true, JSONL: true}}, true
	case PostgreSQL:
		return Descriptor{kind, "PostgreSQL", "postgres", "server", "postgres", 5432, contract.Capabilities{Objects: true, DesignViews: true, Backup: true, Restore: true, Import: true, Maintenance: true, Query: true, Metadata: true, Schemas: true, Transactions: true, EditableTables: true, Explain: true, DesignTables: true, Keyset: true, Count: true, FieldChunks: true, Analysis: true, Export: true, JSONL: true}}, true
	case SQLite:
		return Descriptor{kind, "SQLite", "sqlite", "file", "sqlite", 0, contract.Capabilities{Backup: true, Restore: true, Import: true, Maintenance: true, DesignTables: true, DesignViews: true, Objects: true, Query: true, Metadata: true, Schemas: true, Transactions: true, EditableTables: true, Explain: true, Keyset: true, Count: true, FieldChunks: true, Analysis: true, Export: true, JSONL: true}}, true
	case ClickHouse:
		return Descriptor{kind, "ClickHouse", "clickhouse", "server", "clickhouse", 9000, contract.Capabilities{Query: true, Metadata: true, Explain: true, Count: true, Export: true, JSONL: true}}, true
	}
	return Descriptor{}, false
}

// FieldTypes offers common candidates without restricting custom declarations.
func (d Descriptor) FieldTypes() []string {
	switch d.Kind {
	case Dameng:
		return []string{"BIGINT", "INT", "DECIMAL(38,20)", "VARCHAR(255)", "CLOB", "BLOB", "VARBINARY(255)", "BIT", "DATE", "TIME(6)", "TIMESTAMP(6)", "TIMESTAMP(6) WITH TIME ZONE"}
	case Oracle:
		return []string{"NUMBER(19,0)", "NUMBER(38,20)", "VARCHAR2(255)", "NVARCHAR2(255)", "DATE", "TIMESTAMP(9)", "TIMESTAMP WITH TIME ZONE", "CLOB", "NCLOB", "BLOB", "RAW(16)"}
	case DuckDB:
		return []string{"BIGINT", "HUGEINT", "DECIMAL(18,2)", "VARCHAR", "BOOLEAN", "DATE", "TIMESTAMP", "TIMESTAMPTZ", "BLOB", "UUID", "JSON", "INTEGER[]"}
	case SQLServer:
		return []string{"bigint", "int", "smallint", "decimal(18,2)", "nvarchar(255)", "nvarchar(max)", "datetime2", "datetimeoffset", "date", "varbinary(max)", "bit", "uniqueidentifier"}
	case MySQL, MariaDB, TiDB, OceanBase:
		return []string{"bigint", "int", "smallint", "decimal(10,2)", "varchar(255)", "text", "datetime", "timestamp", "date", "json", "blob", "boolean"}
	case PostgreSQL, CockroachDB, OpenGauss, GaussDB, Kingbase:
		return []string{"bigint", "integer", "smallint", "numeric(10,2)", "text", "varchar(255)", "boolean", "timestamp", "timestamptz", "date", "json", "jsonb", "bytea", "uuid"}
	case SQLite:
		return []string{"INTEGER", "TEXT", "REAL", "BLOB", "NUMERIC"}
	}
	return nil
}
