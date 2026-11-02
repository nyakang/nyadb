package engines

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/nyakang/nyadb/internal/services/connections"
	sqlitedriver "modernc.org/sqlite"
)

type sqliteConnector struct {
	driver  *sqlitedriver.Driver
	dsn     string
	options connections.SQLiteOptions
}

func (c *sqliteConnector) Driver() driver.Driver { return c.driver }
func (c *sqliteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	exec, ok := conn.(driver.ExecerContext)
	if !ok {
		conn.Close()
		return nil, errors.New("SQLite connector does not support initialization")
	}
	for _, a := range c.options.Attachments {
		file, err := sqliteExistingFile(a.File)
		if err != nil {
			conn.Close()
			return nil, err
		}
		uri := sqliteAttachmentURI(file, a.ReadOnly || c.options.ReadOnly)
		if _, err = exec.ExecContext(ctx, "ATTACH DATABASE ? AS "+quote(a.Name), []driver.NamedValue{{Ordinal: 1, Value: uri}}); err != nil {
			conn.Close()
			return nil, fmt.Errorf("initialize SQLite attached database %s: %w", a.Name, err)
		}
	}
	if err := ctx.Err(); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}
func sqliteExistingFile(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("SQLite database file is unavailable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("SQLite database must be a regular file")
	}
	return abs, nil
}
func sqliteAttachmentURI(path string, readOnly bool) string {
	u, _ := url.Parse(sqliteFileURI(filepath.ToSlash(path)))
	q := u.Query()
	q.Del("_pragma")
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	q.Set("mode", mode)
	u.RawQuery = q.Encode()
	return u.String()
}
func sqliteConfiguredURI(path string, a connections.SQLiteOptions) string {
	u, _ := url.Parse(sqliteFileURI(filepath.ToSlash(path)))
	q := u.Query()
	q.Del("_pragma")
	q.Add("_pragma", "busy_timeout("+strconv.Itoa(a.BusyTimeoutMS)+")")
	enabled := "0"
	if a.ForeignKeys {
		enabled = "1"
	}
	q.Add("_pragma", "foreign_keys("+enabled+")")
	if a.ReadOnly {
		q.Set("mode", "ro")
	} else if a.RequireExisting {
		q.Set("mode", "rw")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
