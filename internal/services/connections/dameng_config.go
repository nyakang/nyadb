package connections

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

// DamengOptions contains only public native client settings. The caller
// installs the client and provides authentication secrets separately.
type DamengOptions struct {
	Version                 int    `json:"version"`
	LibraryFile             string `json:"library_file"`
	DefaultSchema           string `json:"default_schema,omitempty"`
	ConnectTimeoutSeconds   int    `json:"connect_timeout_seconds"`
	OperationTimeoutSeconds int    `json:"operation_timeout_seconds"`
	MaxConnections          int    `json:"max_connections"`
	SQLMaxConnections       int    `json:"sql_max_connections"`
	CAFile                  string `json:"ca_file,omitempty"`
	CertificateFile         string `json:"certificate_file,omitempty"`
	KeyFile                 string `json:"key_file,omitempty"`
	ServerName              string `json:"server_name,omitempty"`
}

func DefaultDamengOptions() DamengOptions {
	return DamengOptions{Version: 1, ConnectTimeoutSeconds: 10, OperationTimeoutSeconds: 10, MaxConnections: 8, SQLMaxConnections: 8}
}

// DamengConfig identifies one original endpoint and native catalog. It is not
// an Oracle configuration. Profile/provider registration remains separate.
type DamengConfig struct {
	Host     string        `json:"host"`
	Port     uint16        `json:"port"`
	Database string        `json:"database"`
	Username string        `json:"username"`
	TLSMode  string        `json:"tls_mode"`
	Options  DamengOptions `json:"options"`
}

func NormalizeDamengConfig(c DamengConfig) (DamengConfig, error) {
	c.Host = strings.TrimSpace(c.Host)
	if c.Host == "" || !validEngineText(c.Host, 255) || strings.ContainsAny(c.Host, "@/?!#\\[]=,' \t\r\n") || strings.Contains(c.Host, ":") && net.ParseIP(c.Host) == nil {
		return c, fmt.Errorf("%w: invalid explicit DM8 endpoint", ErrInvalid)
	}
	if c.Port == 0 {
		c.Port = 5236
	}
	if c.Database == "" || c.Username == "" || !validEngineText(c.Database, 128) || !validEngineText(c.Username, 128) {
		return c, fmt.Errorf("%w: DM8 requires an explicit catalog and username", ErrInvalid)
	}
	c.TLSMode = strings.ToLower(strings.TrimSpace(c.TLSMode))
	if c.TLSMode == "" {
		c.TLSMode = "verify-full"
	}
	switch c.TLSMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return c, fmt.Errorf("%w: unsupported explicit DM8 TLS policy", ErrInvalid)
	}
	a := c.Options
	if a.Version != 1 || a.ConnectTimeoutSeconds < 1 || a.ConnectTimeoutSeconds > 120 || a.OperationTimeoutSeconds < 1 || a.OperationTimeoutSeconds > 120 || a.MaxConnections < 1 || a.MaxConnections > 64 || a.SQLMaxConnections < 1 || a.SQLMaxConnections > 64 {
		return c, fmt.Errorf("%w: invalid DM8 configuration version, timeouts or connection limits", ErrInvalid)
	}
	if a.LibraryFile == "" {
		return c, fmt.Errorf("%w: DM8 requires the caller's explicit native client path", ErrInvalid)
	}
	for _, path := range []string{a.LibraryFile, a.CAFile, a.CertificateFile, a.KeyFile} {
		if path != "" && (!validEngineText(path, 4096) || !filepath.IsAbs(path)) {
			return c, fmt.Errorf("%w: DM8 requires absolute native client and certificate paths", ErrInvalid)
		}
	}
	if !validEngineText(a.DefaultSchema, 128) || !validEngineText(a.ServerName, 255) || strings.ContainsAny(a.ServerName, "/@?#[]\\ \t\r\n") {
		return c, fmt.Errorf("%w: invalid DM8 schema or TLS peer name", ErrInvalid)
	}
	if (a.CertificateFile == "") != (a.KeyFile == "") {
		return c, fmt.Errorf("%w: DM8 client certificate and private key must be paired", ErrInvalid)
	}
	if c.TLSMode == "disable" {
		if a.CAFile != "" || a.CertificateFile != "" || a.KeyFile != "" || a.ServerName != "" {
			return c, fmt.Errorf("%w: DM8 certificate settings require TLS", ErrInvalid)
		}
	} else if a.CertificateFile == "" {
		return c, fmt.Errorf("%w: the supported DM8 certificate TLS mode requires an explicit client identity", ErrInvalid)
	}
	return c, nil
}
