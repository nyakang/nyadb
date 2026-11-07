package connections

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// GaussDBEndpoint is one native server endpoint, not a URI or a credential.
type GaussDBEndpoint struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}

// GaussDBOptions stores additional host:port endpoints as canonical public
// text, keeping profile snapshots independent without sharing mutable slices.
type GaussDBOptions struct {
	Version             int    `json:"version"`
	AdditionalEndpoints string `json:"additional_endpoints,omitempty"`
	TargetSessionAttrs  string `json:"target_session_attrs,omitempty"`
}

func DefaultGaussDBOptions() GaussDBOptions {
	return GaussDBOptions{Version: 1, TargetSessionAttrs: "read-write"}
}

// ParseGaussDBEndpoints accepts comma-separated hosts, host:port or bracketed
// IPv6:port. An omitted port uses the primary profile port.
func ParseGaussDBEndpoints(raw string, defaultPort uint16) ([]GaussDBEndpoint, error) {
	if defaultPort == 0 || !validEngineText(raw, 8192) {
		return nil, fmt.Errorf("%w: invalid GaussDB endpoint list", ErrInvalid)
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 16 {
		return nil, fmt.Errorf("%w: too many GaussDB endpoints", ErrInvalid)
	}
	out := make([]GaussDBEndpoint, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		host, port := part, defaultPort
		if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
			host = part[1 : len(part)-1]
			if net.ParseIP(host) == nil || !strings.Contains(host, ":") {
				return nil, fmt.Errorf("%w: invalid GaussDB IPv6 endpoint", ErrInvalid)
			}
		} else if strings.HasPrefix(part, "[") || strings.Count(part, ":") == 1 {
			var portText string
			var err error
			host, portText, err = net.SplitHostPort(part)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid GaussDB host and port", ErrInvalid)
			}
			n, err := strconv.ParseUint(portText, 10, 16)
			if err != nil || n == 0 {
				return nil, fmt.Errorf("%w: invalid GaussDB endpoint port", ErrInvalid)
			}
			port = uint16(n)
		}
		out = append(out, GaussDBEndpoint{Host: host, Port: port})
	}
	checked, err := NormalizeGaussDBConfig(GaussDBConfig{Endpoints: out, Database: "_", Username: "_"})
	return checked.Endpoints, err
}

func GaussDBProfileConfig(p Profile) (GaussDBConfig, error) {
	if p.Kind != GaussDB {
		return GaussDBConfig{}, fmt.Errorf("%w: native GaussDB profile required", ErrInvalid)
	}
	a := p.GaussDBOptions
	if a == (GaussDBOptions{}) {
		a = DefaultGaussDBOptions()
	}
	if a.Version != 1 || p.Port == 0 {
		return GaussDBConfig{}, fmt.Errorf("%w: invalid GaussDB profile options", ErrInvalid)
	}
	endpoints := []GaussDBEndpoint{{Host: p.Host, Port: p.Port}}
	if a.AdditionalEndpoints != "" {
		additional, err := ParseGaussDBEndpoints(a.AdditionalEndpoints, p.Port)
		if err != nil {
			return GaussDBConfig{}, err
		}
		endpoints = append(endpoints, additional...)
	}
	return NormalizeGaussDBConfig(GaussDBConfig{Endpoints: endpoints, Database: p.Database, Username: p.Username, TLSMode: p.TLSMode, Options: p.PostgreSQLOptions, TargetSessionAttrs: a.TargetSessionAttrs})
}

func normalizeGaussDBProfile(p Profile) (Profile, error) {
	c, err := GaussDBProfileConfig(p)
	if err != nil {
		return p, err
	}
	p.Host, p.Port, p.TLSMode, p.PostgreSQLOptions = c.Endpoints[0].Host, c.Endpoints[0].Port, c.TLSMode, c.Options
	p.GaussDBOptions = GaussDBOptions{Version: 1, TargetSessionAttrs: c.TargetSessionAttrs}
	var additional []string
	for _, endpoint := range c.Endpoints[1:] {
		additional = append(additional, net.JoinHostPort(endpoint.Host, strconv.Itoa(int(endpoint.Port))))
	}
	p.GaussDBOptions.AdditionalEndpoints = strings.Join(additional, ",")
	return p, nil
}

// GaussDBConfig is the public native transport configuration. Runtime product
// verification and workbench registration remain separate from transport setup.
// Credentials are supplied separately when opening a connector.
type GaussDBConfig struct {
	Endpoints          []GaussDBEndpoint `json:"endpoints"`
	Database           string            `json:"database"`
	Username           string            `json:"username"`
	TLSMode            string            `json:"tls_mode"`
	TargetSessionAttrs string            `json:"target_session_attrs,omitempty"`
	Options            PostgreSQLOptions `json:"options"`
}

func NormalizeGaussDBConfig(c GaussDBConfig) (GaussDBConfig, error) {
	c.Endpoints = append([]GaussDBEndpoint(nil), c.Endpoints...)
	if len(c.Endpoints) == 0 || len(c.Endpoints) > 16 {
		return c, fmt.Errorf("%w: GaussDB requires 1 to 16 endpoints", ErrInvalid)
	}
	seen := make(map[string]bool, len(c.Endpoints))
	for i, endpoint := range c.Endpoints {
		endpoint.Host = strings.TrimSpace(endpoint.Host)
		if !validEngineText(endpoint.Host, 255) || endpoint.Host == "" || strings.ContainsAny(endpoint.Host, "@/?!#\\[]=,' \t\r\n") {
			return c, fmt.Errorf("%w: invalid GaussDB endpoint", ErrInvalid)
		}
		if strings.Contains(endpoint.Host, ":") && net.ParseIP(endpoint.Host) == nil {
			return c, fmt.Errorf("%w: invalid GaussDB IPv6 endpoint", ErrInvalid)
		}
		if endpoint.Port == 0 {
			endpoint.Port = 5432
		}
		key := strings.ToLower(net.JoinHostPort(endpoint.Host, fmt.Sprint(endpoint.Port)))
		if seen[key] {
			return c, fmt.Errorf("%w: duplicate GaussDB endpoint", ErrInvalid)
		}
		seen[key] = true
		c.Endpoints[i] = endpoint
	}
	if c.Database == "" || c.Username == "" || !validEngineText(c.Database, 4096) || !validEngineText(c.Username, 128) {
		return c, fmt.Errorf("%w: GaussDB requires a database and username", ErrInvalid)
	}
	c.TLSMode = strings.ToLower(strings.TrimSpace(c.TLSMode))
	if c.TLSMode == "" {
		c.TLSMode = "verify-full"
	}
	switch c.TLSMode {
	case "disable", "prefer", "require", "verify-ca", "verify-full":
	default:
		return c, fmt.Errorf("%w: unsupported GaussDB TLS mode", ErrInvalid)
	}
	if c.TargetSessionAttrs == "" {
		c.TargetSessionAttrs = "any"
	}
	switch c.TargetSessionAttrs {
	case "any", "read-write", "read-only", "primary", "standby", "prefer-standby":
	default:
		return c, fmt.Errorf("%w: unsupported GaussDB target session attributes", ErrInvalid)
	}
	var err error
	c.Options, err = NormalizePostgreSQLOptions(c.Options)
	if err != nil {
		return c, err
	}
	if c.Options.SSH.Enabled || c.Options.HTTP.Enabled || c.Options.ToolsDirectory != "" || c.Options.LockTimeoutMS != 0 {
		return c, fmt.Errorf("%w: GaussDB tunnels, PostgreSQL backup tools and lock timeout are unavailable", ErrInvalid)
	}
	if c.TLSMode == "disable" && (c.Options.CAFile != "" || c.Options.CertificateFile != "" || c.Options.KeyFile != "") {
		return c, fmt.Errorf("%w: GaussDB certificates require TLS", ErrInvalid)
	}
	return c, nil
}
