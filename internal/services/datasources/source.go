// Package datasources provides optional, read-only native import sources.
// It does not persist connection strings, credentials, source rows or diagnostics.
package datasources

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type Kind string

const (
	ODBC    Kind = "ODBC"
	Access  Kind = "Access"
	Paradox Kind = "Paradox"
)
const MaxFieldBytes = 1 << 20
const MaxRowBytes = 16 << 20

var ErrUnavailable = errors.New("native source component unavailable")
var ErrClosed = errors.New("native source is closed")
var ErrValueLimit = errors.New("source field or row exceeds complete-value limit")
var ErrInvalidSource = errors.New("invalid import source")

// Config is an ephemeral request. ConnectionString may contain credentials:
// callers must never serialize it into preferences, logs or error messages.
type Config struct {
	Kind             Kind
	Path             string
	ConnectionString string
	Driver           string
	ToolPath         string // directory containing MDBTools programs
	LibraryPath      string // explicit driver-manager/libpx shared library
}
type Capability struct {
	Kind              Kind
	Available         bool
	Component, Detail string
}
type Table struct{ Catalog, Schema, Name string }
type Column struct {
	Name, Type string
	Nullable   bool
}
type Value struct {
	Text         string
	Bytes        []byte
	Null, Binary bool
}
type Source interface {
	Tables(context.Context) ([]Table, error)
	Columns(context.Context, Table) ([]Column, error)
	Rows(context.Context, Table) (RowReader, error)
	Close() error
}
type RowReader interface {
	Columns() []Column
	Next(context.Context) ([]Value, error)
	Close() error
}

// Error contains only a fixed action/component and a driver SQLSTATE. Raw
// driver diagnostics can echo credentials, input values, SQL and file paths.
type Error struct {
	Component, Action, SQLState string
	Cause                       error
}

func (e *Error) Error() string {
	s := e.Component + ": " + e.Action
	if e.SQLState != "" {
		s += " (SQLSTATE " + e.SQLState + ")"
	}
	return s
}
func (e *Error) Unwrap() error { return e.Cause }
func safeError(component, action string, cause error) error {
	return &Error{Component: component, Action: action, Cause: cause}
}

func Capabilities(ctx context.Context, config Config) []Capability {
	if ctx.Err() != nil {
		return nil
	}
	od, _ := findLibrary(configFor(config, ODBC), odbcLibraryNames())
	px, _ := findLibrary(configFor(config, Paradox), paradoxLibraryNames())
	_, mt := tool(config, "mdb-tables")
	_, mj := tool(config, "mdb-json")
	_, ms := tool(config, "mdb-schema")
	return []Capability{
		{ODBC, od != "", "ODBC driver manager", "Configure a DSN/connection string and an installed read-only ODBC driver."},
		{Access, (od != "" && config.Driver != "") || mt && mj && ms, "Access ODBC driver or MDBTools", "Configure an Access ODBC driver or the directory containing mdb-tables, mdb-schema and mdb-json."},
		{Paradox, px != "", "libpx", "Configure the path to the optional pxlib shared library."},
	}
}
func configFor(c Config, k Kind) Config {
	if c.Kind != k {
		c.LibraryPath = ""
	}
	return c
}
func tool(c Config, name string) (string, bool) {
	var p string
	var err error
	if c.ToolPath != "" {
		p = filepath.Join(c.ToolPath, name)
		if _, err = os.Stat(p); err == nil {
			p, err = exec.LookPath(p)
		}
	} else {
		p, err = exec.LookPath(name)
	}
	return p, err == nil
}
func findLibrary(c Config, names []string) (string, error) {
	if c.LibraryPath != "" {
		names = []string{c.LibraryPath}
	}
	for _, name := range names {
		h, err := openLibrary(name)
		if err == nil {
			_ = closeLibrary(h)
			return name, nil
		}
	}
	return "", ErrUnavailable
}
func validateFile(path string) error {
	if path == "" || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
		return ErrInvalidSource
	}
	f, err := os.Open(path)
	if err != nil {
		return safeError("source file", "cannot open source", ErrInvalidSource)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ErrInvalidSource
	}
	return nil
}
func validateTable(t Table) error {
	if t.Name == "" {
		return ErrInvalidSource
	}
	for _, v := range []string{t.Catalog, t.Schema, t.Name} {
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) || len(v) > 32767 {
			return ErrInvalidSource
		}
	}
	return nil
}
func Open(ctx context.Context, c Config) (Source, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch c.Kind {
	case ODBC:
		return openODBC(ctx, c)
	case Access:
		if err := validateFile(c.Path); err != nil {
			return nil, err
		}
		if c.Driver != "" || c.ConnectionString != "" {
			native := c
			if native.ConnectionString == "" {
				native.ConnectionString = "DRIVER={" + strings.ReplaceAll(c.Driver, "}", "}}") + "};DBQ={" + strings.ReplaceAll(c.Path, "}", "}}") + "};READONLY=1;"
			}
			if s, err := openODBC(ctx, native); err == nil {
				return s, nil
			} else if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return openMDBTools(ctx, c)
	case Paradox:
		if err := validateFile(c.Path); err != nil {
			return nil, err
		}
		return openParadox(ctx, c)
	default:
		return nil, ErrInvalidSource
	}
}
func checkRow(values []Value) error {
	total := 0
	for _, v := range values {
		n := len(v.Text) + len(v.Bytes)
		if n > MaxFieldBytes {
			return ErrValueLimit
		}
		total += n
	}
	if total > MaxRowBytes {
		return ErrValueLimit
	}
	return nil
}
func checkedColumns(cols []Column) error {
	if len(cols) == 0 || len(cols) > 128 {
		return ErrInvalidSource
	}
	seen := map[string]bool{}
	for _, c := range cols {
		if c.Name == "" || !utf8.ValidString(c.Name) || strings.ContainsRune(c.Name, 0) || seen[c.Name] {
			return ErrInvalidSource
		}
		seen[c.Name] = true
	}
	return nil
}
func unavailable(component string) error {
	return safeError(component, "component is not installed or could not be loaded", ErrUnavailable)
}
