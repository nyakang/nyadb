package connections

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Neo4jConfig describes the private Bolt foundation. Credentials are separate;
// this configuration does not register a product or workbench provider.
type Neo4jConfig struct {
	URI, Auth, Username, Database, CAFile, ServerName, ClientCertFile, ClientKeyFile string
	ConnectTimeoutSeconds, RequestTimeoutSeconds                                     int
}

func NormalizeNeo4jConfig(c Neo4jConfig) (Neo4jConfig, error) {
	for _, v := range []string{c.URI, c.Auth, c.Username, c.Database, c.CAFile, c.ServerName, c.ClientCertFile, c.ClientKeyFile} {
		if len(v) > 4096 || !utf8.ValidString(v) || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return c, ErrInvalid
		}
	}
	u, err := url.Parse(c.URI)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Opaque != "" || u.Path != "" && u.Path != "/" || u.RawPath != "" || u.Fragment != "" || u.ForceQuery || strings.IndexFunc(c.URI, unicode.IsSpace) >= 0 {
		return c, ErrInvalid
	}
	switch u.Scheme {
	case "bolt", "neo4j", "bolt+s", "neo4j+s", "bolt+ssc", "neo4j+ssc":
	default:
		return c, ErrInvalid
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return c, ErrInvalid
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return c, ErrInvalid
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil || len(q) > 16 || strings.HasPrefix(u.Scheme, "bolt") && len(q) > 0 {
		return c, ErrInvalid
	}
	for k, v := range q {
		if k == "" || len(k) > 128 || len(v) != 1 || len(v[0]) > 512 || k == "address" || strings.IndexFunc(k+v[0], unicode.IsControl) >= 0 {
			return c, ErrInvalid
		}
		for _, s := range []string{"password", "secret", "token", "credential"} {
			if strings.Contains(strings.ToLower(k), s) {
				return c, ErrInvalid
			}
		}
	}
	if c.Auth == "" {
		c.Auth = "basic"
	}
	if c.Auth != "basic" && c.Auth != "none" || c.Auth == "basic" && c.Username == "" || c.Auth == "none" && c.Username != "" {
		return c, ErrInvalid
	}
	if c.Database == "" {
		c.Database = "neo4j"
	}
	secure := strings.Contains(u.Scheme, "+")
	if !secure && (c.CAFile != "" || c.ServerName != "" || c.ClientCertFile != "" || c.ClientKeyFile != "") || (c.ClientCertFile == "") != (c.ClientKeyFile == "") || strings.IndexFunc(c.ServerName, unicode.IsSpace) >= 0 {
		return c, ErrInvalid
	}
	if strings.HasSuffix(u.Scheme, "+ssc") && c.CAFile != "" {
		return c, ErrInvalid
	}
	for _, p := range []string{c.CAFile, c.ClientCertFile, c.ClientKeyFile} {
		if p != "" && !filepath.IsAbs(p) {
			return c, ErrInvalid
		}
	}
	if c.ConnectTimeoutSeconds == 0 {
		c.ConnectTimeoutSeconds = 10
	}
	if c.RequestTimeoutSeconds == 0 {
		c.RequestTimeoutSeconds = 40
	}
	if c.ConnectTimeoutSeconds < 1 || c.ConnectTimeoutSeconds > 120 || c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 600 {
		return c, ErrInvalid
	}
	u.Path = ""
	u.RawQuery = q.Encode()
	c.URI = u.String()
	return c, nil
}

func ValidateNeo4jCredential(c Neo4jConfig, secret string) error {
	if c.Auth != "basic" && c.Auth != "none" || c.Auth == "none" && secret != "" || c.Auth == "basic" && secret == "" || len(secret) > 4096 || !utf8.ValidString(secret) || strings.ContainsRune(secret, 0) {
		return ErrInvalid
	}
	return nil
}
