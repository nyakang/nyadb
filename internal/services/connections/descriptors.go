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
	case MySQL:
		return Descriptor{kind, "MySQL", "mysql", "server", "mysql", 3306, contract.Capabilities{Objects: true, Maintenance: true, Accounts: true, Restore: true, DesignViews: true, Query: true, Metadata: true, Transactions: true, EditableTables: true, Explain: true, DesignTables: true, Import: true, Backup: true, Keyset: true, Count: true, FieldChunks: true, Analysis: true, Export: true}}, true
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
	case MySQL:
		return []string{"bigint", "int", "smallint", "decimal(10,2)", "varchar(255)", "text", "datetime", "timestamp", "date", "json", "blob", "boolean"}
	case PostgreSQL:
		return []string{"bigint", "integer", "smallint", "numeric(10,2)", "text", "varchar(255)", "boolean", "timestamp", "timestamptz", "date", "json", "jsonb", "bytea", "uuid"}
	case SQLite:
		return []string{"INTEGER", "TEXT", "REAL", "BLOB", "NUMERIC"}
	}
	return nil
}
