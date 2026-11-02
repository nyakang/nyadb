package schemaddl

import (
	"errors"
	"fmt"
	"strings"
)

// PlanPostgreSQL generates transactional native DDL. It never recreates a
// retained table or appends CASCADE. Adapters must replan under the original
// locks and verify retained typed values/dependencies before committing.
func PlanPostgreSQL(original Snapshot, desired Table, schema string, allowDrop bool) (Plan, error) {
	if original.Engine != "postgresql" || original.Postgres == nil || original.Postgres.Namespace != schema || PostgreSQLIdentifier(schema) != nil {
		return Plan{}, ErrUnsupported
	}
	revision, err := original.Postgres.Fingerprint()
	if err != nil || revision != original.Revision {
		return Plan{}, errors.Join(ErrConflict, err)
	}
	if original.Exists {
		if desired.Postgres == nil {
			return Plan{}, ErrUnsupported
		}
		canonical, e := original.Postgres.Table()
		if e != nil || Signature(canonical) != Signature(original.Table) {
			return Plan{}, errors.Join(ErrConflict, e)
		}
		if canonical.Postgres.Kind != "r" || canonical.Postgres.IsPartition || len(canonical.Postgres.Parents) != 0 {
			return Plan{}, ErrUnsupported
		}
	} else if original.Postgres.RelationOID != 0 {
		return Plan{}, ErrConflict
	}
	if err = validatePostgreSQLDraft(desired); err != nil {
		return Plan{}, err
	}
	if !original.Exists {
		return pgCreatePlan(desired, schema, original.Postgres.ServerVersion)
	}
	p := Plan{Mode: "alter"}
	old := original.Table
	if Signature(old) == Signature(desired) {
		p.Mode = "noop"
		return p, nil
	}
	if old.Name != desired.Name {
		x := old.Clone()
		x.Name = desired.Name
		if Signature(x) != Signature(desired) {
			return Plan{}, errors.New("rename the PostgreSQL table before other schema changes")
		}
		p.Mode, p.RenameFrom, p.RenameTo = "rename_table", old.Name, desired.Name
		p.SQL = []string{"ALTER TABLE " + Quote(schema) + "." + Quote(old.Name) + " RENAME TO " + Quote(desired.Name)}
		return p, nil
	}
	oldColumns := map[string]Clause{}
	oldIDs := map[string]Clause{}
	for _, c := range old.Clauses {
		oldIDs[c.ID] = c
		if c.Kind == "column" {
			oldColumns[c.ID] = c
		}
	}
	kept := map[string]bool{}
	var renames []Mapping
	for _, c := range desired.Clauses {
		if prior, ok := oldIDs[c.ID]; ok && prior.Kind != c.Kind {
			return Plan{}, errors.New("stable PostgreSQL clause identity changed kind")
		}
		if c.Kind != "column" {
			continue
		}
		if prior, ok := oldColumns[c.ID]; ok {
			kept[c.ID] = true
			p.Mappings = append(p.Mappings, Mapping{c.ID, prior.Name, c.Name})
			if prior.Name != c.Name {
				renames = append(renames, Mapping{c.ID, prior.Name, c.Name})
			}
		}
	}
	if len(renames) > 0 {
		if len(renames) != 1 {
			return Plan{}, errors.New("rename one PostgreSQL column at a time")
		}
		x := old.Clone()
		for i := range x.Clauses {
			if x.Clauses[i].ID == renames[0].ID {
				x.Clauses[i].Name = renames[0].Destination
			}
		}
		if Signature(x) != Signature(desired) {
			return Plan{}, errors.New("rename the PostgreSQL column before other schema changes")
		}
		p.Mode, p.RenameFrom, p.RenameTo = "rename_column", renames[0].Source, renames[0].Destination
		p.SQL = []string{"ALTER TABLE " + Quote(schema) + "." + Quote(old.Name) + " RENAME COLUMN " + Quote(p.RenameFrom) + " TO " + Quote(p.RenameTo)}
		return p, nil
	}
	for _, c := range old.Columns() {
		if !kept[c.ID] {
			p.Dropped = append(p.Dropped, c.Name)
		}
	}
	if len(p.Dropped) > 0 && !allowDrop {
		return Plan{}, errors.New("PostgreSQL column removal needs explicit data-loss confirmation")
	}
	// Native PostgreSQL ALTER cannot reorder retained attributes or insert new
	// attributes in between them. Such an edit needs a separate data migration.
	var expected, actual []string
	for _, c := range old.Columns() {
		if kept[c.ID] {
			expected = append(expected, c.ID)
		}
	}
	added := false
	for _, c := range desired.Columns() {
		if kept[c.ID] {
			if added {
				return Plan{}, errors.New("new PostgreSQL columns must follow retained columns")
			}
			actual = append(actual, c.ID)
		} else {
			added = true
		}
	}
	if Signature(expected) != Signature(actual) {
		return Plan{}, errors.New("reordering PostgreSQL columns requires a data migration")
	}
	target := Quote(schema) + "." + Quote(old.Name)
	var tail []string
	oldDefs, newDefs := map[string]Definition{}, map[string]Definition{}
	rebuildTriggers := false
	for _, c := range desired.Columns() {
		prior, ok := oldColumns[c.ID]
		if !ok || postgresSQLSignature(prior.SQL) == postgresSQLSignature(c.SQL) {
			continue
		}
		a, e := ParsePostgreSQLField(prior.SQL)
		if e != nil || a.Unsupported {
			return Plan{}, errors.Join(ErrUnsupported, e)
		}
		b, e := ParsePostgreSQLField(c.SQL)
		if e != nil || b.Unsupported {
			return Plan{}, errors.Join(ErrUnsupported, e)
		}
		if postgresSQLSignature(a.Type) != postgresSQLSignature(b.Type) || postgresSQLSignature(a.Collation) != postgresSQLSignature(b.Collation) {
			rebuildTriggers = true
		}
	}
	for _, d := range old.Definitions {
		oldDefs[d.Kind+":"+d.Name] = d
	}
	for _, d := range desired.Definitions {
		newDefs[d.Kind+":"+d.Name] = d
	}
	for _, d := range old.Definitions {
		next, ok := newDefs[d.Kind+":"+d.Name]
		if !ok || postgresSQLSignature(next.SQL) != postgresSQLSignature(d.SQL) || d.Kind == "trigger" && rebuildTriggers {
			if d.Kind == "index" {
				p.SQL = append(p.SQL, "DROP INDEX "+Quote(schema)+"."+Quote(d.Name)+" RESTRICT")
			} else {
				p.SQL = append(p.SQL, "DROP TRIGGER "+Quote(d.Name)+" ON "+target+" RESTRICT")
			}
		}
	}
	oldConstraints, newConstraints := map[string]Clause{}, map[string]Clause{}
	for _, c := range old.Clauses {
		if c.Kind == "constraint" {
			oldConstraints[c.ID] = c
		}
	}
	for _, c := range desired.Clauses {
		if c.Kind == "constraint" {
			newConstraints[c.ID] = c
		}
	}
	for _, c := range old.Clauses {
		if c.Kind != "constraint" {
			continue
		}
		next, ok := newConstraints[c.ID]
		if ok && c.Name != next.Name {
			a, e := pgConstraintBody(c, schema)
			if e != nil {
				return Plan{}, e
			}
			b, e := pgConstraintBody(next, schema)
			if e != nil {
				return Plan{}, e
			}
			if postgresSQLSignature(a) == postgresSQLSignature(b) {
				p.SQL = append(p.SQL, "ALTER TABLE ONLY "+target+" RENAME CONSTRAINT "+Quote(c.Name)+" TO "+Quote(next.Name))
				continue
			}
		}
		if !ok || postgresSQLSignature(c.SQL) != postgresSQLSignature(next.SQL) {
			p.SQL = append(p.SQL, "ALTER TABLE ONLY "+target+" DROP CONSTRAINT "+Quote(c.Name)+" RESTRICT")
		}
	}
	for _, name := range p.Dropped {
		p.SQL = append(p.SQL, "ALTER TABLE ONLY "+target+" DROP COLUMN "+Quote(name)+" RESTRICT")
	}
	for _, c := range desired.Columns() {
		prior, ok := oldColumns[c.ID]
		if !ok {
			f, e := ParsePostgreSQLField(c.SQL)
			if e != nil || f.Unsupported {
				return Plan{}, errors.Join(ErrUnsupported, e)
			}
			if f.GenerationStorage == "VIRTUAL" && original.Postgres.ServerVersion < 180000 {
				return Plan{}, ErrUnsupported
			}
			p.SQL = append(p.SQL, "ALTER TABLE ONLY "+target+" ADD COLUMN "+Quote(c.Name)+" "+c.SQL+"\n")
			meta, e := pgNewColumnMetadata(c, target)
			if e != nil {
				return Plan{}, e
			}
			tail = append(tail, meta...)
			continue
		}
		if prior.Postgres == nil || c.Postgres == nil || !prior.Postgres.Local || prior.Postgres.InheritanceCount != 0 {
			return Plan{}, ErrUnsupported
		}
		if postgresSQLSignature(prior.SQL) != postgresSQLSignature(c.SQL) {
			a, e := ParsePostgreSQLField(prior.SQL)
			if e != nil || a.Unsupported {
				return Plan{}, errors.Join(ErrUnsupported, e)
			}
			b, e := ParsePostgreSQLField(c.SQL)
			if e != nil || b.Unsupported {
				return Plan{}, errors.Join(ErrUnsupported, e)
			}
			changes, e := pgAlterField(target, c.Name, a, b, original.Postgres.ServerVersion)
			if e != nil {
				return Plan{}, e
			}
			p.SQL = append(p.SQL, changes...)
			if postgresSQLSignature(a.Type) != postgresSQLSignature(b.Type) || postgresSQLSignature(a.Collation) != postgresSQLSignature(b.Collation) {
				// PostgreSQL TYPE resets attstorage and may reset statistics.
				// Reapply the desired public attributes even when the user kept
				// their original values. Opaque ACL/options remain unchanged.
				retained := c
				retained.Postgres = c.Postgres.Clone()
				retained.Postgres.ACL = nil
				retained.Postgres.Parameters = nil
				retained.Postgres.ForeignOptions = nil
				properties, e := pgNewColumnMetadata(retained, target)
				if e != nil {
					return Plan{}, e
				}
				tail = append(tail, properties...)
			}
		}
	}
	for _, c := range desired.Clauses {
		if c.Kind != "constraint" {
			continue
		}
		prior, ok := oldConstraints[c.ID]
		if ok && prior.Name != c.Name {
			a, _ := pgConstraintBody(prior, schema)
			b, _ := pgConstraintBody(c, schema)
			if postgresSQLSignature(a) == postgresSQLSignature(b) {
				continue
			}
		}
		if !ok || postgresSQLSignature(prior.SQL) != postgresSQLSignature(c.SQL) {
			body, e := pgConstraintBody(c, schema)
			if e != nil {
				return Plan{}, e
			}
			p.SQL = append(p.SQL, "ALTER TABLE ONLY "+target+" ADD CONSTRAINT "+Quote(c.Name)+" "+body+"\n")
		}
	}
	for _, d := range desired.Definitions {
		prior, ok := oldDefs[d.Kind+":"+d.Name]
		recreated := !ok || postgresSQLSignature(prior.SQL) != postgresSQLSignature(d.SQL) || d.Kind == "trigger" && rebuildTriggers
		if recreated {
			sql, e := DefinitionPostgreSQL(d, schema, desired.Name)
			if e != nil {
				return Plan{}, e
			}
			p.SQL = append(p.SQL, sql)
		}
		meta, e := pgDefinitionMetadata(d, prior, target, schema, recreated)
		if e != nil {
			return Plan{}, e
		}
		tail = append(tail, meta...)
	}
	// Metadata planning sees original field SQL/identity while copying only
	// requested properties. Dropped attributes need no metadata statement.
	metadata := old.Clone()
	for i := range metadata.Clauses {
		for _, c := range desired.Columns() {
			if metadata.Clauses[i].ID == c.ID {
				metadata.Clauses[i].Postgres = c.Postgres.Clone()
			}
		}
	}
	metadata.Postgres = old.Postgres.Clone()
	metadata.Postgres.Comment = desired.Postgres.Comment
	meta, e := PlanPostgreSQLMetadata(original, metadata, schema)
	if e != nil {
		return Plan{}, e
	}
	tail = append(tail, meta.SQL...)
	options, e := pgAlterTableProperties(old.Postgres, desired.Postgres, target)
	if e != nil {
		return Plan{}, e
	}
	tail = append(tail, options...)
	p.SQL = append(p.SQL, tail...)
	retainedMetadata, e := pgRetainedObjectMetadata(original, desired, target)
	if e != nil {
		return Plan{}, e
	}
	p.SQL = append(p.SQL, retainedMetadata...)
	if len(p.SQL) == 0 {
		p.Mode = "noop"
	}
	return p, nil
}

func validatePostgreSQLDraft(t Table) error {
	if PostgreSQLIdentifier(t.Name) != nil || t.Options != "" || len(t.Clauses) == 0 || len(t.Clauses) > 1600 || len(t.Definitions) > 1000 {
		return ErrUnsupported
	}
	ids, names, definitions, constraints := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	bytes, columns := 0, 0
	for _, c := range t.Clauses {
		bytes += len(c.ID) + len(c.Name) + len(c.SQL)
		if c.ID == "" || len(c.ID) > 256 || ids[c.ID] || PostgreSQLIdentifier(c.Name) != nil || c.Kind != "column" && c.Kind != "constraint" {
			return errors.New("invalid PostgreSQL clause identity")
		}
		ids[c.ID] = true
		if c.Kind == "column" {
			columns++
			if names[c.Name] {
				return errors.New("duplicated PostgreSQL column")
			}
			names[c.Name] = true
		} else if c.Postgres != nil {
			return ErrUnsupported
		} else if constraints[c.Name] {
			return errors.New("duplicated PostgreSQL constraint name")
		} else {
			constraints[c.Name] = true
		}
		if _, err := postgresFragment(c.SQL); err != nil {
			return err
		}
		if bytes > 1<<20 {
			return errors.New("PostgreSQL table definition exceeds size limit")
		}
	}
	if columns == 0 {
		return errors.New("PostgreSQL table needs a column")
	}
	for _, d := range t.Definitions {
		bytes += len(d.Name) + len(d.SQL)
		if bytes > 16<<20 || PostgreSQLIdentifier(d.Name) != nil || d.Kind != "index" && d.Kind != "trigger" || definitions[d.Kind+":"+d.Name] {
			return ErrUnsupported
		}
		definitions[d.Kind+":"+d.Name] = true
	}
	if t.Postgres != nil && (t.Postgres.Kind != "" && t.Postgres.Kind != "r" || t.Postgres.IsPartition || t.Postgres.PartitionKey != "" || t.Postgres.PartitionBound != "" || len(t.Postgres.Parents) > 0) {
		return ErrUnsupported
	}
	return nil
}

func pgConstraintBody(c Clause, schema string) (string, error) {
	ts, err := postgresFragment(c.SQL)
	if err != nil {
		return "", err
	}
	if len(ts) < 4 || !ts[0].word("CONSTRAINT") || !identifierToken(ts[1]) || pgTokenName(ts[1]) != c.Name {
		return "", errors.New("PostgreSQL constraint name differs from its stable clause")
	}
	if !(ts[2].word("PRIMARY") || ts[2].word("UNIQUE") || ts[2].word("FOREIGN") || ts[2].word("CHECK") || ts[2].word("EXCLUDE")) {
		return "", ErrUnsupported
	}
	body := strings.TrimSpace(c.SQL[ts[2].start:])
	if !ts[2].word("FOREIGN") {
		return body, nil
	}
	refTokens, err := postgresFragment(body)
	if err != nil {
		return "", err
	}
	for i, t := range refTokens {
		if !t.word("REFERENCES") {
			continue
		}
		at := i + 1
		if at < len(refTokens) && refTokens[at].word("ONLY") {
			at++
		}
		if at >= len(refTokens) || !identifierToken(refTokens[at]) {
			return "", ErrUnsupported
		}
		refSchema, name, end := schema, pgTokenName(refTokens[at]), at+1
		if end < len(refTokens) && refTokens[end].text == "." {
			if end+1 >= len(refTokens) || !identifierToken(refTokens[end+1]) {
				return "", ErrUnsupported
			}
			refSchema, name, end = name, pgTokenName(refTokens[end+1]), end+2
		}
		if PostgreSQLIdentifier(refSchema) != nil || PostgreSQLIdentifier(name) != nil || end < len(refTokens) && refTokens[end].text == "." {
			return "", ErrUnsupported
		}
		return body[:refTokens[at].start] + Quote(refSchema) + "." + Quote(name) + body[refTokens[end-1].end:], nil
	}
	return "", errors.New("PostgreSQL foreign key reference is missing")
}

func pgAlterField(target, name string, a, b PostgreSQLField, version int) ([]string, error) {
	var sql []string
	base := "ALTER TABLE ONLY " + target + " ALTER COLUMN " + Quote(name)
	typeChanged := postgresSQLSignature(a.Type) != postgresSQLSignature(b.Type) || postgresSQLSignature(a.Collation) != postgresSQLSignature(b.Collation)
	if a.Identity != "" && b.Identity != "" && postgresSQLSignature(a.IdentityOptions) != postgresSQLSignature(b.IdentityOptions) {
		return nil, errors.New("identity sequence option changes require a separate sequence plan")
	}
	if a.Generation != "" && b.Generation != "" && a.GenerationStorage != b.GenerationStorage || a.Generation == "" && b.Generation != "" {
		return nil, errors.New("changing generated storage needs a separate data migration")
	}
	if a.Generation != "" && b.Generation == "" {
		sql = append(sql, base+" DROP EXPRESSION")
	}
	if a.Identity != "" && b.Identity == "" {
		sql = append(sql, base+" DROP IDENTITY")
	}
	if a.HasDefault && (typeChanged || !b.HasDefault) {
		sql = append(sql, base+" DROP DEFAULT")
	}
	if typeChanged {
		if a.Generation != "" && b.Generation != "" {
			return nil, errors.New("change generated expression before changing its type")
		}
		statement := base + " TYPE " + b.Type + "\n"
		if b.Collation != "" {
			statement += " COLLATE " + b.Collation + "\n"
		}
		statement += " USING " + Quote(name) + "::" + b.Type + "\n"
		sql = append(sql, statement)
		if a.Identity != "" && b.Identity != "" {
			// TYPE also widens default identity sequence bounds. The captured
			// declaration has explicit original bounds; restore those settings
			// without RESTART, which would change its current value.
			schema, sequence, options, e := PostgreSQLSequenceName(b.IdentityOptions)
			if e != nil || sequence == "" || strings.TrimSpace(options) == "" {
				return nil, errors.Join(errors.New("identity type changes require complete sequence identity and options"), e)
			}
			if schema == "" {
				ts, e := postgresFragment(target)
				if e != nil {
					return nil, e
				}
				schema, _, _, e = qualifiedName(ts, 0)
				if e != nil || schema == "" {
					return nil, ErrUnsupported
				}
			}
			sql = append(sql, "ALTER SEQUENCE "+Quote(schema)+"."+Quote(sequence)+" "+options+"\n")
		}
	}
	if a.Identity == "" && b.Identity != "" {
		if !a.NotNull {
			sql = append(sql, base+" SET NOT NULL")
		}
		statement := base + " ADD GENERATED " + b.Identity + " AS IDENTITY"
		if b.IdentityOptions != "" {
			statement += " (" + b.IdentityOptions + "\n)"
		}
		sql = append(sql, statement)
	} else if a.Identity != "" && b.Identity != "" && a.Identity != b.Identity {
		sql = append(sql, base+" SET GENERATED "+b.Identity)
	}
	if a.Generation != "" && b.Generation != "" && postgresSQLSignature(a.Generation) != postgresSQLSignature(b.Generation) {
		if version < 170000 {
			return nil, ErrUnsupported
		}
		sql = append(sql, base+" SET EXPRESSION AS ("+b.Generation+"\n)")
	}
	if b.HasDefault && (typeChanged || !a.HasDefault || postgresSQLSignature(a.Default) != postgresSQLSignature(b.Default)) {
		sql = append(sql, base+" SET DEFAULT "+b.Default+"\n")
	}
	if a.NotNull != b.NotNull && !(a.Identity == "" && b.Identity != "") {
		mode := "DROP"
		if b.NotNull {
			mode = "SET"
		}
		sql = append(sql, base+" "+mode+" NOT NULL")
	}
	return sql, nil
}

func pgTokenName(t token) string {
	if t.kind == 'w' {
		return asciiLower(t.text)
	}
	return t.text
}

// DefinitionPostgreSQL binds one CREATE INDEX/TRIGGER to the captured table.
// PostgreSQL index/trigger names are unqualified; the target is fully qualified.
// Concurrent/conditional/replacement forms cannot acquire transaction authority.
func DefinitionPostgreSQL(d Definition, schema, table string) (string, error) {
	if d.Kind != "index" && d.Kind != "trigger" || PostgreSQLIdentifier(d.Name) != nil || PostgreSQLIdentifier(schema) != nil || PostgreSQLIdentifier(table) != nil {
		return "", ErrUnsupported
	}
	raw := strings.TrimSpace(d.SQL)
	ts, err := postgresLex(raw)
	if err != nil {
		return "", err
	}
	if len(ts) > 0 && ts[len(ts)-1].kind == 'p' && ts[len(ts)-1].text == ";" {
		raw = strings.TrimSpace(raw[:ts[len(ts)-1].start])
	}
	ts, err = postgresTokens(raw, true)
	if err != nil {
		return "", err
	}
	if len(ts) < 6 || !ts[0].word("CREATE") {
		return "", ErrUnsupported
	}
	at := 1
	if ts[at].word("UNIQUE") && d.Kind == "index" || ts[at].word("CONSTRAINT") && d.Kind == "trigger" {
		at++
	}
	if at >= len(ts) || !ts[at].word(strings.ToUpper(d.Kind)) {
		return "", ErrUnsupported
	}
	at++
	if at >= len(ts) || !identifierToken(ts[at]) || ts[at].word("CONCURRENTLY") || ts[at].word("IF") || pgTokenName(ts[at]) != d.Name {
		return "", errors.New("PostgreSQL definition identity differs from its captured name")
	}
	nameAt := at
	at++
	if at < len(ts) && ts[at].text == "." {
		return "", errors.New("PostgreSQL index/trigger name must be unqualified")
	}
	on := -1
	for i := at; i < len(ts); i++ {
		if ts[i].word("ON") {
			on = i
			break
		}
		if ts[i].word("EXECUTE") {
			break
		}
	}
	if on < 0 {
		return "", errors.New("PostgreSQL definition has no target table")
	}
	start := on + 1
	if start < len(ts) && ts[start].word("ONLY") {
		start++
	}
	if start >= len(ts) || !identifierToken(ts[start]) {
		return "", ErrUnsupported
	}
	end := start + 1
	targetSchema, target := "", pgTokenName(ts[start])
	if end < len(ts) && ts[end].text == "." {
		if end+1 >= len(ts) || !identifierToken(ts[end+1]) {
			return "", ErrUnsupported
		}
		targetSchema, target = target, pgTokenName(ts[end+1])
		end += 2
	}
	if target != table || targetSchema != "" && targetSchema != schema {
		return "", errors.New("PostgreSQL definition target differs from the original table")
	}
	return raw[:ts[nameAt].start] + Quote(d.Name) + raw[ts[nameAt].end:ts[start].start] + Quote(schema) + "." + Quote(table) + raw[ts[end-1].end:] + "\n", nil
}

func pgNewColumnMetadata(c Clause, target string) ([]string, error) {
	if c.Postgres == nil {
		return nil, nil
	}
	p := c.Postgres
	if len(p.ACL) > 0 || len(p.Parameters) > 0 || len(p.ForeignOptions) > 0 || p.InheritanceCount != 0 {
		return nil, ErrUnsupported
	}
	var sql []string
	base := "ALTER TABLE ONLY " + target + " ALTER COLUMN " + Quote(c.Name)
	if p.Comment != "" {
		text, e := postgresComment(p.Comment)
		if e != nil {
			return nil, e
		}
		sql = append(sql, "COMMENT ON COLUMN "+target+"."+Quote(c.Name)+" IS "+text)
	}
	if p.Storage != "" {
		value := map[string]string{"p": "PLAIN", "e": "EXTERNAL", "m": "MAIN", "x": "EXTENDED"}[p.Storage]
		if value == "" {
			return nil, ErrUnsupported
		}
		sql = append(sql, base+" SET STORAGE "+value)
	}
	if p.Compression != "" {
		value := map[string]string{"p": "pglz", "l": "lz4"}[p.Compression]
		if value == "" {
			return nil, ErrUnsupported
		}
		sql = append(sql, base+" SET COMPRESSION "+value)
	}
	if p.Statistics != -1 {
		if p.Statistics < 0 || p.Statistics > 10000 {
			return nil, ErrUnsupported
		}
		sql = append(sql, fmt.Sprintf("%s SET STATISTICS %d", base, p.Statistics))
	}
	return sql, nil
}

func pgDefinitionMetadata(d, old Definition, target, schema string, recreated bool) ([]string, error) {
	if d.Postgres == nil {
		if old.Postgres != nil {
			return nil, ErrUnsupported
		}
		return nil, nil
	}
	a := old.Postgres
	if a == nil {
		a = &PostgreSQLDefinitionProperties{}
	}
	b := d.Postgres
	var sql []string
	if d.Kind == "index" && b.Enabled != "" {
		return nil, ErrUnsupported
	}
	if d.Kind == "trigger" && (recreated || a.Enabled != b.Enabled) {
		mode, ok := map[string]string{"": "ENABLE", "O": "ENABLE", "D": "DISABLE", "R": "ENABLE REPLICA", "A": "ENABLE ALWAYS"}[b.Enabled]
		if !ok {
			return nil, ErrUnsupported
		}
		sql = append(sql, "ALTER TABLE ONLY "+target+" "+mode+" TRIGGER "+Quote(d.Name))
	}
	if recreated && b.Comment != "" || !recreated && a.Comment != b.Comment {
		text, e := postgresComment(b.Comment)
		if e != nil {
			return nil, e
		}
		object := "INDEX " + Quote(schema) + "." + Quote(d.Name)
		if d.Kind == "trigger" {
			object = "TRIGGER " + Quote(d.Name) + " ON " + target
		}
		sql = append(sql, "COMMENT ON "+object+" IS "+text)
	}
	return sql, nil
}

func pgParameters(values []string) (map[string]string, []string, error) {
	result := map[string]string{}
	var order []string
	for _, raw := range values {
		key, value, ok := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || value == "" || result[key] != "" {
			return nil, nil, ErrUnsupported
		}
		ts, e := postgresFragment(key)
		if e != nil {
			return nil, nil, e
		}
		for i, t := range ts {
			if i%2 == 0 && t.kind != 'w' || i%2 == 1 && t.text != "." {
				return nil, nil, ErrUnsupported
			}
		}
		if len(ts)%2 == 0 {
			return nil, nil, ErrUnsupported
		}
		if _, e = postgresFragment(value); e != nil {
			return nil, nil, e
		}
		result[key] = value
		order = append(order, key)
	}
	return result, order, nil
}

func pgAlterTableProperties(a, b *PostgreSQLTableProperties, target string) ([]string, error) {
	if a == nil || b == nil {
		return nil, ErrUnsupported
	}
	x := b.Clone()
	x.Comment = a.Comment
	x.Persistence = a.Persistence
	x.AccessMethod = a.AccessMethod
	x.Tablespace = a.Tablespace
	x.ReplicaIdentity = a.ReplicaIdentity
	x.RowSecurity = a.RowSecurity
	x.ForceRowSecurity = a.ForceRowSecurity
	x.Parameters = a.Parameters
	if Signature(x) != Signature(a) {
		return nil, ErrUnsupported
	}
	var sql []string
	if a.Persistence != b.Persistence {
		value := map[string]string{"p": "LOGGED", "u": "UNLOGGED"}[b.Persistence]
		if value == "" {
			return nil, ErrUnsupported
		}
		sql = append(sql, "ALTER TABLE ONLY "+target+" SET "+value)
	}
	if a.AccessMethod != b.AccessMethod {
		if PostgreSQLIdentifier(b.AccessMethod) != nil {
			return nil, ErrUnsupported
		}
		sql = append(sql, "ALTER TABLE ONLY "+target+" SET ACCESS METHOD "+Quote(b.AccessMethod))
	}
	if a.Tablespace != b.Tablespace {
		if PostgreSQLIdentifier(b.Tablespace) != nil {
			return nil, ErrUnsupported
		}
		sql = append(sql, "ALTER TABLE ONLY "+target+" SET TABLESPACE "+Quote(b.Tablespace))
	}
	if a.ReplicaIdentity != b.ReplicaIdentity {
		value := map[string]string{"d": "DEFAULT", "n": "NOTHING", "f": "FULL"}[b.ReplicaIdentity]
		if value == "" {
			return nil, ErrUnsupported
		}
		sql = append(sql, "ALTER TABLE ONLY "+target+" REPLICA IDENTITY "+value)
	}
	if a.RowSecurity != b.RowSecurity {
		value := "DISABLE"
		if b.RowSecurity {
			value = "ENABLE"
		}
		sql = append(sql, "ALTER TABLE ONLY "+target+" "+value+" ROW LEVEL SECURITY")
	}
	if a.ForceRowSecurity != b.ForceRowSecurity {
		value := "NO FORCE"
		if b.ForceRowSecurity {
			value = "FORCE"
		}
		sql = append(sql, "ALTER TABLE ONLY "+target+" "+value+" ROW LEVEL SECURITY")
	}
	prior, oldOrder, e := pgParameters(a.Parameters)
	if e != nil {
		return nil, e
	}
	next, newOrder, e := pgParameters(b.Parameters)
	if e != nil {
		return nil, e
	}
	for _, key := range oldOrder {
		if _, ok := next[key]; !ok {
			sql = append(sql, "ALTER TABLE ONLY "+target+" RESET ("+key+")")
		}
	}
	for _, key := range newOrder {
		if next[key] != prior[key] {
			sql = append(sql, "ALTER TABLE ONLY "+target+" SET ("+key+"="+next[key]+"\n)")
		}
	}
	return sql, nil
}

func pgCreatePlan(t Table, schema string, version int) (Plan, error) {
	p := Plan{Mode: "create"}
	target := Quote(schema) + "." + Quote(t.Name)
	props := t.Postgres
	if props == nil {
		props = &PostgreSQLTableProperties{}
	}
	if len(props.ACL) > 0 {
		return Plan{}, ErrUnsupported
	}
	prefix := "CREATE TABLE "
	if props.Persistence == "u" {
		prefix = "CREATE UNLOGGED TABLE "
	} else if props.Persistence != "" && props.Persistence != "p" {
		return Plan{}, ErrUnsupported
	}
	var parts, tail []string
	for _, c := range t.Columns() {
		f, e := ParsePostgreSQLField(c.SQL)
		if e != nil || f.Unsupported || f.GenerationStorage == "VIRTUAL" && version < 180000 {
			return Plan{}, errors.Join(ErrUnsupported, e)
		}
		parts = append(parts, Quote(c.Name)+" "+c.SQL+"\n")
		meta, e := pgNewColumnMetadata(c, target)
		if e != nil {
			return Plan{}, e
		}
		tail = append(tail, meta...)
	}
	for _, c := range t.Clauses {
		if c.Kind == "constraint" {
			body, e := pgConstraintBody(c, schema)
			if e != nil {
				return Plan{}, e
			}
			parts = append(parts, "CONSTRAINT "+Quote(c.Name)+" "+body+"\n")
		}
	}
	statement := prefix + target + " (\n" + strings.Join(parts, ",\n") + ")"
	if props.AccessMethod != "" {
		if PostgreSQLIdentifier(props.AccessMethod) != nil {
			return Plan{}, ErrUnsupported
		}
		statement += " USING " + Quote(props.AccessMethod)
	}
	parameters, order, e := pgParameters(props.Parameters)
	if e != nil {
		return Plan{}, e
	}
	var options []string
	for _, key := range order {
		options = append(options, key+"="+parameters[key]+"\n")
	}
	if len(options) > 0 {
		statement += " WITH (" + strings.Join(options, ",") + ")"
	}
	if props.Tablespace != "" {
		if PostgreSQLIdentifier(props.Tablespace) != nil {
			return Plan{}, ErrUnsupported
		}
		statement += " TABLESPACE " + Quote(props.Tablespace)
	}
	p.SQL = append(p.SQL, statement)
	for _, d := range t.Definitions {
		sql, e := DefinitionPostgreSQL(d, schema, t.Name)
		if e != nil {
			return Plan{}, e
		}
		p.SQL = append(p.SQL, sql)
		meta, e := pgDefinitionMetadata(d, Definition{}, target, schema, true)
		if e != nil {
			return Plan{}, e
		}
		tail = append(tail, meta...)
	}
	if props.Comment != "" {
		text, e := postgresComment(props.Comment)
		if e != nil {
			return Plan{}, e
		}
		tail = append(tail, "COMMENT ON TABLE "+target+" IS "+text)
	}
	if props.RowSecurity {
		tail = append(tail, "ALTER TABLE ONLY "+target+" ENABLE ROW LEVEL SECURITY")
	}
	if props.ForceRowSecurity {
		tail = append(tail, "ALTER TABLE ONLY "+target+" FORCE ROW LEVEL SECURITY")
	}
	if props.ReplicaIdentity != "" && props.ReplicaIdentity != "d" {
		value := map[string]string{"n": "NOTHING", "f": "FULL"}[props.ReplicaIdentity]
		if value == "" {
			return Plan{}, ErrUnsupported
		}
		tail = append(tail, "ALTER TABLE ONLY "+target+" REPLICA IDENTITY "+value)
	}
	if props.Owner != "" {
		if PostgreSQLIdentifier(props.Owner) != nil {
			return Plan{}, ErrUnsupported
		}
		tail = append(tail, "ALTER TABLE ONLY "+target+" OWNER TO "+Quote(props.Owner))
	}
	p.SQL = append(p.SQL, tail...)
	return p, nil
}
