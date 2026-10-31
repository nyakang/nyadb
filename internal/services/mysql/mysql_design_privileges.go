package mysql

import (
	"context"
	"database/sql"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

// An empty trigger list is insufficient evidence when metadata privileges hide
// triggers. Role grants that cannot be resolved remain conservatively unknown.
func canReadAllTriggers(ctx context.Context, conn *sql.Conn, database, table string) bool {
	rows, e := conn.QueryContext(ctx, "SHOW GRANTS FOR CURRENT_USER()")
	if e != nil {
		return false
	}
	defer rows.Close()
	allowed := false
	for rows.Next() {
		var grant string
		if rows.Scan(&grant) != nil {
			return false
		}
		if grantCoversTriggers(grant, database, table) {
			allowed = true
		}
	}
	return allowed && rows.Err() == nil
}
func grantCoversTriggers(grant, database, table string) bool {
	ts, e := sqltext.Tokens(grant)
	if e != nil {
		return false
	}
	trigger := false
	for i, t := range ts {
		if sqltext.Word(t, "TRIGGER") || sqltext.Word(t, "ALL") {
			trigger = true
		}
		if sqltext.Word(t, "ON") {
			if !trigger || i+3 >= len(ts) || ts[i+2].Text != "." {
				return false
			}
			db, obj := mysqlddl.Name(ts[i+1]), mysqlddl.Name(ts[i+3])
			return (db == "*" || db == database) && (obj == "*" || obj == table)
		}
		if sqltext.Word(t, "TO") {
			break
		}
	}
	return false
}
