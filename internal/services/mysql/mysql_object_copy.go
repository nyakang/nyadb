package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

func copyObjectName(target, original string) string {
	h := sha256.Sum256([]byte(target + "\x00" + original))
	r := []rune(target + "_" + original)
	if len(r) > 45 {
		r = r[:45]
	}
	return string(r) + fmt.Sprintf("_%x", h[:6])
}

type sqlReplacement struct {
	start, end int
	value      string
}

func replaceSQL(raw string, rs []sqlReplacement) string {
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		raw = raw[:r.start] + r.value + raw[r.end:]
	}
	return raw
}

// CopyTableDefinition retains all public clauses, including generated fields,
// foreign keys, check constraints and unknown options. Only scoped object
// identities are rewritten; string literals and trigger bodies stay verbatim.
func CopyTableDefinition(source mysqlddl.Snapshot, target string) (mysqlddl.Table, []mysqlddl.Trigger, error) {
	if strings.Contains(source.SQLMode, "NO_BACKSLASH_ESCAPES") || strings.Contains(source.SQLMode, "ANSI_QUOTES") {
		return mysqlddl.Table{}, nil, errors.New("copy requires standard MySQL quote modes")
	}
	if source.TriggersUnknown {
		return mysqlddl.Table{}, nil, errors.New("copy blocked: full trigger metadata cannot be established")
	}
	if _, e := quotedMySQLIdentifier(target); e != nil {
		return mysqlddl.Table{}, nil, e
	}
	t, e := mysqlddl.Parse(source.Database, source.Table, source.DDL)
	if e != nil {
		return t, nil, e
	}
	t.Name = target
	for i, c := range t.Clauses {
		ts, e := sqltext.Tokens(c.SQL)
		if e != nil {
			return t, nil, e
		}
		var rs []sqlReplacement
		newname := c.Name
		for j, tok := range ts {
			if sqltext.Word(tok, "CONSTRAINT") && j+1 < len(ts) && (c.Kind == mysqlddl.Foreign || c.Kind == mysqlddl.Check) {
				newname = copyObjectName(target, c.Name)
				quoted, _ := quotedMySQLIdentifier(newname)
				rs = append(rs, sqlReplacement{ts[j+1].Start, ts[j+1].End, quoted})
			}
			if sqltext.Word(tok, "REFERENCES") && j+1 < len(ts) {
				idx := j + 1
				db := source.Database
				name := unquoteObjectToken(ts[idx])
				if idx+2 < len(ts) && ts[idx+1].Text == "." {
					db = name
					idx += 2
					name = unquoteObjectToken(ts[idx])
				}
				if db == source.Database && name == source.Table {
					quoted, _ := quotedMySQLIdentifier(target)
					rs = append(rs, sqlReplacement{ts[idx].Start, ts[idx].End, quoted})
				}
			}
		}
		c.SQL = replaceSQL(c.SQL, rs)
		c.Name = newname
		c.ID = string(c.Kind) + ":" + newname
		t.Clauses[i] = c
	}
	if e = mysqlddl.Validate(t); e != nil {
		return t, nil, e
	}
	var triggers []mysqlddl.Trigger
	for _, tr := range source.Triggers {
		tokens, e := sqltext.Tokens(tr.SQL)
		if e != nil {
			return t, nil, e
		}
		var rs []sqlReplacement
		seen := false
		name := copyObjectName(target, tr.Name)
		dbq, _ := quotedMySQLIdentifier(source.Database)
		for j, tok := range tokens {
			if !seen && sqltext.Word(tok, "TRIGGER") && j+1 < len(tokens) {
				idx := j + 1
				start := tokens[idx].Start
				if idx+2 < len(tokens) && tokens[idx+1].Text == "." {
					idx += 2
				}
				q, _ := quotedMySQLIdentifier(name)
				rs = append(rs, sqlReplacement{start, tokens[idx].End, dbq + "." + q})
				seen = true
				continue
			}
			if seen && sqltext.Word(tok, "ON") && j+1 < len(tokens) {
				idx := j + 1
				start := tokens[idx].Start
				if idx+2 < len(tokens) && tokens[idx+1].Text == "." {
					idx += 2
				}
				q, _ := quotedMySQLIdentifier(target)
				rs = append(rs, sqlReplacement{start, tokens[idx].End, dbq + "." + q})
				break
			}
		}
		if len(rs) != 2 {
			return t, nil, errors.New("cannot safely identify complete trigger target")
		}
		tr.Name = name
		tr.SQL = replaceSQL(tr.SQL, rs)
		if e = mysqlddl.ValidateTrigger(tr, source.Database, target); e != nil {
			return t, nil, e
		}
		triggers = append(triggers, tr)
	}
	return t, triggers, nil
}
func tableCopyDataConnSQL(ctx context.Context, c *sql.Conn, change MySQLTableChange) (string, error) {
	rows, e := c.QueryContext(ctx, "SELECT COLUMN_NAME, GENERATION_EXPRESSION FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION", change.Database, change.Table)
	if e != nil {
		return "", e
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		var expr sql.NullString
		if e = rows.Scan(&n, &expr); e != nil {
			return "", e
		}
		if expr.Valid && expr.String != "" {
			continue
		}
		q, e := quotedMySQLIdentifier(n)
		if e != nil {
			return "", e
		}
		names = append(names, q)
	}
	if e = rows.Err(); e != nil {
		return "", e
	}
	if len(names) == 0 {
		return "", errors.New("copy has no writable columns")
	}
	db, _ := quotedMySQLIdentifier(change.Database)
	src, _ := quotedMySQLIdentifier(change.Table)
	dst, _ := quotedMySQLIdentifier(change.NewName)
	list := strings.Join(names, ", ")
	return "INSERT INTO " + db + "." + dst + " (" + list + ") SELECT " + list + " FROM " + db + "." + src, nil
}
func (m *MySQLSessionManager) copyCompleteTable(ctx context.Context, id int64, change MySQLTableChange) error {
	s, qctx, cancel, e := m.requestContext(ctx, id, 0)
	if e != nil {
		return e
	}
	defer cancel()
	gen := m.SessionGeneration(id)
	if change.RuntimeGeneration != 0 && gen != change.RuntimeGeneration {
		return ErrMySQLNotConnected
	}
	valid := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.sessions[id] == s && m.versions[id] == gen && s.ctx.Err() == nil
	}
	c, e := s.db.Conn(qctx)
	if e != nil {
		return e
	}
	defer c.Close()
	defer c.Raw(func(any) error { return driver.ErrBadConn })
	original, e := readTableDesign(qctx, c, change.Database, change.Table)
	if e != nil {
		return e
	}
	desired, triggers, e := CopyTableDefinition(original, change.NewName)
	if e != nil {
		return e
	}
	var n int
	if e = c.QueryRowContext(qctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", change.Database, change.NewName).Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return errors.New("copy target already exists")
	}
	statements := []string{desired.DDL()}
	if change.Action == MySQLTableCopyData {
		stmt, e := tableCopyDataConnSQL(qctx, c, change)
		if e != nil {
			return e
		}
		statements = append(statements, stmt)
	}
	for _, tr := range triggers {
		if tr.SQLMode != original.SQLMode {
			return errors.New("copy requires compatible trigger SQL modes")
		}
		statements = append(statements, tr.SQL)
	}
	actual, e := readTableDesign(qctx, c, change.Database, change.Table)
	if e != nil {
		return e
	}
	if original.Fingerprint() != actual.Fingerprint() {
		return errors.New("source changed externally before copy")
	}
	for i, stmt := range statements {
		if !valid() || qctx.Err() != nil {
			return fmt.Errorf("copy stopped before step %d; completed steps remain: %w", i+1, errors.Join(ErrMySQLNotConnected, qctx.Err()))
		}
		if _, e = c.ExecContext(qctx, stmt); e != nil {
			return fmt.Errorf("copy step %d failed; earlier steps remain and this step may be uncertain: %w", i+1, e)
		}
	}
	got, e := readTableDesign(qctx, c, change.Database, change.NewName)
	if e != nil {
		return fmt.Errorf("copy completed; verification failed: %w", e)
	}
	draft := mysqlddl.Draft{Table: desired, Triggers: triggers}
	if e = mysqlddl.Verify(draft, got); e != nil {
		return fmt.Errorf("copy completed; structure verification: %w", e)
	}
	return nil
}
