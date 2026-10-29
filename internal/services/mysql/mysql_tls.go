package mysql

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// verifiedMySQLTLS never weakens certificate-chain or hostname verification.
func verifiedMySQLTLS(host string, ssl connections.MySQLSSL) (*tls.Config, error) {
	name := strings.Trim(strings.TrimSpace(host), "[]")
	if name == "" {
		return nil, fmt.Errorf("%w: missing TLS hostname", connections.ErrInvalid)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: name}
	if ssl.CACertificate != "" {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		data, err := os.ReadFile(filepath.Clean(ssl.CACertificate))
		if err != nil {
			return nil, fmt.Errorf("read MySQL CA certificate: %w", err)
		}
		if !roots.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("%w: invalid MySQL CA PEM", connections.ErrInvalid)
		}
		cfg.RootCAs = roots
	}
	if (ssl.ClientCertificate == "") != (ssl.ClientKey == "") {
		return nil, fmt.Errorf("%w: both TLS client certificate and key are required", connections.ErrInvalid)
	}
	if ssl.ClientCertificate != "" {
		cert, err := tls.LoadX509KeyPair(filepath.Clean(ssl.ClientCertificate), filepath.Clean(ssl.ClientKey))
		if err != nil {
			return nil, fmt.Errorf("load TLS client key pair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}
