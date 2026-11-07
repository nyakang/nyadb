package connections

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// EasysearchConfig holds public native connection settings. Basic passwords
// and X-API-TOKEN credentials are provided separately. Registration and profile
// persistence are independent of the native transport foundation.
type EasysearchConfig struct {
	Endpoint, Auth, Username, CAFile, ServerName string
}

func NormalizeEasysearchConfig(c EasysearchConfig) (EasysearchConfig, error) {
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
	case "none", "access_token":
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

func ValidateEasysearchCredential(c EasysearchConfig, secret string, required bool) error {
	if c.Auth != "none" && c.Auth != "basic" && c.Auth != "access_token" || len(secret) > 4096 || !utf8.ValidString(secret) || strings.ContainsRune(secret, 0) || c.Auth == "none" && secret != "" || required && c.Auth != "none" && secret == "" {
		return ErrInvalid
	}
	if c.Auth == "access_token" && strings.IndexFunc(secret, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) || r > unicode.MaxASCII }) >= 0 {
		return ErrInvalid
	}
	return nil
}
