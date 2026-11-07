package connections

import (
	"fmt"
	"net"
	"strings"
)

// KingbaseConfig stores only public native transport settings. Workbench
// registration and supported server versions are verified separately. Database
// is explicit because legacy Kingbase installations need not contain postgres.
type KingbaseConfig struct {
	Host     string            `json:"host"`
	Port     uint16            `json:"port"`
	Database string            `json:"database"`
	Username string            `json:"username"`
	TLSMode  string            `json:"tls_mode"`
	Options  PostgreSQLOptions `json:"options"`
}

func KingbaseProfileConfig(p Profile) (KingbaseConfig, error) {
	if p.Kind != Kingbase || p.Port == 0 {
		return KingbaseConfig{}, fmt.Errorf("%w: native Kingbase profile with explicit port required", ErrInvalid)
	}
	return NormalizeKingbaseConfig(KingbaseConfig{Host: p.Host, Port: p.Port, Database: p.Database, Username: p.Username, TLSMode: p.TLSMode, Options: p.PostgreSQLOptions})
}

func NormalizeKingbaseConfig(c KingbaseConfig) (KingbaseConfig, error) {
	c.Host = strings.TrimSpace(c.Host)
	if c.Host == "" || !validEngineText(c.Host, 255) || strings.ContainsAny(c.Host, "@/?!#\\[]=,' \t\r\n") || strings.Contains(c.Host, ":") && net.ParseIP(c.Host) == nil {
		return c, fmt.Errorf("%w: invalid Kingbase endpoint", ErrInvalid)
	}
	if c.Port == 0 {
		c.Port = 54321
	}
	if c.Database == "" || c.Username == "" || !validEngineText(c.Database, 4096) || !validEngineText(c.Username, 128) {
		return c, fmt.Errorf("%w: Kingbase requires an explicit database and username", ErrInvalid)
	}
	c.TLSMode = strings.ToLower(strings.TrimSpace(c.TLSMode))
	if c.TLSMode == "" {
		c.TLSMode = "verify-full"
	}
	switch c.TLSMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return c, fmt.Errorf("%w: unsupported Kingbase TLS mode", ErrInvalid)
	}
	var err error
	c.Options, err = NormalizePostgreSQLOptions(c.Options)
	if err != nil {
		return c, err
	}
	if c.Options.SSH.Enabled || c.Options.HTTP.Enabled || c.Options.ToolsDirectory != "" || c.Options.LockTimeoutMS != 0 {
		return c, fmt.Errorf("%w: Kingbase tunnels, backup tools and lock timeout are unavailable", ErrInvalid)
	}
	if c.TLSMode == "disable" && (c.Options.CAFile != "" || c.Options.CertificateFile != "" || c.Options.KeyFile != "") {
		return c, fmt.Errorf("%w: Kingbase certificates require TLS", ErrInvalid)
	}
	return c, nil
}
