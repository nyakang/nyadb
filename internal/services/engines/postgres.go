package engines

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type postgresDialect struct{}

func (postgresDialect) open(p connections.Profile, password string, dial func(context.Context, string, string) (net.Conn, error)) (*sql.DB, error) {
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Path: "/" + p.Database, User: url.UserPassword(p.Username, password)}
	q := u.Query()
	mode := p.TLSMode
	if mode == "" {
		mode = "require"
	}
	switch mode {
	case "disable", "prefer", "require", "verify-ca", "verify-full":
	default:
		return nil, errors.New("unsupported PostgreSQL TLS mode")
	}
	q.Set("sslmode", mode)
	q.Set("connect_timeout", "10")
	u.RawQuery = q.Encode()
	cfg, e := pgx.ParseConfig(u.String())
	if e != nil {
		return nil, errors.New("invalid PostgreSQL configuration")
	}
	if dial != nil {
		cfg.DialFunc = dial
	}
	return stdlib.OpenDB(*cfg), nil
}
func (postgresDialect) catalog(name string) string { return name }
func (postgresDialect) namespace(string) error     { return nil }
func (postgresDialect) argument(i int) string      { return "$" + strconv.Itoa(i) }
func (postgresDialect) setSchema(ctx context.Context, c *sql.Conn, schema string) error {
	_, e := c.ExecContext(ctx, "SET search_path TO "+quote(schema))
	return e
}
func (postgresDialect) comparison(f contract.Field, left, right string) string {
	if strings.EqualFold(f.DatabaseType, "json") {
		left += "::text"
		right += "::text"
	}
	return left + " IS NOT DISTINCT FROM " + right
}
