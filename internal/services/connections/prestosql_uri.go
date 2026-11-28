package connections

import (
	"net/url"
	"strconv"
	"strings"
)

// ParsePrestoSQLURI requires an explicit product context for jdbc:presto URLs:
// that JDBC scheme is shared with PrestoDB and cannot select a product alone.
func ParsePrestoSQLURI(raw string) (URIImport, error) {
	raw = strings.TrimSpace(raw)
	jdbc := strings.HasPrefix(strings.ToLower(raw), "jdbc:presto://")
	if jdbc {
		raw = "prestosql://" + raw[len("jdbc:presto://"):]
	}
	u, err := url.Parse(raw)
	if err != nil || strings.ToLower(u.Scheme) != "prestosql" || u.Opaque != "" || u.Hostname() == "" || u.Fragment != "" || strings.Contains(raw, "#") || len(raw) > 16<<10 || !prestoConfigText(raw, 16<<10) {
		return URIImport{}, ErrURI
	}
	p, _ := Defaults(PrestoSQL)
	p.Name = "PrestoSQL (URI)"
	p.Host = u.Hostname()
	if u.Port() != "" {
		n, e := strconv.ParseUint(u.Port(), 10, 16)
		if e != nil || n == 0 {
			return URIImport{}, ErrURI
		}
		p.Port = uint16(n)
	} else if strings.HasSuffix(u.Host, ":") {
		return URIImport{}, ErrURI
	}
	var secrets PrestoSQLCredentials
	if u.User != nil {
		p.Username = u.User.Username()
		secrets.Password, _ = u.User.Password()
	}
	if u.Path != "" && u.Path != "/" {
		parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(parts) > 2 || parts[0] == "" {
			return URIImport{}, ErrURI
		}
		p.Database = parts[0]
		if len(parts) == 2 {
			if parts[1] == "" {
				return URIImport{}, ErrURI
			}
			p.PrestoSQLOptions.DefaultSchema = parts[1]
		}
	}
	var params url.Values
	if jdbc {
		decoded, e := url.PathUnescape(u.RawQuery)
		if e != nil {
			return URIImport{}, ErrURI
		}
		params = url.Values{}
		if decoded != "" {
			for _, part := range strings.Split(decoded, "&") {
				key, value, ok := strings.Cut(part, "=")
				if !ok || key == "" {
					return URIImport{}, ErrURI
				}
				params.Add(key, value)
			}
		}
	} else {
		params, err = url.ParseQuery(u.RawQuery)
		if err != nil {
			return URIImport{}, ErrURI
		}
	}
	public := url.Values{}
	for key, values := range params {
		if len(values) != 1 {
			return URIImport{}, ErrURIOptions
		}
		value := values[0]
		switch key {
		case "user":
			if u.User != nil {
				return URIImport{}, ErrURIOptions
			}
			p.Username = value
		case "password":
			if u.User != nil {
				if _, ok := u.User.Password(); ok {
					return URIImport{}, ErrURIOptions
				}
			}
			secrets.Password = value
		case "sslmode":
			if jdbc {
				return URIImport{}, ErrURIOptions
			}
			p.TLSMode = value
		case "SSL":
			if !jdbc || value != "true" && value != "false" {
				return URIImport{}, ErrURIOptions
			}
			if value == "true" {
				p.TLSMode = "verify-full"
			} else {
				p.TLSMode = "disable"
			}
		case "SSLVerification":
			if !jdbc {
				return URIImport{}, ErrURIOptions
			}
			if value != "FULL" && value != "CA" && value != "NONE" {
				return URIImport{}, ErrURIOptions
			}
		case "SSLTrustStorePath":
			p.PrestoSQLOptions.TrustStorePath = value
		case "SSLTrustStoreType":
			p.PrestoSQLOptions.TrustStoreType = value
		case "SSLKeyStorePath":
			p.PrestoSQLOptions.KeyStorePath = value
		case "SSLKeyStoreType":
			p.PrestoSQLOptions.KeyStoreType = value
		case "SSLTrustStorePassword":
			secrets.TrustStorePassword = value
		case "SSLKeyStorePassword":
			secrets.KeyStorePassword = value
		case "accessToken":
			secrets.AccessToken = value
		case "extraCredentials":
			secrets.ExtraCredentials = value
		default:
			if !prestoPublicParameter(key) {
				return URIImport{}, ErrURIOptions
			}
			public.Set(key, value)
		}
	}
	if jdbc && params.Get("SSLVerification") != "" {
		if params.Get("SSL") != "true" {
			return URIImport{}, ErrURIOptions
		}
		p.TLSMode = map[string]string{"FULL": "verify-full", "CA": "verify-ca", "NONE": "require"}[params.Get("SSLVerification")]
	}
	p.PrestoSQLOptions.JDBCParameters = public.Encode()
	p, err = NormalizeProfile(p)
	if err != nil || ValidatePrestoSQLCredentials(secrets) != nil {
		return URIImport{}, ErrURI
	}
	return URIImport{Profile: p, Password: secrets.Password, PrestoSQLCredentials: secrets}, nil
}
