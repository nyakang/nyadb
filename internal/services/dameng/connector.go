// Package dameng implements native DM8 product/session rules using the
// caller-installed DPI client. It does not select an Oracle driver or catalog.
package dameng

import (
	"database/sql/driver"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
	"github.com/nyakang/nyadb/internal/services/dameng/dpiworker"
)

func NewConnector(c connections.DamengConfig, password string) (driver.Connector, error) {
	c, err := connections.NormalizeDamengConfig(c)
	if err != nil {
		return nil, err
	}
	a := c.Options
	return dpiworker.NewConnector(dpi.Options{
		LibraryFile: a.LibraryFile, Host: c.Host, Port: c.Port, Username: c.Username,
		Schema: a.DefaultSchema, ConnectTimeoutSeconds: a.ConnectTimeoutSeconds,
		OperationTimeoutSeconds: a.OperationTimeoutSeconds,
		TLS:                     &dpi.TLSOptions{Mode: c.TLSMode, CAFile: a.CAFile, CertificateFile: a.CertificateFile, KeyFile: a.KeyFile, ServerName: a.ServerName},
	}, password)
}
