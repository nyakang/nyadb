package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// PostgreSQLCatalog retains complete public catalog records separately from
// the editable structural declaration. Physical row statistics and sequence
// current values are not schema revisions. Unknown catalog attributes remain
// in Data; they must not acquire write permission through a partial form.
type PostgreSQLCatalog struct {
	ServerVersion                          int
	NativeByteOrder                        string
	Database, Namespace                    string
	DatabaseOID, NamespaceOID, RelationOID int64
	Entries                                []PostgreSQLCatalogEntry
}
type PostgreSQLCatalogEntry struct {
	Kind  string
	OID   int64
	SubID int
	Data  json.RawMessage
}
type PostgreSQLTableProperties struct {
	Kind, Persistence, Owner, AccessMethod, Tablespace     string
	ReplicaIdentity, PartitionKey, PartitionBound, Comment string
	RowSecurity, ForceRowSecurity, IsPartition             bool
	Parameters, ACL                                        []string
	Parents                                                []string
}
type PostgreSQLColumnProperties struct {
	Storage, Compression, Comment   string
	Statistics                      int
	Parameters, ForeignOptions, ACL []string
	Local                           bool
	InheritanceCount                int
}
type PostgreSQLDefinitionProperties struct {
	Enabled, Comment string
}

func (p *PostgreSQLTableProperties) Clone() *PostgreSQLTableProperties {
	if p == nil {
		return nil
	}
	x := *p
	x.Parameters = slices.Clone(p.Parameters)
	x.ACL = slices.Clone(p.ACL)
	x.Parents = slices.Clone(p.Parents)
	return &x
}
func (p *PostgreSQLColumnProperties) Clone() *PostgreSQLColumnProperties {
	if p == nil {
		return nil
	}
	x := *p
	x.Parameters = slices.Clone(p.Parameters)
	x.ForeignOptions = slices.Clone(p.ForeignOptions)
	x.ACL = slices.Clone(p.ACL)
	return &x
}
func (p *PostgreSQLDefinitionProperties) Clone() *PostgreSQLDefinitionProperties {
	if p == nil {
		return nil
	}
	x := *p
	return &x
}
func (p *PostgreSQLCatalog) Clone() *PostgreSQLCatalog {
	if p == nil {
		return nil
	}
	x := *p
	x.Entries = slices.Clone(p.Entries)
	for i := range x.Entries {
		x.Entries[i].Data = append(json.RawMessage(nil), p.Entries[i].Data...)
	}
	return &x
}

// Fingerprint is independent of query row ordering and JSON key/whitespace
// formatting. Duplicate/malformed/truncated records are errors, not baselines.
func (p PostgreSQLCatalog) Fingerprint() (string, error) {
	if p.ServerVersion < 140000 || p.DatabaseOID <= 0 || p.NamespaceOID <= 0 {
		return "", errors.New("missing PostgreSQL catalog scope or unsupported server version")
	}
	x := p.Clone()
	if len(x.Entries) > 10000 {
		return "", errors.New("complete PostgreSQL catalog exceeds record limit")
	}
	size, seen := 0, map[string]bool{}
	for i, e := range x.Entries {
		if e.Kind == "" || e.OID <= 0 || e.SubID < 0 || len(e.Data) == 0 || len(e.Data) > 1<<20 || !json.Valid(e.Data) || !utf8.Valid(e.Data) {
			return "", errors.New("invalid or oversized PostgreSQL catalog record")
		}
		key := fmt.Sprintf("%s:%d:%d", e.Kind, e.OID, e.SubID)
		if seen[key] {
			return "", errors.New("duplicate PostgreSQL catalog identity")
		}
		seen[key] = true
		d := json.NewDecoder(strings.NewReader(string(e.Data)))
		d.UseNumber()
		var v map[string]any
		if err := d.Decode(&v); err != nil || v == nil {
			return "", errors.New("invalid PostgreSQL catalog JSON object")
		}
		var extra any
		if d.Decode(&extra) == nil {
			return "", errors.New("multiple PostgreSQL catalog JSON objects")
		}
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		x.Entries[i].Data = b
		size += len(b) + len(key)
		if size > 16<<20 {
			return "", errors.New("complete PostgreSQL catalog exceeds size limit")
		}
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

func PostgreSQLIdentifier(name string) error {
	if err := Identifier(name); err != nil {
		return err
	}
	if len(name) > 63 || !utf8.ValidString(name) {
		return errors.New("PostgreSQL identifier exceeds 63 UTF-8 bytes")
	}
	return nil
}

type postgresRelationRecord struct {
	Name             string   `json:"relname"`
	Kind             string   `json:"relkind"`
	Persistence      string   `json:"relpersistence"`
	Owner            string   `json:"owner_name"`
	AccessMethod     string   `json:"access_method"`
	Tablespace       string   `json:"tablespace_name"`
	ReplicaIdentity  string   `json:"relreplident"`
	Parameters       []string `json:"reloptions"`
	ACL              []string `json:"relacl"`
	RowSecurity      bool     `json:"relrowsecurity"`
	ForceRowSecurity bool     `json:"relforcerowsecurity"`
	IsPartition      bool     `json:"relispartition"`
	PartitionKey     string   `json:"partition_key"`
	PartitionBound   string   `json:"partition_bound"`
	Comment          string   `json:"comment"`
	Parents          []string `json:"parents"`
}
type postgresColumnRecord struct {
	Number           int                     `json:"attnum"`
	Name             string                  `json:"attname"`
	Dropped          bool                    `json:"attisdropped"`
	Type             string                  `json:"type_sql"`
	NotNull          bool                    `json:"attnotnull"`
	Identity         string                  `json:"attidentity"`
	Generated        string                  `json:"attgenerated"`
	Default          string                  `json:"default_sql"`
	Collation        string                  `json:"collation_sql"`
	Storage          string                  `json:"attstorage"`
	Compression      string                  `json:"attcompression"`
	Statistics       *int                    `json:"attstattarget"`
	Parameters       []string                `json:"attoptions"`
	ForeignOptions   []string                `json:"attfdwoptions"`
	ACL              []string                `json:"attacl"`
	Comment          string                  `json:"comment"`
	Sequence         *postgresSequenceRecord `json:"identity_sequence"`
	Local            bool                    `json:"attislocal"`
	InheritanceCount int                     `json:"attinhcount"`
}
type postgresSequenceRecord struct {
	Schema    string `json:"schema"`
	Name      string `json:"name"`
	Start     int64  `json:"seqstart"`
	Increment int64  `json:"seqincrement"`
	Minimum   int64  `json:"seqmin"`
	Maximum   int64  `json:"seqmax"`
	Cache     int64  `json:"seqcache"`
	Cycle     bool   `json:"seqcycle"`
}
type postgresDefinitionRecord struct {
	Name       string `json:"name"`
	SQL        string `json:"definition"`
	Internal   bool   `json:"internal"`
	Constraint int64  `json:"constraint_oid"`
	Enabled    string `json:"enabled"`
	Comment    string `json:"comment"`
	Relation   int64  `json:"relation_oid"`
	Type       string `json:"contype"`
}

// Table extracts an editable declaration from the target records. All original
// metadata remains available in the catalog for conflict and preservation checks.
func (p PostgreSQLCatalog) Table() (Table, error) {
	t := Table{}
	if p.RelationOID == 0 {
		return t, nil
	}
	var columns []struct {
		entry  PostgreSQLCatalogEntry
		column postgresColumnRecord
	}
	for _, e := range p.Entries {
		if e.Kind == "relation" && e.OID == p.RelationOID {
			var r postgresRelationRecord
			if err := json.Unmarshal(e.Data, &r); err != nil {
				return t, err
			}
			if r.Kind != "r" && r.Kind != "p" {
				return t, errors.New("target is not an ordinary or partitioned PostgreSQL table")
			}
			t.Name = r.Name
			t.Postgres = &PostgreSQLTableProperties{Kind: r.Kind, Persistence: r.Persistence, Owner: r.Owner, AccessMethod: r.AccessMethod, Tablespace: r.Tablespace, ReplicaIdentity: r.ReplicaIdentity, Parameters: r.Parameters, ACL: r.ACL, RowSecurity: r.RowSecurity, ForceRowSecurity: r.ForceRowSecurity, IsPartition: r.IsPartition, PartitionKey: r.PartitionKey, PartitionBound: r.PartitionBound, Comment: r.Comment, Parents: r.Parents}
		}
		if e.Kind == "column" && e.OID == p.RelationOID {
			var c postgresColumnRecord
			if err := json.Unmarshal(e.Data, &c); err != nil {
				return t, err
			}
			if !c.Dropped {
				columns = append(columns, struct {
					entry  PostgreSQLCatalogEntry
					column postgresColumnRecord
				}{e, c})
			}
		}
	}
	if t.Name == "" || t.Postgres == nil {
		return t, errors.New("target relation catalog record missing")
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].column.Number < columns[j].column.Number })
	for _, item := range columns {
		c := item.column
		if c.Number <= 0 || c.Number != item.entry.SubID || c.Type == "" {
			return t, errors.New("invalid complete column catalog record")
		}
		body := c.Type
		if c.Collation != "" {
			body += " COLLATE " + c.Collation
		}
		if c.Identity != "" {
			mode := "BY DEFAULT"
			if c.Identity == "a" {
				mode = "ALWAYS"
			} else if c.Identity != "d" {
				return t, errors.New("unknown PostgreSQL identity mode")
			}
			body += " GENERATED " + mode + " AS IDENTITY"
			if c.Sequence == nil {
				return t, errors.New("identity sequence settings missing")
			}
			s := c.Sequence
			if PostgreSQLIdentifier(s.Schema) != nil || PostgreSQLIdentifier(s.Name) != nil || s.Increment == 0 || s.Cache <= 0 {
				return t, errors.New("invalid identity sequence catalog")
			}
			body += fmt.Sprintf(" (SEQUENCE NAME %s.%s INCREMENT BY %d START WITH %d MINVALUE %d MAXVALUE %d CACHE %d", Quote(s.Schema), Quote(s.Name), s.Increment, s.Start, s.Minimum, s.Maximum, s.Cache)
			if s.Cycle {
				body += " CYCLE)"
			} else {
				body += " NO CYCLE)"
			}
		} else if c.Generated != "" {
			mode := "STORED"
			if c.Generated == "v" {
				mode = "VIRTUAL"
			} else if c.Generated != "s" {
				return t, errors.New("unknown PostgreSQL generated column mode")
			}
			if c.Default == "" {
				return t, errors.New("generated expression missing")
			}
			body += " GENERATED ALWAYS AS (" + c.Default + ") " + mode
		} else if c.Default != "" {
			body += " DEFAULT " + c.Default
		}
		if c.NotNull {
			body += " NOT NULL"
		}
		statistics := -1
		if c.Statistics != nil {
			statistics = *c.Statistics
		}
		t.Clauses = append(t.Clauses, Clause{ID: fmt.Sprintf("column:%d", c.Number), Kind: "column", Name: c.Name, SQL: body, Postgres: &PostgreSQLColumnProperties{Storage: c.Storage, Compression: c.Compression, Statistics: statistics, Parameters: c.Parameters, ForeignOptions: c.ForeignOptions, ACL: c.ACL, Comment: c.Comment, Local: c.Local, InheritanceCount: c.InheritanceCount}})
	}
	for _, e := range p.Entries {
		if e.Kind != "constraint" && e.Kind != "index" && e.Kind != "trigger" {
			continue
		}
		var r postgresDefinitionRecord
		if err := json.Unmarshal(e.Data, &r); err != nil {
			return t, err
		}
		if r.Relation != p.RelationOID {
			continue
		}
		if e.Kind == "constraint" {
			if r.Type == "t" || r.Type == "n" {
				continue
			} // Constraint triggers are definitions; PG 18 NOT NULL belongs to its column.
			if r.SQL == "" || r.Name == "" {
				return t, errors.New("complete constraint definition missing")
			}
			t.Clauses = append(t.Clauses, Clause{ID: fmt.Sprintf("constraint:%d", e.OID), Kind: "constraint", Name: r.Name, SQL: "CONSTRAINT " + Quote(r.Name) + " " + r.SQL})
		} else if !r.Internal && (e.Kind == "trigger" || r.Constraint == 0) {
			if r.SQL == "" || r.Name == "" {
				return t, errors.New("complete index/trigger definition missing")
			}
			t.Definitions = append(t.Definitions, Definition{Name: r.Name, Kind: e.Kind, SQL: r.SQL, Postgres: &PostgreSQLDefinitionProperties{Enabled: r.Enabled, Comment: r.Comment}})
		}
	}
	return t, nil
}

// PostgreSQLStructuralDDL is a structural declaration, not a full dump. The
// original ACL grantors, security labels, inheritance and publication metadata
// remain in Catalog. A future migration must preserve or explicitly edit them.
func PostgreSQLStructuralDDL(t Table, schema string) (string, error) {
	if PostgreSQLIdentifier(t.Name) != nil || PostgreSQLIdentifier(schema) != nil {
		return "", errors.New("invalid PostgreSQL table identity")
	}
	var clauses []string
	for _, c := range t.Clauses {
		if c.Kind == "column" {
			if err := PostgreSQLIdentifier(c.Name); err != nil {
				return "", err
			}
			clauses = append(clauses, Quote(c.Name)+" "+c.SQL)
		}
	}
	for _, c := range t.Clauses {
		if c.Kind == "constraint" {
			clauses = append(clauses, c.SQL)
		}
	}
	kind := "CREATE TABLE "
	if t.Postgres != nil && t.Postgres.Persistence == "u" {
		kind = "CREATE UNLOGGED TABLE "
	}
	ddl := kind + Quote(schema) + "." + Quote(t.Name) + " (\n  " + strings.Join(clauses, "\n, ") + "\n)"
	if t.Postgres != nil {
		p := t.Postgres
		if p.IsPartition {
			if len(p.Parents) != 1 || p.PartitionBound == "" {
				return "", errors.New("partition parent or bound missing")
			}
			ddl = kind + Quote(schema) + "." + Quote(t.Name) + " PARTITION OF " + p.Parents[0] + " " + p.PartitionBound
		} else if len(p.Parents) > 0 {
			ddl += " INHERITS (" + strings.Join(p.Parents, ", ") + ")"
		}
		if p.PartitionKey != "" {
			ddl += " PARTITION BY " + p.PartitionKey
		}
		if p.AccessMethod != "" && p.Kind == "r" {
			ddl += " USING " + Quote(p.AccessMethod)
		}
		if len(p.Parameters) > 0 {
			ddl += " WITH (" + strings.Join(p.Parameters, ", ") + ")"
		}
		if p.Tablespace != "" {
			ddl += " TABLESPACE " + Quote(p.Tablespace)
		}
	}
	for _, d := range t.Definitions {
		ddl += ";\n\n" + d.SQL
	}
	if len(ddl) > 16<<20 {
		return "", errors.New("complete structural declaration exceeds size limit")
	}
	return ddl, nil
}
