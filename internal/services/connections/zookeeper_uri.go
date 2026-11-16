package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ParseZooKeeperURI(raw string) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	raw = strings.TrimSpace(raw)
	if len(raw) > 16<<10 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 || strings.ContainsRune(raw, '#') {
		return fail()
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || !strings.EqualFold(scheme, "zookeeper") {
		return fail()
	}
	end := strings.IndexAny(rest, "/?")
	if end < 0 {
		end = len(rest)
	}
	authority, suffix := rest[:end], rest[end:]
	serversRaw, userinfo := authority, ""
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		userinfo, serversRaw = authority[:at+1], authority[at+1:]
		if strings.ContainsRune(authority[:at], '@') {
			return fail()
		}
	}
	// net/url validates one host and rejects valid multi-host IPv6 authorities.
	// Decode credentials and paths with a placeholder; validate every host below.
	u, err := url.Parse("zookeeper://" + userinfo + "placeholder" + suffix)
	if err != nil || serversRaw == "" || u.Opaque != "" || u.Fragment != "" || u.ForceQuery {
		return fail()
	}
	p, _ := Defaults(ZooKeeper)
	servers := strings.Split(serversRaw, ",")
	for i, v := range servers {
		servers[i], err = endpoint(v)
		if err != nil {
			return fail()
		}
	}
	host, port, err := net.SplitHostPort(servers[0])
	if err != nil {
		return fail()
	}
	p.Host = host
	n, _ := strconv.Atoi(port)
	p.Port = uint16(n)
	if len(servers) > 1 {
		p.ZooKeeperOptions.AdditionalServers = strings.Join(servers[1:], ",")
	}
	p.ZooKeeperOptions.Chroot = u.Path
	secret := ""
	if u.User != nil {
		p.Username = u.User.Username()
		secret, _ = u.User.Password()
		p.ZooKeeperOptions.Auth = "digest"
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	p.Name = "ZooKeeper · " + p.Host
	seen := map[string]bool{}
	for k, vs := range q {
		if len(vs) != 1 || vs[0] == "" {
			return fail()
		}
		v := vs[0]
		// DBX URLs use these spellings; both forms bind the same native option.
		switch k {
		case "session_timeout":
			k = "session_timeout_ms"
		case "connect_timeout":
			k = "connection_timeout_ms"
		}
		if seen[k] {
			return fail()
		}
		seen[k] = true
		switch k {
		case "name":
			p.Name = v
		case "namespace":
			p.ZooKeeperOptions.Namespace = v
		case "prefix":
			p.ZooKeeperOptions.Prefix = v
		case "auth_scheme":
			p.ZooKeeperOptions.Auth = v
		case "sslmode":
			p.TLSMode = v
		case "ca_file":
			p.ZooKeeperOptions.CAFile = v
		case "client_cert_file":
			p.ZooKeeperOptions.ClientCertFile = v
		case "client_key_file":
			p.ZooKeeperOptions.ClientKeyFile = v
		case "driver_version":
			p.ZooKeeperOptions.DriverVersion = v
		case "driver_sha256":
			p.ZooKeeperOptions.DriverSHA256 = v
		case "driver_directory":
			p.ZooKeeperOptions.DriverDirectory = v
		case "session_timeout_ms", "connection_timeout_ms", "max_buffer_size", "max_documents":
			n, e := strconv.Atoi(v)
			if e != nil || strconv.Itoa(n) != v {
				return fail()
			}
			switch k {
			case "session_timeout_ms":
				p.ZooKeeperOptions.SessionTimeoutMS = n
			case "connection_timeout_ms":
				p.ZooKeeperOptions.ConnectionTimeoutMS = n
			case "max_buffer_size":
				p.ZooKeeperOptions.MaxBufferSize = n
			case "max_documents":
				p.ZooKeeperOptions.MaxDocuments = n
			}
		default:
			return fail()
		}
	}
	p, err = NormalizeProfile(p)
	if err != nil || ValidateZooKeeperCredential(p, secret) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
