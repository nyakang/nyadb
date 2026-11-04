package schemaddl

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func pgCopyName(destination, source string) string {
	h := sha256.Sum256([]byte(destination + "\x00" + source))
	prefix := destination
	if len(prefix) > 32 {
		prefix = "copy"
	}
	return fmt.Sprintf("%s_%x", prefix, h[:8])
}
func pgCopyTarget(raw, keyword, schema, source, destination string) (string, error) {
	ts, e := postgresLex(raw)
	if e != nil {
		return "", e
	}
	for i, t := range ts {
		if !t.word(keyword) {
			continue
		}
		i++
		if i < len(ts) && ts[i].word("ONLY") {
			i++
		}
		if i >= len(ts) || !identifierToken(ts[i]) {
			return "", ErrUnsupported
		}
		start, end := i, i
		ns, name := "", pgTokenName(ts[i])
		if i+2 < len(ts) && ts[i+1].text == "." {
			end = i + 2
			ns, name = name, pgTokenName(ts[end])
		}
		if name == source && (ns == "" || ns == schema) {
			return raw[:ts[start].start] + Quote(schema) + "." + Quote(destination) + raw[ts[end].end:], nil
		}
		return raw, nil
	}
	return raw, nil
}

// PlanPostgreSQLCopy renders all DDL before executing anything. Names for
// schema-wide indexes and owned sequences are deterministic and independent.
func PlanPostgreSQLCopy(original Snapshot, destination string, data, self bool) (Plan, error) {
	if original.Engine != "postgresql" || !original.Exists || original.Postgres == nil || PostgreSQLIdentifier(destination) != nil || destination == original.Table.Name {
		return Plan{}, ErrUnsupported
	}
	catalog := original.Postgres
	revision, e := catalog.Fingerprint()
	if e != nil || revision != original.Revision {
		return Plan{}, ErrConflict
	}
	t, e := catalog.Table()
	if e != nil || Signature(t) != Signature(original.Table) {
		return Plan{}, ErrConflict
	}
	if t.Postgres == nil || t.Postgres.Kind != "r" || t.Postgres.IsPartition || len(t.Postgres.Parents) > 0 || t.Postgres.RowSecurity || t.Postgres.ForceRowSecurity || len(t.Postgres.ACL) > 0 {
		return Plan{}, ErrUnsupported
	}
	schema, source := catalog.Namespace, t.Name
	t = t.Clone()
	t.Name = destination
	// Refuse catalog features that are not represented by the copy declaration.
	for _, entry := range catalog.Entries {
		if entry.Kind == "policy" || entry.Kind == "publication_relation" || strings.HasPrefix(entry.Kind, "security_label:") {
			return Plan{}, errors.New("table contains catalog features unsupported by copying")
		}
		if entry.Kind == "rewrite" {
			var rule struct {
				Table int64 `json:"ev_class"`
			}
			if json.Unmarshal(entry.Data, &rule) != nil || rule.Table == catalog.RelationOID {
				return Plan{}, errors.New("table rewrite rules cannot be represented by copying")
			}
		}
	}
	owned := map[int64]int{}
	for _, entry := range catalog.Entries {
		if entry.Kind != "dependency" {
			continue
		}
		var dep struct {
			Edges []struct {
				Class    json.Number `json:"classid"`
				RefClass json.Number `json:"refclassid"`
				Ref      json.Number `json:"refobjid"`
				Sub      int         `json:"refobjsubid"`
				Type     string      `json:"deptype"`
			}
		}
		if json.Unmarshal(entry.Data, &dep) != nil {
			return Plan{}, ErrUnsupported
		}
		for _, edge := range dep.Edges {
			if edge.Class.String() == "1259" && edge.RefClass.String() == "1259" && edge.Ref.String() == fmt.Sprint(catalog.RelationOID) && edge.Sub > 0 && (edge.Type == "a" || edge.Type == "i") {
				owned[entry.OID] = edge.Sub
			}
		}
	}
	var before, after []string
	sequenceNames := map[int64]string{}
	constraintNames := map[int64]string{}
	for _, entry := range catalog.Entries {
		columnNumber, ok := owned[entry.OID]
		if entry.Kind != "sequence" || !ok {
			continue
		}
		var seq postgresSequenceRecord
		var seqType struct {
			Type json.Number `json:"seqtypid"`
		}
		if json.Unmarshal(entry.Data, &seqType) != nil {
			return Plan{}, ErrUnsupported
		}
		typeSQL := map[string]string{"20": "bigint", "23": "integer", "21": "smallint"}[seqType.Type.String()]
		if typeSQL == "" {
			return Plan{}, ErrUnsupported
		}
		if json.Unmarshal(entry.Data, &seq) != nil {
			return Plan{}, ErrUnsupported
		}
		for _, relation := range catalog.Entries {
			if relation.Kind == "relation" && relation.OID == entry.OID {
				var v struct {
					Name   string `json:"relname"`
					Schema string `json:"namespace"`
				}
				if json.Unmarshal(relation.Data, &v) != nil {
					return Plan{}, ErrUnsupported
				}
				seq.Name, seq.Schema = v.Name, v.Schema
			}
		}
		if PostgreSQLIdentifier(seq.Name) != nil || PostgreSQLIdentifier(seq.Schema) != nil {
			return Plan{}, ErrUnsupported
		}
		name := pgCopyName(destination, seq.Name)
		sequenceNames[entry.OID] = name
		found := false
		for i := range t.Clauses {
			c := &t.Clauses[i]
			if c.Kind != "column" || c.ID != fmt.Sprintf("column:%d", columnNumber) {
				continue
			}
			found = true
			f, e := ParsePostgreSQLField(c.SQL)
			if e != nil || f.Unsupported {
				return Plan{}, ErrUnsupported
			}
			if f.Identity != "" {
				_, _, options, e := PostgreSQLSequenceName(f.IdentityOptions)
				if e != nil {
					return Plan{}, e
				}
				f.IdentityOptions = "SEQUENCE NAME " + Quote(schema) + "." + Quote(name) + " " + options
			} else {
				expected := "nextval('" + strings.ReplaceAll(Quote(seq.Schema)+"."+Quote(seq.Name), "'", "''") + "'::regclass)"
				// Deparsed simple names may be unquoted in a regclass literal.
				plain := "nextval('" + strings.ReplaceAll(seq.Schema+"."+seq.Name, "'", "''") + "'::regclass)"
				if !f.HasDefault || postgresSQLSignature(f.Default) != postgresSQLSignature(expected) && postgresSQLSignature(f.Default) != postgresSQLSignature(plain) {
					return Plan{}, errors.New("owned sequence has an unsupported default expression")
				}
				f.Default = "nextval('" + strings.ReplaceAll(Quote(schema)+"."+Quote(name), "'", "''") + "'::regclass)"
				cycle := "NO CYCLE"
				if seq.Cycle {
					cycle = "CYCLE"
				}
				before = append(before, fmt.Sprintf("CREATE SEQUENCE %s.%s AS %s INCREMENT BY %d START WITH %d MINVALUE %d MAXVALUE %d CACHE %d %s", Quote(schema), Quote(name), typeSQL, seq.Increment, seq.Start, seq.Minimum, seq.Maximum, seq.Cache, cycle))
				if t.Postgres.Owner != "" {
					after = append(after, "ALTER SEQUENCE "+Quote(schema)+"."+Quote(name)+" OWNER TO "+Quote(t.Postgres.Owner))
				}
				after = append(after, "ALTER SEQUENCE "+Quote(schema)+"."+Quote(name)+" OWNED BY "+Quote(schema)+"."+Quote(destination)+"."+Quote(c.Name))
			}
			c.SQL, e = f.SQL()
			if e != nil {
				return Plan{}, e
			}
			if data {
				after = append(after, "SELECT pg_catalog.setval('"+strings.ReplaceAll(Quote(schema)+"."+Quote(name), "'", "''")+"'::regclass,last_value,is_called) FROM "+Quote(seq.Schema)+"."+Quote(seq.Name))
			}
		}
		if !found {
			return Plan{}, ErrUnsupported
		}
	}
	for i := range t.Clauses {
		c := &t.Clauses[i]
		if c.Kind == "constraint" {
			old := c.Name
			c.Name = pgCopyName(destination, old)
			if oid, e := strconv.ParseInt(strings.TrimPrefix(c.ID, "constraint:"), 10, 64); e == nil {
				constraintNames[oid] = c.Name
			}
			ts, e := postgresLex(c.SQL)
			if e != nil || len(ts) < 2 {
				return Plan{}, ErrUnsupported
			}
			c.SQL = c.SQL[:ts[1].start] + Quote(c.Name) + c.SQL[ts[1].end:]
			if self {
				c.SQL, e = pgCopyTarget(c.SQL, "REFERENCES", schema, source, destination)
				if e != nil {
					return Plan{}, e
				}
			}
		}
	}
	for i := range t.Definitions {
		d := &t.Definitions[i]
		if d.Kind == "index" {
			old := d.Name
			d.Name = pgCopyName(destination, old)
			ts, e := postgresLex(d.SQL)
			if e != nil {
				return Plan{}, e
			}
			for j, x := range ts {
				if x.word("INDEX") && j+1 < len(ts) {
					d.SQL = d.SQL[:ts[j+1].start] + Quote(d.Name) + d.SQL[ts[j+1].end:]
					break
				}
			}
		}
		d.SQL, e = pgCopyTarget(d.SQL, "ON", schema, source, destination)
		if e != nil {
			return Plan{}, e
		}
	}
	for _, entry := range catalog.Entries {
		if entry.Kind != "relation" && entry.Kind != "index" && entry.Kind != "constraint" {
			continue
		}
		var meta struct {
			Name       string   `json:"name"`
			Comment    string   `json:"comment"`
			Relation   int64    `json:"relation_oid"`
			Constraint int64    `json:"constraint_oid"`
			ACL        []string `json:"relacl"`
		}
		if json.Unmarshal(entry.Data, &meta) != nil {
			return Plan{}, ErrUnsupported
		}
		target := ""
		switch entry.Kind {
		case "constraint":
			if meta.Relation == catalog.RelationOID && meta.Comment != "" {
				name := constraintNames[entry.OID]
				if name == "" {
					return Plan{}, errors.New("commented constraint cannot be represented by copying")
				}
				target = "CONSTRAINT " + Quote(name) + " ON " + Quote(schema) + "." + Quote(destination)
			}
		case "index":
			if meta.Relation == catalog.RelationOID && meta.Constraint != 0 && meta.Comment != "" {
				name := constraintNames[meta.Constraint]
				if name == "" {
					return Plan{}, ErrUnsupported
				}
				target = "INDEX " + Quote(schema) + "." + Quote(name)
			}
		case "relation":
			if name, ok := sequenceNames[entry.OID]; ok {
				if len(meta.ACL) > 0 {
					return Plan{}, errors.New("sequence ACL cannot be represented by copying")
				}
				if meta.Comment != "" {
					target = "SEQUENCE " + Quote(schema) + "." + Quote(name)
				}
			}
		}
		if target != "" {
			value, e := postgresComment(meta.Comment)
			if e != nil {
				return Plan{}, e
			}
			after = append(after, "COMMENT ON "+target+" IS "+value)
		}
	}
	if e = validatePostgreSQLDraft(t); e != nil {
		return Plan{}, e
	}
	p, e := pgCreatePlan(t, schema, catalog.ServerVersion)
	if e != nil {
		return Plan{}, fmt.Errorf("copy create plan: %w", e)
	}
	var middle, triggers []string
	for _, statement := range p.SQL {
		if strings.Contains(statement, "CREATE TRIGGER ") || strings.Contains(statement, "CREATE CONSTRAINT TRIGGER ") || strings.Contains(statement, " TRIGGER ") {
			triggers = append(triggers, statement)
		} else {
			middle = append(middle, statement)
		}
	}
	if data {
		var columns []string
		for _, c := range t.Columns() {
			f, e := ParsePostgreSQLField(c.SQL)
			if e != nil {
				return Plan{}, e
			}
			if f.Generation == "" {
				columns = append(columns, Quote(c.Name))
			}
		}
		if len(columns) == 0 {
			return Plan{}, ErrUnsupported
		}
		cols := strings.Join(columns, ",")
		middle = append(middle, "INSERT INTO "+Quote(schema)+"."+Quote(destination)+" ("+cols+") OVERRIDING SYSTEM VALUE SELECT "+cols+" FROM "+Quote(schema)+"."+Quote(source))
	}
	p.Mode = "copy"
	p.SQL = append(before, middle...)
	p.SQL = append(p.SQL, after...)
	p.SQL = append(p.SQL, triggers...)
	return p, nil
}
