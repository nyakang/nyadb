package connections

import (
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// EasysearchOptions is a public versioned envelope. Passwords and native
// access tokens use the same separate primary credential slot, never these options.
type EasysearchOptions struct {
	Version    int    `json:"version"`
	Auth       string `json:"auth"`
	CAFile     string `json:"ca_file,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

func DefaultEasysearchOptions() EasysearchOptions {
	return EasysearchOptions{Version: 1, Auth: "basic"}
}

func NormalizeEasysearchOptions(o EasysearchOptions) (EasysearchOptions, error) {
	if o == (EasysearchOptions{}) {
		o = DefaultEasysearchOptions()
	}
	if o.Version != 1 || o.Auth != "none" && o.Auth != "basic" && o.Auth != "access_token" {
		return o, ErrInvalid
	}
	if !validEngineText(o.CAFile, 4096) || !validEngineText(o.ServerName, 4096) || o.CAFile != "" && !filepath.IsAbs(o.CAFile) {
		return o, ErrInvalid
	}
	return o, nil
}

func normalizeEasysearchProfile(p Profile) (Profile, error) {
	var err error
	p.EasysearchOptions, err = NormalizeEasysearchOptions(p.EasysearchOptions)
	if err != nil {
		return p, err
	}
	if p.TLSMode == "" {
		p.TLSMode = "verify-full"
	}
	_, err = EasysearchProfileConfig(p)
	return p, err
}

func EasysearchProfileConfig(p Profile) (EasysearchConfig, error) {
	if p.Kind != Easysearch || p.MySQLAdvanced != (MySQLAdvanced{}) || p.Database != "" || p.Port == 0 || p.Host == "" || strings.ContainsAny(p.Host, "@/?!#\\, \t\r\n") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.TLSMode != "verify-full" && p.TLSMode != "disable" {
		return EasysearchConfig{}, ErrInvalid
	}
	o, err := NormalizeEasysearchOptions(p.EasysearchOptions)
	if err != nil {
		return EasysearchConfig{}, err
	}
	scheme := "https"
	if p.TLSMode == "disable" {
		scheme = "http"
	}
	return NormalizeEasysearchConfig(EasysearchConfig{Endpoint: scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Auth: o.Auth, Username: p.Username, CAFile: o.CAFile, ServerName: o.ServerName})
}

func parseEasysearchURI(u *url.URL) (URIImport, error) {
	fail := func() (URIImport, error) { return URIImport{}, ErrURIOptions }
	p, _ := Defaults(Easysearch)
	if u.Path != "" && u.Path != "/" || u.RawPath != "" || u.ForceQuery || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fail()
	}
	p.Host = u.Hostname()
	if raw := u.Port(); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 16)
		if err != nil || n == 0 {
			return fail()
		}
		p.Port = uint16(n)
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	for key, values := range options {
		if len(values) != 1 || values[0] == "" {
			return fail()
		}
		switch key {
		case "auth":
			p.EasysearchOptions.Auth = values[0]
		case "sslmode":
			p.TLSMode = values[0]
		case "ca_file":
			p.EasysearchOptions.CAFile = values[0]
		case "server_name":
			p.EasysearchOptions.ServerName = values[0]
		default:
			return fail()
		}
	}
	if p.EasysearchOptions.Auth != "basic" {
		p.Username = ""
	}
	secret := ""
	if u.User != nil {
		if p.EasysearchOptions.Auth == "none" {
			return fail()
		}
		p.Username = u.User.Username()
		secret, _ = u.User.Password()
	}
	p.Name = "Easysearch · " + p.Host
	if len([]rune(p.Name)) > 120 {
		p.Name = string([]rune(p.Name)[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail()
	}
	cfg, err := EasysearchProfileConfig(p)
	if err != nil || ValidateEasysearchCredential(cfg, secret, false) != nil {
		return fail()
	}
	return URIImport{Profile: p, Password: secret}, nil
}
