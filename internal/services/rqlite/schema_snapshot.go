package rqlite

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const schemaCatalogSQL = "SELECT hex(type),hex(name),hex(tbl_name),rootpage,hex(coalesce(sql,'')),sql IS NULL FROM main.sqlite_schema ORDER BY type COLLATE BINARY,name COLLATE BINARY"

// Snapshot is read-only foundation for native schema operations. Write methods
// remain absent until original document receipts and full native DDL semantics
// pass acceptance; a readable snapshot does not publish schema write tools.
func (s *Session) SchemaDesign() contract.SchemaDesignOperations {
	return contract.SchemaDesignOperations{Snapshot: s.schemaSnapshot}
}

func sqliteSchemaIdentity(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, name)
}

func (s *Session) checkSchemaTarget(t contract.SchemaDesignTarget) error {
	if !s.Alive() || t.Generation != s.Generation() || t.Generation == 0 {
		return contract.ErrRevoked
	}
	if err := s.checkNamespace(t.Object.Namespace()); err != nil {
		return err
	}
	if t.Object.Kind != "table" || schemaddl.Identifier(t.Object.Name) != nil || !validName(t.Object.Name) || strings.HasPrefix(sqliteSchemaIdentity(t.Object.Name), "sqlite_") {
		return contract.ErrUnsupported
	}
	return nil
}

func (s *Session) schemaSnapshot(ctx context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
	if err := s.checkSchemaTarget(target); err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	// Hex projections retain catalog bytes even when the native JSON serializer
	// would normalize malformed UTF-8. Unsupported text is rejected completely.
	r, err := s.requestBounded(ctx, "/db/query", []any{"SELECT schema_version FROM pragma_schema_version", schemaCatalogSQL}, true, false, 64<<20)
	if err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	if ctx.Err() != nil {
		return contract.SchemaDesignSnapshot{}, ctx.Err()
	}
	if err := s.checkSchemaTarget(target); err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	out, err := s.decodeSchemaSnapshot(target, r, 0)
	if err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	out.Schema.Revision = s.schemaSnapshotSeal(out)
	if ctx.Err() != nil {
		return contract.SchemaDesignSnapshot{}, ctx.Err()
	}
	if err := s.checkSchemaTarget(target); err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	return out, nil
}

func schemaCatalogText(c contract.Cell) (string, error) {
	encoded, err := cellText(c)
	if err != nil {
		return "", err
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil || !utf8.Valid(raw) || len(raw) > 1<<20 {
		return "", errIncomplete
	}
	return string(raw), nil
}

// decodeSchemaSnapshot can also consume the final catalog queries inside a
// future native atomic write. It never replaces the captured original target.
func (s *Session) decodeSchemaSnapshot(target contract.SchemaDesignTarget, r receipt, index int) (contract.SchemaDesignSnapshot, error) {
	versionRows, err := decodedRows(r, index, 1)
	if err != nil || len(versionRows) != 1 {
		return contract.SchemaDesignSnapshot{}, errIncomplete
	}
	version, err := cellInt(versionRows[0][0])
	if err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	rows, err := decodedRows(r, index+1, 6)
	if err != nil || len(rows) > 10000 {
		return contract.SchemaDesignSnapshot{}, errIncomplete
	}
	catalog := &schemaddl.SQLiteCatalog{Version: version, Entries: make([]schemaddl.SQLiteCatalogEntry, 0, len(rows))}
	for _, row := range rows {
		var e schemaddl.SQLiteCatalogEntry
		var decodeErr error
		e.Type, decodeErr = schemaCatalogText(row[0])
		if decodeErr == nil {
			e.Name, decodeErr = schemaCatalogText(row[1])
		}
		if decodeErr == nil {
			e.Table, decodeErr = schemaCatalogText(row[2])
		}
		if decodeErr == nil {
			e.Root, decodeErr = cellInt(row[3])
		}
		if decodeErr == nil {
			e.SQL, decodeErr = schemaCatalogText(row[4])
		}
		null, nullErr := cellInt(row[5])
		if decodeErr != nil || nullErr != nil || null != 0 && null != 1 {
			return contract.SchemaDesignSnapshot{}, errIncomplete
		}
		e.SQLNull = null == 1
		catalog.Entries = append(catalog.Entries, e)
	}
	if err := catalog.Validate(); err != nil {
		return contract.SchemaDesignSnapshot{}, errIncomplete
	}
	out := contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Engine: "sqlite", Table: schemaddl.Table{Name: target.Object.Name}, SQLite: catalog}}
	for _, entry := range catalog.Entries {
		if (entry.Type == "table" || entry.Type == "view") && sqliteSchemaIdentity(entry.Name) == sqliteSchemaIdentity(target.Object.Name) {
			if entry.Name != target.Object.Name || entry.Type != "table" || entry.SQLNull {
				return contract.SchemaDesignSnapshot{}, connections.ErrInvalid
			}
			out.Schema.Exists, out.Schema.DDL = true, entry.SQL
		}
	}
	if !out.Schema.Exists {
		return out, nil
	}
	out.Schema.Table, err = schemaddl.ParseSQLite(out.Schema.DDL)
	if err != nil || out.Schema.Table.Name != target.Object.Name {
		return contract.SchemaDesignSnapshot{}, errors.Join(schemaddl.ErrUnsupported, errors.New("RQLite table declaration cannot be represented completely"))
	}
	for _, entry := range catalog.Entries {
		if entry.Table == target.Object.Name && !entry.SQLNull && (entry.Type == "index" || entry.Type == "trigger") {
			out.Schema.Table.Definitions = append(out.Schema.Table.Definitions, schemaddl.Definition{Name: entry.Name, Kind: entry.Type, SQL: entry.SQL})
		}
	}
	return out, nil
}

func (s *Session) schemaSnapshotSeal(original contract.SchemaDesignSnapshot) string {
	original = original.Clone()
	original.Schema.Revision = ""
	raw, err := json.Marshal(original)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(s.dataScope))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

// schemaSnapshotGuard revalidates the complete, session-sealed original catalog
// on the executor inside the same native transaction as a change. It compares
// bytes, NULLness, root pages and schema version without connection PRAGMA
// mutation. Temporary triggers must be observed on that original writer.
func (s *Session) schemaSnapshotGuard(original contract.SchemaDesignSnapshot) (string, error) {
	original = original.Clone()
	if err := s.checkSchemaTarget(original.Target); err != nil {
		return "", err
	}
	if original.Schema.Engine != "sqlite" || original.Schema.SQLite == nil || original.Schema.Postgres != nil || original.Schema.OpenGauss != nil || original.Schema.SQLite.Validate() != nil {
		return "", connections.ErrInvalid
	}
	if !hmac.Equal([]byte(original.Schema.Revision), []byte(s.schemaSnapshotSeal(original))) {
		return "", schemaddl.ErrConflict
	}
	catalog := original.Schema.SQLite
	query := fmt.Sprintf("SELECT (SELECT schema_version FROM pragma_schema_version)=%d AND (SELECT count(*) FROM main.sqlite_schema)=%d", catalog.Version, len(catalog.Entries))
	if len(catalog.Entries) > 0 {
		var values strings.Builder
		for i, e := range catalog.Entries {
			if i > 0 {
				values.WriteByte(',')
			}
			null := 0
			if e.SQLNull {
				null = 1
			}
			fmt.Fprintf(&values, "(%s,%s,%s,%d,%s,%d)", schemaTextLiteral(e.Type), schemaTextLiteral(e.Name), schemaTextLiteral(e.Table), e.Root, schemaTextLiteral(e.SQL), null)
			if values.Len() > maxRequestBytes-4096 {
				return "", errors.Join(contract.ErrUnsupported, errors.New("complete original schema guard exceeds native request bounds"))
			}
		}
		query += " AND NOT EXISTS(WITH original(type,name,table_name,rootpage,sql,is_null) AS (VALUES" + values.String() + ") SELECT 1 FROM original o LEFT JOIN main.sqlite_schema a ON a.type COLLATE BINARY=o.type AND a.name COLLATE BINARY=o.name WHERE a.name IS NULL OR a.tbl_name IS NULL OR a.rootpage IS NULL OR a.tbl_name COLLATE BINARY<>o.table_name OR a.rootpage<>o.rootpage OR coalesce(a.sql,'') COLLATE BINARY<>o.sql OR (a.sql IS NULL)<>o.is_null)"
	}
	query += " AND NOT EXISTS(SELECT 1 FROM temp.sqlite_schema WHERE type='trigger' AND tbl_name COLLATE NOCASE=" + schemaTextLiteral(original.Target.Object.Name) + ")"
	return query, nil
}

func schemaTextLiteral(value string) string {
	return "CAST(X'" + hex.EncodeToString([]byte(value)) + "' AS TEXT)"
}

var _ contract.SchemaDesignProvider = (*Session)(nil)
