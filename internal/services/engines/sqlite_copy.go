package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func (s *Session) sqliteDesignCopy(ctx context.Context, request contract.SchemaCopyRequest) (out contract.SchemaDesignOutcome) {
	out.State = "not_executed"
	r := request.Clone()
	t := r.Original.Target
	if err := s.checkSQLiteDrop(t); err != nil {
		out.Error = err
		return
	}
	if r.Original.Schema.Engine != "sqlite" || !r.Original.Schema.Exists || r.Original.Schema.Revision == "" || r.Original.Schema.Table.Name != t.Object.Name {
		out.Error = errors.New("copy requires the complete original SQLite table snapshot")
		return
	}
	desired, err := schemaddl.CopySQLite(r.Original.Schema.Table, t.Object.Schema, r.Destination, r.RebindSelfReferences)
	if err != nil {
		out.Error = err
		return
	}
	statements, err := schemaddl.CopySQLiteSQL(desired, t.Object.Schema)
	if err != nil {
		out.Error = err
		return
	}
	ctx, done := s.requestContext(ctx)
	defer done()
	release, err := s.schema.acquire(ctx, true)
	if err != nil {
		out.Error = err
		return
	}
	defer release()
	if err = s.sqliteLeaseTemporaryTriggers(ctx, t.Object.Name); err != nil {
		out.Error = err
		return
	}
	c, err := s.db.Conn(ctx)
	if err != nil {
		out.Error = err
		return
	}
	defer c.Close()
	var foreignKeys int
	if err = c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		out.Error = err
		return
	}
	defer func() {
		restore, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, a := c.ExecContext(restore, fmt.Sprintf("PRAGMA foreign_keys=%d", foreignKeys))
		var actual int
		b := c.QueryRowContext(restore, "PRAGMA foreign_keys").Scan(&actual)
		if a != nil || b != nil || actual != foreignKeys {
			discardDesign(c)
			out.Error = errors.Join(out.Error, errors.New("copy connection discarded after PRAGMA restoration failed"), a, b)
		}
	}()
	if _, err = c.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		out.Error = err
		return
	}
	var disabled int
	if err = c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&disabled); err != nil || disabled != 0 {
		out.Error = errors.Join(err, errors.New("copy could not disable foreign key actions"))
		return
	}
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		out.Error = err
		return
	}
	settled := false
	defer func() {
		if !settled {
			if rollbackErr := rollbackDesign(c); rollbackErr != nil {
				discardDesign(c)
				out.State, out.Verified = "uncertain", false
				out.Error = errors.Join(out.Error, ErrUncertain, rollbackErr)
			} else {
				out.State = "rolled_back"
			}
		}
	}()
	actual, err := sqliteSnapshot(ctx, c, t)
	if err != nil {
		out.Error = err
		return
	}
	if actual.Schema.Revision != r.Original.Schema.Revision || schemaddl.Signature(actual.Schema) != schemaddl.Signature(r.Original.Schema) {
		out.Error = schemaddl.ErrConflict
		return
	}
	destination := t
	destination.Object.Name = r.Destination
	absent, err := sqliteSnapshot(ctx, c, destination)
	if err != nil {
		out.Error = err
		return
	}
	if absent.Schema.Exists {
		out.Error = errors.New("copy destination already exists; no object will be overwritten")
		return
	}
	for _, d := range desired.Definitions {
		var count int
		if err = c.QueryRowContext(ctx, "SELECT count(*) FROM "+quote(t.Object.Schema)+".sqlite_schema WHERE name=? COLLATE NOCASE", d.Name).Scan(&count); err != nil {
			out.Error = err
			return
		}
		if count != 0 {
			out.Error = fmt.Errorf("copied definition name %q already exists; choose another destination", d.Name)
			return
		}
	}
	if err = sqliteValidateTablePrograms(ctx, c, t.Object.Schema, t.Object.Name); err != nil {
		out.Error = err
		return
	}
	if err = sqliteForeignKeyCheck(ctx, c, t.Object.Schema); err != nil {
		out.Error = err
		return
	}
	var highWater sqliteCopySequence
	if r.WithData && schemaddl.SQLiteAutoIncrement(actual.Schema.Table) {
		highWater, err = sqliteReadCopySequence(ctx, c, t.Object.Schema, t.Object.Name)
		if err != nil {
			out.Error = err
			return
		}
	}
	if _, err = c.ExecContext(ctx, statements[0]); err != nil {
		out.Error = err
		return
	}
	if r.WithData {
		out.Rows, err = sqliteCopyRows(ctx, c, t.Object.Schema, t.Object.Name, r.Destination, actual.Schema.Table, r.PreserveRowID)
		if err != nil {
			out.Error = err
			return
		}
		if schemaddl.SQLiteAutoIncrement(actual.Schema.Table) {
			if err = sqliteRestoreCopySequence(ctx, c, t.Object.Schema, r.Destination, highWater); err != nil {
				out.Error = err
				return
			}
		}
	}
	// Data insertion precedes trigger installation. No copied program executes
	// during insertion, including bodies that deliberately refer to the source.
	for _, statement := range statements[1:] {
		if _, err = c.ExecContext(ctx, statement); err != nil {
			out.Error = err
			return
		}
	}
	if err = sqliteValidateTablePrograms(ctx, c, t.Object.Schema, r.Destination); err != nil {
		out.Error = err
		return
	}
	if err = sqliteForeignKeyCheck(ctx, c, t.Object.Schema); err != nil {
		out.Error = err
		return
	}
	verified, err := sqliteSnapshot(ctx, c, destination)
	if err != nil || !verified.Schema.Exists || !sqliteDesiredMatches(verified.Schema.Table, desired, t.Object.Schema) {
		out.Error = errors.Join(err, errors.New("copied table schema could not be verified"))
		return
	}
	retained, err := sqliteSnapshot(ctx, c, t)
	if err != nil || retained.Schema.DDL != actual.Schema.DDL || schemaddl.Signature(retained.Schema.Table) != schemaddl.Signature(actual.Schema.Table) {
		out.Error = errors.Join(err, errors.New("copy changed the original table schema"))
		return
	}
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		out.Error = err
		return
	}
	settled = true
	out.State, out.Verified, out.Actual = "committed", true, &verified
	return
}

func sqliteCopyRows(ctx context.Context, c *sql.Conn, schema, source, destination string, table schemaddl.Table, preserveRowID bool) (int64, error) {
	columns, err := sqliteDesignColumns(ctx, c, schema, source)
	if err != nil {
		return 0, err
	}
	var insert, verify []string
	if preserveRowID && !schemaddl.SQLiteWithoutRowID(table) {
		identity, err := sqliteCopyRowIdentity(ctx, c, schema, source, columns)
		if err != nil {
			return 0, err
		}
		insert = append(insert, quote(identity))
		verify = append(verify, quote(identity))
	}
	for _, col := range columns {
		if col.Hidden == 1 {
			return 0, schemaddl.ErrUnsupported
		}
		if col.Hidden == 0 && !containsSQLiteCopyColumn(insert, col.Name) {
			insert = append(insert, quote(col.Name))
		}
		verify = append(verify, quote(col.Name))
	}
	if len(insert) == 0 || len(verify) == 0 {
		return 0, errors.New("table has no complete copyable columns")
	}
	before, err := sqliteValueDigest(ctx, c, schema, source, verify)
	if err != nil {
		return 0, err
	}
	sql := "INSERT INTO " + quote(schema) + "." + quote(destination) + " (" + strings.Join(insert, ",") + ") SELECT " + strings.Join(insert, ",") + " FROM " + quote(schema) + "." + quote(source)
	if _, err = c.ExecContext(ctx, sql); err != nil {
		return 0, err
	}
	after, err := sqliteValueDigest(ctx, c, schema, destination, verify)
	if err != nil {
		return 0, err
	}
	if before != after {
		return 0, schemaddl.ErrDataChanged
	}
	return before.Rows, nil
}
func containsSQLiteCopyColumn(quoted []string, name string) bool {
	for _, v := range quoted {
		if sqliteCopyIdentifierKey(v) == sqliteCopyIdentifierKey(quote(name)) {
			return true
		}
	}
	return false
}

func sqliteCopyIdentifierKey(value string) string {
	b := []byte(value)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func sqliteCopyRowIdentity(ctx context.Context, c *sql.Conn, schema, table string, columns []sqliteDesignColumn) (string, error) {
	if alias := sqliteRowIDName(columns); alias != "" {
		return alias, nil
	}
	// When all hidden aliases are shadowed, an actual INTEGER PRIMARY KEY
	// still exposes rowid. A separate PK index distinguishes inline DESC (which
	// is not a rowid alias) without guessing from formatted CREATE text.
	var key string
	pk := 0
	for _, col := range columns {
		if col.Primary > 0 {
			pk++
			if strings.EqualFold(strings.TrimSpace(col.Type), "INTEGER") {
				key = col.Name
			}
		}
	}
	if pk == 1 && key != "" {
		rows, err := c.QueryContext(ctx, "PRAGMA "+quote(schema)+".index_list("+quote(table)+")")
		if err != nil {
			return "", err
		}
		indexed := false
		for rows.Next() {
			var seq, unique, partial int
			var name, origin string
			if err = rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				break
			}
			if origin == "pk" {
				indexed = true
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return "", err
		}
		if !indexed {
			return key, nil
		}
	}
	return "", errors.New("hidden rowid is inaccessible; explicitly allow new rowids to copy this table's data")
}

type sqliteCopySequence struct {
	Exists bool
	Value  int64
}

func sqliteReadCopySequence(ctx context.Context, c *sql.Conn, schema, table string) (sqliteCopySequence, error) {
	var out sqliteCopySequence
	rows, err := c.QueryContext(ctx, "SELECT typeof(seq),seq FROM "+quote(schema)+".sqlite_sequence WHERE name=?", table)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		if out.Exists {
			return out, errors.New("original table has duplicate autoincrement records")
		}
		var kind string
		var value any
		if err = rows.Scan(&kind, &value); err != nil {
			return out, err
		}
		integer, ok := value.(int64)
		if kind != "integer" || !ok {
			return out, errors.New("original autoincrement state is not an exact integer")
		}
		out = sqliteCopySequence{Exists: true, Value: integer}
	}
	return out, rows.Err()
}
func sqliteRestoreCopySequence(ctx context.Context, c *sql.Conn, schema, table string, original sqliteCopySequence) error {
	if _, err := c.ExecContext(ctx, "DELETE FROM "+quote(schema)+".sqlite_sequence WHERE name=?", table); err != nil {
		return err
	}
	if original.Exists {
		if _, err := c.ExecContext(ctx, "INSERT INTO "+quote(schema)+".sqlite_sequence(name,seq) VALUES(?,?)", table, original.Value); err != nil {
			return err
		}
	}
	actual, err := sqliteReadCopySequence(ctx, c, schema, table)
	if err != nil {
		return err
	}
	if actual != original {
		return errors.New("copied autoincrement state differs from the original")
	}
	return nil
}
