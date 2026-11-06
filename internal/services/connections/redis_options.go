package connections

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// RedisOptions contains public topology settings. Passwords are held by the vault.
type RedisOptions struct {
	Topology              string `json:"topology,omitempty"`
	Seeds                 string `json:"seeds,omitempty"`
	MasterName            string `json:"master_name,omitempty"`
	SentinelUsername      string `json:"sentinel_username,omitempty"`
	SentinelTLSMode       string `json:"sentinel_tls_mode,omitempty"`
	SentinelCAFile        string `json:"sentinel_ca_file,omitempty"`
	SentinelServerName    string `json:"sentinel_server_name,omitempty"`
	Version               int    `json:"version"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	MaxConnections        int    `json:"max_connections"`
	QueryMaxConnections   int    `json:"query_max_connections"`
	CAFile                string `json:"ca_file,omitempty"`
	ServerName            string `json:"server_name,omitempty"`
}

func DefaultRedisOptions() RedisOptions {
	return RedisOptions{Version: 1, Topology: "standalone", ConnectTimeoutSeconds: 10, MaxConnections: 8, QueryMaxConnections: 8}
}
func NormalizeRedisOptions(a RedisOptions) (RedisOptions, error) {
	if a == (RedisOptions{}) {
		a = DefaultRedisOptions()
	}
	a.Topology = strings.ToLower(strings.TrimSpace(a.Topology))
	if a.Topology == "" {
		a.Topology = "standalone"
	}
	if a.Topology != "standalone" && a.Topology != "cluster" && a.Topology != "sentinel" {
		return a, fmt.Errorf("%w: invalid Redis topology", ErrInvalid)
	}
	a.MasterName = strings.TrimSpace(a.MasterName)
	if a.Topology == "sentinel" {
		if a.MasterName == "" || !validEngineText(a.MasterName, 255) || strings.ContainsAny(a.MasterName, " \t\r\n") {
			return a, fmt.Errorf("%w: Sentinel requires a master name", ErrInvalid)
		}
		if a.SentinelTLSMode == "" {
			a.SentinelTLSMode = "disable"
		}
	}
	if a.Topology != "sentinel" && (a.MasterName != "" || a.SentinelUsername != "" || a.SentinelTLSMode != "" || a.SentinelCAFile != "" || a.SentinelServerName != "") {
		return a, fmt.Errorf("%w: Sentinel settings require Sentinel topology", ErrInvalid)
	}
	if a.SentinelTLSMode != "" && a.SentinelTLSMode != "disable" && a.SentinelTLSMode != "verify-full" || a.SentinelTLSMode != "verify-full" && (a.SentinelCAFile != "" || a.SentinelServerName != "") {
		return a, fmt.Errorf("%w: invalid Sentinel TLS settings", ErrInvalid)
	}
	if !validEngineText(a.SentinelUsername, 1024) || !validEngineText(a.SentinelCAFile, 4096) || !validEngineText(a.SentinelServerName, 253) {
		return a, ErrInvalid
	}
	if a.Topology == "standalone" && a.Seeds != "" {
		return a, fmt.Errorf("%w: seed nodes require Cluster or Sentinel topology", ErrInvalid)
	}
	if a.Seeds != "" {
		seeds := strings.Split(a.Seeds, ",")
		if len(seeds) > 31 {
			return a, ErrInvalid
		}
		seen := map[string]bool{}
		for i, seed := range seeds {
			host, port, e := net.SplitHostPort(strings.TrimSpace(seed))
			n, e2 := strconv.ParseUint(port, 10, 16)
			if e != nil || e2 != nil || n == 0 || !mongoHost(host) {
				return a, fmt.Errorf("%w: Redis seeds require host:port", ErrInvalid)
			}
			seeds[i] = net.JoinHostPort(host, strconv.Itoa(int(n)))
			if seen[seeds[i]] {
				return a, fmt.Errorf("%w: duplicate Redis seed", ErrInvalid)
			}
			seen[seeds[i]] = true
		}
		a.Seeds = strings.Join(seeds, ",")
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
	if p.RedisOptions.Topology == "cluster" && p.Database != "0" {
		return p, fmt.Errorf("%w: Redis Cluster supports only database 0", ErrInvalid)
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
	if err != nil {
		return URIImport{}, ErrURIOptions
	}
	for k, values := range q {
		if len(values) != 1 {
			return URIImport{}, ErrURIOptions
		}
		v := values[0]
		switch k {
		case "topology":
			p.RedisOptions.Topology = v
		case "seeds":
			p.RedisOptions.Seeds = v
		case "master_name":
			p.RedisOptions.MasterName = v
		case "sentinel_username":
			p.RedisOptions.SentinelUsername = v
		case "sentinel_tls":
			if v != "true" && v != "false" {
				return URIImport{}, ErrURIOptions
			}
			p.RedisOptions.SentinelTLSMode = "disable"
			if v == "true" {
				p.RedisOptions.SentinelTLSMode = "verify-full"
			}
		default:
			return URIImport{}, ErrURIOptions
		}
	}
	p.Name = "Redis · " + p.Host
	p, err = NormalizeProfile(p)
	if err != nil {
		return URIImport{}, ErrURI
	}
	return URIImport{Profile: p, Password: password}, nil
}
