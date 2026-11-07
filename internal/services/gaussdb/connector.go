// Package gaussdb provides native GaussDB transport through Huawei's Go driver.
// Transport tests do not establish product or workbench capability support.
package gaussdb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	gaussdbgo "github.com/HuaweiCloudDeveloper/gaussdb-go"
	"github.com/HuaweiCloudDeveloper/gaussdb-go/gaussdbconn"
	"github.com/HuaweiCloudDeveloper/gaussdb-go/stdlib"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// NewConnector captures public settings and a temporary password. It performs
// no network I/O and does not use libpq environment credentials or certificates.
func NewConnector(c connections.GaussDBConfig, password string, dial func(context.Context, string, string) (net.Conn, error)) (driver.Connector, error) {
	cfg, err := nativeConfig(c, password, dial)
	if err != nil {
		return nil, err
	}
	return stdlib.GetConnector(*cfg), nil
}

// OpenDB configures the operation pool. SQL document leases need a separate
// pool and explicit original-session ownership in the runtime adapter.
func OpenDB(c connections.GaussDBConfig, password string, dial func(context.Context, string, string) (net.Conn, error)) (*sql.DB, error) {
	c, err := connections.NormalizeGaussDBConfig(c)
	if err != nil {
		return nil, err
	}
	connector, err := NewConnector(c, password, dial)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(c.Options.MaxConnections)
	db.SetMaxIdleConns(min(2, c.Options.MaxConnections))
	return db, nil
}

func nativeConfig(c connections.GaussDBConfig, password string, dial func(context.Context, string, string) (net.Conn, error)) (*gaussdbgo.ConnConfig, error) {
	c, err := connections.NormalizeGaussDBConfig(c)
	if err != nil {
		return nil, err
	}
	// The driver requires ParseConfig's private initialization. This constant
	// seed contains no actual endpoint or secret; profile values replace it.
	// Never return the driver's DSN-bearing parse errors to the caller.
	cfg, err := gaussdbgo.ParseConfig("host=localhost port=5432 database=postgres user=gaussdb password='' passfile='' sslmode=disable sslcert='' sslkey='' sslrootcert='' sslpassword='' target_session_attrs=" + c.TargetSessionAttrs)
	if err != nil {
		return nil, errors.New("invalid native GaussDB configuration")
	}
	cfg.User, cfg.Database, cfg.Password = c.Username, c.Database, password
	cfg.Tracer = nil
	cfg.KerberosSrvName, cfg.KerberosSpn = "", ""
	cfg.StatementCacheCapacity, cfg.DescriptionCacheCapacity = 0, 0
	cfg.DefaultQueryExecMode = gaussdbgo.QueryExecModeExec
	cfg.ConnectTimeout = time.Duration(c.Options.ConnectTimeoutSeconds) * time.Second
	cfg.DialFunc = (&net.Dialer{Timeout: cfg.ConnectTimeout}).DialContext
	if dial != nil {
		cfg.DialFunc = dial
	}
	cfg.LookupFunc = net.DefaultResolver.LookupHost
	cfg.RuntimeParams = map[string]string{"application_name": c.Options.ApplicationName, "statement_timeout": strconv.Itoa(c.Options.StatementTimeoutMS)}
	if c.Options.DefaultSchema != "" {
		cfg.RuntimeParams["search_path"] = `"` + strings.ReplaceAll(c.Options.DefaultSchema, `"`, `""`) + `"`
	}
	// Native cancellation can finish cleanup asynchronously. Preserve the
	// default deadline handler; a runtime lease must wait for CleanupDone and
	// retire this physical session before publishing cancellation completion.
	templates, err := tlsTemplates(c)
	if err != nil {
		return nil, err
	}
	cfg.Fallbacks = nil
	for i, endpoint := range c.Endpoints {
		for j, template := range templates {
			var tlsConfig *tls.Config
			if template != nil {
				tlsConfig = template.Clone()
				tlsConfig.ServerName = endpoint.Host
			}
			if i == 0 && j == 0 {
				cfg.Host, cfg.Port, cfg.TLSConfig = endpoint.Host, endpoint.Port, tlsConfig
			} else {
				cfg.Fallbacks = append(cfg.Fallbacks, &gaussdbconn.FallbackConfig{Host: endpoint.Host, Port: endpoint.Port, TLSConfig: tlsConfig})
			}
		}
	}
	return cfg, nil
}

func tlsTemplates(c connections.GaussDBConfig) ([]*tls.Config, error) {
	if c.TLSMode == "disable" {
		return []*tls.Config{nil}, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.Options.CAFile != "" {
		pem, err := os.ReadFile(c.Options.CAFile)
		if err != nil {
			return nil, errors.New("cannot read GaussDB CA certificate")
		}
		cfg.RootCAs = x509.NewCertPool()
		if !cfg.RootCAs.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid GaussDB CA certificate")
		}
	}
	if c.Options.CertificateFile != "" {
		cert, err := tls.LoadX509KeyPair(c.Options.CertificateFile, c.Options.KeyFile)
		if err != nil {
			return nil, errors.New("invalid GaussDB client certificate or key")
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if c.TLSMode == "prefer" || c.TLSMode == "require" {
		cfg.InsecureSkipVerify = true
	}
	if c.TLSMode == "verify-ca" || c.TLSMode == "require" && cfg.RootCAs != nil {
		cfg.InsecureSkipVerify = true
		roots := cfg.RootCAs
		cfg.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("GaussDB server certificate is missing")
			}
			intermediates := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}
			_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates})
			return err
		}
	}
	if c.TLSMode == "prefer" {
		return []*tls.Config{cfg, nil}, nil
	}
	return []*tls.Config{cfg}, nil
}
