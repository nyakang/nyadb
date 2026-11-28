package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"sync"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// Every Connect captures its own physical channel, including concurrent opens
// from the separate metadata and SQL pools.
type mysqlTransportConnector struct {
	driver.Connector
	config *mysqldriver.Config
}

func (c *mysqlTransportConnector) Connect(ctx context.Context) (driver.Conn, error) {
	cfg := c.config.Clone()
	dial := cfg.DialFunc
	var physical net.Conn
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		physical = conn
		return conn, err
	}
	connector, err := mysqldriver.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	conn, err := connector.Connect(ctx)
	if err != nil {
		if physical != nil {
			_ = physical.Close()
		}
		return nil, err
	}
	native, ok := conn.(mysqlTransportDriverConnection)
	if !ok || physical == nil {
		if physical != nil {
			_ = physical.Close()
		}
		_ = conn.Close()
		return nil, errors.New("MySQL transport connection lacks required driver interfaces")
	}
	return &mysqlTransportConnection{mysqlTransportDriverConnection: native, physical: physical}, nil
}

// Preserve the pinned driver's optional interfaces so context cancellation,
// prepared statements, transactions, uint64 values and pool validation retain
// their native behavior through the transport owner.
type mysqlTransportDriverConnection interface {
	driver.Conn
	driver.ConnPrepareContext
	driver.ConnBeginTx
	driver.Execer
	driver.ExecerContext
	driver.Queryer
	driver.QueryerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
	driver.NamedValueChecker
}

type mysqlTransportConnection struct {
	mysqlTransportDriverConnection
	physical  net.Conn
	closeOnce sync.Once
	closeErr  error
}

func (c *mysqlTransportConnection) Close() error {
	c.closeOnce.Do(func() {
		// Retire this channel before the driver sends COM_QUIT. Otherwise the
		// server EOF can revoke the shared tunnel before net.Conn.Close runs.
		c.closeErr = c.physical.Close()
		_ = c.mysqlTransportDriverConnection.Close()
	})
	return c.closeErr
}
