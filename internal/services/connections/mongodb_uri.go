package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

func parseMongoDBURI(u *url.URL) (URIImport, error) {
	p, _ := Defaults(MongoDB)
	p.MongoDBOptions.SRV = strings.EqualFold(u.Scheme, "mongodb+srv")
	if p.MongoDBOptions.SRV {
		p.TLSMode = "verify-full"
	}
	hosts := strings.Split(u.Host, ",")
	if len(hosts) == 0 || len(hosts) > 16 || p.MongoDBOptions.SRV && len(hosts) != 1 {
		return URIImport{}, ErrURI
	}
	for i, h := range hosts {
		host, port := h, "27017"
		if strings.Contains(h, ":") {
			var err error
			host, port, err = net.SplitHostPort(h)
			if err != nil || p.MongoDBOptions.SRV {
				return URIImport{}, ErrURI
			}
		}
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 || !mongoHost(host) {
			return URIImport{}, ErrURI
		}
		if i == 0 {
			p.Host, p.Port = host, uint16(n)
		} else {
			if p.MongoDBOptions.Seeds != "" {
				p.MongoDBOptions.Seeds += ","
			}
			p.MongoDBOptions.Seeds += net.JoinHostPort(host, port)
		}
	}
	if !strings.HasPrefix(u.Path, "/") && u.Path != "" || strings.Contains(strings.TrimPrefix(u.Path, "/"), "/") {
		return URIImport{}, ErrURI
	}
	p.Database = strings.TrimPrefix(u.Path, "/")
	password := ""
	if u.User != nil {
		p.Username = u.User.Username()
		password, _ = u.User.Password()
		if p.Username == "" {
			return URIImport{}, ErrURI
		}
	}
	if p.Database != "" {
		p.MongoDBOptions.AuthSource = p.Database
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return URIImport{}, ErrURI
	}
	seen := map[string]bool{}
	for key, values := range q {
		k := strings.ToLower(key)
		if len(values) != 1 || seen[k] {
			return URIImport{}, ErrURIOptions
		}
		seen[k] = true
		v := values[0]
		switch k {
		case "authsource":
			p.MongoDBOptions.AuthSource = v
		case "authmechanism":
			p.MongoDBOptions.AuthMechanism = v
		case "replicaset":
			p.MongoDBOptions.ReplicaSet = v
		case "appname":
			p.MongoDBOptions.ApplicationName = v
		case "tls", "ssl":
			if seen["tls"] && seen["ssl"] || v != "true" && v != "false" {
				return URIImport{}, ErrURIOptions
			}
			p.TLSMode = "disable"
			if v == "true" {
				p.TLSMode = "verify-full"
			}
		case "directconnection":
			if v != "true" && v != "false" {
				return URIImport{}, ErrURIOptions
			}
			p.MongoDBOptions.DirectConnection = v == "true"
		case "connecttimeoutms", "serverselectiontimeoutms":
			if seen["connecttimeoutms"] && seen["serverselectiontimeoutms"] {
				return URIImport{}, ErrURIOptions
			}
			n, e := strconv.Atoi(v)
			if e != nil || n%1000 != 0 {
				return URIImport{}, ErrURIOptions
			}
			p.MongoDBOptions.ConnectTimeoutSeconds = n / 1000
		case "maxpoolsize":
			n, e := strconv.Atoi(v)
			if e != nil {
				return URIImport{}, ErrURIOptions
			}
			p.MongoDBOptions.MaxConnections = n
		case "retrywrites", "retryreads":
			if v != "false" {
				return URIImport{}, ErrURIOptions
			}
		case "readpreference":
			if v != "primary" {
				return URIImport{}, ErrURIOptions
			}
		default:
			return URIImport{}, ErrURIOptions
		}
	}
	p.Name = "MongoDB · " + p.Host
	p, err = NormalizeProfile(p)
	if err != nil {
		return URIImport{}, ErrURI
	}
	return URIImport{Profile: p, Password: password}, nil
}
