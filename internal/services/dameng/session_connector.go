package dameng

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

const verifiedServerVersion = "08134283904-20220804-166351-20005"

// The product gate runs against every captured physical handle before it can
// enter a pool. Future metadata reads and SQL leases use the same connector.
func NewSessionConnector(c connections.DamengConfig, password string) (driver.Connector, error) {
	c, err := connections.NormalizeDamengConfig(c)
	if err != nil {
		return nil, err
	}
	initial := c
	initial.Options.DefaultSchema = ""
	base, err := NewConnector(initial, password)
	if err != nil {
		return nil, err
	}
	return sessionConnector{base: base, config: c}, nil
}

type sessionConnector struct {
	base   driver.Connector
	config connections.DamengConfig
}

func (c sessionConnector) Driver() driver.Driver { return c.base.Driver() }

func (c sessionConnector) Connect(ctx context.Context) (result driver.Conn, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.config.Options.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, conn.Close())
		}
	}()
	query, ok := conn.(driver.QueryerContext)
	if !ok {
		return nil, errors.New("DM8 native physical connection cannot inspect product identity")
	}
	initial := c.config
	initial.Options.DefaultSchema = ""
	if err = inspectProduct(ctx, query, initial); err != nil {
		return nil, err
	}
	if schema := c.config.Options.DefaultSchema; schema != "" && schema != c.config.Username {
		if err = dpi.InitializeSchema(ctx, conn, schema); err != nil {
			return nil, err
		}
		if err = inspectProduct(ctx, query, c.config); err != nil {
			return nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	complete = true
	return conn, nil
}

// SF_* and CURRENT_USER/CURRENT_SCHID are native built-ins, not stored
// functions in SYS. Qualify the authoritative system views and omit DUAL,
// which can also be a caller-owned object, from the identity expressions.
const identitySQL = `SELECT (SELECT NAME FROM SYS.V$DATABASE),CURRENT_USER,SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID),SF_GET_UNICODE_FLAG(),SF_GET_CASE_SENSITIVE_FLAG(),SF_GET_PARA_VALUE(2,'COMPATIBLE_MODE'),SF_GET_PARA_VALUE(2,'LENGTH_IN_CHAR'),SF_GET_PARA_VALUE(2,'ENABLE_ENCRYPT')`

const versionSQL = "SELECT BANNER FROM SYS.V$VERSION"

func nativeText(value driver.Value) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, utf8.ValidString(v)
	case []byte:
		return string(v), utf8.Valid(v)
	}
	return "", false
}

func inspectProduct(ctx context.Context, query driver.QueryerContext, config connections.DamengConfig) error {
	rows, err := query.QueryContext(ctx, versionSQL, nil)
	if err != nil {
		return err
	}
	var versionErr error
	if len(rows.Columns()) != 1 {
		versionErr = contract.ErrUnsupported
	}
	seen := make(map[string]bool, 3)
	values := make([]driver.Value, 1)
	for versionErr == nil {
		err = rows.Next(values)
		if err == io.EOF {
			break
		}
		if err != nil {
			versionErr = err
			break
		}
		banner, ok := nativeText(values[0])
		banner = strings.TrimSpace(banner)
		if !ok || seen[banner] || len(seen) >= 3 {
			versionErr = contract.ErrUnsupported
			break
		}
		seen[banner] = true
	}
	versionErr = errors.Join(versionErr, rows.Close(), ctx.Err())
	if versionErr != nil {
		return versionErr
	}
	if len(seen) != 3 || !seen["DM Database Server 64 V8"] || !seen["DB Version: 0x7000c"] || !seen[verifiedServerVersion] {
		return fmt.Errorf("%w: an independently verified native DM8 server version is required", contract.ErrUnsupported)
	}
	rows, err = query.QueryContext(ctx, identitySQL, nil)
	if err != nil {
		return err
	}
	if len(rows.Columns()) != 8 {
		return errors.Join(contract.ErrUnsupported, rows.Close())
	}
	values = make([]driver.Value, 8)
	err = rows.Next(values)
	if err == nil {
		catalog, cok := nativeText(values[0])
		user, uok := nativeText(values[1])
		schema, sok := nativeText(values[2])
		expectedSchema := config.Options.DefaultSchema
		if expectedSchema == "" {
			expectedSchema = user
		}
		if !cok || !uok || !sok || catalog != config.Database || user != config.Username || schema == "" || schema != expectedSchema {
			err = fmt.Errorf("%w: original DM8 catalog, authenticated user or schema differs from the explicit configuration", contract.ErrUnsupported)
		}
		for i, expected := range []int64{1, 1, 0, 1} {
			if actual, ok := values[i+3].(int64); !ok || actual != expected {
				err = fmt.Errorf("%w: native DM mode, UTF-8, case sensitivity and character-length settings are required", contract.ErrUnsupported)
			}
		}
		encryption := int64(1)
		if config.TLSMode == "disable" {
			encryption = 0
		}
		if actual, ok := values[7].(int64); !ok || actual != encryption {
			err = fmt.Errorf("%w: DM8 native encryption mode differs from the explicit TLS policy", contract.ErrUnsupported)
		}
		if next := rows.Next(values); next != io.EOF && err == nil {
			if next != nil {
				err = next
			} else {
				err = contract.ErrUnsupported
			}
		}
	}
	return errors.Join(err, rows.Close(), ctx.Err())
}
