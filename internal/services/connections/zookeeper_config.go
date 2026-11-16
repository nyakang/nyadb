package connections

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrZooKeeperBounds = errors.New("ZooKeeper resource limit exceeded")

type ZooKeeperConfig struct {
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

func (c ZooKeeperConfig) Clone() ZooKeeperConfig {
	if c.EndpointRoutes != nil {
		m := make(map[string]string, len(c.EndpointRoutes))
		for k, v := range c.EndpointRoutes {
			m[k] = v
		}
		c.EndpointRoutes = m
	}
	return c
}

func NormalizeZNodePath(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimRight(p, "/")
}

func ValidZNodePath(p string) bool {
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
	if !utf8.ValidString(s) || strings.ContainsAny(s, "/@?#\\") || strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
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

func NormalizeZooKeeperConfig(c ZooKeeperConfig) (ZooKeeperConfig, string, error) {
	c = c.Clone()
	c.ConnectString = strings.TrimPrefix(strings.TrimSpace(c.ConnectString), "zookeeper://")
	if len(c.ConnectString) > 8192 || len(c.Username) > 1024 || strings.ContainsAny(c.Username, "\x00\r\n") {
		return c, "", ErrInvalid
	}
	parts := strings.SplitN(c.ConnectString, "/", 2)
	servers := strings.Split(parts[0], ",")
	if len(servers) > 64 {
		return c, "", ErrZooKeeperBounds
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
		chroot = NormalizeZNodePath(parts[1])
		if !ValidZNodePath(chroot) {
			return c, "", ErrInvalid
		}
		if chroot == "/" {
			chroot = ""
		}
	}
	c.Namespace = NormalizeZNodePath(c.Namespace)
	if !ValidZNodePath(c.Namespace) {
		return c, "", ErrInvalid
	}
	if c.Namespace == "/" {
		c.Namespace = ""
	}
	root := NormalizeZNodePath(chroot + c.Namespace)
	if !ValidZNodePath(root) {
		return c, "", ErrInvalid
	}
	c.ConnectString = strings.Join(servers, ",") + chroot
	c.Prefix = NormalizeZNodePath(c.Prefix)
	if !ValidZNodePath(c.Prefix) {
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
		return c, "", ErrZooKeeperBounds
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
