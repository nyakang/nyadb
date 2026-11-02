package engines

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type sqliteDesignColumn struct {
	Name, Type      string
	Hidden, Primary int
}

func sqliteDesignColumns(ctx context.Context, c *sql.Conn, schema, name string) ([]sqliteDesignColumn, error) {
	rows, err := c.QueryContext(ctx, "PRAGMA "+quote(schema)+".table_xinfo("+quote(name)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sqliteDesignColumn
	for rows.Next() {
		var col sqliteDesignColumn
		var cid, notnull int
		var def sql.NullString
		if err = rows.Scan(&cid, &col.Name, &col.Type, &notnull, &def, &col.Primary, &col.Hidden); err != nil {
			return nil, err
		}
		out = append(out, col)
	}
	return out, rows.Err()
}
func sqliteRowIDName(cols []sqliteDesignColumn) string {
	for _, candidate := range []string{"rowid", "_rowid_", "oid"} {
		available := true
		for _, c := range cols {
			if strings.EqualFold(candidate, c.Name) {
				available = false
				break
			}
		}
		if available {
			return candidate
		}
	}
	return ""
}

func sqliteRebuild(ctx context.Context, c *sql.Conn, o contract.ObjectRef, old, desired schemaddl.Table, plan schemaddl.Plan) (int64, error) {
	shadow, err := sqliteShadowName()
	if err != nil {
		return 0, err
	}
	create, err := schemaddl.CreateSQLite(desired, o.Schema, shadow)
	if err != nil {
		return 0, err
	}
	oldColumns, err := sqliteDesignColumns(ctx, c, o.Schema, o.Name)
	if err != nil {
		return 0, err
	}
	var highWater sql.NullInt64
	if schemaddl.SQLiteAutoIncrement(old) {
		err = c.QueryRowContext(ctx, "SELECT seq FROM "+quote(o.Schema)+".sqlite_sequence WHERE name=?", o.Name).Scan(&highWater)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	if _, err = c.ExecContext(ctx, create); err != nil {
		return 0, err
	}
	newColumns, err := sqliteDesignColumns(ctx, c, o.Schema, shadow)
	if err != nil {
		return 0, err
	}
	oldInfo, newInfo := map[string]sqliteDesignColumn{}, map[string]sqliteDesignColumn{}
	for _, col := range oldColumns {
		oldInfo[col.Name] = col
	}
	for _, col := range newColumns {
		newInfo[col.Name] = col
	}
	var source, destination, verifySource, verifyDestination []string
	// Preserve the hidden identity even on tables without an explicit key. The
	// digest includes it so an INTEGER PRIMARY KEY reassignment is also caught.
	if !schemaddl.SQLiteWithoutRowID(old) {
		oldRowID, newRowID := sqliteRowIDName(oldColumns), sqliteRowIDName(newColumns)
		if oldRowID == "" || newRowID == "" {
			return 0, errors.New("hidden rowid cannot be preserved because all aliases are shadowed")
		}
		source, destination = append(source, quote(oldRowID)), append(destination, quote(newRowID))
		verifySource, verifyDestination = append(verifySource, quote(oldRowID)), append(verifyDestination, quote(newRowID))
	}
	oldClauses, desiredClauses := map[string]schemaddl.Clause{}, map[string]schemaddl.Clause{}
	for _, col := range old.Columns() {
		oldClauses[col.ID] = col
	}
	for _, col := range desired.Columns() {
		desiredClauses[col.ID] = col
	}
	for _, mapping := range plan.Mappings {
		oldCol, ok := oldInfo[mapping.Source]
		if !ok {
			return 0, errors.New("original column mapping is incomplete")
		}
		newCol, ok := newInfo[mapping.Destination]
		if !ok {
			return 0, errors.New("desired column mapping is incomplete")
		}
		if oldCol.Hidden == 1 || newCol.Hidden == 1 {
			return 0, schemaddl.ErrUnsupported
		}
		if oldCol.Hidden == 0 && newCol.Hidden != 0 {
			return 0, errors.New("replacing stored values with a generated column requires a separate data migration")
		}
		if newCol.Hidden == 0 {
			source, destination = append(source, quote(mapping.Source)), append(destination, quote(mapping.Destination))
		}
		// A deliberately edited generated expression has new computed values.
		// Unchanged computed columns and every retained ordinary value must match.
		if newCol.Hidden == 0 || oldCol.Hidden == newCol.Hidden && oldClauses[mapping.ID].SQL == desiredClauses[mapping.ID].SQL {
			verifySource, verifyDestination = append(verifySource, quote(mapping.Source)), append(verifyDestination, quote(mapping.Destination))
		}
	}
	if len(source) == 0 || len(verifySource) == 0 {
		return 0, errors.New("rebuild has no safely retained identity or columns")
	}
	before, err := sqliteValueDigest(ctx, c, o.Schema, o.Name, verifySource)
	if err != nil {
		return 0, err
	}
	copySQL := "INSERT INTO " + quote(o.Schema) + "." + quote(shadow) + " (" + strings.Join(destination, ",") + ") SELECT " + strings.Join(source, ",") + " FROM " + qualified(o)
	if _, err = c.ExecContext(ctx, copySQL); err != nil {
		return 0, err
	}
	after, err := sqliteValueDigest(ctx, c, o.Schema, shadow, verifyDestination)
	if err != nil {
		return 0, err
	}
	if before != after {
		return 0, schemaddl.ErrDataChanged
	}
	if _, err = c.ExecContext(ctx, "DROP TABLE "+qualified(o)); err != nil {
		return 0, err
	}
	if _, err = c.ExecContext(ctx, "ALTER TABLE "+quote(o.Schema)+"."+quote(shadow)+" RENAME TO "+quote(o.Name)); err != nil {
		return 0, err
	}
	if highWater.Valid && schemaddl.SQLiteAutoIncrement(desired) {
		var seq sql.NullInt64
		err = c.QueryRowContext(ctx, "SELECT seq FROM "+quote(o.Schema)+".sqlite_sequence WHERE name=?", o.Name).Scan(&seq)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = c.ExecContext(ctx, "INSERT INTO "+quote(o.Schema)+".sqlite_sequence(name,seq) VALUES(?,?)", o.Name, highWater.Int64)
		} else if err == nil && (!seq.Valid || seq.Int64 < highWater.Int64) {
			_, err = c.ExecContext(ctx, "UPDATE "+quote(o.Schema)+".sqlite_sequence SET seq=? WHERE name=?", highWater.Int64, o.Name)
		}
		if err != nil {
			return 0, err
		}
	}
	for _, definition := range desired.Definitions {
		if err := schemaddl.ValidateSQLiteTriggerColumns(definition, desired); err != nil {
			return 0, err
		}
		statement, err := schemaddl.DefinitionSQLite(definition, o.Schema, o.Name)
		if err != nil {
			return 0, err
		}
		if _, err = c.ExecContext(ctx, statement); err != nil {
			return 0, err
		}
	}
	return before.Rows, nil
}

// A streaming multiset fingerprint uses storage-class tags, exact driver
// bytes/IEEE floats and a 256-bit sum of SHA-256 row hashes. Duplicate rows
// count separately; no SQL cast, collation, LIMIT or cell truncation is used.
type sqliteDigest struct {
	Rows int64
	Sum  [32]byte
}

func sqliteValueDigest(ctx context.Context, c *sql.Conn, schema, table string, columns []string) (sqliteDigest, error) {
	d := sqliteDigest{}
	var selectColumns []string
	for _, column := range columns {
		selectColumns = append(selectColumns, "typeof("+column+")", column)
	}
	rows, err := c.QueryContext(ctx, "SELECT "+strings.Join(selectColumns, ",")+" FROM "+quote(schema)+"."+quote(table))
	if err != nil {
		return d, err
	}
	defer rows.Close()
	values, pointers := make([]any, len(selectColumns)), make([]any, len(selectColumns))
	for i := range pointers {
		pointers[i] = &values[i]
	}
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return d, err
		}
		if err = rows.Scan(pointers...); err != nil {
			return d, err
		}
		h := sha256.New()
		for i := 0; i < len(values); i += 2 {
			storage, ok := values[i].(string)
			if !ok {
				return d, errors.New("SQLite storage class absent")
			}
			writeDigestBytes(h, []byte(storage))
			switch v := values[i+1].(type) {
			case nil:
				writeDigestBytes(h, nil)
			case string:
				writeDigestBytes(h, []byte(v))
			case []byte:
				writeDigestBytes(h, v)
			case int64:
				var b [8]byte
				binary.LittleEndian.PutUint64(b[:], uint64(v))
				writeDigestBytes(h, b[:])
			case float64:
				var b [8]byte
				binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
				writeDigestBytes(h, b[:])
			default:
				return d, fmt.Errorf("unsupported SQLite value representation %T", v)
			}
		}
		sum := h.Sum(nil)
		carry := 0
		for i := len(d.Sum) - 1; i >= 0; i-- {
			n := int(d.Sum[i]) + int(sum[i]) + carry
			d.Sum[i], carry = byte(n), n>>8
		}
		d.Rows++
	}
	return d, rows.Err()
}
func writeDigestBytes(h io.Writer, b []byte) {
	var length [8]byte
	binary.LittleEndian.PutUint64(length[:], uint64(len(b)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(b)
}
