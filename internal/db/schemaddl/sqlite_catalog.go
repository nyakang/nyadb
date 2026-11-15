package schemaddl

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// SQLiteCatalog retains the complete persistent catalog independently of the
// editable table. SQLNull distinguishes implicit indexes from empty SQL text.
// Row values and sqlite_sequence contents are not schema revisions. Connection
// state and temporary triggers must be checked on the original executor.
type SQLiteCatalog struct {
	Version int64
	Entries []SQLiteCatalogEntry
}

type SQLiteCatalogEntry struct {
	Type, Name, Table, SQL string
	Root                   int64
	SQLNull                bool
}

func (p *SQLiteCatalog) Clone() *SQLiteCatalog {
	if p == nil {
		return nil
	}
	x := *p
	x.Entries = slices.Clone(p.Entries)
	return &x
}

// Validate rejects partial, malformed and ambiguously ordered catalog records.
// Persistent SQLite type/name ordering uses byte comparisons, not Unicode case
// folding; identifiers that differ outside ASCII can be distinct objects.
func (p SQLiteCatalog) Validate() error {
	if p.Version < -1<<31 || p.Version > 1<<31-1 || len(p.Entries) > 10000 {
		return errors.New("complete SQLite catalog exceeds supported bounds")
	}
	bytes := 0
	for i, e := range p.Entries {
		if e.Type != "table" && e.Type != "index" && e.Type != "view" && e.Type != "trigger" || e.Root < 0 || e.Root > 1<<32-2 || e.SQLNull && e.SQL != "" {
			return errors.New("invalid original SQLite catalog record")
		}
		for _, name := range []string{e.Name, e.Table} {
			if name == "" || len(name) > 4096 || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
				return errors.New("invalid original SQLite catalog identity")
			}
		}
		bytes += len(e.Type) + len(e.Name) + len(e.Table) + len(e.SQL)
		if len(e.SQL) > 1<<20 || bytes > 16<<20 || !utf8.ValidString(e.SQL) || strings.ContainsRune(e.SQL, 0) {
			return errors.New("complete SQLite catalog exceeds supported text bounds")
		}
		if i > 0 {
			previous := p.Entries[i-1]
			if previous.Type > e.Type || previous.Type == e.Type && previous.Name >= e.Name {
				return errors.New("original SQLite catalog order is incomplete or ambiguous")
			}
		}
	}
	return nil
}
