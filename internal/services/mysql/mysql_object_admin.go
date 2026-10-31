package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	mysqldriver "github.com/go-sql-driver/mysql"
	"strings"
	"unicode/utf8"
)

type MaintenanceMessage struct{ Table, Operation, Type, Message string }

func (m *MySQLSessionManager) MaintainTable(ctx context.Context, id int64, database, table, operation string) ([]MaintenanceMessage, error) {
	op := strings.ToUpper(operation)
	switch op {
	case "ANALYZE", "CHECK", "OPTIMIZE", "REPAIR":
	default:
		return nil, errors.New("unsupported maintenance operation")
	}
	db, e := quotedMySQLIdentifier(database)
	if e != nil {
		return nil, e
	}
	name, e := quotedMySQLIdentifier(table)
	if e != nil {
		return nil, e
	}
	s, qctx, cancel, e := m.requestContext(ctx, id, 0)
	if e != nil {
		return nil, e
	}
	defer cancel()
	if e = m.expectedObjectGeneration(ctx, id); e != nil {
		return nil, e
	}
	c, e := s.db.Conn(qctx)
	if e != nil {
		return nil, e
	}
	defer c.Close()
	rows, e := c.QueryContext(qctx, op+" TABLE "+db+"."+name)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []MaintenanceMessage
	for rows.Next() {
		var r MaintenanceMessage
		if e = rows.Scan(&r.Table, &r.Operation, &r.Type, &r.Message); e != nil {
			return out, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type MySQLAccount struct {
	User, Host string
	Role       bool
}
type AccountChange struct {
	Action          string
	Account         MySQLAccount
	Password        string
	Privileges      []string
	Database, Table string
	Role            MySQLAccount
}

func quoteAccount(a MySQLAccount) (string, error) {
	if a.User == "" || strings.ContainsRune(a.User, 0) || strings.ContainsRune(a.Host, 0) || strings.ContainsRune(a.User, '\\') || strings.ContainsRune(a.Host, '\\') || len(a.User) > 128 || len(a.Host) > 255 {
		return "", errors.New("invalid account")
	}
	quote := func(s string) string {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "'", "''") + "'"
	}
	host := a.Host
	if host == "" {
		host = "%"
	}
	return quote(a.User) + "@" + quote(host), nil
}

var accountPrivileges = map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "CREATE": true, "DROP": true, "ALTER": true, "INDEX": true, "REFERENCES": true, "TRIGGER": true, "EXECUTE": true, "CREATE VIEW": true, "SHOW VIEW": true, "CREATE ROUTINE": true, "ALTER ROUTINE": true, "EVENT": true, "CREATE TEMPORARY TABLES": true, "LOCK TABLES": true, "ALL PRIVILEGES": true, "USAGE": true}

func accountChangeSQL(ch AccountChange) (string, []any, error) {
	target, e := quoteAccount(ch.Account)
	if e != nil {
		return "", nil, e
	}
	switch ch.Action {
	case "create_user":
		return "CREATE USER " + target + " IDENTIFIED BY ?", []any{ch.Password}, nil
	case "password":
		return "ALTER USER " + target + " IDENTIFIED BY ?", []any{ch.Password}, nil
	case "create_role":
		return "CREATE ROLE " + target, nil, nil
	case "drop":
		return "DROP USER " + target, nil, nil
	case "drop_role":
		return "DROP ROLE " + target, nil, nil
	case "lock":
		return "ALTER USER " + target + " ACCOUNT LOCK", nil, nil
	case "unlock":
		return "ALTER USER " + target + " ACCOUNT UNLOCK", nil, nil
	case "grant_role", "revoke_role":
		role, e := quoteAccount(ch.Role)
		if e != nil {
			return "", nil, e
		}
		if ch.Action == "grant_role" {
			return "GRANT " + role + " TO " + target, nil, nil
		}
		return "REVOKE " + role + " FROM " + target, nil, nil
	case "grant", "revoke":
		if len(ch.Privileges) == 0 {
			return "", nil, errors.New("select a privilege")
		}
		privs := make([]string, len(ch.Privileges))
		for i, p := range ch.Privileges {
			p = strings.ToUpper(strings.TrimSpace(p))
			if !accountPrivileges[p] {
				return "", nil, errors.New("unsupported privilege")
			}
			privs[i] = p
		}
		db := "*"
		table := "*"
		if ch.Database != "" {
			db, e = quotedMySQLIdentifier(ch.Database)
			if e != nil {
				return "", nil, e
			}
		}
		if ch.Table != "" {
			table, e = quotedMySQLIdentifier(ch.Table)
			if e != nil {
				return "", nil, e
			}
		}
		verb, to := "GRANT", "TO"
		if ch.Action == "revoke" {
			verb, to = "REVOKE", "FROM"
		}
		return verb + " " + strings.Join(privs, ", ") + " ON " + db + "." + table + " " + to + " " + target, nil, nil
	}
	return "", nil, errors.New("unsupported account operation")
}
func (m *MySQLSessionManager) Accounts(ctx context.Context, id int64) ([]MySQLAccount, error) {
	s, qctx, cancel, e := m.requestContext(ctx, id, 0)
	if e != nil {
		return nil, e
	}
	defer cancel()
	if e = m.expectedObjectGeneration(ctx, id); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(qctx, "SELECT User, Host, account_locked, authentication_string FROM mysql.user ORDER BY User,Host")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []MySQLAccount
	for rows.Next() {
		var a MySQLAccount
		var locked, auth string
		if e = rows.Scan(&a.User, &a.Host, &locked, &auth); e != nil {
			return out, e
		}
		a.Role = false // Locked passwordless users cannot be reliably classified as roles.
		out = append(out, a)
		if len(out) > 4096 {
			return nil, ErrMySQLMetadataLimit
		}
	}
	return out, rows.Err()
}
func (m *MySQLSessionManager) AccountGrants(ctx context.Context, id int64, a MySQLAccount) ([]string, error) {
	target, e := quoteAccount(a)
	if e != nil {
		return nil, e
	}
	s, qctx, cancel, e := m.requestContext(ctx, id, 0)
	if e != nil {
		return nil, e
	}
	defer cancel()
	if e = m.expectedObjectGeneration(ctx, id); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(qctx, "SHOW GRANTS FOR "+target)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var grant string
		if e = rows.Scan(&grant); e != nil {
			return out, e
		}
		out = append(out, grant)
	}
	return out, rows.Err()
}

// ChangeAccounts returns one acknowledged result per requested action. It uses
// one pinned connection and stops on the first rejection or uncertain result.
// Account DDL does not accept prepared password parameters. Its preview uses
// placeholders; transient execution literals follow the real session SQL mode.
// Passwords never appear in outcomes.
func (m *MySQLSessionManager) ChangeAccounts(ctx context.Context, id int64, generation uint64, changes []AccountChange) DDLOutcome {
	out := DDLOutcome{State: "not_executed"}
	statements := make([]string, len(changes))
	args := make([][]any, len(changes))
	for i, ch := range changes {
		stmt, a, e := accountChangeSQL(ch)
		if e != nil {
			out.Error = e
			return out
		}
		statements[i], args[i] = stmt, a
		out.Steps = append(out.Steps, DDLStep{SQL: stmt, State: "not_executed"})
	}
	s, qctx, cancel, e := m.requestContext(ctx, id, 0)
	if e != nil {
		out.Error = e
		return out
	}
	defer cancel()
	if m.SessionGeneration(id) != generation {
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
	for i, stmt := range statements {
		if qctx.Err() != nil || m.SessionGeneration(id) != generation {
			out.Error = errors.Join(qctx.Err(), ErrMySQLNotConnected)
			if i > 0 {
				out.State = "partial"
			}
			return out
		}
		executionSQL := stmt
		executionArgs := args[i]
		if len(executionArgs) > 0 {
			password, ok := executionArgs[0].(string)
			if !ok || len(executionArgs) != 1 || len(password) > 64<<10 || !utf8.ValidString(password) || strings.ContainsRune(password, 0) {
				out.Error = errors.New("invalid account password")
				return out
			}
			var mode string
			if e = c.QueryRowContext(qctx, "SELECT @@SESSION.sql_mode").Scan(&mode); e != nil {
				out.Error = e
				return out
			}
			literal, e := mysqlAccountPasswordLiteral(password, mode)
			if e != nil {
				out.Error = e
				return out
			}
			executionSQL = strings.TrimSuffix(stmt, "?") + literal
			executionArgs = nil
		}
		_, e = c.ExecContext(qctx, executionSQL, executionArgs...)
		if e != nil {
			out.Error = fmt.Errorf("account step %d failed", i+1)
			var serverCode *mysqldriver.MySQLError
			if errors.As(e, &serverCode) {
				out.Error = fmt.Errorf("account step %d rejected by server (code %d)", i+1, serverCode.Number)
			}
			out.Steps[i].Error = out.Error
			var server *mysqldriver.MySQLError
			if errors.As(e, &server) {
				if i > 0 {
					out.State = "partial"
				}
			} else {
				out.Steps[i].State = "uncertain"
				out.State = "uncertain"
			}
			return out
		}
		out.Steps[i].State = "executed"
		out.State = "executed"
	}
	out.Verified = true
	return out
}

func mysqlAccountPasswordLiteral(password, mode string) (string, error) {
	if len(password) > 64<<10 || !utf8.ValidString(password) || strings.ContainsRune(password, 0) {
		return "", errors.New("invalid account password")
	}
	if !strings.Contains(strings.ToUpper(mode), "NO_BACKSLASH_ESCAPES") {
		password = strings.ReplaceAll(password, "\\", "\\\\")
	}
	password = strings.ReplaceAll(password, "'", "''")
	return "'" + password + "'", nil
}

type mysqlGenerationContextKey struct{}

// WithMySQLSessionGeneration binds asynchronous maintenance/admin reads to the
// original physical-session generation captured by their UI owner.
func WithMySQLSessionGeneration(ctx context.Context, generation uint64) context.Context {
	return context.WithValue(ctx, mysqlGenerationContextKey{}, generation)
}
func (m *MySQLSessionManager) expectedObjectGeneration(ctx context.Context, id int64) error {
	if generation, ok := ctx.Value(mysqlGenerationContextKey{}).(uint64); ok && generation != 0 && m.SessionGeneration(id) != generation {
		return ErrMySQLNotConnected
	}
	return nil
}
