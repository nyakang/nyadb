// Package schemaddl contains lossless, driver-independent schema drafts and
// plans. SQL fragments are definitions supplied by the user, not executable
// plans: adapters rebuild and validate a plan before executing it.
package schemaddl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

var ErrConflict = errors.New("table schema changed; read the original table again")
var ErrDataChanged = errors.New("schema conversion would change retained values or row identity")
var ErrUnsupported = errors.New("schema change is not supported safely")

type Clause struct {
	ID, Kind, Name string
	// SQL is the complete constraint, or the column definition after its name.
	SQL      string
	Postgres *PostgreSQLColumnProperties `json:",omitempty"`
}
type Definition struct {
	Name, Kind, SQL string
	Postgres        *PostgreSQLDefinitionProperties `json:",omitempty"`
}
type Table struct {
	Name, Options string
	Clauses       []Clause
	Definitions   []Definition
	Postgres      *PostgreSQLTableProperties `json:",omitempty"`
}
type Snapshot struct {
	Engine, Revision, DDL string
	Exists                bool
	Table                 Table
	Postgres              *PostgreSQLCatalog `json:",omitempty"`
	OpenGauss             *OpenGaussCatalog  `json:",omitempty"`
}

func (t Table) Clone() Table {
	t.Clauses = slices.Clone(t.Clauses)
	for i := range t.Clauses {
		t.Clauses[i].Postgres = t.Clauses[i].Postgres.Clone()
	}
	t.Definitions = slices.Clone(t.Definitions)
	for i := range t.Definitions {
		t.Definitions[i].Postgres = t.Definitions[i].Postgres.Clone()
	}
	t.Postgres = t.Postgres.Clone()
	return t
}
func (s Snapshot) Clone() Snapshot {
	s.Table = s.Table.Clone()
	s.Postgres = s.Postgres.Clone()
	s.OpenGauss = s.OpenGauss.Clone()
	return s
}
func Signature(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func Identifier(s string) error {
	if s == "" || strings.ContainsRune(s, 0) || len(s) > 1024 {
		return errors.New("invalid schema identifier")
	}
	return nil
}
func (t Table) Columns() []Clause {
	var out []Clause
	for _, c := range t.Clauses {
		if c.Kind == "column" {
			out = append(out, c)
		}
	}
	return out
}

type Mapping struct{ ID, Source, Destination string }
type Plan struct {
	Mode                 string
	SQL                  []string
	Mappings             []Mapping
	Dropped              []string
	RenameFrom, RenameTo string
}
