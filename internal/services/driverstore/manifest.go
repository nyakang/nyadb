// Package driverstore installs immutable, explicitly selected agent bundles.
// It owns artifacts and launch lifetimes, never connection settings or secrets.
package driverstore

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"
)

var (
	ErrPackage = errors.New("invalid or changed database driver package")
	ErrBusy    = errors.New("database driver package is in use")
	ErrExists  = errors.New("database driver version is already installed")
	ErrMissing = errors.New("database driver version is not installed")
)

type File struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Executable bool   `json:"executable,omitempty"`
}

// JDBCDriver names a complete, immutable custom JDBC graph within this bundle.
// It is separate from the agent/JRE launch classpath and supplies no JVM args.
type JDBCDriver struct {
	Class     string   `json:"class"`
	Classpath []string `json:"classpath"`
}

// Manifest names every payload file, including licenses and provenance. Java
// bundles can include their own JRE; native bundles name their own executable.
// Files outside the package and arbitrary launch arguments are not accepted.
type Manifest struct {
	Format     int         `json:"format"`
	Key        string      `json:"key"`
	Version    string      `json:"version"`
	Platform   string      `json:"platform"`
	Runtime    string      `json:"runtime"`
	Executable string      `json:"executable"`
	MainClass  string      `json:"main_class,omitempty"`
	Classpath  []string    `json:"classpath,omitempty"`
	Profiles   []string    `json:"profiles"`
	Files      []File      `json:"files"`
	Licenses   []string    `json:"licenses"`
	Provenance string      `json:"provenance"`
	JDBC       *JDBCDriver `json:"jdbc,omitempty"`
}

var component = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var javaClass = regexp.MustCompile(`^[a-zA-Z_$][a-zA-Z0-9_$]*(?:\.[a-zA-Z_$][a-zA-Z0-9_$]*)+$`)

func validPath(p string) bool {
	if len(p) == 0 || len(p) > 1024 || !utf8.ValidString(p) || strings.IndexFunc(p, func(r rune) bool { return r < 32 || r == 127 }) >= 0 || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		customPath := strings.HasPrefix(p, "jdbc/") && utf8.ValidString(part) && strings.TrimSpace(part) != "" && !strings.ContainsAny(part, "<>\"|?*\x00\r\n\t") && !strings.HasSuffix(part, ".") && !strings.HasSuffix(part, " ")
		if (!component.MatchString(part) && !customPath) || part == "." || part == ".." {
			return false
		}
	}
	return p != "manifest.json" && p != "archive.sha256"
}

func validDigest(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == 64 && err == nil && strings.ToLower(s) == s
}

func decodeManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if len(raw) > 1<<20 {
		return m, ErrPackage
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF {
		return m, ErrPackage
	}
	if m.Format != 1 || !component.MatchString(m.Key) || !component.MatchString(m.Version) || m.Platform != runtime.GOOS+"-"+runtime.GOARCH || len(m.Files) == 0 || len(m.Files) > 20000 || len(m.Profiles) == 0 || len(m.Profiles) > 256 || len(m.Licenses) == 0 || len(m.Licenses) > 256 {
		return m, ErrPackage
	}
	files := map[string]File{}
	folded := map[string]bool{}
	for _, f := range m.Files {
		name := strings.ToLower(f.Path)
		if !validPath(f.Path) || !validDigest(f.SHA256) || folded[name] {
			return m, ErrPackage
		}
		files[f.Path] = f
		folded[name] = true
	}
	for _, f := range m.Files {
		for parent := path.Dir(f.Path); parent != "."; parent = path.Dir(parent) {
			if folded[strings.ToLower(parent)] {
				return m, ErrPackage
			}
		}
	}
	executable, ok := files[m.Executable]
	if !ok || !executable.Executable {
		return m, ErrPackage
	}
	if m.Runtime == "java" {
		if !javaClass.MatchString(m.MainClass) || len(m.Classpath) == 0 || len(m.Classpath) > 256 {
			return m, ErrPackage
		}
		seen := map[string]bool{}
		for _, p := range m.Classpath {
			if _, ok := files[p]; !ok || !strings.HasSuffix(p, ".jar") || seen[p] {
				return m, ErrPackage
			}
			seen[p] = true
		}
	} else if m.Runtime != "native" || m.MainClass != "" || len(m.Classpath) != 0 {
		return m, ErrPackage
	}
	if m.JDBC != nil {
		if m.Runtime != "java" || !javaClass.MatchString(m.JDBC.Class) || len(m.JDBC.Classpath) == 0 || len(m.JDBC.Classpath) > 128 {
			return m, ErrPackage
		}
		seen := map[string]bool{}
		for _, p := range m.JDBC.Classpath {
			if _, ok := files[p]; !ok || !strings.HasPrefix(p, "jdbc/") || !strings.HasSuffix(strings.ToLower(p), ".jar") || seen[p] {
				return m, ErrPackage
			}
			seen[p] = true
		}
	}
	for _, p := range append(append([]string(nil), m.Licenses...), m.Provenance) {
		if _, ok := files[p]; !ok {
			return m, ErrPackage
		}
	}
	seen := map[string]bool{}
	for _, p := range m.Profiles {
		if !component.MatchString(p) || seen[p] {
			return m, ErrPackage
		}
		seen[p] = true
	}
	return m, nil
}

func cloneManifest(m Manifest) Manifest {
	m.Files = append([]File(nil), m.Files...)
	m.Classpath = append([]string(nil), m.Classpath...)
	m.Profiles = append([]string(nil), m.Profiles...)
	m.Licenses = append([]string(nil), m.Licenses...)
	if m.JDBC != nil {
		m.JDBC = &JDBCDriver{Class: m.JDBC.Class, Classpath: append([]string(nil), m.JDBC.Classpath...)}
	}
	return m
}
