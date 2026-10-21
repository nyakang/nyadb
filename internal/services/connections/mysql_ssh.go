package connections

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MySQLSSH stores non-secret SSH tunnel metadata only. Authentication
// passwords, passphrases, and private key contents are never serialized.
type MySQLSSH struct {
	Enabled    bool   `json:"enabled,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port"`
	Username   string `json:"username,omitempty"`
	AuthMethod string `json:"authMethod"`
	PrivateKey string `json:"privateKey,omitempty"`
}

const (
	MySQLSSHPassword       = "密码"
	MySQLSSHPublicKey      = "公钥"
	MySQLSSHPasswordAndKey = "密码和公钥"
)

var MySQLSSHAuthMethods = []string{
	MySQLSSHPassword, MySQLSSHPublicKey, MySQLSSHPasswordAndKey,
}

func DefaultMySQLSSH() MySQLSSH {
	return MySQLSSH{Port: 22, AuthMethod: MySQLSSHPassword}
}

func normalizeMySQLSSH(cfg MySQLSSH) (MySQLSSH, error) {
	if cfg == (MySQLSSH{}) {
		cfg = DefaultMySQLSSH()
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return cfg, fmt.Errorf("%w: MySQL SSH port must be between 1 and 65535", ErrInvalid)
	}
	validMethod := false
	for _, method := range MySQLSSHAuthMethods {
		if cfg.AuthMethod == method {
			validMethod = true
			break
		}
	}
	if !validMethod {
		return cfg, fmt.Errorf("%w: unsupported MySQL SSH authentication method", ErrInvalid)
	}
	for _, part := range []string{cfg.Host, cfg.Username, cfg.PrivateKey} {
		if strings.ContainsRune(part, 0) ||
			strings.ContainsAny(part, "\r\n") ||
			utf8.RuneCountInString(part) > 4096 {
			return cfg, fmt.Errorf("%w: invalid MySQL SSH field", ErrInvalid)
		}
	}
	return cfg, nil
}
