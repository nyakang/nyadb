package connections

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// parseH2URI maps supported native JDBC settings into typed options. The raw
// connection string is never retained or passed through to a driver.
func parseH2URI(raw string) (URIImport, error) {
	fail := func(err error) (URIImport, error) { return URIImport{}, err }
	parts := strings.Split(raw[len("jdbc:h2:"):], ";")
	p, err := Defaults(H2)
	if err != nil {
		return fail(ErrURI)
	}
	p.Host, p.Port = "", 0
	a := &p.H2Options
	identity := parts[0]
	lower := strings.ToLower(identity)
	switch {
	case strings.HasPrefix(lower, "file:"):
		a.Mode = "file"
		p.Database, err = url.PathUnescape(identity[len("file:"):])
		if err == nil {
			p.Database = H2FileBasePath(filepath.FromSlash(p.Database))
		}
	case strings.HasPrefix(lower, "mem:"):
		a.Mode, a.RequireExisting, a.AutoServer = "memory", false, false
		p.Database, err = url.PathUnescape(identity[len("mem:"):])
	case strings.HasPrefix(lower, "tcp://"), strings.HasPrefix(lower, "ssl://"):
		var u *url.URL
		u, err = url.Parse(identity)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || strings.HasSuffix(u.Host, ":") || strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
			return fail(ErrURI)
		}
		a.Mode, a.AutoServer = strings.ToLower(u.Scheme), false
		p.Host, p.Port = u.Hostname(), 9092
		p.Database = strings.TrimPrefix(u.Path, "/")
		if port := u.Port(); port != "" {
			n, parseErr := strconv.ParseUint(port, 10, 16)
			if parseErr != nil || n == 0 {
				return fail(ErrURI)
			}
			p.Port = uint16(n)
		}
	default:
		return fail(ErrURIOptions)
	}
	if err != nil {
		return fail(ErrURI)
	}
	password := ""
	seen := map[string]bool{}
	for _, item := range parts[1:] {
		key, value, ok := strings.Cut(item, "=")
		key = strings.ToUpper(strings.TrimSpace(key))
		if !ok || key == "" || seen[key] {
			return fail(ErrURIOptions)
		}
		seen[key] = true
		value, err = url.PathUnescape(value)
		if err != nil {
			return fail(ErrURI)
		}
		switch key {
		case "USER":
			p.Username = value
		case "PASSWORD":
			if len(value) > 512 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
				return fail(ErrURI)
			}
			password = value
		case "AUTO_SERVER", "IFEXISTS":
			if !strings.EqualFold(value, "true") && !strings.EqualFold(value, "false") {
				return fail(ErrURIOptions)
			}
			if key == "AUTO_SERVER" {
				if a.Mode != "file" {
					return fail(ErrURIOptions)
				}
				a.AutoServer = strings.EqualFold(value, "true")
			} else {
				a.RequireExisting = strings.EqualFold(value, "true")
			}
		case "ACCESS_MODE_DATA":
			if a.Mode != "file" || value != "r" && value != "rw" {
				return fail(ErrURIOptions)
			}
			a.ReadOnly = value == "r"
		case "DB_CLOSE_DELAY":
			if a.Mode != "memory" || value != "-1" {
				return fail(ErrURIOptions)
			}
		default:
			return fail(ErrURIOptions)
		}
	}
	if a.ReadOnly {
		if seen["AUTO_SERVER"] && a.AutoServer {
			return fail(ErrURIOptions)
		}
		a.AutoServer = false
	}
	p.Name = "H2 · " + p.Database
	if runes := []rune(p.Name); len(runes) > 120 {
		p.Name = string(runes[:120])
	}
	p, err = NormalizeProfile(p)
	if err != nil {
		return fail(ErrURI)
	}
	return URIImport{Profile: p, Password: password}, nil
}
