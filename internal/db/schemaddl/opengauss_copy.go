package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// OpenGaussCopyDefinition supplements the immutable native records with
// deparsed expressions. It is a copy blueprint, not an editable PG declaration.
// Sequence current values are read only by the owning execution transaction.
type OpenGaussCopyDefinition struct {
	Owner, Creator, Tablespace string
	CanCreate                  bool `json:"can_create"`
	Comment                    *string
	Columns                    []OpenGaussCopyColumn
	Indexes, Constraints       []OpenGaussCopyObject
	Triggers, Rules            []OpenGaussCopyObject
	Policies                   []OpenGaussCopyPolicy
	Sequences                  []OpenGaussCopySequence
}

type OpenGaussCopyColumn struct {
	Number             int
	Dimensions         int
	Name, Type         string
	Default, Generated string
	Collation          string
	NotNull            bool
	Comment            *string
}

type OpenGaussCopyObject struct {
	OID, IndexOID, ReferenceOID uint64
	Name, Kind, SQL, Enabled    string
	ReferenceSchema             string
	ReferenceName               string
	Internal                    bool
	Comment                     *string
}

type OpenGaussCopyPolicy struct {
	OID           uint64
	Name, Command string
	Permissive    bool
	Roles         []OpenGaussCopyRole
	Expression    string
}
type OpenGaussCopyRole struct {
	OID  uint64
	Name string
}

// Native sequences may use 128-bit bounds. Decimal integer strings avoid
// float conversion, overflow, or a PostgreSQL-only int64 sequence assumption.
type OpenGaussCopySequence struct {
	OID                        uint64
	Column                     int
	Schema, Name, Owner        string
	Start, Increment, Min, Max string
	Cache                      string
	Cycle                      bool
	Large                      bool
	Comment                    *string
}

func (p OpenGaussCatalog) CopyDefinition() (OpenGaussCopyDefinition, error) {
	var out OpenGaussCopyDefinition
	if _, err := p.Fingerprint(); err != nil || p.RelationOID == 0 {
		return out, errors.Join(err, ErrConflict)
	}
	found := false
	for _, e := range p.Entries {
		if e.Kind != "copy_definition" || e.OID != p.RelationOID {
			continue
		}
		if found || json.Unmarshal(e.Data, &out) != nil {
			return OpenGaussCopyDefinition{}, ErrConflict
		}
		found = true
	}
	if !found || PostgreSQLIdentifier(out.Owner) != nil || PostgreSQLIdentifier(out.Creator) != nil || len(out.Columns) == 0 || len(out.Columns) > 10000 || len(out.Sequences) > 10000 {
		return OpenGaussCopyDefinition{}, errors.New("complete native copy blueprint is unavailable")
	}
	seen := map[int]bool{}
	previous := 0
	for _, c := range out.Columns {
		if c.Number <= previous || seen[c.Number] || PostgreSQLIdentifier(c.Name) != nil || c.Type == "" || c.Dimensions < 0 || c.Dimensions > 6 || len(c.Type)+len(c.Default) > 1<<20 || c.Generated != "" && c.Generated != "s" {
			return OpenGaussCopyDefinition{}, ErrUnsupported
		}
		previous, seen[c.Number] = c.Number, true
	}
	for _, s := range out.Sequences {
		if s.OID == 0 || !seen[s.Column] || PostgreSQLIdentifier(s.Schema) != nil || PostgreSQLIdentifier(s.Name) != nil || PostgreSQLIdentifier(s.Owner) != nil || !s.valid() {
			return OpenGaussCopyDefinition{}, errors.New("invalid native owned sequence blueprint")
		}
	}
	return out, nil
}

func gaussInteger(raw string) (*big.Int, bool) {
	if raw == "" || len(raw) > 40 || strings.TrimSpace(raw) != raw {
		return nil, false
	}
	n, ok := new(big.Int).SetString(raw, 10)
	return n, ok && n.BitLen() <= 127 && n.String() == raw
}

func (s OpenGaussCopySequence) valid() bool {
	start, a := gaussInteger(s.Start)
	inc, b := gaussInteger(s.Increment)
	min, c := gaussInteger(s.Min)
	max, d := gaussInteger(s.Max)
	cache, e := gaussInteger(s.Cache)
	return a && b && c && d && e && (s.Large || start.IsInt64() && inc.IsInt64() && min.IsInt64() && max.IsInt64() && cache.IsInt64()) && inc.Sign() != 0 && cache.Sign() > 0 && min.Cmp(max) < 0 && start.Cmp(min) >= 0 && start.Cmp(max) <= 0
}

// OpenGaussCopyPlan keeps native LIKE semantics (including generated columns
// and partitions), but builds indexes/constraints and programs explicitly.
// Defaults are installed separately so owned sequences are never shared.
type OpenGaussCopyPlan struct {
	SQL, Before, After []string
	DataSQL            string
	SequenceNames      map[uint64]string
}

func gaussCopyStatement(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ";")
	_, err := postgresTokens(raw, true)
	return raw, err
}

func gaussCopyIndex(raw, schema, source, destination, name string) (string, error) {
	raw, err := gaussCopyStatement(raw)
	if err != nil {
		return "", err
	}
	ts, err := postgresLex(raw)
	if err != nil {
		return "", err
	}
	found := false
	for i, t := range ts {
		if t.word("INDEX") && i+1 < len(ts) && identifierToken(ts[i+1]) {
			raw = raw[:ts[i+1].start] + Quote(name) + raw[ts[i+1].end:]
			found = true
			break
		}
	}
	if !found {
		return "", ErrUnsupported
	}
	return pgCopyTarget(raw, "ON", schema, source, destination)
}

func gaussCopyDefault(c OpenGaussCopyColumn, s OpenGaussCopySequence, schema, name string) (string, error) {
	ts, err := postgresLex(c.Default)
	found := false
	if err == nil && len(ts) == 7 && ts[0].word("nextval") && ts[1].text == "(" && ts[2].kind == 's' && ts[3].text == ":" && ts[4].text == ":" && ts[5].word("regclass") && ts[6].text == ")" {
		literal := c.Default[ts[2].start:ts[2].end]
		if strings.HasPrefix(literal, "'") && strings.HasSuffix(literal, "'") {
			reference := ts[2].text
			names, e := postgresLex(reference)
			if e == nil && len(names) == 3 && identifierToken(names[0]) && names[1].text == "." && identifierToken(names[2]) {
				found = pgTokenName(names[0]) == s.Schema && pgTokenName(names[2]) == s.Name
			}
		}
	}
	if !found || c.Generated != "" {
		return "", errors.New("owned sequence requires a simple native nextval default")
	}
	return "nextval('" + strings.ReplaceAll(Quote(schema)+"."+Quote(name), "'", "''") + "'::regclass)", nil
}

// PlanOpenGaussCopy validates the immutable native blueprint before producing
// preview SQL. Unrepresented grants/policies/security features are rejected.
func PlanOpenGaussCopy(original Snapshot, destination string, data, self bool) (OpenGaussCopyPlan, error) {
	p := OpenGaussCopyPlan{SequenceNames: map[uint64]string{}}
	if original.Engine != "opengauss" || !original.Exists || original.OpenGauss == nil || original.Postgres != nil || PostgreSQLIdentifier(destination) != nil || destination == original.Table.Name {
		return p, ErrUnsupported
	}
	catalog := original.OpenGauss
	revision, err := catalog.Fingerprint()
	if err != nil || revision != original.Revision || catalog.Name != original.Table.Name || catalog.Hooks || !catalog.CanManage {
		return p, errors.Join(err, ErrConflict)
	}
	d, err := catalog.CopyDefinition()
	if err != nil {
		return p, err
	}
	if !d.CanCreate || len(d.Policies) != 0 {
		return p, errors.New("copy requires schema CREATE and a table without row security policies")
	}
	schema, source := catalog.Namespace, catalog.Name
	dest := Quote(schema) + "." + Quote(destination)
	persistence := ""
	columns := map[int]OpenGaussCopyColumn{}
	for _, c := range d.Columns {
		columns[c.Number] = c
	}
	for _, e := range catalog.Entries {
		if e.Kind == "relation" && e.OID != catalog.RelationOID {
			var r struct {
				Kind string   `json:"relkind"`
				ACL  []string `json:"relacl"`
			}
			if json.Unmarshal(e.Data, &r) != nil || (r.Kind == "S" || r.Kind == "L") && len(r.ACL) != 0 {
				return p, errors.New("owned sequence grants cannot be represented by copying")
			}
		}
		if e.Kind == "relation" && e.OID == catalog.RelationOID {
			var r struct {
				Kind             string   `json:"relkind"`
				Persistence      string   `json:"relpersistence"`
				ACL              []string `json:"relacl"`
				RowSecurity      bool     `json:"relrowsecurity"`
				ForceRowSecurity bool     `json:"relforcerowsecurity"`
				HasOIDs          bool     `json:"relhasoids"`
				OfType           uint64   `json:"reloftype"`
			}
			if json.Unmarshal(e.Data, &r) != nil || r.Kind != "r" || r.OfType != 0 || r.HasOIDs || r.RowSecurity || r.ForceRowSecurity || len(r.ACL) != 0 {
				return p, ErrUnsupported
			}
			switch r.Persistence {
			case "p":
			case "u":
				persistence = "UNLOGGED "
			default:
				return p, ErrUnsupported
			}
		}
		if e.Kind == "column" && e.OID == catalog.RelationOID {
			var c struct {
				ACL        []string `json:"attacl"`
				Dropped    bool     `json:"attisdropped"`
				Statistics int      `json:"attstattarget"`
				Storage    string   `json:"attstorage"`
			}
			if json.Unmarshal(e.Data, &c) != nil || len(c.ACL) != 0 {
				return p, ErrUnsupported
			}
			if !c.Dropped {
				column, ok := columns[e.SubID]
				if !ok {
					return p, ErrConflict
				}
				if c.Statistics != -1 {
					p.Before = append(p.Before, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET STATISTICS %d", dest, Quote(column.Name), c.Statistics))
				}
				storage := map[string]string{"p": "PLAIN", "e": "EXTERNAL", "m": "MAIN", "x": "EXTENDED"}[c.Storage]
				if storage == "" {
					return p, ErrUnsupported
				}
				// Native INCLUDING STORAGE preserves this attribute without an ALTER
				// rejected by column-store tables.
			}
		}
		if e.Kind == "inheritance" || e.Kind == "publications" || e.Kind == "security_labels" {
			var rows struct{ Rows []json.RawMessage }
			if json.Unmarshal(e.Data, &rows) != nil || len(rows.Rows) != 0 {
				return p, errors.New("inherited, published or security-labelled tables cannot be copied")
			}
		}
	}
	create := "CREATE " + persistence + "TABLE " + dest + " (LIKE " + Quote(schema) + "." + Quote(source) + " INCLUDING ALL EXCLUDING INDEXES EXCLUDING CONSTRAINTS EXCLUDING DEFAULTS)"
	if d.Tablespace != "" {
		if PostgreSQLIdentifier(d.Tablespace) != nil {
			return p, ErrUnsupported
		}
		create += " TABLESPACE " + Quote(d.Tablespace)
	}
	p.Before = append([]string{create}, p.Before...)
	var sequences []string
	defaults := map[int]string{}
	owned := map[int]bool{}
	for _, s := range d.Sequences {
		if owned[s.Column] || s.Owner != d.Owner {
			return p, ErrUnsupported
		}
		owned[s.Column] = true
		name := pgCopyName(destination, "sequence:"+s.Name)
		p.SequenceNames[s.OID] = name
		def, err := gaussCopyDefault(columns[s.Column], s, schema, name)
		if err != nil {
			return p, err
		}
		defaults[s.Column] = def
		cycle := "NO CYCLE"
		if s.Cycle {
			cycle = "CYCLE"
		}
		large := ""
		if s.Large {
			large = "LARGE "
		}
		seq := Quote(schema) + "." + Quote(name)
		sequenceKind := "SEQUENCE"
		if s.Large {
			sequenceKind = "LARGE SEQUENCE"
		}
		sequences = append(sequences, fmt.Sprintf("CREATE %sSEQUENCE %s INCREMENT BY %s START WITH %s MINVALUE %s MAXVALUE %s CACHE %s %s", large, seq, s.Increment, s.Start, s.Min, s.Max, s.Cache, cycle))
		p.Before = append(p.Before, "ALTER "+sequenceKind+" "+seq+" OWNED BY "+dest+"."+Quote(columns[s.Column].Name))
		if s.Comment != nil {
			v, err := postgresComment(*s.Comment)
			if err != nil {
				return p, err
			}
			p.After = append(p.After, "COMMENT ON "+sequenceKind+" "+seq+" IS "+v)
		}
		p.After = append(p.After, "ALTER "+sequenceKind+" "+seq+" OWNER TO "+Quote(s.Owner))
	}
	p.Before = append(sequences, p.Before...)
	for _, c := range d.Columns {
		if c.Dimensions > 0 {
			if !strings.HasSuffix(c.Type, "[]") {
				return p, ErrUnsupported
			}
			if _, err := postgresFragment(c.Type); err != nil {
				return p, err
			}
			// Native LIKE 6.0.0 loses attndims. Restore the declared dimensions
			// on the empty target instead of ignoring the catalog difference.
			p.Before = append(p.Before, "ALTER TABLE "+dest+" ALTER COLUMN "+Quote(c.Name)+" TYPE "+c.Type+strings.Repeat("[]", c.Dimensions-1))
		}
		if c.Default != "" && c.Generated == "" {
			def := c.Default
			if v, ok := defaults[c.Number]; ok {
				def = v
			}
			if _, err := postgresFragment(def); err != nil {
				return p, err
			}
			p.Before = append(p.Before, "ALTER TABLE "+dest+" ALTER COLUMN "+Quote(c.Name)+" SET DEFAULT "+def)
		}
		if c.Comment != nil {
			v, err := postgresComment(*c.Comment)
			if err != nil {
				return p, err
			}
			p.After = append(p.After, "COMMENT ON COLUMN "+dest+"."+Quote(c.Name)+" IS "+v)
		}
	}
	indexNames := map[uint64]string{}
	var constraints, indexes, programs []string
	for _, c := range d.Constraints {
		if c.OID == 0 || PostgreSQLIdentifier(c.Name) != nil || c.Kind != "p" && c.Kind != "u" && c.Kind != "c" && c.Kind != "f" && c.Kind != "x" {
			return p, ErrUnsupported
		}
		name := pgCopyName(destination, "constraint:"+c.Name)
		definition, err := gaussCopyStatement(c.SQL)
		if err != nil {
			return p, err
		}
		if c.Kind == "f" && self && c.ReferenceOID == catalog.RelationOID {
			definition, err = pgCopyTarget(definition, "REFERENCES", schema, source, destination)
			if err != nil {
				return p, err
			}
		}
		constraints = append(constraints, "ALTER TABLE "+dest+" ADD CONSTRAINT "+Quote(name)+" "+definition)
		if c.IndexOID != 0 && c.Kind != "f" {
			indexNames[c.IndexOID] = name
		}
		if c.Comment != nil {
			v, err := postgresComment(*c.Comment)
			if err != nil {
				return p, err
			}
			p.After = append(p.After, "COMMENT ON CONSTRAINT "+Quote(name)+" ON "+dest+" IS "+v)
		}
	}
	for _, i := range d.Indexes {
		if i.OID == 0 || PostgreSQLIdentifier(i.Name) != nil {
			return p, ErrUnsupported
		}
		name, exists := indexNames[i.OID]
		if !exists {
			name = pgCopyName(destination, "index:"+i.Name)
			q, err := gaussCopyIndex(i.SQL, schema, source, destination, name)
			if err != nil {
				return p, err
			}
			indexes = append(indexes, q)
		}
		if i.Comment != nil {
			v, err := postgresComment(*i.Comment)
			if err != nil {
				return p, err
			}
			p.After = append(p.After, "COMMENT ON INDEX "+Quote(schema)+"."+Quote(name)+" IS "+v)
		}
		for _, e := range catalog.Entries {
			if e.Kind == "index" && e.OID == i.OID {
				var flags struct {
					Valid     bool `json:"indisvalid"`
					Ready     bool `json:"indisready"`
					Usable    bool `json:"indisusable"`
					Clustered bool `json:"indisclustered"`
				}
				if json.Unmarshal(e.Data, &flags) != nil || !flags.Valid || !flags.Ready || !flags.Usable {
					return p, ErrUnsupported
				}
				if flags.Clustered {
					indexes = append(indexes, "ALTER TABLE "+dest+" CLUSTER ON "+Quote(name))
				}
			}
		}
	}
	for _, list := range [][]OpenGaussCopyObject{d.Triggers, d.Rules} {
		for _, o := range list {
			if o.Internal {
				continue
			}
			if PostgreSQLIdentifier(o.Name) != nil {
				return p, ErrUnsupported
			}
			q, err := gaussCopyStatement(o.SQL)
			if err != nil {
				return p, err
			}
			keyword := "ON"
			if o.Kind == "rule" {
				keyword = "TO"
			}
			q, err = pgCopyTarget(q, keyword, schema, source, destination)
			if err != nil {
				return p, err
			}
			programs = append(programs, q)
			status := map[string]string{"O": "ENABLE", "D": "DISABLE", "A": "ENABLE ALWAYS", "R": "ENABLE REPLICA"}[o.Enabled]
			if status == "" {
				return p, ErrUnsupported
			}
			programs = append(programs, "ALTER TABLE "+dest+" "+status+" "+strings.ToUpper(o.Kind)+" "+Quote(o.Name))
			if o.Comment != nil {
				v, err := postgresComment(*o.Comment)
				if err != nil {
					return p, err
				}
				programs = append(programs, "COMMENT ON "+strings.ToUpper(o.Kind)+" "+Quote(o.Name)+" ON "+dest+" IS "+v)
			}
		}
	}
	if d.Comment != nil {
		v, err := postgresComment(*d.Comment)
		if err != nil {
			return p, err
		}
		p.After = append(p.After, "COMMENT ON TABLE "+dest+" IS "+v)
	}
	p.After = append(append(constraints, indexes...), p.After...)
	p.After = append(p.After, programs...)
	p.After = append(p.After, "ALTER TABLE "+dest+" OWNER TO "+Quote(d.Owner))
	if data {
		var names []string
		for _, c := range d.Columns {
			if c.Generated == "" {
				names = append(names, Quote(c.Name))
			}
		}
		if len(names) == 0 {
			return p, ErrUnsupported
		}
		cols := strings.Join(names, ",")
		p.DataSQL = "INSERT INTO " + dest + " (" + cols + ") SELECT " + cols + " FROM " + Quote(schema) + "." + Quote(source)
	}
	p.SQL = append(p.SQL, p.Before...)
	if p.DataSQL != "" {
		p.SQL = append(p.SQL, p.DataSQL)
	}
	if data {
		for _, s := range d.Sequences {
			p.SQL = append(p.SQL, "SELECT pg_catalog.setval('"+strings.ReplaceAll(Quote(schema)+"."+Quote(p.SequenceNames[s.OID]), "'", "''")+"'::regclass,last_value,is_called) FROM "+Quote(s.Schema)+"."+Quote(s.Name))
		}
	}
	p.SQL = append(p.SQL, p.After...)
	return p, nil
}

// OpenGaussCopyShape compares deparsed expressions and every retained native
// column/table/partition property, removing only new object identities and
// documented physical layout statistics. Unknown native properties remain.
func OpenGaussCopyShape(original Snapshot, destination string, self bool) (string, error) {
	if original.OpenGauss == nil {
		return "", ErrUnsupported
	}
	d, err := original.OpenGauss.CopyDefinition()
	if err != nil {
		return "", err
	}
	catalog := original.OpenGauss
	source := catalog.Name
	schema := catalog.Namespace
	for i := range d.Columns {
		for _, s := range d.Sequences {
			if s.Column == d.Columns[i].Number {
				ns, name := s.Schema, s.Name
				if destination != "" {
					ns, name = schema, pgCopyName(destination, "sequence:"+s.Name)
				}
				d.Columns[i].Default, err = gaussCopyDefault(d.Columns[i], s, ns, name)
				if err != nil {
					return "", err
				}
			}
		}
	}
	if destination != "" {
		for i := range d.Sequences {
			d.Sequences[i].Name = pgCopyName(destination, "sequence:"+d.Sequences[i].Name)
			d.Sequences[i].Schema = schema
		}
	}
	for i := range d.Sequences {
		d.Sequences[i].OID = 0
		d.Sequences[i].Column = columnOrdinal(d.Columns, d.Sequences[i].Column)
	}
	for i := range d.Columns {
		d.Columns[i].Number = i + 1
		d.Columns[i].Default = gaussCopySQLSignature(d.Columns[i].Default)
	}
	indexNames := map[uint64]string{}
	for i := range d.Constraints {
		c := &d.Constraints[i]
		if c.Kind != "f" && c.IndexOID != 0 {
			indexNames[c.IndexOID] = c.Name
			if destination != "" {
				indexNames[c.IndexOID] = pgCopyName(destination, "constraint:"+c.Name)
			}
		}
		if destination != "" {
			c.Name = pgCopyName(destination, "constraint:"+c.Name)
			if c.Kind == "f" && self && c.ReferenceOID == catalog.RelationOID {
				c.SQL, err = pgCopyTarget(c.SQL, "REFERENCES", schema, source, destination)
				if err != nil {
					return "", err
				}
				c.ReferenceName = destination
			}
		}
		c.OID, c.IndexOID, c.ReferenceOID = 0, 0, 0
		c.SQL = gaussCopySQLSignature(c.SQL)
	}
	for i := range d.Indexes {
		x := &d.Indexes[i]
		name := x.Name
		if destination != "" {
			name = pgCopyName(destination, "index:"+x.Name)
			if n, ok := indexNames[x.OID]; ok {
				name = n
			}
			x.SQL, err = gaussCopyIndex(x.SQL, schema, source, destination, name)
			if err != nil {
				return "", err
			}
		}
		x.Name, x.OID, x.SQL = name, 0, gaussCopySQLSignature(x.SQL)
	}
	for _, list := range [][]OpenGaussCopyObject{d.Triggers, d.Rules} {
		for i := range list {
			o := &list[i]
			if o.Internal {
				continue
			}
			if destination != "" {
				key := "ON"
				if o.Kind == "rule" {
					key = "TO"
				}
				o.SQL, err = pgCopyTarget(o.SQL, key, schema, source, destination)
				if err != nil {
					return "", err
				}
			}
			o.OID = 0
			o.SQL = gaussCopySQLSignature(o.SQL)
		}
	}
	// Internal FK triggers are rebuilt by ADD CONSTRAINT, not copied programs.
	var triggers []OpenGaussCopyObject
	for _, o := range d.Triggers {
		if !o.Internal {
			triggers = append(triggers, o)
		}
	}
	d.Triggers = triggers
	sort.Slice(d.Constraints, func(i, j int) bool { return d.Constraints[i].Name < d.Constraints[j].Name })
	sort.Slice(d.Indexes, func(i, j int) bool { return d.Indexes[i].Name < d.Indexes[j].Name })
	sort.Slice(d.Triggers, func(i, j int) bool { return d.Triggers[i].Name < d.Triggers[j].Name })
	sort.Slice(d.Rules, func(i, j int) bool { return d.Rules[i].Name < d.Rules[j].Name })
	sort.Slice(d.Sequences, func(i, j int) bool { return d.Sequences[i].Column < d.Sequences[j].Column })
	indexIDs := map[uint64]bool{}
	sequenceIDs := map[uint64]bool{}
	originalDefinition, err := catalog.CopyDefinition()
	if err != nil {
		return "", err
	}
	for _, i := range originalDefinition.Indexes {
		indexIDs[i.OID] = true
	}
	for _, s := range originalDefinition.Sequences {
		sequenceIDs[s.OID] = true
	}
	var raw []string
	for _, e := range catalog.Entries {
		if e.Kind != "relation" && e.Kind != "column" && e.Kind != "partition" && e.Kind != "index" {
			continue
		}
		if e.Kind == "relation" && e.OID != catalog.RelationOID && !indexIDs[e.OID] && !sequenceIDs[e.OID] || e.Kind == "column" && e.OID != catalog.RelationOID {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(string(e.Data)))
		decoder.UseNumber()
		var v map[string]any
		if decoder.Decode(&v) != nil || v == nil {
			return "", ErrConflict
		}
		if e.Kind == "index" {
			// Deparsed index SQL retains mapped columns, expression/predicate
			// trees and index names; all remaining native flags stay compared.
			for _, key := range []string{"indexrelid", "indrelid", "indkey", "indexprs", "indpred", "indcheckxmin"} {
				delete(v, key)
			}
		} else if e.Kind == "column" {
			if dropped, _ := v["attisdropped"].(bool); dropped {
				continue
			}
			for _, k := range []string{"attrelid", "attnum", "attcacheoff", "attinitdefval"} {
				delete(v, k)
			}
		} else {
			for _, k := range []string{"reltype", "relfilenode", "reltoastrelid", "reltoastidxid", "reldeltarelid", "reldeltaidx", "relcudescrelid", "relcudescidx", "relpages", "reltuples", "relallvisible", "relfrozenxid", "relfrozenxid64", "relminmxid", "relnatts"} {
				delete(v, k)
			}
			if e.Kind == "relation" || v["parttype"] == "r" {
				delete(v, "relname")
			}
			if e.Kind == "partition" {
				delete(v, "parentid")
				delete(v, "indextblid")
			}
		}
		b, _ := json.Marshal(v)
		raw = append(raw, e.Kind+":"+string(b))
	}
	sort.Strings(raw)
	b, err := json.Marshal(struct {
		Definition OpenGaussCopyDefinition
		Native     []string
	}{d, raw})
	return string(b), err
}

func columnOrdinal(columns []OpenGaussCopyColumn, number int) int {
	for i, c := range columns {
		if c.Number == number {
			return i + 1
		}
	}
	return 0
}
func gaussCopySQLSignature(raw string) string {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ";")
	ts, err := postgresLex(raw)
	if err != nil {
		return "invalid:" + raw
	}
	var values []string
	for _, t := range ts {
		if identifierToken(t) {
			values = append(values, "i:"+pgTokenName(t))
		} else if t.kind == 's' {
			values = append(values, "s:"+raw[t.start:t.end])
		} else {
			values = append(values, string(t.kind)+":"+t.text)
		}
	}
	return Signature(values)
}
