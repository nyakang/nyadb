package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"

	gausspq "gitcode.com/opengauss/openGauss-connector-go-pq"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// openGauss uses its native SHA256/SM3 connector. Quoting, placeholders and
// profile TLS settings are shared with PostgreSQL; catalogs are product specific.
type openGaussDialect struct{ postgresDialect }

func (openGaussDialect) schemas(ctx context.Context, q queryer) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT nspname FROM pg_catalog.pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname NOT LIKE 'dbe_%'
 AND nspname NOT IN ('information_schema','blockchain','coverage','cstore','db4ai','pkg_service','snapshot','sqladvisor')
 AND has_schema_privilege(oid,'USAGE') ORDER BY nspname LIMIT 50001`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			return nil, err
		}
		if len(names) >= 50000 {
			return nil, errors.New("schema metadata limit exceeded")
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

func openGaussConfig(p connections.Profile, password string, dial func(context.Context, string, string) (net.Conn, error)) (*gausspq.Config, error) {
	pg, err := postgresConfig(p, password, dial)
	if err != nil {
		return nil, err
	}
	dsn, _, err := postgresConnectionURL(p, password)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, errors.New("invalid openGauss configuration")
	}
	query := u.Query()
	// The native connector must not discover certificates from the user's home.
	// Adopt the explicitly normalized TLS settings after parsing its core config.
	query.Set("sslmode", "disable")
	u.RawQuery = query.Encode()
	cfg, err := gausspq.ParseConfig(u.String())
	if err != nil {
		return nil, errors.New("invalid openGauss configuration")
	}
	cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.Password = pg.Host, pg.Port, pg.Database, pg.User, password
	cfg.DialFunc, cfg.LookupFunc, cfg.ConnectTimeout = gausspq.DialFunc(pg.DialFunc), gausspq.LookupFunc(pg.LookupFunc), pg.ConnectTimeout
	cfg.TLSConfig = pg.TLSConfig
	cfg.Fallbacks = nil
	for _, fallback := range pg.Fallbacks {
		cfg.Fallbacks = append(cfg.Fallbacks, &gausspq.FallbackConfig{Host: fallback.Host, Port: fallback.Port, TLSConfig: fallback.TLSConfig})
	}
	cfg.RuntimeParams = pg.RuntimeParams
	delete(cfg.RuntimeParams, "lock_timeout")
	// Native lockwait_timeout requires administrator privileges; do not inject
	// a PostgreSQL lock_timeout or change the server policy on ordinary sessions.
	if p.PostgreSQLOptions.LockTimeoutMS != 0 {
		return nil, fmt.Errorf("%w: openGauss per-connection lock timeout is unavailable", contract.ErrUnsupported)
	}
	cfg.Logger, cfg.LogLevel = nil, gausspq.LogLevelNone
	return cfg, nil
}

func (openGaussDialect) open(p connections.Profile, password string, dial func(context.Context, string, string) (net.Conn, error)) (*sql.DB, error) {
	cfg, err := openGaussConfig(p, password, dial)
	if err != nil {
		return nil, err
	}
	connector, err := gausspq.NewConnectorConfig(cfg)
	if err != nil {
		return nil, errors.New("invalid openGauss connector")
	}
	return sql.OpenDB(connector), nil
}

var openGaussVersionPattern = regexp.MustCompile(`\(openGauss ([0-9]+)\.([0-9]+)\.([0-9]+)(?: |\))`)

func validateOpenGaussServer(ctx context.Context, db *sql.DB) error {
	var version, compatibility string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		return err
	}
	m := openGaussVersionPattern.FindStringSubmatch(version)
	if m == nil {
		return fmt.Errorf("%w: openGauss connection requires an openGauss server", contract.ErrUnsupported)
	}
	major, err := strconv.Atoi(m[1])
	if err != nil || major < 6 {
		return fmt.Errorf("%w: openGauss 6.0 or later is required", contract.ErrUnsupported)
	}
	if err := db.QueryRowContext(ctx, "SHOW sql_compatibility").Scan(&compatibility); err != nil {
		return err
	}
	if compatibility != "PG" {
		return fmt.Errorf("%w: openGauss PG compatibility is required", contract.ErrUnsupported)
	}
	return nil
}
