package connections

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MySQLAdvanced contains non-secret MySQL connection preferences.
// This data is saved as configuration only; the database driver has not
// implemented these behaviors yet. No passwords, credentials or raw DSNs.
type MySQLAdvanced struct {
	SettingsLocation      string             `json:"settingsLocation,omitempty"`
	ClientDriver          string             `json:"clientDriver"`
	Charset               string             `json:"charset"`
	KeepAlive             bool               `json:"keepAlive,omitempty"`
	KeepAliveSeconds      int                `json:"keepAliveSeconds"`
	ConnectTimeout        bool               `json:"connectTimeout"`
	ConnectTimeoutSeconds int                `json:"connectTimeoutSeconds"`
	ReadTimeout           bool               `json:"readTimeout,omitempty"`
	ReadTimeoutSeconds    int                `json:"readTimeoutSeconds"`
	WriteTimeout          bool               `json:"writeTimeout"`
	WriteTimeoutSeconds   int                `json:"writeTimeoutSeconds"`
	Compression           bool               `json:"compression,omitempty"`
	AutoConnect           bool               `json:"autoConnect,omitempty"`
	NamedPipe             bool               `json:"namedPipe,omitempty"`
	PipeName              string             `json:"pipeName,omitempty"`
	LimitSessions         bool               `json:"limitSessions,omitempty"`
	MaxSessions           int                `json:"maxSessions"`
	UseCustomDatabaseList bool               `json:"useCustomDatabaseList,omitempty"`
	Compatibility         MySQLCompatibility `json:"compatibility,omitempty"`
	SSL                   MySQLSSL           `json:"ssl,omitempty"`
}

func DefaultMySQLAdvanced() MySQLAdvanced {
	return MySQLAdvanced{
		ClientDriver: "默认", Charset: "自动",
		KeepAliveSeconds: 240,
		ConnectTimeout:   true, ConnectTimeoutSeconds: 30,
		ReadTimeoutSeconds: 30, WriteTimeout: true,
		WriteTimeoutSeconds: 30, MaxSessions: 10,
		Compatibility: DefaultMySQLCompatibility(),
	}
}

// MySQLCharsets are standard MySQL charset names. The original menu's
// beginning (自动, None, armscii8, ascii, big5...) was runtime-observed;
// the remaining choices are independently curated, not fully differential-verified.
var MySQLCharsets = []string{
	"自动", "None", "armscii8", "ascii", "big5", "binary",
	"cp1250", "cp1251", "cp1256", "cp1257", "cp850", "cp852",
	"cp866", "dec8", "eucjpms", "euckr", "gb18030",
	"gb2312", "gbk", "geostd8", "greek", "hebrew", "hp8",
	"keybcs2", "koi8r", "koi8u", "latin1", "latin2", "latin5",
	"latin7", "macce", "macroman", "sjis", "swe7", "tis620",
	"ucs2", "ujis", "utf16", "utf16le", "utf32", "utf8",
	"utf8mb3", "utf8mb4",
}

func normalizeMySQLAdvanced(a MySQLAdvanced) (MySQLAdvanced, error) {
	if a == (MySQLAdvanced{}) {
		a = DefaultMySQLAdvanced()
	}
	if a.Compatibility == (MySQLCompatibility{}) {
		a.Compatibility = DefaultMySQLCompatibility()
	}
	if err := validateMySQLCompatibility(a.Compatibility); err != nil {
		return a, err
	}
	if err := validateMySQLSSL(a.SSL); err != nil {
		return a, err
	}
	if a.ClientDriver != "默认" && a.ClientDriver != "旧版" {
		return a, fmt.Errorf("%w: unsupported MySQL client driver", ErrInvalid)
	}
	validCharset := false
	for _, option := range MySQLCharsets {
		if a.Charset == option {
			validCharset = true
			break
		}
	}
	if !validCharset {
		return a, fmt.Errorf("%w: unsupported MySQL client character set", ErrInvalid)
	}
	for _, n := range []int{a.KeepAliveSeconds, a.ConnectTimeoutSeconds, a.ReadTimeoutSeconds, a.WriteTimeoutSeconds} {
		if n < 1 || n > 86400 {
			return a, fmt.Errorf("%w: MySQL timeout must be between 1 and 86400 seconds", ErrInvalid)
		}
	}
	if a.MaxSessions < 1 || a.MaxSessions > 10000 {
		return a, fmt.Errorf("%w: MySQL session limit must be between 1 and 10000", ErrInvalid)
	}
	for _, val := range []string{a.SettingsLocation, a.PipeName} {
		if strings.ContainsRune(val, 0) || utf8.RuneCountInString(val) > 4096 {
			return a, fmt.Errorf("%w: invalid MySQL path or pipe name", ErrInvalid)
		}
	}
	return a, nil
}
