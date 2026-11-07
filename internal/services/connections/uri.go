package connections

import (
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// URIImport contains a temporary secret, never part of Profile or storage.
// Errors and formatted values deliberately do not contain the original URI.
type URIImport struct {
	Profile  Profile
	Password string `json:"-"`
}

func (URIImport) String() string   { return "connection URI import (credentials redacted)" }
func (URIImport) GoString() string { return "connection URI import (credentials redacted)" }

var ErrURI = errors.New("invalid or unsupported connection URI")
var ErrURIOptions = errors.New("unsupported connection URI options")

// ParseURI accepts only settings the current engines actually execute. It
// never opens a file or network connection, and never returns url.Parse's
// userinfo-bearing errors to a caller.
func ParseURI(raw string) (URIImport, error) {
	fail := func(err error) (URIImport, error) { return URIImport{}, err }
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 16<<10 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return fail(ErrURI)
	}
	if scheme, _, ok := strings.Cut(raw, ":"); ok && strings.EqualFold(scheme, "gaussdb") {
		return parseRawGaussDBURI(raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Fragment != "" || u.RawFragment != "" || strings.ContainsRune(raw, '#') {
		return fail(ErrURI)
	}
	var kind Kind
	switch strings.ToLower(u.Scheme) {
	case "trino":
		return parseTrinoURI(u)
	case "dameng", "dm":
		return parseDamengURI(u)
	case "redis", "rediss":
		return parseRedisURI(u)
	case "mongodb", "mongodb+srv":
		return parseMongoDBURI(u)
	case "oracle":
		kind = Oracle
	case "duckdb":
		kind = DuckDB
	case "sqlserver", "mssql":
		kind = SQLServer
	case "mysql":
		kind = MySQL
	case "mariadb":
		kind = MariaDB
	case "oceanbase":
		kind = OceanBase
	case "tidb":
		kind = TiDB
	case "cockroachdb", "cockroach":
		kind = CockroachDB
	case "opengauss":
		kind = OpenGauss
	case "kingbase":
		kind = Kingbase
	case "postgres", "postgresql":
		kind = PostgreSQL
	case "clickhouse":
		kind = ClickHouse
	case "doris":
		kind = Doris
	case "starrocks":
		kind = StarRocks
	case "sqlite", "file":
		kind = SQLite
	default:
		return fail(ErrURI)
	}
	p, err := Defaults(kind)
	if err != nil {
		return fail(ErrURI)
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail(ErrURI)
	}
	if kind == DuckDB {
		if u.User != nil || u.Host != "" && u.Host != "localhost" || !strings.HasPrefix(u.Path, "/") || strings.IndexFunc(u.Path, unicode.IsControl) >= 0 {
			return fail(ErrURIOptions)
		}
		for key, values := range options {
			if key != "read_only" || len(values) != 1 || values[0] != "true" && values[0] != "false" {
				return fail(ErrURIOptions)
			}
			p.DuckDBOptions.ReadOnly = values[0] == "true"
		}
		path := u.Path
		if path == "/:memory:" {
			p.Database = ":memory:"
		} else {
			if runtime.GOOS == "windows" && len(path) >= 4 && path[2] == ':' && path[3] == '/' {
				path = path[1:]
			}
			p.Database = filepath.Clean(filepath.FromSlash(path))
			if !filepath.IsAbs(p.Database) {
				return fail(ErrURI)
			}
		}
		p.Name = "DuckDB · " + filepath.Base(p.Database)
		p, err = NormalizeProfile(p)
		if err != nil {
			return fail(ErrURI)
		}
		return URIImport{Profile: p}, nil
	}
	if kind == SQLite {
		if u.User != nil || u.Host != "" && u.Host != "localhost" || len(options) > 0 || !strings.HasPrefix(u.Path, "/") || strings.IndexFunc(u.Path, unicode.IsControl) >= 0 {
			return fail(ErrURIOptions)
		}
		path := u.Path
		if runtime.GOOS == "windows" && len(path) >= 4 && path[0] == '/' && path[2] == ':' && path[3] == '/' {
			path = path[1:]
		}
		p.Database = filepath.Clean(filepath.FromSlash(path))
		if !filepath.IsAbs(p.Database) {
			return fail(ErrURI)
		}
		p.Name = "SQLite · " + filepath.Base(p.Database)
		p, err = NormalizeProfile(p)
		if err != nil {
			return fail(ErrURI)
		}
		return URIImport{Profile: p}, nil
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "@/?#\\\r\n\x00") || strings.IndexFunc(host, unicode.IsSpace) >= 0 {
		return fail(ErrURI)
	}
	if strings.Contains(host, ":") && net.ParseIP(strings.Split(host, "%")[0]) == nil {
		return fail(ErrURI)
	}
	// Bare IPv6 and dangling colons are ambiguous rather than default ports.
	if strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") || strings.HasSuffix(u.Host, ":") {
		return fail(ErrURI)
	}
	p.Host = host
	if value := u.Port(); value != "" {
		n, e := strconv.ParseUint(value, 10, 16)
		if e != nil || n == 0 {
			return fail(ErrURI)
		}
		p.Port = uint16(n)
	}
	password := ""
	if u.User != nil {
		p.Username = u.User.Username()
		password, _ = u.User.Password()
	}
	path := strings.TrimPrefix(u.EscapedPath(), "/")
	if strings.Contains(path, "/") {
		return fail(ErrURI)
	}
	p.Database, err = url.PathUnescape(path)
	if err != nil {
		return fail(ErrURI)
	}
	if kind == Kingbase && (p.Database == "" || u.User == nil || p.Username == "") {
		return fail(ErrURI)
	}
	if kind == ClickHouse && p.Database == "" {
		p.Database = "default"
	}
	if kind == SQLServer && p.Database == "" {
		p.Database = "master"
	}
	if kind == CockroachDB && p.Database == "" {
		p.Database = "defaultdb"
	}
	if kind == OpenGauss && p.Database == "" {
		p.Database = "postgres"
	}
	if kind == PostgreSQL && p.Database == "" {
		p.Database = p.Username
		if p.Database == "" {
			p.Database = "postgres"
		}
	}
	if kind == Oracle {
		for _, key := range []string{"connection_type", "schema"} {
			if values, ok := options[key]; ok {
				if len(values) != 1 || values[0] == "" {
					return fail(ErrURIOptions)
				}
				if key == "connection_type" {
					p.OracleOptions.ConnectionType = values[0]
				} else {
					p.OracleOptions.DefaultSchema = values[0]
				}
				delete(options, key)
			}
		}
	}
	if kind == SQLServer {
		canonical := url.Values{}
		for key, values := range options {
			lower := strings.ToLower(key)
			if _, exists := canonical[lower]; exists {
				return fail(ErrURIOptions)
			}
			canonical[lower] = values
		}
		options = canonical
		for _, key := range []string{"database", "encrypt", "trustservercertificate", "app name"} {
			if values, ok := options[key]; ok && len(values) != 1 {
				return fail(ErrURIOptions)
			}
		}
		if values, ok := options["database"]; ok {
			if path != "" || values[0] == "" {
				return fail(ErrURIOptions)
			}
			p.Database = values[0]
			delete(options, "database")
		}
		_, encryptSet := options["encrypt"]
		_, trustSet := options["trustservercertificate"]
		_, sslSet := options["sslmode"]
		if sslSet && (encryptSet || trustSet) {
			return fail(ErrURIOptions)
		}
		if values, ok := options["encrypt"]; ok {
			switch strings.ToLower(values[0]) {
			case "true", "mandatory", "yes", "1":
				p.TLSMode = "verify-full"
			case "disable":
				p.TLSMode = "disable"
			default:
				return fail(ErrURIOptions)
			}
			delete(options, "encrypt")
		}
		if values, ok := options["trustservercertificate"]; ok {
			switch strings.ToLower(values[0]) {
			case "true", "yes", "1":
				if p.TLSMode == "disable" {
					return fail(ErrURIOptions)
				}
				p.TLSMode = "require"
			case "false", "no", "0":
			default:
				return fail(ErrURIOptions)
			}
			delete(options, "trustservercertificate")
		}
		if values, ok := options["app name"]; ok {
			p.SQLServerOptions.ApplicationName = values[0]
			delete(options, "app name")
		}
	}
	for key, values := range options {
		if kind == StarRocks && (key == "catalog" || key == "ca_file" || key == "server_name") {
			if len(values) != 1 || values[0] == "" {
				return fail(ErrURIOptions)
			}
			switch key {
			case "catalog":
				p.StarRocksOptions.Catalog = values[0]
			case "ca_file":
				p.StarRocksOptions.CAFile = values[0]
			case "server_name":
				p.StarRocksOptions.ServerName = values[0]
			}
			continue
		}
		if kind == Doris && (key == "catalog" || key == "ca_file" || key == "server_name") {
			if len(values) != 1 || values[0] == "" {
				return fail(ErrURIOptions)
			}
			switch key {
			case "catalog":
				p.DorisOptions.Catalog = values[0]
			case "ca_file":
				p.DorisOptions.CAFile = values[0]
			case "server_name":
				p.DorisOptions.ServerName = values[0]
			}
			continue
		}
		if key != "sslmode" || len(values) != 1 {
			return fail(ErrURIOptions)
		}
		mode := strings.ToLower(values[0])
		if kind == Oracle && mode != "disable" && mode != "require" && mode != "verify-full" {
			return fail(ErrURIOptions)
		}
		if kind == SQLServer && mode != "disable" && mode != "require" && mode != "verify-full" {
			return fail(ErrURIOptions)
		}
		if kind == ClickHouse && mode != "disable" && mode != "verify-full" {
			return fail(ErrURIOptions)
		}
		if kind.IsMySQLFamily() && mode != "disable" && mode != "require" && mode != "verify-full" {
			return fail(ErrURIOptions)
		}
		if kind == PostgreSQL && mode != "disable" && mode != "prefer" && mode != "require" && mode != "verify-ca" && mode != "verify-full" {
			return fail(ErrURIOptions)
		}
		p.TLSMode = mode
	}
	for _, value := range []string{p.Host, p.Database, p.Username, password} {
		if !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fail(ErrURI)
		}
	}
	p.Name = string(kind) + " · " + p.Host
	if p.Database != "" {
		p.Name += " · " + p.Database
	}
	if len([]rune(p.Name)) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail(ErrURI)
	}
	return URIImport{Profile: p, Password: password}, nil
}
