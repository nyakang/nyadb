package connections

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateElasticsearchCredential can allow an empty temporary credential when
// saving a profile. Actual connection attempts require one for Basic/API key.
func ValidateElasticsearchCredential(c ElasticsearchConfig, secret string, required bool) error {
	if c.Auth != "none" && c.Auth != "basic" && c.Auth != "api_key" {
		return ErrInvalid
	}
	if len(secret) > 4096 || !utf8.ValidString(secret) || strings.ContainsRune(secret, 0) || c.Auth == "none" && secret != "" || required && c.Auth != "none" && secret == "" {
		return ErrInvalid
	}
	if c.Auth == "api_key" && secret != "" {
		decoded, err := base64.StdEncoding.DecodeString(secret)
		colon := bytes.IndexByte(decoded, ':')
		if err != nil || colon <= 0 || colon == len(decoded)-1 || strings.ContainsAny(secret, "\r\n ") {
			return ErrInvalid
		}
	}
	return nil
}

// ElasticsearchConfig contains public native REST settings. Passwords and
// encoded API keys are supplied separately. Product registration is separate.
type ElasticsearchConfig struct {
	Endpoint, Auth, Username, CAFile, ServerName string
}

func NormalizeElasticsearchConfig(c ElasticsearchConfig) (ElasticsearchConfig, error) {
	for _, s := range []string{c.Endpoint, c.Auth, c.Username, c.CAFile, c.ServerName} {
		if len(s) > 4096 || !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return c, ErrInvalid
		}
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" || u.Scheme != "https" && u.Scheme != "http" || u.Path != "" && u.Path != "/" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.IndexFunc(c.Endpoint, unicode.IsSpace) >= 0 {
		return c, ErrInvalid
	}
	if port := u.Port(); port != "" {
		value, e := strconv.Atoi(port)
		if e != nil || value < 1 || value > 65535 {
			return c, ErrInvalid
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return c, ErrInvalid
	}
	if c.Auth == "" {
		c.Auth = "none"
	}
	switch c.Auth {
	case "none", "api_key":
		if c.Username != "" {
			return c, ErrInvalid
		}
	case "basic":
		if c.Username == "" || strings.Contains(c.Username, ":") {
			return c, ErrInvalid
		}
	default:
		return c, ErrInvalid
	}
	if u.Scheme == "http" && (c.Auth != "none" || c.CAFile != "" || c.ServerName != "") || c.CAFile != "" && !filepath.IsAbs(c.CAFile) {
		return c, ErrInvalid
	}
	u.Path = ""
	c.Endpoint = u.String()
	return c, nil
}
