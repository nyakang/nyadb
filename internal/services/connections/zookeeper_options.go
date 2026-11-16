package connections

import (
	"encoding/hex"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// AdditionalServers is a comma-separated list of host:port endpoints. The
// primary Host/Port stays in Profile; chroot and namespace are separate paths.
// Routes and secret/trust bytes are runtime captures, never profile contents.
type ZooKeeperOptions struct {
	Version             int    `json:"version"`
	AdditionalServers   string `json:"additional_servers,omitempty"`
	Chroot              string `json:"chroot,omitempty"`
	Namespace           string `json:"namespace,omitempty"`
	Prefix              string `json:"prefix"`
	Auth                string `json:"auth"`
	SessionTimeoutMS    int    `json:"session_timeout_ms"`
	ConnectionTimeoutMS int    `json:"connection_timeout_ms"`
	MaxBufferSize       int    `json:"max_buffer_size"`
	MaxDocuments        int    `json:"max_documents"`
	CAFile              string `json:"ca_file,omitempty"`
	ClientCertFile      string `json:"client_cert_file,omitempty"`
	ClientKeyFile       string `json:"client_key_file,omitempty"`
	DriverVersion       string `json:"driver_version,omitempty"`
	DriverSHA256        string `json:"driver_sha256,omitempty"`
	DriverDirectory     string `json:"driver_directory,omitempty"`
}

func DefaultZooKeeperOptions() ZooKeeperOptions {
	return ZooKeeperOptions{Version: 1, Prefix: "/", Auth: "none", SessionTimeoutMS: 30000, ConnectionTimeoutMS: 15000, MaxBufferSize: 32 << 20, MaxDocuments: 8}
}

func NormalizeZooKeeperOptions(a ZooKeeperOptions) (ZooKeeperOptions, error) {
	if a == (ZooKeeperOptions{}) {
		a = DefaultZooKeeperOptions()
	}
	if a.Version != 1 || a.Auth != "none" && a.Auth != "digest" && a.Auth != "sasl_digest" || a.SessionTimeoutMS < 1000 || a.SessionTimeoutMS > 3600000 || a.ConnectionTimeoutMS < 250 || a.ConnectionTimeoutMS > 300000 || a.MaxBufferSize < 1024 || a.MaxBufferSize > 256<<20 || a.MaxDocuments < 1 || a.MaxDocuments > 32 {
		return a, ErrInvalid
	}
	for _, text := range []string{a.AdditionalServers, a.Chroot, a.Namespace, a.Prefix, a.CAFile, a.ClientCertFile, a.ClientKeyFile, a.DriverDirectory} {
		if !validEngineText(text, 8192) {
			return a, ErrInvalid
		}
	}
	a.Chroot = NormalizeZNodePath(a.Chroot)
	if a.Chroot == "/" {
		a.Chroot = ""
	}
	a.Namespace = NormalizeZNodePath(a.Namespace)
	if a.Namespace == "/" {
		a.Namespace = ""
	}
	a.Prefix = NormalizeZNodePath(a.Prefix)
	if !ValidZNodePath(NormalizeZNodePath(a.Chroot+a.Namespace)) || !ValidZNodePath(a.Prefix) || !ValidZNodePath(NormalizeZNodePath(a.Chroot)) || !ValidZNodePath(NormalizeZNodePath(a.Namespace)) {
		return a, ErrInvalid
	}
	if (a.ClientCertFile == "") != (a.ClientKeyFile == "") || a.DriverDirectory != "" && !filepath.IsAbs(a.DriverDirectory) {
		return a, ErrInvalid
	}
	if a.DriverVersion != "" || a.DriverSHA256 != "" {
		if !h2VersionName.MatchString(a.DriverVersion) || len(a.DriverSHA256) != 64 || strings.ToLower(a.DriverSHA256) != a.DriverSHA256 {
			return a, ErrInvalid
		}
		if _, err := hex.DecodeString(a.DriverSHA256); err != nil {
			return a, ErrInvalid
		}
	}
	return a, nil
}

func ZooKeeperProfileConfig(p Profile) (ZooKeeperConfig, error) {
	if p.Kind != ZooKeeper || p.Port == 0 || p.Host == "" || p.Database != "" || p.MySQLAdvanced != (MySQLAdvanced{}) || !validEngineText(p.Host, 1024) || strings.ContainsAny(p.Host, "/@?#,\\ \t") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || !validEngineText(p.Username, 1024) || strings.TrimSpace(p.Username) != p.Username || strings.Contains(p.Username, ":") {
		return ZooKeeperConfig{}, ErrInvalid
	}
	a, err := NormalizeZooKeeperOptions(p.ZooKeeperOptions)
	if err != nil {
		return ZooKeeperConfig{}, err
	}
	if p.TLSMode != "disable" && p.TLSMode != "verify-full" || p.TLSMode == "disable" && (a.CAFile != "" || a.ClientCertFile != "") || a.Auth == "none" && p.Username != "" || a.Auth != "none" && p.Username == "" {
		return ZooKeeperConfig{}, ErrInvalid
	}
	connect := net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
	if a.AdditionalServers != "" {
		connect += "," + a.AdditionalServers
	}
	connect += a.Chroot
	auth := a.Auth
	if auth == "none" {
		auth = "digest"
	}
	c, _, err := NormalizeZooKeeperConfig(ZooKeeperConfig{ConnectString: connect, Namespace: a.Namespace, Prefix: a.Prefix, Username: p.Username, AuthScheme: auth, SessionTimeoutMS: a.SessionTimeoutMS, ConnectionTimeoutMS: a.ConnectionTimeoutMS, MaxBufferSize: a.MaxBufferSize, SSL: p.TLSMode == "verify-full", CACertPath: a.CAFile, ClientCertPath: a.ClientCertFile, ClientKeyPath: a.ClientKeyFile})
	return c, err
}

func normalizeZooKeeperProfile(p Profile) (Profile, error) {
	a, err := NormalizeZooKeeperOptions(p.ZooKeeperOptions)
	if err != nil {
		return p, err
	}
	p.ZooKeeperOptions = a
	if p.TLSMode == "" {
		p.TLSMode = "disable"
	}
	_, err = ZooKeeperProfileConfig(p)
	return p, err
}

func ValidateZooKeeperCredential(p Profile, secret string) error {
	a, err := NormalizeZooKeeperOptions(p.ZooKeeperOptions)
	if err != nil || p.Kind != ZooKeeper || len(secret) > 65536 || !utf8.ValidString(secret) || a.Auth == "none" && secret != "" || a.Auth == "sasl_digest" && secret == "" {
		return ErrInvalid
	}
	return nil
}
