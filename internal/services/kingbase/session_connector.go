package kingbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// NewSessionConnector checks the native identity of every physical connection,
// including later metadata opens and SQL leases. Rejection closes only this
// captured handle, before it can enter a pool or be published by the runtime.
func NewSessionConnector(c connections.KingbaseConfig, password string, dial func(context.Context, string, string) (net.Conn, error)) (driver.Connector, error) {
	c, err := connections.NormalizeKingbaseConfig(c)
	if err != nil {
		return nil, err
	}
	base, err := NewConnector(c, password, dial)
	if err != nil {
		return nil, err
	}
	return sessionConnector{base: base, timeout: time.Duration(c.Options.ConnectTimeoutSeconds) * time.Second}, nil
}

type sessionConnector struct {
	base    driver.Connector
	timeout time.Duration
}

func (c sessionConnector) Driver() driver.Driver { return c.base.Driver() }
func (c sessionConnector) Connect(ctx context.Context) (driver.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	query, ok := conn.(driver.QueryerContext)
	if !ok {
		conn.Close()
		return nil, errors.New("unexpected native Kingbase driver")
	}
	rows, err := query.QueryContext(ctx, "SELECT pg_catalog.version(),pg_catalog.current_setting('database_mode')", nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	values := make([]driver.Value, 2)
	err = rows.Next(values)
	if err == nil {
		version, vok := values[0].(string)
		if b, ok := values[0].([]byte); ok {
			version, vok = string(b), true
		}
		mode, mok := values[1].(string)
		if b, ok := values[1].([]byte); ok {
			mode, mok = string(b), true
		}
		if !vok || !mok {
			err = contract.ErrUnsupported
		} else {
			err = validateProduct(version, mode)
		}
		if next := rows.Next(values); next != io.EOF && err == nil {
			err = errors.New("unexpected Kingbase identity result")
		}
	}
	err = errors.Join(err, rows.Close(), ctx.Err())
	if err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func validateProduct(version, mode string) error {
	// Only this catalog/version family has real workbench acceptance. Different
	// major releases and compatibility modes require their own native evidence.
	if !strings.HasPrefix(version, "KingbaseES V008R006C009B0014 on ") || mode != "pg" {
		return fmt.Errorf("%w: verified KingbaseES V8R6 C9 B14 in PG mode required", contract.ErrUnsupported)
	}
	return nil
}
