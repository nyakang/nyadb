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
	"time"
)

type postgresDialect struct{}

func (postgresDialect) open(p connections.Profile, password string, dial func(context.Context, string, string) (net.Conn, error)) (*sql.DB, error) {
	cfg, e := postgresConfig(p, password, dial)
	if e != nil {
		return nil, e
	}
	return stdlib.OpenDB(*cfg), nil
}
func postgresConfig(p connections.Profile, password string, dial func(context.Context, string, string) (net.Conn, error)) (*pgx.ConnConfig, error) {
	dsn, a, e := postgresConnectionURL(p, password)
	if e != nil {
		return nil, e
	}
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		return nil, errors.New("invalid PostgreSQL configuration")
	}
	cfg.Password = password
	cfg.ConnectTimeout = time.Duration(a.ConnectTimeoutSeconds) * time.Second
	cfg.RuntimeParams = map[string]string{"application_name": a.ApplicationName, "statement_timeout": strconv.Itoa(a.StatementTimeoutMS), "lock_timeout": strconv.Itoa(a.LockTimeoutMS)}
	if p.Kind == connections.CockroachDB {
		cfg.RuntimeParams["autocommit_before_ddl"] = "off"
	}
	if a.DefaultSchema != "" {
		cfg.RuntimeParams["search_path"] = quote(a.DefaultSchema)
	}
	if dial != nil {
		cfg.DialFunc = dial
	}
	return cfg, nil
}

func postgresConnectionURL(p connections.Profile, password string) (string, connections.PostgreSQLOptions, error) {
	a, e := connections.NormalizePostgreSQLOptions(p.PostgreSQLOptions)
	if e != nil {
		return "", a, e
	}
	if a.SSH.Enabled || a.HTTP.Enabled {
		return "", a, errors.New("PostgreSQL-family tunnels are not supported")
	}
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Path: "/" + p.Database, User: url.UserPassword(p.Username, password)}
	q := u.Query()
	mode := p.TLSMode
	if mode == "" {
		mode = "require"
		if p.Kind == connections.CockroachDB || p.Kind == connections.OpenGauss {
			mode = "verify-full"
		}
	}
	switch mode {
	case "disable", "prefer", "require", "verify-ca", "verify-full":
	default:
		return "", a, errors.New("unsupported PostgreSQL-family TLS mode")
	}
	q.Set("sslmode", mode)
	q.Set("connect_timeout", strconv.Itoa(a.ConnectTimeoutSeconds))
	// Override libpq environment certificate/password defaults with this profile.
	q.Set("sslrootcert", a.CAFile)
	q.Set("sslcert", a.CertificateFile)
	q.Set("sslkey", a.KeyFile)
	q.Set("sslpassword", "")
	q.Set("passfile", "")
	q.Set("sslnegotiation", "postgres")
	u.RawQuery = q.Encode()
	return u.String(), a, nil
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
