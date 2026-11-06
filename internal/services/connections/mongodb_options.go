package connections

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// MongoDBOptions contains public topology and authentication settings only.
// Seeds supplements Host:Port; passwords always come from the credential vault.
type MongoDBOptions struct {
	Version               int    `json:"version"`
	SRV                   bool   `json:"srv"`
	Seeds                 string `json:"seeds,omitempty"`
	ReplicaSet            string `json:"replica_set,omitempty"`
	AuthSource            string `json:"auth_source"`
	AuthMechanism         string `json:"auth_mechanism,omitempty"`
	DirectConnection      bool   `json:"direct_connection"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	MaxConnections        int    `json:"max_connections"`
	QueryMaxConnections   int    `json:"query_max_connections"`
	ApplicationName       string `json:"application_name"`
	CAFile                string `json:"ca_file,omitempty"`
}

func DefaultMongoDBOptions() MongoDBOptions {
	return MongoDBOptions{Version: 1, AuthSource: "admin", ConnectTimeoutSeconds: 10, MaxConnections: 8, QueryMaxConnections: 8, ApplicationName: "NyaDB"}
}

func NormalizeMongoDBOptions(a MongoDBOptions) (MongoDBOptions, error) {
	if a == (MongoDBOptions{}) {
		a = DefaultMongoDBOptions()
	}
	a.Seeds = strings.TrimSpace(a.Seeds)
	a.ReplicaSet = strings.TrimSpace(a.ReplicaSet)
	a.AuthSource = strings.TrimSpace(a.AuthSource)
	a.AuthMechanism = strings.ToUpper(strings.TrimSpace(a.AuthMechanism))
	if a.Version != 1 || a.ConnectTimeoutSeconds < 1 || a.ConnectTimeoutSeconds > 3600 || a.MaxConnections < 1 || a.MaxConnections > 64 || a.QueryMaxConnections < 1 || a.QueryMaxConnections > 64 || a.SRV && (a.Seeds != "" || a.DirectConnection) || a.DirectConnection && a.Seeds != "" {
		return a, fmt.Errorf("%w: invalid MongoDB topology or limits", ErrInvalid)
	}
	for _, v := range []string{a.Seeds, a.ReplicaSet, a.AuthSource, a.AuthMechanism, a.ApplicationName, a.CAFile} {
		if !validEngineText(v, 4096) {
			return a, fmt.Errorf("%w: invalid MongoDB option", ErrInvalid)
		}
	}
	if a.AuthSource == "" {
		a.AuthSource = "admin"
	}
	if a.AuthMechanism != "" && a.AuthMechanism != "SCRAM-SHA-1" && a.AuthMechanism != "SCRAM-SHA-256" {
		return a, fmt.Errorf("%w: unsupported MongoDB authentication", ErrInvalid)
	}
	if a.Seeds != "" {
		seeds := strings.Split(a.Seeds, ",")
		if len(seeds) > 15 {
			return a, ErrInvalid
		}
		for _, seed := range seeds {
			host, port, err := net.SplitHostPort(seed)
			n, e := strconv.ParseUint(port, 10, 16)
			if err != nil || e != nil || n == 0 || !mongoHost(host) {
				return a, fmt.Errorf("%w: MongoDB seeds require host:port", ErrInvalid)
			}
		}
	}
	return a, nil
}

func mongoHost(host string) bool {
	return host != "" && validEngineText(host, 253) && !strings.ContainsAny(host, "@/?!#\\, \t\r\n") && (!strings.Contains(host, ":") || net.ParseIP(host) != nil)
}

func normalizeMongoDBProfile(p Profile) (Profile, error) {
	var err error
	p.MongoDBOptions, err = NormalizeMongoDBOptions(p.MongoDBOptions)
	if err != nil {
		return p, err
	}
	a := p.MongoDBOptions
	if !mongoHost(p.Host) || p.Port == 0 || a.SRV && (p.Port != 27017 || net.ParseIP(p.Host) != nil || strings.Contains(p.Host, ":")) || p.MySQLAdvanced != (MySQLAdvanced{}) || !validEngineText(p.Username, 1024) || !validEngineText(p.Database, 64) || strings.ContainsAny(p.Database, "/\\.\"$ *<>:|?") {
		return p, fmt.Errorf("%w: invalid MongoDB server/database settings", ErrInvalid)
	}
	if p.TLSMode == "" {
		p.TLSMode = "disable"
		if a.SRV {
			p.TLSMode = "verify-full"
		}
	}
	if p.TLSMode != "disable" && p.TLSMode != "verify-full" || a.SRV && p.TLSMode != "verify-full" || p.TLSMode == "disable" && a.CAFile != "" {
		return p, fmt.Errorf("%w: invalid MongoDB TLS settings", ErrInvalid)
	}
	return p, nil
}
