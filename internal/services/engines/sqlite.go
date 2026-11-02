package engines

import (
	"context"
	"database/sql"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	sqlitedriver "modernc.org/sqlite"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type sqliteDialect struct{ options connections.SQLiteOptions }

func (sqliteDialect) open(p connections.Profile, password string, dial func(context.Context, string, string) (net.Conn, error)) (*sql.DB, error) {
	abs, err := filepath.Abs(p.Database)
	if err != nil {
		return nil, err
	}
	a, err := connections.NormalizeSQLiteOptions(p.SQLiteOptions)
	if err != nil {
		return nil, err
	}
	if a.RequireExisting || a.ReadOnly {
		if _, err = sqliteExistingFile(abs); err != nil {
			return nil, err
		}
	}
	for _, attachment := range a.Attachments {
		if filepath.Clean(attachment.File) == filepath.Clean(abs) {
			return nil, errors.New("SQLite main file cannot also be attached")
		}
		if _, err = sqliteExistingFile(attachment.File); err != nil {
			return nil, err
		}
	}
	// Different paths can refer to the same file through symlinks/hard links.
	// Opening it under two schemas would make scope and transaction ownership
	// ambiguous. Compare physical identities before any driver opens a file.
	files := []string{abs}
	for _, attachment := range a.Attachments {
		files = append(files, attachment.File)
	}
	for i, file := range files {
		info, e := os.Stat(file)
		if e != nil {
			continue
		} // rw/create legacy main need not exist yet
		for _, other := range files[:i] {
			otherInfo, e := os.Stat(other)
			if e == nil && os.SameFile(info, otherInfo) {
				return nil, errors.New("SQLite attachment aliases another configured database file")
			}
		}
	}
	return sql.OpenDB(&sqliteConnector{driver: &sqlitedriver.Driver{}, dsn: sqliteConfiguredURI(abs, a), options: a}), nil
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
func (d sqliteDialect) namespace(name string) error {
	if name == "main" {
		return nil
	}
	for _, a := range d.options.Attachments {
		if a.Name == name {
			return nil
		}
	}
	return errors.New("SQLite namespace is not configured in this session")
}
func (sqliteDialect) argument(i int) string { return "?" + strconv.Itoa(i) }
func (d sqliteDialect) setSchema(ctx context.Context, c *sql.Conn, schema string) error {
	return d.namespace(schema)
}
func (sqliteDialect) comparison(f contract.Field, left, right string) string {
	return "typeof(" + left + ") = typeof(" + right + ") AND " + left + " COLLATE BINARY IS " + right
}
