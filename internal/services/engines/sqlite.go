package engines

import (
	"context"
	"database/sql"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	_ "modernc.org/sqlite"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
)

type sqliteDialect struct{}

func (sqliteDialect) open(p connections.Profile, password string, dial func(context.Context, string, string) (net.Conn, error)) (*sql.DB, error) {
	var db *sql.DB
	abs, e := filepath.Abs(p.Database)
	if e != nil {
		return nil, e
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(1000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	var e2 error
	db, e2 = sql.Open("sqlite", u.String())
	if e2 != nil {
		return nil, e2
	}
	return db, nil
}
func (sqliteDialect) catalog(string) string { return "main" }
func (sqliteDialect) namespace(name string) error {
	if name != "main" {
		return errors.New("SQLite supports main schema only")
	}
	return nil
}
func (sqliteDialect) argument(i int) string { return "?" + strconv.Itoa(i) }
func (d sqliteDialect) setSchema(ctx context.Context, c *sql.Conn, schema string) error {
	return d.namespace(schema)
}
func (sqliteDialect) comparison(f contract.Field, left, right string) string {
	return "typeof(" + left + ") = typeof(" + right + ") AND " + left + " COLLATE BINARY IS " + right
}
