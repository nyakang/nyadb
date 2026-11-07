package connections

import (
	"fmt"
	"net"
	"strings"
)

// GaussDBEndpoint is one native server endpoint, not a URI or a credential.
type GaussDBEndpoint struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
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
