// Package etcd implements bounded, one-use native v3 requests. It is a private
// transport foundation; registration and workbench contracts are separate work.
package etcd

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Config struct {
	Endpoint, Auth, Username                          string
	CAFile, ServerName, ClientCertFile, ClientKeyFile string
	ConnectTimeout, RequestTimeout                    time.Duration
	// ProbeKey must be readable by the configured identity. No full-range
	// permission is inferred from a successful single-key probe.
	ProbeKey []byte
}

func normalizeConfig(c Config, secret string) (Config, *tls.Config, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u == nil || len(c.Endpoint) > 4096 || !utf8.ValidString(c.Endpoint) || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" || strings.ContainsAny(c.Endpoint, "\r\n\t ") {
		return c, nil, ErrInvalid
	}
	if u.Port() != "" {
		port, err := strconv.ParseUint(u.Port(), 10, 16)
		if err != nil || port == 0 {
			return c, nil, ErrInvalid
		}
	}
	if c.Auth == "" {
		c.Auth = "none"
	}
	if c.Auth != "none" && c.Auth != "basic" || c.Auth == "none" && (c.Username != "" || secret != "") || c.Auth == "basic" && (c.Username == "" || secret == "" || len(c.Username) > 4096 || len(secret) > 1<<20 || !utf8.ValidString(c.Username) || !utf8.ValidString(secret)) || len(c.ServerName) > 255 || strings.ContainsAny(c.ServerName, "\r\n\t /\\") {
		return c, nil, ErrInvalid
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 10 * time.Second
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 30 * time.Second
	}
	if c.ConnectTimeout < time.Second || c.ConnectTimeout > 5*time.Minute || c.RequestTimeout < time.Second || c.RequestTimeout > 10*time.Minute || len(c.ProbeKey) > 4096 {
		return c, nil, ErrInvalid
	}
	if len(c.ProbeKey) == 0 {
		c.ProbeKey = []byte{0}
	} else {
		c.ProbeKey = append([]byte(nil), c.ProbeKey...)
	}
	if (c.ClientCertFile == "") != (c.ClientKeyFile == "") {
		return c, nil, ErrInvalid
	}
	if u.Scheme == "http" {
		if c.CAFile != "" || c.ServerName != "" || c.ClientCertFile != "" {
			return c, nil, ErrInvalid
		}
		return c, nil, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ServerName}
	if tc.ServerName == "" {
		tc.ServerName = u.Hostname()
	}
	if c.CAFile != "" {
		pem, err := readFile(c.CAFile)
		if err != nil {
			return c, nil, err
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return c, nil, ErrInvalid
		}
		tc.RootCAs = roots
	}
	if c.ClientCertFile != "" {
		cert, e1 := readFile(c.ClientCertFile)
		key, e2 := readFile(c.ClientKeyFile)
		pair, e3 := tls.X509KeyPair(cert, key)
		if e1 != nil || e2 != nil || e3 != nil {
			return c, nil, ErrInvalid
		}
		tc.Certificates = []tls.Certificate{pair}
	}
	return c, tc, nil
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalid
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return nil, ErrBounds
	}
	return b, nil
}
