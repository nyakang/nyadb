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
	"strings"
)

type sqliteDialect struct{}

func (sqliteDialect) open(p connections.Profile, password string, dial func(context.Context, string, string) (net.Conn, error)) (*sql.DB, error) {
	abs, err := filepath.Abs(p.Database)
	if err != nil {
		return nil, err
	}
	return sql.Open("sqlite", sqliteFileURI(filepath.ToSlash(abs)))
}

// sqliteFileURI receives a slash-normalized absolute path. Windows drive
// letters belong in the path, never in the URI authority.
func sqliteFileURI(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(1000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	return u.String()
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
