// Package consul implements original Consul HTTP requests without retry or replay.
package consul

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Config struct {
	Endpoint, Auth                                        string
	Datacenter, Namespace, Partition, Prefix, Consistency string
	CAFile, ServerName, ClientCertFile, ClientKeyFile     string
	ConnectTimeout, RequestTimeout                        time.Duration
}

func validText(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}
func normalizeConfig(c Config, token string) (Config, *tls.Config, error) {
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.HasSuffix(u.Host, ":") || !validText(c.Endpoint, 4096) || strings.ContainsAny(c.Endpoint, " \t\\") || u.RawPath != "" {
		return c, nil, ErrInvalid
	}
	if u.Port() != "" {
		n, e := strconv.ParseUint(u.Port(), 10, 16)
		if e != nil || n == 0 {
			return c, nil, ErrInvalid
		}
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return c, nil, ErrInvalid
		}
	}
	c.Endpoint = strings.TrimSuffix(u.String(), "/")
	if c.Auth == "" {
		c.Auth = "none"
	}
	if c.Auth != "none" && c.Auth != "token" || c.Auth == "none" && token != "" || c.Auth == "token" && (token == "" || !validText(token, 4096) || strings.Contains(token, " ")) {
		return c, nil, ErrInvalid
	}
	for _, s := range []string{c.Datacenter, c.Namespace, c.Partition} {
		if !validText(s, 255) || strings.ContainsAny(s, "*/?\\ ") {
			return c, nil, ErrInvalid
		}
	}
	if c.Namespace != "" || c.Partition != "" {
		return c, nil, ErrUnsupported
	}
	if !validKey(c.Prefix, true) {
		return c, nil, ErrInvalid
	}
	if c.Consistency == "" {
		c.Consistency = "default"
	}
	if c.Consistency != "default" && c.Consistency != "consistent" && c.Consistency != "stale" {
		return c, nil, ErrInvalid
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 10 * time.Second
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 30 * time.Second
	}
	if c.ConnectTimeout < time.Second || c.ConnectTimeout > 5*time.Minute || c.RequestTimeout < time.Second || c.RequestTimeout > 10*time.Minute {
		return c, nil, ErrInvalid
	}
	if !validText(c.ServerName, 255) || strings.ContainsAny(c.ServerName, " /\\") || (c.ClientCertFile == "") != (c.ClientKeyFile == "") {
		return c, nil, ErrInvalid
	}
	for _, s := range []string{c.CAFile, c.ClientCertFile, c.ClientKeyFile} {
		if !validText(s, 4096) {
			return c, nil, ErrInvalid
		}
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
		raw, e := readFile(c.CAFile)
		pool, e2 := x509.SystemCertPool()
		if e2 != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if e != nil || !pool.AppendCertsFromPEM(raw) {
			return c, nil, errors.New("invalid Consul CA")
		}
		tc.RootCAs = pool
	}
	if c.ClientCertFile != "" {
		cert, e1 := readFile(c.ClientCertFile)
		key, e2 := readFile(c.ClientKeyFile)
		defer clear(key)
		pair, e3 := tls.X509KeyPair(cert, key)
		if e1 != nil || e2 != nil || e3 != nil {
			return c, nil, errors.New("invalid Consul client certificate")
		}
		tc.Certificates = []tls.Certificate{pair}
	}
	return c, tc, nil
}
func readFile(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, ErrInvalid
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, ErrInvalid
	}
	raw, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return nil, ErrBounds
	}
	return raw, nil
}
