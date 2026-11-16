// Package zookeeper adapts the original managed native agent without SQL or
// public chooser/provider registration. Each document owns its original process.
package zookeeper

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalid    = errors.New("invalid native ZooKeeper configuration or request")
	ErrBounds     = errors.New("native ZooKeeper resource limit exceeded")
	ErrProjection = errors.New("native ZooKeeper receipt projection is incomplete")
)

const nativeProtocol = "zookeeper-jsonrpc-v1"
const frameLimit = 32 << 20
const requestLimit = 2 << 20
const valueLimit = 1 << 20

type Config struct {
	ConnectString       string            `json:"zookeeper_connect_string"`
	Namespace           string            `json:"namespace,omitempty"`
	Prefix              string            `json:"-"` // initial logical browser suggestion; never prepended to requests
	Username            string            `json:"username,omitempty"`
	AuthScheme          string            `json:"auth_scheme,omitempty"`
	SessionTimeoutMS    int               `json:"session_timeout_ms"`
	ConnectionTimeoutMS int               `json:"connection_timeout_ms"`
	MaxBufferSize       int               `json:"max_buffer_size"`
	SSL                 bool              `json:"ssl,omitempty"`
	CACertPath          string            `json:"ca_cert_path,omitempty"`
	ClientCertPath      string            `json:"client_cert_path,omitempty"`
	ClientKeyPath       string            `json:"client_key_path,omitempty"`
	EndpointRoutes      map[string]string `json:"zookeeper_endpoint_routes,omitempty"`
}

func (c Config) Clone() Config {
	if c.EndpointRoutes != nil {
		m := make(map[string]string, len(c.EndpointRoutes))
		for k, v := range c.EndpointRoutes {
			m[k] = v
		}
		c.EndpointRoutes = m
	}
	return c
}

func normalizePath(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimRight(p, "/")
}

func validPath(p string) bool {
	if p == "/" {
		return true
	}
	if len(p) == 0 || len(p) > 4096 || !utf8.ValidString(p) || !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, part := range strings.Split(p[1:], "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, r := range p {
		if r == 0 || r >= 1 && r <= 31 || r >= 127 && r <= 159 || r >= 0xd800 && r <= 0xf8ff || r >= 0xfff0 && r <= 0xffff {
			return false
		}
	}
	return true
}

func endpoint(s string) (string, error) {
	if strings.ContainsAny(s, "/@?#\x00\r\n\t ") {
		return "", ErrInvalid
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		if strings.Contains(s, ":") {
			return "", ErrInvalid
		}
		host, port = s, "2181"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || host == "" {
		return "", ErrInvalid
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}

func normalizeConfig(c Config) (Config, string, error) {
	c = c.Clone()
	c.ConnectString = strings.TrimPrefix(strings.TrimSpace(c.ConnectString), "zookeeper://")
	if len(c.ConnectString) > 8192 || len(c.Username) > 1024 || strings.ContainsAny(c.Username, "\x00\r\n") {
		return c, "", ErrInvalid
	}
	parts := strings.SplitN(c.ConnectString, "/", 2)
	servers := strings.Split(parts[0], ",")
	if len(servers) > 64 {
		return c, "", ErrBounds
	}
	seen := map[string]bool{}
	for i, s := range servers {
		a, err := endpoint(strings.TrimSpace(s))
		if err != nil || seen[a] {
			return c, "", ErrInvalid
		}
		servers[i] = a
		seen[a] = true
	}
	chroot := ""
	if len(parts) == 2 {
		chroot = normalizePath(parts[1])
		if !validPath(chroot) {
			return c, "", ErrInvalid
		}
		if chroot == "/" {
			chroot = ""
		}
	}
	c.Namespace = normalizePath(c.Namespace)
	if !validPath(c.Namespace) {
		return c, "", ErrInvalid
	}
	if c.Namespace == "/" {
		c.Namespace = ""
	}
	root := normalizePath(chroot + c.Namespace)
	if !validPath(root) {
		return c, "", ErrInvalid
	}
	c.ConnectString = strings.Join(servers, ",") + chroot
	c.Prefix = normalizePath(c.Prefix)
	if !validPath(c.Prefix) {
		return c, "", ErrInvalid
	}
	c.Username = strings.TrimSpace(c.Username)
	c.AuthScheme = strings.ToLower(strings.TrimSpace(c.AuthScheme))
	if c.AuthScheme == "" {
		c.AuthScheme = "digest"
	}
	if c.AuthScheme != "digest" && c.AuthScheme != "sasl_digest" {
		return c, "", ErrInvalid
	}
	if c.SessionTimeoutMS == 0 {
		c.SessionTimeoutMS = 30000
	}
	if c.ConnectionTimeoutMS == 0 {
		c.ConnectionTimeoutMS = 15000
	}
	if c.MaxBufferSize == 0 {
		c.MaxBufferSize = 32 << 20
	}
	if c.SessionTimeoutMS < 1000 || c.SessionTimeoutMS > 3600000 || c.ConnectionTimeoutMS < 250 || c.ConnectionTimeoutMS > 300000 || c.MaxBufferSize < 1024 || c.MaxBufferSize > 256<<20 {
		return c, "", ErrBounds
	}
	if c.EndpointRoutes != nil {
		if len(c.EndpointRoutes) != len(servers) {
			return c, "", ErrInvalid
		}
		routes := make(map[string]string, len(servers))
		for logical, to := range c.EndpointRoutes {
			a, err := endpoint(logical)
			h, p, e := net.SplitHostPort(to)
			n, ne := strconv.Atoi(p)
			if err != nil || !seen[a] || e != nil || ne != nil || n < 1 || n > 65535 || !net.ParseIP(h).IsLoopback() || routes[a] != "" {
				return c, "", ErrInvalid
			}
			routes[a] = net.JoinHostPort(h, strconv.Itoa(n))
		}
		c.EndpointRoutes = routes
	}
	if (c.ClientCertPath == "") != (c.ClientKeyPath == "") {
		return c, "", ErrInvalid
	}
	if c.CACertPath != "" || c.ClientCertPath != "" {
		c.SSL = true
	}
	return c, root, nil
}

type capturedConfig struct {
	config                       Config
	root, fingerprint, directory string
}

func captureConfig(c Config) (capturedConfig, error) {
	c, root, err := normalizeConfig(c)
	if err != nil {
		return capturedConfig{}, err
	}
	original := c.Clone()
	var materials [3][]byte
	for i, path := range []string{c.CACertPath, c.ClientCertPath, c.ClientKeyPath} {
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return capturedConfig{}, ErrInvalid
		}
		stat, err := f.Stat()
		if err != nil || !stat.Mode().IsRegular() || stat.Size() > 1<<20 {
			f.Close()
			return capturedConfig{}, ErrBounds
		}
		materials[i] = make([]byte, stat.Size())
		_, err = io.ReadFull(f, materials[i])
		f.Close()
		if err != nil {
			return capturedConfig{}, ErrInvalid
		}
	}
	if materials[0] != nil && !x509.NewCertPool().AppendCertsFromPEM(materials[0]) {
		return capturedConfig{}, ErrInvalid
	}
	if materials[1] != nil {
		if _, err := tls.X509KeyPair(materials[1], materials[2]); err != nil {
			return capturedConfig{}, ErrInvalid
		}
	}
	var hashes [3]string
	for i, b := range materials {
		if b != nil {
			h := sha256.Sum256(b)
			hashes[i] = hex.EncodeToString(h[:])
		}
	}
	original.CACertPath = ""
	original.ClientCertPath = ""
	original.ClientKeyPath = ""
	raw, _ := json.Marshal(struct {
		Config Config
		Prefix string
		Trust  [3]string
	}{original, c.Prefix, hashes})
	h := sha256.Sum256(raw)
	result := capturedConfig{config: c, root: root, fingerprint: hex.EncodeToString(h[:])}
	if materials[0] != nil || materials[1] != nil {
		result.directory, err = os.MkdirTemp("", "nyadb-zookeeper-trust-")
		if err != nil {
			return capturedConfig{}, ErrInvalid
		}
		for i, b := range materials {
			if b == nil {
				continue
			}
			path := filepath.Join(result.directory, strconv.Itoa(i)+".pem")
			if err = os.WriteFile(path, b, 0600); err != nil {
				os.RemoveAll(result.directory)
				return capturedConfig{}, ErrInvalid
			}
			switch i {
			case 0:
				result.config.CACertPath = path
			case 1:
				result.config.ClientCertPath = path
			case 2:
				result.config.ClientKeyPath = path
			}
		}
	}
	return result, nil
}
