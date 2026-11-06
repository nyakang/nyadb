package connections

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// RedisOptions contains public standalone settings. Passwords are held by the vault.
type RedisOptions struct {
	Version               int    `json:"version"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	MaxConnections        int    `json:"max_connections"`
	QueryMaxConnections   int    `json:"query_max_connections"`
	CAFile                string `json:"ca_file,omitempty"`
	ServerName            string `json:"server_name,omitempty"`
}

func DefaultRedisOptions() RedisOptions {
	return RedisOptions{Version: 1, ConnectTimeoutSeconds: 10, MaxConnections: 8, QueryMaxConnections: 8}
}
func NormalizeRedisOptions(a RedisOptions) (RedisOptions, error) {
	if a == (RedisOptions{}) {
		a = DefaultRedisOptions()
	}
	if a.Version != 1 || a.ConnectTimeoutSeconds < 1 || a.ConnectTimeoutSeconds > 3600 || a.MaxConnections < 1 || a.MaxConnections > 64 || a.QueryMaxConnections < 1 || a.QueryMaxConnections > 64 || !validEngineText(a.CAFile, 4096) || !validEngineText(a.ServerName, 253) {
		return a, fmt.Errorf("%w: invalid Redis options", ErrInvalid)
	}
	return a, nil
}
func RedisDatabase(name string) (int, error) {
	n, e := strconv.Atoi(name)
	if e != nil || n < 0 || n > 65535 || strconv.Itoa(n) != name {
		return 0, fmt.Errorf("%w: Redis database must be an integer from 0 to 65535", ErrInvalid)
	}
	return n, nil
}
func normalizeRedisProfile(p Profile) (Profile, error) {
	var err error
	p.RedisOptions, err = NormalizeRedisOptions(p.RedisOptions)
	if err != nil {
		return p, err
	}
	if p.Database == "" {
		p.Database = "0"
	}
	if _, err = RedisDatabase(p.Database); err != nil {
		return p, err
	}
	if !mongoHost(p.Host) || p.Port == 0 || !validEngineText(p.Username, 1024) || p.MySQLAdvanced != (MySQLAdvanced{}) {
		return p, ErrInvalid
	}
	if p.TLSMode == "" {
		p.TLSMode = "disable"
	}
	if p.TLSMode != "disable" && p.TLSMode != "verify-full" || p.TLSMode == "disable" && (p.RedisOptions.CAFile != "" || p.RedisOptions.ServerName != "") {
		return p, fmt.Errorf("%w: invalid Redis TLS settings", ErrInvalid)
	}
	return p, nil
}
func parseRedisURI(u *url.URL) (URIImport, error) {
	p, _ := Defaults(Redis)
	p.Host = u.Hostname()
	if u.Port() != "" {
		n, e := strconv.ParseUint(u.Port(), 10, 16)
		if e != nil || n == 0 {
			return URIImport{}, ErrURI
		}
		p.Port = uint16(n)
	}
	if u.Host == "" || strings.ContainsAny(u.Host, "@,\\ ") {
		return URIImport{}, ErrURI
	}
	if u.Path != "" && u.Path != "/" {
		p.Database = strings.TrimPrefix(u.Path, "/")
	}
	if strings.EqualFold(u.Scheme, "rediss") {
		p.TLSMode = "verify-full"
	}
	password := ""
	if u.User != nil {
		p.Username = u.User.Username()
		password, _ = u.User.Password()
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) > 0 {
		return URIImport{}, ErrURIOptions
	}
	p.Name = "Redis · " + p.Host
	p, err = NormalizeProfile(p)
	if err != nil {
		return URIImport{}, ErrURI
	}
	return URIImport{Profile: p, Password: password}, nil
}
