// Package kingbase provides native KingbaseES transport through the gokb driver.
// Transport acceptance alone does not register workbench capabilities.
package kingbase

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

	"gitea.com/kingbase/gokb"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// NewConnector captures explicit settings and a temporary password without
// reading KB environment variables, .kbpass or home-directory certificates.
func NewConnector(c connections.KingbaseConfig, password string, dial func(context.Context, string, string) (net.Conn, error)) (driver.Connector, error) {
	c, err := connections.NormalizeKingbaseConfig(c)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := nativeTLS(c)
	if err != nil {
		return nil, err
	}
	runtime := map[string]string{"application_name": c.Options.ApplicationName, "statement_timeout": strconv.Itoa(c.Options.StatementTimeoutMS)}
	if c.Options.DefaultSchema != "" {
		runtime["search_path"] = `"` + strings.ReplaceAll(c.Options.DefaultSchema, `"`, `""`) + `"`
	}
	return gokb.NewConnectorConfig(gokb.Config{Host: c.Host, Port: c.Port, User: c.Username, Database: c.Database, Password: password, ConnectTimeout: time.Duration(c.Options.ConnectTimeoutSeconds) * time.Second, RuntimeParams: runtime, TLSConfig: tlsConfig, DialFunc: dial})
}

// OpenDB creates a bounded operation pool. SQL document leases require their
// own original physical connection and explicit recovery in a future adapter.
func OpenDB(c connections.KingbaseConfig, password string, dial func(context.Context, string, string) (net.Conn, error)) (*sql.DB, error) {
	c, err := connections.NormalizeKingbaseConfig(c)
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

func nativeTLS(c connections.KingbaseConfig) (*tls.Config, error) {
	if c.TLSMode == "disable" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.Host}
	if c.Options.CAFile != "" {
		pem, err := os.ReadFile(c.Options.CAFile)
		if err != nil {
			return nil, errors.New("cannot read Kingbase CA certificate")
		}
		cfg.RootCAs = x509.NewCertPool()
		if !cfg.RootCAs.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid Kingbase CA certificate")
		}
	}
	if c.Options.CertificateFile != "" {
		cert, err := tls.LoadX509KeyPair(c.Options.CertificateFile, c.Options.KeyFile)
		if err != nil {
			return nil, errors.New("invalid Kingbase client certificate or key")
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if c.TLSMode == "require" {
		cfg.InsecureSkipVerify = true
	}
	if c.TLSMode == "verify-ca" || c.TLSMode == "require" && cfg.RootCAs != nil {
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("Kingbase server certificate is missing")
			}
			intermediates := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}
			_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: cfg.RootCAs, Intermediates: intermediates})
			return err
		}
	}
	return cfg, nil
}
