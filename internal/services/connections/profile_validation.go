package connections

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// NormalizeProfile validates public settings and applies their canonical values.
// Variants are normalized in place; callers retaining them must clone the slice.
func NormalizeProfile(p Profile) (Profile, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.GroupName = strings.TrimSpace(p.GroupName)
	if p.GroupName != "" {
		if err := ValidateGroupName(p.GroupName); err != nil {
			return p, err
		}
	}
	p.ActiveVariant = strings.TrimSpace(p.ActiveVariant)
	if len(p.Variants) > 16 {
		return p, fmt.Errorf("%w: too many connection configurations", ErrInvalid)
	}
	for i, variant := range p.Variants {
		variant.Name = strings.TrimSpace(variant.Name)
		if variant.Name == "" || utf8.RuneCountInString(variant.Name) > 120 ||
			strings.ContainsRune(variant.Name, 0) {
			return p, fmt.Errorf("%w: invalid configuration name", ErrInvalid)
		}
		for j := 0; j < i; j++ {
			if strings.EqualFold(p.Variants[j].Name, variant.Name) {
				return p, fmt.Errorf("%w: duplicate configuration name", ErrInvalid)
			}
		}
		if p.Kind != MySQL {
			return p, fmt.Errorf("%w: alternative configurations require MySQL", ErrInvalid)
		}
		checked, err := NormalizeProfile(Profile{Name: "configuration", Kind: MySQL,
			Host: variant.Host, Port: variant.Port, Database: variant.Database,
			Username: variant.Username, TLSMode: variant.TLSMode,
			MySQLAdvanced: variant.MySQLAdvanced})
		if err != nil {
			return p, fmt.Errorf("%w: configuration %s: %v", ErrInvalid, variant.Name, err)
		}
		// The host is a hostname/IP, never a URL or userinfo-bearing DSN.
		if strings.ContainsAny(checked.Host, "@/?!#") {
			return p, fmt.Errorf("%w: invalid configuration host", ErrInvalid)
		}
		variant.Host, variant.Port, variant.Database = checked.Host, checked.Port, checked.Database
		variant.Username, variant.TLSMode = checked.Username, checked.TLSMode
		variant.MySQLAdvanced = checked.MySQLAdvanced
		p.Variants[i] = variant
	}
	if p.ActiveVariant != "" {
		if p.Kind != MySQL {
			return p, fmt.Errorf("%w: active alternatives require MySQL", ErrInvalid)
		}
		found := false
		for _, variant := range p.Variants {
			if strings.EqualFold(variant.Name, p.ActiveVariant) {
				p.ActiveVariant, found = variant.Name, true
				break
			}
		}
		if !found {
			return p, fmt.Errorf("%w: active configuration does not exist", ErrInvalid)
		}
	}
	p.Host = strings.TrimSpace(p.Host)
	p.Database = strings.TrimSpace(p.Database)
	p.Username = strings.TrimSpace(p.Username)
	p.TLSMode = strings.ToLower(strings.TrimSpace(p.TLSMode))
	// Notes are user-authored multiline profile metadata, not credentials.
	if !utf8.ValidString(p.Notes) || strings.ContainsRune(p.Notes, 0) || len(p.Notes) > 64*1024 {
		return p, fmt.Errorf("%w: connection notes must be valid UTF-8, NUL-free, and at most 64 KiB", ErrInvalid)
	}
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 120 || strings.ContainsRune(p.Name, 0) {
		return p, fmt.Errorf("%w: name must contain 1 to 120 characters", ErrInvalid)
	}
	for _, v := range []string{p.Host, p.Database, p.Username, p.TLSMode} {
		if strings.ContainsRune(v, 0) {
			return p, fmt.Errorf("%w: NUL byte in field", ErrInvalid)
		}
	}
	switch p.Kind {
	case MySQL, PostgreSQL:
		if p.Kind == MySQL {
			var err error
			p.MySQLAdvanced, err = NormalizeMySQLAdvanced(p.MySQLAdvanced)
			if err != nil {
				return p, err
			}
		} else if p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: MySQL options on PostgreSQL profile", ErrInvalid)
		}
		if p.Host == "" || p.Port == 0 {
			return p, fmt.Errorf("%w: server host and port are required", ErrInvalid)
		}
		switch p.TLSMode {
		case "", "disable", "prefer", "require", "verify-ca", "verify-full":
		default:
			return p, fmt.Errorf("%w: unsupported TLS mode", ErrInvalid)
		}
	case SQLite:
		if p.MySQLAdvanced != (MySQLAdvanced{}) {
			return p, fmt.Errorf("%w: MySQL options on SQLite profile", ErrInvalid)
		}
		if p.Database == "" || p.Host != "" || p.Port != 0 || p.Username != "" || p.TLSMode != "" {
			return p, fmt.Errorf("%w: SQLite requires a file path and no server settings", ErrInvalid)
		}
	default:
		return p, fmt.Errorf("%w: unsupported database kind", ErrInvalid)
	}
	return p, nil
}
