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

func (s *Session) checkSQLiteDrop(t contract.SchemaDesignTarget) error {
	if err := s.checkDesign(t, true); err != nil {
		return err
	}
	if strings.HasPrefix(strings.ToLower(t.Object.Name), "sqlite_") {
		return schemaddl.ErrUnsupported
	}
	return nil
}

// Drop is a transaction over the captured namespace, with no implicit FK
// delete actions. All remaining schema programs must still compile. Existing
// unresolved programs are conservatively refused, not silently repaired.
func (s *Session) sqliteDesignDrop(ctx context.Context, original contract.SchemaDesignSnapshot) (out contract.SchemaDesignOutcome) {
	out.State = "not_executed"
	r := original.Clone()
	t := r.Target
	if err := s.checkSQLiteDrop(t); err != nil {
		out.Error = err
		return
	}
	if r.Schema.Engine != "sqlite" || !r.Schema.Exists || r.Schema.Revision == "" || r.Schema.Table.Name != t.Object.Name {
		out.Error = errors.New("drop requires the complete original SQLite table snapshot")
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
	// TEMP views/triggers can refer across namespaces and are absent from the
	// persistent revision. Refuse them on every owned lease and the writer.
	s.mu.Lock()
	var leases []*Lease
	for l := range s.leases {
		leases = append(leases, l)
	}
	s.mu.Unlock()
	for _, l := range leases {
		l.mu.Lock()
		closed, c := l.closed, l.c
		l.mu.Unlock()
		if closed {
			continue
		}
		if err = sqliteDropNoTemporaryPrograms(ctx, c); err != nil {
			l.mu.Lock()
			closed = l.closed
			l.mu.Unlock()
			if closed && ctx.Err() == nil {
				continue
			}
			out.Error = err
			return
		}
	}
	c, err := s.db.Conn(ctx)
	if err != nil {
		out.Error = err
		return
	}
	defer c.Close()
	if err = sqliteDropNoTemporaryPrograms(ctx, c); err != nil {
		out.Error = err
		return
	}
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
			out.Error = errors.Join(out.Error, errors.New("drop connection discarded after PRAGMA restoration failed"), a, b)
		}
	}()
	if _, err = c.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		out.Error = err
		return
	}
	var disabled int
	if err = c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&disabled); err != nil || disabled != 0 {
		out.Error = errors.Join(err, errors.New("drop could not disable implicit foreign key actions"))
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
	if actual.Schema.Revision != r.Schema.Revision || schemaddl.Signature(actual.Schema) != schemaddl.Signature(r.Schema) {
		out.Error = schemaddl.ErrConflict
		return
	}
	if err = sqliteDropDependencies(ctx, c, t.Object.Schema, t.Object.Name); err != nil {
		out.Error = err
		return
	}
	if _, err = c.ExecContext(ctx, "DROP TABLE "+quote(t.Object.Schema)+"."+quote(t.Object.Name)); err != nil {
		out.Error = err
		return
	}
	if err = sqliteDropDependencies(ctx, c, t.Object.Schema, t.Object.Name); err != nil {
		out.Error = err
		return
	}
	verified, err := sqliteSnapshot(ctx, c, t)
	if err != nil || verified.Schema.Exists {
		out.Error = errors.Join(err, errors.New("dropped table absence could not be verified"))
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

func sqliteDropNoTemporaryPrograms(ctx context.Context, c *sql.Conn) error {
	var count int
	if err := c.QueryRowContext(ctx, "SELECT count(*) FROM temp.sqlite_schema WHERE type IN ('view','trigger')").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("remove temporary views and triggers before dropping a table")
	}
	return nil
}

func sqliteDropDependencies(ctx context.Context, c *sql.Conn, schema, removed string) error {
	rows, err := c.QueryContext(ctx, "SELECT type,name FROM "+quote(schema)+".sqlite_schema WHERE type IN ('table','view') AND substr(name,1,7) != 'sqlite_' ORDER BY type,name")
	if err != nil {
		return err
	}
	var objects [][2]string
	for rows.Next() {
		var o [2]string
		if err = rows.Scan(&o[0], &o[1]); err != nil {
			break
		}
		objects = append(objects, o)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, o := range objects {
		if o[1] == removed {
			continue
		} // own triggers and self-FKs disappear with the table
		if o[0] == "table" {
			rows, err = c.QueryContext(ctx, "PRAGMA "+quote(schema)+".foreign_key_list("+quote(o[1])+")")
			if err != nil {
				return err
			}
			for rows.Next() {
				var id, seq int
				var parent, from, update, delete, match string
				var to sql.NullString
				if err = rows.Scan(&id, &seq, &parent, &from, &to, &update, &delete, &match); err != nil {
					break
				}
				if strings.EqualFold(parent, removed) {
					err = fmt.Errorf("table %q has a foreign key to %q; remove it explicitly before dropping", o[1], removed)
					break
				}
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				return err
			}
			if err = sqliteValidateTablePrograms(ctx, c, schema, o[1]); err != nil {
				return err
			}
		} else {
			rows, err = c.QueryContext(ctx, "SELECT * FROM "+quote(schema)+"."+quote(o[1])+" LIMIT 0")
			if err != nil {
				return fmt.Errorf("remaining view %q cannot be compiled: %w", o[1], err)
			}
			rows.Close()
			// Compile INSTEAD OF programs too, but avoid treating an ordinary
			// read-only view as a writable table.
			if err = sqliteDropViewTriggers(ctx, c, schema, o[1]); err != nil {
				return err
			}
		}
	}
	return sqliteForeignKeyCheck(ctx, c, schema)
}

func sqliteDropViewTriggers(ctx context.Context, c *sql.Conn, schema, view string) error {
	rows, err := c.QueryContext(ctx, "SELECT sql FROM "+quote(schema)+".sqlite_schema WHERE type='trigger' AND tbl_name=?", view)
	if err != nil {
		return err
	}
	var definitions []string
	for rows.Next() {
		var ddl string
		if err = rows.Scan(&ddl); err != nil {
			break
		}
		definitions = append(definitions, ddl)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, ddl := range definitions {
		info, err := sqliteDesignColumns(ctx, c, schema, view)
		if err != nil {
			return err
		}
		columns := schemaddl.Table{}
		for _, col := range info {
			columns.Clauses = append(columns.Clauses, schemaddl.Clause{Kind: "column", Name: col.Name})
		}
		if err := schemaddl.ValidateSQLiteTriggerColumns(schemaddl.Definition{Kind: "trigger", SQL: ddl}, columns); err != nil {
			return err
		}
		event, err := schemaddl.SQLiteTriggerEvent(ddl)
		if err != nil {
			return err
		}
		qualified := quote(schema) + "." + quote(view)
		statement := "EXPLAIN DELETE FROM " + qualified
		switch event {
		case "insert":
			statement = "EXPLAIN INSERT INTO " + qualified + " DEFAULT VALUES"
		case "update":
			info, err := sqliteDesignColumns(ctx, c, schema, view)
			if err != nil {
				return err
			}
			var set []string
			for _, col := range info {
				set = append(set, quote(col.Name)+"="+quote(col.Name))
			}
			if len(set) == 0 {
				return errors.New("trigger view has no reported columns")
			}
			statement = "EXPLAIN UPDATE " + qualified + " SET " + strings.Join(set, ",")
		}
		rows, err = c.QueryContext(ctx, statement)
		if err != nil {
			return fmt.Errorf("remaining view trigger cannot be compiled: %w", err)
		}
		rows.Close()
	}
	return nil
}
