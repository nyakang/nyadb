package connections

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// MySQLHTTP contains non-secret HTTP tunnel and proxy configuration.
// Authentication passwords, certificate passphrases and exported tunnel
// scripts are not stored in the profile.
type MySQLHTTP struct {
	Enabled           bool   `json:"enabled,omitempty"`
	URL               string `json:"url,omitempty"`
	TargetID          string `json:"targetId,omitempty"`
	Base64Queries     bool   `json:"base64Queries,omitempty"`
	PasswordAuth      bool   `json:"passwordAuth,omitempty"`
	Username          string `json:"username,omitempty"`
	CertificateAuth   bool   `json:"certificateAuth,omitempty"`
	ClientKey         string `json:"clientKey,omitempty"`
	ClientCertificate string `json:"clientCertificate,omitempty"`
	CACertificate     string `json:"caCertificate,omitempty"`
	ProxyEnabled      bool   `json:"proxyEnabled,omitempty"`
	ProxyHost         string `json:"proxyHost,omitempty"`
	ProxyPort         int    `json:"proxyPort"`
	ProxyUsername     string `json:"proxyUsername,omitempty"`
}

func DefaultMySQLHTTP() MySQLHTTP {
	return MySQLHTTP{ProxyPort: 8080}
}

func normalizeMySQLHTTP(v MySQLHTTP) (MySQLHTTP, error) {
	if v == (MySQLHTTP{}) {
		v = DefaultMySQLHTTP()
	}
	if v.ProxyPort < 1 || v.ProxyPort > 65535 {
		return v, fmt.Errorf("%w: MySQL HTTP proxy port out of range", ErrInvalid)
	}
	for _, value := range []string{v.URL, v.TargetID, v.Username, v.ClientKey, v.ClientCertificate,
		v.CACertificate, v.ProxyHost, v.ProxyUsername} {
		if strings.ContainsRune(value, 0) || strings.ContainsAny(value, "\r\n") ||
			utf8.RuneCountInString(value) > 4096 {
			return v, fmt.Errorf("%w: invalid MySQL HTTP setting", ErrInvalid)
		}
	}
	if v.URL != "" {
		u, err := url.Parse(v.URL)
		if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") ||
			u.Host == "" || u.Fragment != "" || u.RawQuery != "" {
			// Tunnel URLs must not put secrets into a profile query string.
			return v, fmt.Errorf("%w: HTTP tunnel URL must be an http(s) URL without credentials, query or fragment", ErrInvalid)
		}
	}
	return v, nil
}
