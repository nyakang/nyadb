package schemaddl

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Native drafts contain editable declarations separately from the immutable
// catalog. Storage, partitioning, grants and dependencies stay in that catalog.
type OpenGaussTableProperties struct {
	Persistence, Owner, Tablespace string
	Parameters                     []string
	Comment                        *string
}

type OpenGaussColumnProperties struct {
	Number  int
	OID     uint64
	Comment *string
}

type OpenGaussDefinitionProperties struct {
	OID     uint64
	Enabled string
	Comment *string
}

func gaussCloneComment(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func (p *OpenGaussTableProperties) Clone() *OpenGaussTableProperties {
	if p == nil {
		return nil
	}
	v := *p
	v.Parameters = slices.Clone(p.Parameters)
	v.Comment = gaussCloneComment(p.Comment)
	return &v
}
func (p *OpenGaussColumnProperties) Clone() *OpenGaussColumnProperties {
	if p == nil {
		return nil
	}
	v := *p
	v.Comment = gaussCloneComment(p.Comment)
	return &v
}
func (p *OpenGaussDefinitionProperties) Clone() *OpenGaussDefinitionProperties {
	if p == nil {
		return nil
	}
	v := *p
	v.Comment = gaussCloneComment(p.Comment)
	return &v
}

// DesignTable renders the native deparse blueprint. It never invents a
// PostgreSQL server version or replaces native partition/security records.
func (p OpenGaussCatalog) DesignTable() (Table, string, error) {
	t := Table{Name: p.Name}
	if _, err := p.Fingerprint(); err != nil {
		return t, "", err
	}
	if p.RelationOID == 0 {
		t.OpenGauss = &OpenGaussTableProperties{Persistence: "p"}
		return t, "", nil
	}
	d, err := p.CopyDefinition()
	if err != nil {
		return t, "", err
	}
	t.OpenGauss = &OpenGaussTableProperties{Owner: d.Owner, Tablespace: d.Tablespace, Comment: gaussCloneComment(d.Comment)}
	ddl := ""
	for _, e := range p.Entries {
		if e.Kind == "relation" && e.OID == p.RelationOID {
			var r struct {
				Persistence string   `json:"relpersistence"`
				Parameters  []string `json:"reloptions"`
			}
			if json.Unmarshal(e.Data, &r) != nil || r.Persistence != "p" && r.Persistence != "u" {
				return t, "", ErrUnsupported
			}
			t.OpenGauss.Persistence, t.OpenGauss.Parameters = r.Persistence, slices.Clone(r.Parameters)
		}
		if e.Kind == "design_definition" && e.OID == p.RelationOID {
			var r struct{ DDL string }
			if json.Unmarshal(e.Data, &r) != nil || r.DDL == "" {
				return t, "", ErrConflict
			}
			ddl = r.DDL
		}
	}
	if t.OpenGauss.Persistence == "" {
		return t, "", ErrConflict
	}
	for _, c := range d.Columns {
		typ := c.Type
		if c.Dimensions > 1 && strings.HasSuffix(typ, "[]") {
			typ += strings.Repeat("[]", c.Dimensions-1)
		}
		f := PostgreSQLField{Type: typ, Collation: c.Collation, NotNull: c.NotNull}
		if c.Generated == "s" {
			f.Generation, f.GenerationStorage = c.Default, "STORED"
		} else if c.Default != "" {
			f.HasDefault, f.Default = true, c.Default
		}
		body, err := f.SQL()
		if err != nil {
			return t, "", err
		}
		t.Clauses = append(t.Clauses, Clause{ID: fmt.Sprintf("column:%d", c.Number), Kind: "column", Name: c.Name, SQL: body, OpenGauss: &OpenGaussColumnProperties{Number: c.Number, Comment: gaussCloneComment(c.Comment)}})
	}
	ownedIndexes := map[uint64]bool{}
	for _, c := range d.Constraints {
		if c.IndexOID != 0 && c.Kind != "f" {
			ownedIndexes[c.IndexOID] = true
		}
		t.Clauses = append(t.Clauses, Clause{ID: fmt.Sprintf("constraint:%d", c.OID), Kind: "constraint", Name: c.Name, SQL: "CONSTRAINT " + Quote(c.Name) + " " + c.SQL, OpenGauss: &OpenGaussColumnProperties{OID: c.OID, Comment: gaussCloneComment(c.Comment)}})
	}
	for _, list := range [][]OpenGaussCopyObject{d.Indexes, d.Triggers, d.Rules} {
		for _, o := range list {
			if o.Internal || o.Kind == "index" && ownedIndexes[o.OID] {
				continue
			}
			t.Definitions = append(t.Definitions, Definition{Name: o.Name, Kind: o.Kind, SQL: o.SQL, OpenGauss: &OpenGaussDefinitionProperties{OID: o.OID, Enabled: o.Enabled, Comment: gaussCloneComment(o.Comment)}})
		}
	}
	return t, ddl, nil
}

// ParseOpenGaussField shares only the overlapping declaration grammar.
// PostgreSQL identity and virtual-generated semantics are not native options.
func ParseOpenGaussField(raw string) (PostgreSQLField, error) {
	f, err := ParsePostgreSQLField(raw)
	if err != nil {
		return f, err
	}
	if f.Identity != "" || f.IdentityOptions != "" || f.GenerationStorage != "" && f.GenerationStorage != "STORED" {
		return f, ErrUnsupported
	}
	return f, nil
}

func gaussDesignDefinition(d Definition, schema, table string) (string, error) {
	if d.Kind == "index" || d.Kind == "trigger" {
		return DefinitionPostgreSQL(d, schema, table)
	}
	if d.Kind != "rule" || PostgreSQLIdentifier(d.Name) != nil {
		return "", ErrUnsupported
	}
	raw, err := gaussCopyStatement(d.SQL)
	if err != nil {
		return "", err
	}
	ts, err := postgresLex(raw)
	if err != nil || len(ts) < 9 || !ts[0].word("CREATE") || !ts[1].word("RULE") || !identifierToken(ts[2]) || pgTokenName(ts[2]) != d.Name {
		return "", ErrUnsupported
	}
	return pgCopyTarget(raw, "TO", schema, table, table)
}

func gaussDesignComment(target string, comment *string) (string, error) {
	v := "NULL"
	var err error
	if comment != nil {
		v, err = postgresComment(*comment)
	}
	return "COMMENT ON " + target + " IS " + v, err
}

func gaussDesignSameConstraint(a, b Clause) bool {
	a.OpenGauss = a.OpenGauss.Clone()
	b.OpenGauss = b.OpenGauss.Clone()
	if a.OpenGauss != nil {
		a.OpenGauss.Comment = nil
	}
	if b.OpenGauss != nil {
		b.OpenGauss.Comment = nil
	}
	return Signature(a) == Signature(b)
}

func gaussDesignSameDefinition(a, b Definition) bool {
	a.OpenGauss = a.OpenGauss.Clone()
	b.OpenGauss = b.OpenGauss.Clone()
	if a.OpenGauss != nil {
		a.OpenGauss.Comment = nil
		a.OpenGauss.Enabled = ""
	}
	if b.OpenGauss != nil {
		b.OpenGauss.Comment = nil
		b.OpenGauss.Enabled = ""
	}
	return Signature(a) == Signature(b)
}

func validateOpenGaussDraft(t Table, schema string) error {
	if PostgreSQLIdentifier(schema) != nil || PostgreSQLIdentifier(t.Name) != nil || t.Postgres != nil || t.OpenGauss == nil || t.Options != "" || len(t.Clauses) == 0 || len(t.Clauses) > 10000 {
		return ErrUnsupported
	}
	if t.OpenGauss.Persistence != "p" && t.OpenGauss.Persistence != "u" {
		return ErrUnsupported
	}
	if t.OpenGauss.Tablespace != "" && PostgreSQLIdentifier(t.OpenGauss.Tablespace) != nil {
		return ErrUnsupported
	}
	if _, _, err := pgParameters(t.OpenGauss.Parameters); err != nil {
		return err
	}
	ids, names := map[string]bool{}, map[string]bool{}
	columns := 0
	for _, c := range t.Clauses {
		if c.ID == "" || ids[c.ID] || c.Postgres != nil || PostgreSQLIdentifier(c.Name) != nil {
			return ErrUnsupported
		}
		ids[c.ID] = true
		key := c.Kind + ":" + c.Name
		if names[key] {
			return errors.New("duplicate native declaration name")
		}
		names[key] = true
		switch c.Kind {
		case "column":
			columns++
			f, err := ParseOpenGaussField(c.SQL)
			if err != nil || f.Unsupported {
				return errors.Join(err, ErrUnsupported)
			}
		case "constraint":
			if _, err := pgConstraintBody(c, schema); err != nil {
				return err
			}
		default:
			return ErrUnsupported
		}
	}
	if columns == 0 {
		return ErrUnsupported
	}
	for _, d := range t.Definitions {
		key := d.Kind + ":" + d.Name
		if names[key] || d.Postgres != nil {
			return ErrUnsupported
		}
		names[key] = true
		if _, err := gaussDesignDefinition(d, schema, t.Name); err != nil {
			return err
		}
	}
	return nil
}

// PlanOpenGauss emits direct, transactional native DDL. The adapter validates
// callbacks, complete retained values and native properties before commit.
func PlanOpenGauss(original Snapshot, desired Table, schema string, allowDrop bool) (Plan, error) {
	p := Plan{Mode: "noop"}
	if original.Engine != "opengauss" || original.OpenGauss == nil || original.Postgres != nil || original.OpenGauss.Namespace != schema || original.OpenGauss.Name != original.Table.Name {
		return p, ErrUnsupported
	}
	revision, err := original.OpenGauss.Fingerprint()
	if err != nil || revision != original.Revision || original.Exists != (original.OpenGauss.RelationOID != 0) {
		return p, errors.Join(err, ErrConflict)
	}
	baseline, _, err := original.OpenGauss.DesignTable()
	if err != nil || Signature(baseline) != Signature(original.Table) {
		return p, errors.Join(err, ErrConflict)
	}
	if original.OpenGauss.Hooks {
		return p, errors.New("enabled DDL event triggers are unsupported for table design")
	}
	if err = validateOpenGaussDraft(desired, schema); err != nil {
		return p, err
	}
	target := Quote(schema) + "." + Quote(original.Table.Name)
	if !original.Exists {
		return gaussDesignCreate(original, desired, schema)
	}
	if !original.OpenGauss.CanManage {
		return p, ErrUnsupported
	}
	priorClauses := map[string]Clause{}
	for _, c := range original.Table.Clauses {
		priorClauses[c.ID] = c
	}
	for _, c := range desired.Clauses {
		if old, ok := priorClauses[c.ID]; ok {
			if old.Kind != c.Kind || old.OpenGauss == nil || c.OpenGauss == nil || old.OpenGauss.Number != c.OpenGauss.Number || old.OpenGauss.OID != c.OpenGauss.OID {
				return p, ErrConflict
			}
		} else if !strings.HasPrefix(c.ID, "new:") || c.OpenGauss != nil && (c.OpenGauss.OID != 0 || c.OpenGauss.Number != 0) {
			return p, ErrConflict
		}
	}
	priorDefinitions := map[string]Definition{}
	for _, d := range original.Table.Definitions {
		priorDefinitions[d.Kind+":"+d.Name] = d
	}
	for _, d := range desired.Definitions {
		if old, ok := priorDefinitions[d.Kind+":"+d.Name]; ok {
			if old.OpenGauss == nil || d.OpenGauss == nil || old.OpenGauss.OID != d.OpenGauss.OID {
				return p, ErrConflict
			}
		} else if d.OpenGauss != nil && d.OpenGauss.OID != 0 {
			return p, ErrConflict
		}
	}
	if original.Table.Name != desired.Name {
		x := desired.Clone()
		x.Name = original.Table.Name
		if Signature(x) != Signature(original.Table) {
			return p, errors.New("table rename must be a separate native design operation")
		}
		p.Mode, p.RenameFrom, p.RenameTo = "rename", original.Table.Name, desired.Name
		p.SQL = []string{"ALTER TABLE " + target + " RENAME TO " + Quote(desired.Name)}
		return p, nil
	}
	oldProps, nextProps := original.Table.OpenGauss, desired.OpenGauss
	if oldProps.Owner != nextProps.Owner || oldProps.Persistence != nextProps.Persistence || oldProps.Tablespace != nextProps.Tablespace || Signature(oldProps.Parameters) != Signature(nextProps.Parameters) {
		return p, errors.New("native owner, persistence and storage changes require a dedicated verified operation")
	}
	oldColumns, newColumns := map[string]Clause{}, map[string]Clause{}
	for _, c := range original.Table.Columns() {
		oldColumns[c.ID] = c
	}
	for _, c := range desired.Columns() {
		newColumns[c.ID] = c
	}
	// Native ALTER ADD appends columns. It cannot apply a visual reorder or
	// insert a new field ahead of an existing retained field.
	var retained []string
	for _, c := range original.Table.Columns() {
		if _, ok := newColumns[c.ID]; ok {
			retained = append(retained, c.ID)
		}
	}
	var requested []string
	added := false
	for _, c := range desired.Columns() {
		if _, ok := oldColumns[c.ID]; ok {
			if added {
				return p, errors.New("native new fields must follow retained fields")
			}
			requested = append(requested, c.ID)
		} else {
			if !strings.HasPrefix(c.ID, "new:") {
				return p, ErrConflict
			}
			added = true
		}
	}
	if !slices.Equal(retained, requested) {
		return p, errors.New("native field order changes require a dedicated verified migration")
	}
	if err = gaussDesignCheckGeneratedTypeChanges(original, desired); err != nil {
		return p, err
	}
	var renames []string
	for _, c := range desired.Columns() {
		old, ok := oldColumns[c.ID]
		if !ok {
			continue
		}
		if c.Name != old.Name {
			x := c
			x.Name = old.Name
			if Signature(x) != Signature(old) {
				return p, errors.New("column rename must be a separate native design operation")
			}
			renames = append(renames, "ALTER TABLE "+target+" RENAME COLUMN "+Quote(old.Name)+" TO "+Quote(c.Name))
		}
	}
	if len(renames) > 0 {
		if len(renames) != 1 || len(oldColumns) != len(newColumns) {
			return p, ErrUnsupported
		}
		x := desired.Clone()
		for i := range x.Clauses {
			if old, ok := oldColumns[x.Clauses[i].ID]; ok {
				x.Clauses[i].Name = old.Name
			}
		}
		if Signature(x) != Signature(original.Table) {
			return p, errors.New("column rename must retain all other native declarations")
		}
		p.Mode, p.SQL = "rename_column", renames
		return p, nil
	}
	// Remove only explicitly edited dependent declarations, without CASCADE.
	for _, old := range original.Table.Clauses {
		if old.Kind != "constraint" {
			continue
		}
		found := false
		for _, c := range desired.Clauses {
			if c.ID == old.ID && gaussDesignSameConstraint(c, old) {
				found = true
			}
		}
		if !found {
			p.SQL = append(p.SQL, "ALTER TABLE "+target+" DROP CONSTRAINT "+Quote(old.Name)+" RESTRICT")
		}
	}
	for _, old := range original.Table.Definitions {
		found := false
		for _, d := range desired.Definitions {
			if d.Kind == old.Kind && d.Name == old.Name && gaussDesignSameDefinition(d, old) {
				found = true
			}
		}
		if found {
			continue
		}
		q := "DROP " + strings.ToUpper(old.Kind) + " " + Quote(old.Name)
		if old.Kind == "index" {
			q = "DROP INDEX " + Quote(schema) + "." + Quote(old.Name)
		} else {
			q += " ON " + target
		}
		p.SQL = append(p.SQL, q+" RESTRICT")
	}
	// openGauss forbids a zero-column table, even inside a transaction. When
	// replacing every field, add the desired fields before removing the old
	// ones. Free reused names through qualified temporary renames; original
	// identities and dependent objects remain subject to RESTRICT deletion.
	dropNames := map[string]string{}
	for _, old := range original.Table.Columns() {
		if _, ok := newColumns[old.ID]; ok {
			continue
		}
		if !allowDrop {
			return p, errors.New("column deletion requires explicit confirmation")
		}
		p.Dropped = append(p.Dropped, old.Name)
		dropNames[old.ID] = old.Name
		if len(retained) != 0 {
			continue
		}
		for _, next := range desired.Columns() {
			if next.Name != old.Name {
				continue
			}
			digest := sha256.Sum256([]byte(schema + "\x00" + desired.Name + "\x00" + old.ID))
			private := fmt.Sprintf("__nyadb_retired_%x", digest[:16])
			for _, col := range append(original.Table.Columns(), desired.Columns()...) {
				if col.Name == private {
					return p, errors.New("native temporary column name is already in use")
				}
			}
			p.SQL = append(p.SQL, "ALTER TABLE "+target+" RENAME COLUMN "+Quote(old.Name)+" TO "+Quote(private))
			dropNames[old.ID] = private
		}
	}
	var drops []string
	// Remove generated fields before their inputs. Native dependency handling
	// must not implicitly remove a field whose separate deletion is still due.
	for _, generated := range []bool{true, false} {
		for _, old := range original.Table.Columns() {
			name, removed := dropNames[old.ID]
			if !removed {
				continue
			}
			field, e := ParseOpenGaussField(old.SQL)
			if e != nil {
				return p, e
			}
			if (field.Generation != "") == generated {
				drops = append(drops, "ALTER TABLE "+target+" DROP COLUMN "+Quote(name)+" RESTRICT")
			}
		}
	}
	if len(retained) != 0 {
		p.SQL = append(p.SQL, drops...)
	}
	for _, c := range desired.Columns() {
		old, ok := oldColumns[c.ID]
		if !ok {
			if c.OpenGauss != nil && (c.OpenGauss.Number != 0 || c.OpenGauss.OID != 0) {
				return p, ErrConflict
			}
			statements, e := gaussDesignAddField(original, c, schema, desired.Name)
			if e != nil {
				return p, e
			}
			p.SQL = append(p.SQL, statements...)
		} else {
			if c.OpenGauss == nil || old.OpenGauss.Number != c.OpenGauss.Number || old.OpenGauss.OID != c.OpenGauss.OID {
				return p, ErrConflict
			}
			a, _ := ParseOpenGaussField(old.SQL)
			b, _ := ParseOpenGaussField(c.SQL)
			if a.Generation != b.Generation || a.GenerationStorage != b.GenerationStorage {
				return p, errors.New("existing native generated expressions require a dedicated verified migration")
			}
			column := target + " ALTER COLUMN " + Quote(c.Name)
			if a.Type != b.Type || a.Collation != b.Collation {
				q := "ALTER TABLE " + column + " TYPE " + b.Type
				if b.Collation != "" {
					q += " COLLATE " + b.Collation
				}
				q += " USING " + Quote(c.Name) + "::" + b.Type
				p.SQL = append(p.SQL, q)
			}
			if a.NotNull != b.NotNull {
				if err := OpenGaussNotNullEditable(original, c.ID); err != nil {
					return p, err
				}
				action := "DROP NOT NULL"
				if b.NotNull {
					action = "SET NOT NULL"
				}
				p.SQL = append(p.SQL, "ALTER TABLE "+column+" "+action)
			}
			if a.HasDefault != b.HasDefault || a.Default != b.Default {
				action := "DROP DEFAULT"
				if b.HasDefault {
					action = "SET DEFAULT " + b.Default
				}
				p.SQL = append(p.SQL, "ALTER TABLE "+column+" "+action)
			}
			p.Mappings = append(p.Mappings, Mapping{ID: c.ID, Source: old.Name, Destination: c.Name})
		}
		var a, b *string
		if ok && old.OpenGauss != nil {
			a = old.OpenGauss.Comment
		}
		if c.OpenGauss != nil {
			b = c.OpenGauss.Comment
		}
		if Signature(a) != Signature(b) {
			q, e := gaussDesignComment("COLUMN "+target+"."+Quote(c.Name), b)
			if e != nil {
				return p, e
			}
			p.SQL = append(p.SQL, q)
		}
	}
	if len(retained) == 0 {
		p.SQL = append(p.SQL, drops...)
	}
	for _, c := range desired.Clauses {
		if c.Kind != "constraint" {
			continue
		}
		found := false
		var previous Clause
		for _, old := range original.Table.Clauses {
			if c.ID == old.ID && gaussDesignSameConstraint(c, old) {
				found = true
				previous = old
			}
		}
		if found {
			var a, b *string
			if previous.OpenGauss != nil {
				a = previous.OpenGauss.Comment
			}
			if c.OpenGauss != nil {
				b = c.OpenGauss.Comment
			}
			if Signature(a) != Signature(b) {
				q, e := gaussDesignComment("CONSTRAINT "+Quote(c.Name)+" ON "+target, b)
				if e != nil {
					return p, e
				}
				p.SQL = append(p.SQL, q)
			}
			continue
		}
		body, err := pgConstraintBody(c, schema)
		if err != nil {
			return p, err
		}
		p.SQL = append(p.SQL, "ALTER TABLE "+target+" ADD CONSTRAINT "+Quote(c.Name)+" "+body)
		if c.OpenGauss != nil && c.OpenGauss.Comment != nil {
			q, e := gaussDesignComment("CONSTRAINT "+Quote(c.Name)+" ON "+target, c.OpenGauss.Comment)
			if e != nil {
				return p, e
			}
			p.SQL = append(p.SQL, q)
		}
	}
	for _, d := range desired.Definitions {
		found := false
		var previous Definition
		for _, old := range original.Table.Definitions {
			if d.Kind == old.Kind && d.Name == old.Name && gaussDesignSameDefinition(d, old) {
				found = true
				previous = old
			}
		}
		if !found {
			q, e := gaussDesignDefinition(d, schema, desired.Name)
			if e != nil {
				return p, e
			}
			p.SQL = append(p.SQL, q)
			if e = gaussDesignDefinitionMetadata(&p, d, schema, target); e != nil {
				return p, e
			}
		} else if Signature(previous.OpenGauss) != Signature(d.OpenGauss) {
			if e := gaussDesignDefinitionMetadata(&p, d, schema, target); e != nil {
				return p, e
			}
			if previous.OpenGauss != nil && previous.OpenGauss.Comment != nil && (d.OpenGauss == nil || d.OpenGauss.Comment == nil) {
				name := strings.ToUpper(d.Kind) + " " + Quote(d.Name) + " ON " + target
				if d.Kind == "index" {
					name = "INDEX " + Quote(schema) + "." + Quote(d.Name)
				}
				q, e := gaussDesignComment(name, nil)
				if e != nil {
					return p, e
				}
				p.SQL = append(p.SQL, q)
			}
		}
	}
	if Signature(oldProps.Comment) != Signature(nextProps.Comment) {
		q, e := gaussDesignComment("TABLE "+target, nextProps.Comment)
		if e != nil {
			return p, e
		}
		p.SQL = append(p.SQL, q)
	}
	if len(p.SQL) > 0 {
		p.Mode = "alter"
	}
	return p, nil
}

func gaussDesignDefinitionMetadata(p *Plan, d Definition, schema, target string) error {
	if d.OpenGauss == nil {
		return nil
	}
	if d.Kind != "index" && d.OpenGauss.Enabled != "" {
		status := map[string]string{"O": "ENABLE", "D": "DISABLE", "A": "ENABLE ALWAYS", "R": "ENABLE REPLICA"}[d.OpenGauss.Enabled]
		if status == "" {
			return ErrUnsupported
		}
		p.SQL = append(p.SQL, "ALTER TABLE "+target+" "+status+" "+strings.ToUpper(d.Kind)+" "+Quote(d.Name))
	}
	if d.OpenGauss.Comment != nil {
		name := strings.ToUpper(d.Kind) + " " + Quote(d.Name) + " ON " + target
		if d.Kind == "index" {
			name = "INDEX " + Quote(schema) + "." + Quote(d.Name)
		}
		q, err := gaussDesignComment(name, d.OpenGauss.Comment)
		if err != nil {
			return err
		}
		p.SQL = append(p.SQL, q)
	}
	return nil
}

func gaussDesignCreate(original Snapshot, t Table, schema string) (Plan, error) {
	p := Plan{Mode: "create"}
	if !original.OpenGauss.CanCreate || t.OpenGauss.Owner != "" {
		return p, errors.New("new native table requires schema CREATE and the current owner")
	}
	var clauses []string
	for _, c := range t.Clauses {
		if c.OpenGauss != nil && (c.OpenGauss.OID != 0 || c.OpenGauss.Number != 0) {
			return p, ErrConflict
		}
		if c.Kind == "column" {
			clauses = append(clauses, Quote(c.Name)+" "+c.SQL)
		} else {
			body, e := pgConstraintBody(c, schema)
			if e != nil {
				return p, e
			}
			clauses = append(clauses, "CONSTRAINT "+Quote(c.Name)+" "+body)
		}
	}
	persistence := ""
	if t.OpenGauss.Persistence == "u" {
		persistence = "UNLOGGED "
	}
	target := Quote(schema) + "." + Quote(t.Name)
	q := "CREATE " + persistence + "TABLE " + target + " (" + strings.Join(clauses, ",\n") + ")"
	if len(t.OpenGauss.Parameters) > 0 {
		q += " WITH (" + strings.Join(t.OpenGauss.Parameters, ",") + ")"
	}
	if t.OpenGauss.Tablespace != "" {
		q += " TABLESPACE " + Quote(t.OpenGauss.Tablespace)
	}
	p.SQL = append(p.SQL, q)
	for _, c := range t.Clauses {
		if c.OpenGauss == nil || c.OpenGauss.Comment == nil {
			continue
		}
		name := "COLUMN " + target + "." + Quote(c.Name)
		if c.Kind == "constraint" {
			name = "CONSTRAINT " + Quote(c.Name) + " ON " + target
		}
		q, e := gaussDesignComment(name, c.OpenGauss.Comment)
		if e != nil {
			return p, e
		}
		p.SQL = append(p.SQL, q)
	}
	for _, d := range t.Definitions {
		q, e := gaussDesignDefinition(d, schema, t.Name)
		if e != nil {
			return p, e
		}
		p.SQL = append(p.SQL, q)
		if e = gaussDesignDefinitionMetadata(&p, d, schema, target); e != nil {
			return p, e
		}
	}
	if t.OpenGauss.Comment != nil {
		q, e := gaussDesignComment("TABLE "+target, t.OpenGauss.Comment)
		if e != nil {
			return p, e
		}
		p.SQL = append(p.SQL, q)
	}
	return p, nil
}
