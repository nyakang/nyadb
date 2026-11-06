package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// OpenGaussCatalog retains native catalog records, including unknown fields.
// It is deliberately independent of the PostgreSQL version and catalog model.
// No editable declaration or executable dump is inferred from these records.
type OpenGaussCatalog struct {
	Database, Namespace, Name              string
	DatabaseOID, NamespaceOID, RelationOID uint64
	RoleOID                                uint64
	CanManage, Hooks                       bool
	Entries                                []OpenGaussCatalogEntry
}

type OpenGaussCatalogEntry struct {
	Kind  string
	OID   uint64
	SubID int
	Data  json.RawMessage
}

func (p *OpenGaussCatalog) Clone() *OpenGaussCatalog {
	if p == nil {
		return nil
	}
	x := *p
	x.Entries = slices.Clone(p.Entries)
	for i := range x.Entries {
		x.Entries[i].Data = slices.Clone(p.Entries[i].Data)
	}
	return &x
}

// Fingerprint ignores only documented physical statistics and native object
// modification timestamps. OIDs, ACLs, dependencies and unknown fields remain.
// UseNumber preserves integers beyond float64 precision in native node trees.
func (p OpenGaussCatalog) Fingerprint() (string, error) {
	if p.DatabaseOID == 0 || p.NamespaceOID == 0 || p.RoleOID == 0 || p.Database == "" || PostgreSQLIdentifier(p.Namespace) != nil || PostgreSQLIdentifier(p.Name) != nil || len(p.Entries) > 10000 {
		return "", errors.New("invalid or excessive openGauss catalog scope")
	}
	rawSize := 0
	for _, e := range p.Entries {
		rawSize += len(e.Kind) + len(e.Data)
		if rawSize > 16<<20 {
			return "", errors.New("complete openGauss catalog exceeds size limit")
		}
	}
	x := p.Clone()
	size, seen := 0, map[string]bool{}
	relation, namespace := false, false
	for i, e := range x.Entries {
		if e.Kind == "" || e.OID == 0 || e.SubID < 0 || len(e.Data) == 0 || len(e.Data) > 1<<20 || !utf8.Valid(e.Data) {
			return "", errors.New("invalid or excessive openGauss catalog record")
		}
		key := fmt.Sprintf("%s:%d:%d", e.Kind, e.OID, e.SubID)
		if seen[key] {
			return "", errors.New("duplicate openGauss catalog identity")
		}
		seen[key] = true
		d := json.NewDecoder(strings.NewReader(string(e.Data)))
		d.UseNumber()
		var v map[string]any
		if err := d.Decode(&v); err != nil || v == nil {
			return "", errors.New("invalid openGauss catalog JSON object")
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return "", errors.New("multiple or incomplete openGauss catalog objects")
		}
		if e.Kind == "relation" || e.Kind == "partition" {
			for _, name := range []string{"relfilenode", "relpages", "reltuples", "relallvisible", "relfrozenxid", "relfrozenxid64", "relminmxid"} {
				delete(v, name)
			}
		}
		if e.Kind == "object" {
			delete(v, "mtime")
			delete(v, "changecsn")
		}
		relation = relation || e.Kind == "relation" && e.OID == p.RelationOID
		namespace = namespace || e.Kind == "namespace" && e.OID == p.NamespaceOID
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		x.Entries[i].Data = b
		size += len(b) + len(key)
		if size > 16<<20 {
			return "", errors.New("complete openGauss catalog exceeds size limit")
		}
	}
	if !namespace || p.RelationOID != 0 && !relation {
		return "", errors.New("openGauss catalog identity records missing")
	}
	sort.Slice(x.Entries, func(i, j int) bool {
		a, b := x.Entries[i], x.Entries[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.OID != b.OID {
			return a.OID < b.OID
		}
		return a.SubID < b.SubID
	})
	return Signature(x), nil
}
