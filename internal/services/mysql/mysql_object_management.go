package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

// ObjectIdentity is a fully qualified, immutable MySQL target.
type ObjectIdentity struct{ Database, Name, Kind string }
type ObjectDefinition struct {
	SQLModeKnown bool

	ObjectIdentity
	SQL, SQLMode, Fingerprint string
	Generation                uint64
}
type ObjectPlan struct {
	Original    ObjectDefinition
	Action, SQL string
}
type ObjectOutcome struct {
	DDLOutcome
	ActualDefinition *ObjectDefinition
}

func objectKind(kind string) (string, error) {
	switch kind {
	case "database", "数据库":
		return "DATABASE", nil
	case "view", "视图":
		return "VIEW", nil
	case "function", "函数":
		return "FUNCTION", nil
	case "procedure", "存储过程":
		return "PROCEDURE", nil
	case "event", "事件":
		return "EVENT", nil
	}
	return "", errors.New("unsupported MySQL object kind")
}
func objectTarget(ref ObjectIdentity) (string, error) {
	db, e := quotedMySQLIdentifier(ref.Database)
	if e != nil {
		return "", e
	}
	kind, e := objectKind(ref.Kind)
	if e != nil {
		return "", e
	}
	if kind == "DATABASE" {
		return db, nil
	}
	n, e := quotedMySQLIdentifier(ref.Name)
	return db + "." + n, e
}
func definitionFingerprint(s ObjectDefinition) string {
	h := sha256.Sum256([]byte(s.SQL + "\x00" + s.SQLMode))
	return hex.EncodeToString(h[:])
}
func readObjectDefinition(ctx context.Context, c *sql.Conn, ref ObjectIdentity) (ObjectDefinition, error) {
	r := ObjectDefinition{ObjectIdentity: ref}
	kind, e := objectKind(ref.Kind)
	if e != nil {
		return r, e
	}
	target, e := objectTarget(ref)
	if e != nil {
		return r, e
	}
	rows, e := c.QueryContext(ctx, "SHOW CREATE "+kind+" "+target)
	if e != nil {
		return r, e
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		return r, e
	}
	if !rows.Next() {
		return r, errors.Join(ErrMySQLTableNotFound, rows.Err())
	}
	v := make([]sql.NullString, len(cols))
	dest := make([]any, len(v))
	for i := range v {
		dest[i] = &v[i]
	}
	if e = rows.Scan(dest...); e != nil {
		return r, e
	}
	for i, k := range cols {
		if strings.EqualFold(k, "sql_mode") {
			r.SQLMode = v[i].String
			r.SQLModeKnown = true
		}
		if strings.EqualFold(k, "Create "+kind) {
			r.SQL = v[i].String
		}
	}
	if r.SQL == "" || len(r.SQL) > 8<<20 {
		return r, errors.New("complete public object definition is unavailable or oversized")
	}
	r.Fingerprint = definitionFingerprint(r)
	return r, rows.Err()
}
func (m *MySQLSessionManager) ObjectDefinition(ctx context.Context, id int64, ref ObjectIdentity) (ObjectDefinition, error) {
	s, qctx, cancel, e := m.requestContext(ctx, id, 0)
	if e != nil {
		return ObjectDefinition{}, e
	}
	defer cancel()
	gen := m.SessionGeneration(id)
	c, e := s.db.Conn(qctx)
	if e != nil {
		return ObjectDefinition{}, e
	}
	defer c.Close()
	r, e := readObjectDefinition(qctx, c, ref)
	r.Generation = gen
	return r, e
}

// ValidateObjectSQL binds the first CREATE/ALTER definition to its displayed
// target. Complete routine bodies are retained, including internal delimiters.
func ValidateObjectSQL(ref ObjectIdentity, action, raw string) error {
	kind, e := objectKind(ref.Kind)
	if e != nil {
		return e
	}
	if e := sqltext.SingleDefinition(raw); e != nil {
		return e
	}
	if _, e = objectTarget(ref); e != nil {
		return e
	}
	if len(raw) == 0 || len(raw) > 8<<20 || strings.ContainsRune(raw, 0) {
		return errors.New("invalid complete object SQL")
	}
	ts, e := sqltext.Tokens(raw)
	if e != nil {
		return e
	}
	tokens := ts[:0]
	for _, t := range ts {
		if t.Comment {
			continue
		}
		tokens = append(tokens, t)
	}
	if len(tokens) < 3 {
		return errors.New("incomplete object definition")
	}
	verb := "CREATE"
	if action == "alter" && (kind == "DATABASE" || kind == "EVENT") {
		verb = "ALTER"
	}
	if !sqltext.Word(tokens[0], verb) {
		return fmt.Errorf("expected %s %s", verb, kind)
	}
	i := 1
	for i < len(tokens) && !sqltext.Word(tokens[i], kind) {
		if sqltext.Word(tokens[i], "TABLE") || tokens[i].Text == ";" {
			return errors.New("SQL definition kind differs from target")
		}
		i++
	}
	i++
	if i >= len(tokens) {
		return errors.New("missing object name")
	}
	name := unquoteObjectToken(tokens[i])
	i++
	db := ref.Database
	if i < len(tokens) && tokens[i].Text == "." {
		db = name
		i++
		if i >= len(tokens) {
			return errors.New("missing qualified name")
		}
		name = unquoteObjectToken(tokens[i])
		i++
	}
	want := ref.Name
	if kind == "DATABASE" {
		want = ref.Database
	}
	if db != ref.Database || name != want {
		return errors.New("definition identity differs from preview target")
	}
	if kind != "FUNCTION" && kind != "PROCEDURE" {
		for j := i; j < len(tokens); j++ {
			if tokens[j].Text == ";" && j != len(tokens)-1 {
				return errors.New("multiple top-level statements are not allowed")
			}
		}
	}
	return nil
}
func unquoteObjectToken(t sqltext.Token) string {
	if strings.HasPrefix(t.Text, "`") {
		return strings.ReplaceAll(strings.Trim(t.Text, "`"), "``", "`")
	}
	return t.Text
}
func BuildObjectPlan(original ObjectDefinition, action, raw string) (ObjectPlan, error) {
	if action != "create" && action != "alter" && action != "drop" {
		return ObjectPlan{}, errors.New("invalid object operation")
	}
	if action == "drop" {
		k, e := objectKind(original.Kind)
		if e != nil {
			return ObjectPlan{}, e
		}
		t, e := objectTarget(original.ObjectIdentity)
		if e != nil {
			return ObjectPlan{}, e
		}
		raw = "DROP " + k + " " + t
	} else if e := ValidateObjectSQL(original.ObjectIdentity, action, raw); e != nil {
		return ObjectPlan{}, e
	}
	if action != "create" && original.Fingerprint == "" {
		return ObjectPlan{}, errors.New("original full definition required")
	}
	return ObjectPlan{Original: original, Action: action, SQL: raw}, nil
}
func objectExists(ctx context.Context, c *sql.Conn, ref ObjectIdentity) (bool, error) {
	kind, e := objectKind(ref.Kind)
	if e != nil {
		return false, e
	}
	var n int
	switch kind {
	case "DATABASE":
		e = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME=?", ref.Database).Scan(&n)
	case "VIEW":
		e = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", ref.Database, ref.Name).Scan(&n)
	case "FUNCTION", "PROCEDURE":
		e = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA=? AND ROUTINE_NAME=? AND ROUTINE_TYPE=?", ref.Database, ref.Name, kind).Scan(&n)
	case "EVENT":
		e = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA=? AND EVENT_NAME=?", ref.Database, ref.Name).Scan(&n)
	}
	return n > 0, e
}

// ApplyObject executes on the captured session and one physical connection.
// Recreating a routine is explicitly two steps; a later error retains the
// partial outcome and never replays either statement.
func (m *MySQLSessionManager) ApplyObject(ctx context.Context, id int64, p ObjectPlan) ObjectOutcome {
	out := ObjectOutcome{DDLOutcome: DDLOutcome{State: "not_executed"}}
	checked, e := BuildObjectPlan(p.Original, p.Action, p.SQL)
	if e != nil {
		out.Error = e
		return out
	}
	s, qctx, cancel, e := m.requestContext(ctx, id, 0)
	if e != nil {
		out.Error = e
		return out
	}
	defer cancel()
	valid := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.sessions[id] == s && m.versions[id] == p.Original.Generation && s.ctx.Err() == nil
	}
	if !valid() {
		out.Error = ErrMySQLNotConnected
		return out
	}
	c, e := s.db.Conn(qctx)
	if e != nil {
		out.Error = e
		return out
	}
	defer c.Close()
	defer c.Raw(func(any) error { return driver.ErrBadConn })
	exists, e := objectExists(qctx, c, p.Original.ObjectIdentity)
	if e != nil {
		out.Error = e
		return out
	}
	if p.Action == "create" {
		if exists {
			out.Error = errors.New("target object already exists")
			return out
		}
	} else {
		actual, e := readObjectDefinition(qctx, c, p.Original.ObjectIdentity)
		if e != nil {
			out.Error = e
			return out
		}
		if actual.Fingerprint != p.Original.Fingerprint {
			out.ActualDefinition = &actual
			out.Error = errors.New("object changed externally; draft retained")
			return out
		}
	}
	kind, _ := objectKind(p.Original.Kind)
	if kind != "DATABASE" {
		db, _ := quotedMySQLIdentifier(p.Original.Database)
		if _, e = c.ExecContext(qctx, "USE "+db); e != nil {
			out.Error = e
			return out
		}
	}
	statements := []string{checked.SQL}
	if p.Action == "alter" {
		switch kind {
		case "VIEW":
			ts, _ := sqltext.Tokens(checked.SQL)
			for _, token := range ts {
				if !token.Comment {
					statements[0] = checked.SQL[:token.Start] + "CREATE OR REPLACE" + checked.SQL[token.End:]
					break
				}
			}
		case "FUNCTION", "PROCEDURE":
			target, _ := objectTarget(p.Original.ObjectIdentity)
			statements = []string{"DROP " + kind + " " + target, checked.SQL}
		}
	}
	if p.Original.SQLModeKnown || p.Original.SQLMode != "" || p.Action != "create" && (kind == "FUNCTION" || kind == "PROCEDURE" || kind == "EVENT") {
		if _, e = c.ExecContext(qctx, "SET SESSION sql_mode=?", p.Original.SQLMode); e != nil {
			out.Error = e
			return out
		}
	}
	for _, stmt := range statements {
		out.Steps = append(out.Steps, DDLStep{SQL: stmt, State: "not_executed"})
	}
	for i, stmt := range statements {
		if !valid() || qctx.Err() != nil {
			out.Error = errors.Join(ErrMySQLNotConnected, qctx.Err())
			if i > 0 {
				out.State = "partial"
			}
			return out
		}
		_, e = c.ExecContext(qctx, stmt)
		if e != nil {
			out.Error = e
			out.Steps[i].Error = e
			var server *mysqldriver.MySQLError
			if errors.As(e, &server) {
				if i > 0 {
					out.State = "partial"
				}
			} else {
				out.State = "uncertain"
				out.Steps[i].State = "uncertain"
			}
			return out
		}
		out.Steps[i].State = "executed"
		out.State = "executed"
	}
	if p.Action == "drop" {
		exists, e = objectExists(qctx, c, p.Original.ObjectIdentity)
		out.Verified = e == nil && !exists
	} else {
		actual, err := readObjectDefinition(qctx, c, p.Original.ObjectIdentity)
		e = err
		actual.Generation = p.Original.Generation
		out.ActualDefinition = &actual
		if e == nil {
			execution, verificationErr := readObjectVerificationContext(qctx, c, checked, kind)
			e = verificationErr
			if e == nil {
				out.Error = ObjectDefinitionEquivalent(checked, actual, execution)
				out.Verified = out.Error == nil
			}
		}
	}
	if e != nil || !valid() {
		out.State = "uncertain"
		out.Error = errors.Join(e, qctx.Err())
		if out.Error == nil {
			out.Error = ErrMySQLNotConnected
		}
		out.Verified = false
	}
	return out
}
