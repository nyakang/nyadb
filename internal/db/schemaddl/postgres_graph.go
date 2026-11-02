package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// PostgreSQLGraphNames joins independently created catalog names to the
// immutable desired declaration. Probe names are deliberately distinct from
// the original assets; names alone must never infer that join.
type PostgreSQLGraphNames struct {
	Columns, Constraints, Indexes, Triggers map[string]string
}

// PostgreSQLCatalogObject is a typed catalog identity. Numeric OIDs can be
// shared by unrelated catalogs and therefore cannot be keyed on their own.
type PostgreSQLCatalogObject struct {
	ClassOID, ObjectOID int64
}

// PostgreSQLGraphBindings is identity evidence, not a shape/preservation
// proof or permission to execute DDL. Unselected context records remain in
// the original complete catalog for the later preservation comparison.
type PostgreSQLGraphBindings struct {
	Classes    map[string]int64
	Objects    map[PostgreSQLCatalogObject]string
	Attributes map[int64]map[int]string
	Node       PostgreSQLNodeBindings
}

type pgGraphRecord struct {
	entry PostgreSQLCatalogEntry
	data  map[string]json.RawMessage
}

func (r pgGraphRecord) integer(field string) (int64, error) {
	// PostgreSQL to_jsonb emits oid-typed fields as decimal strings, while
	// explicitly cast bigint and attribute numbers are JSON numbers.
	raw := string(r.data[field])
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(r.data[field], &raw); err != nil {
			return 0, fmt.Errorf("invalid PostgreSQL %s identity field %s", r.entry.Kind, field)
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid PostgreSQL %s identity field %s", r.entry.Kind, field)
	}
	return value, nil
}

func (r pgGraphRecord) text(field string) (string, error) {
	var value string
	if err := json.Unmarshal(r.data[field], &value); err != nil || string(r.data[field]) == "null" {
		return "", fmt.Errorf("invalid PostgreSQL %s name field %s", r.entry.Kind, field)
	}
	return value, nil
}

func (r pgGraphRecord) boolean(field string) (bool, error) {
	var value bool
	if err := json.Unmarshal(r.data[field], &value); err != nil || string(r.data[field]) == "null" {
		return false, fmt.Errorf("invalid PostgreSQL %s flag field %s", r.entry.Kind, field)
	}
	return value, nil
}

func pgGraphAlias(names map[string]string, used map[string]bool, name, kind string) (string, error) {
	id := names[name]
	if id == "" || used[name] {
		return "", fmt.Errorf("missing or duplicate PostgreSQL %s alias", kind)
	}
	used[name] = true
	return "@" + kind + ":" + id, nil
}

// BuildPostgreSQLGraphBindings selects the desired target and its generated
// catalog objects without selecting an original table present in a probe's
// namespace snapshot. It requires complete, explicit alias coverage and
// rejects absent/ambiguous generated assets instead of guessing from names.
func BuildPostgreSQLGraphBindings(p PostgreSQLCatalog, names PostgreSQLGraphNames) (out PostgreSQLGraphBindings, err error) {
	defer func() {
		if err != nil {
			out = PostgreSQLGraphBindings{}
		}
	}()
	if _, err = p.Fingerprint(); err != nil {
		return out, err
	}
	if p.RelationOID <= 0 {
		return out, errors.New("PostgreSQL graph target is missing")
	}
	out = PostgreSQLGraphBindings{
		Classes: map[string]int64{}, Objects: map[PostgreSQLCatalogObject]string{}, Attributes: map[int64]map[int]string{},
		Node: PostgreSQLNodeBindings{Types: map[int64]string{}, Relations: map[int64]string{}, Variables: map[int]map[int]string{}, CompositeColumns: map[int64]map[int]string{}, ByteOrder: p.NativeByteOrder},
	}
	rows := map[string]map[int64]pgGraphRecord{}
	var columns, dependencies []pgGraphRecord
	for _, entry := range p.Entries {
		r := pgGraphRecord{entry: entry}
		if err = json.Unmarshal(entry.Data, &r.data); err != nil {
			return out, err
		}
		switch entry.Kind {
		case "catalog_class":
			name, e := r.text("name")
			if e != nil {
				return out, e
			}
			if out.Classes[name] != 0 {
				return out, errors.New("duplicate PostgreSQL catalog class")
			}
			out.Classes[name] = entry.OID
		case "column":
			columns = append(columns, r)
		case "dependency":
			dependencies = append(dependencies, r)
		default:
			if rows[entry.Kind] == nil {
				rows[entry.Kind] = map[int64]pgGraphRecord{}
			}
			// Multi-subobject records are preserved by the catalog, but they do
			// not provide relation/type/default/constraint/trigger identities.
			if entry.SubID == 0 {
				rows[entry.Kind][entry.OID] = r
			}
		}
	}
	for _, name := range []string{"pg_class", "pg_type", "pg_attrdef", "pg_constraint", "pg_trigger"} {
		if out.Classes[name] <= 0 {
			return out, errors.New("PostgreSQL graph catalog classes are incomplete")
		}
	}
	reverse := map[string]PostgreSQLCatalogObject{}
	bind := func(class string, oid int64, key string) error {
		object := PostgreSQLCatalogObject{out.Classes[class], oid}
		if oid <= 0 || key == "" || out.Objects[object] != "" {
			return errors.New("ambiguous PostgreSQL graph catalog identity")
		}
		if _, exists := reverse[key]; exists {
			return errors.New("duplicate PostgreSQL graph logical identity")
		}
		kind := map[string]string{"pg_class": "relation", "pg_type": "type", "pg_attrdef": "default", "pg_constraint": "constraint", "pg_trigger": "trigger"}[class]
		record, exists := rows[kind][oid]
		if !exists {
			return errors.New("PostgreSQL graph object catalog row missing")
		}
		storedOID, err := record.integer("oid")
		if err != nil || storedOID != oid {
			return errors.New("PostgreSQL graph object catalog OID mismatch")
		}
		out.Objects[object], reverse[key] = key, object
		if class == "pg_class" {
			out.Node.Relations[oid] = key
		} else if class == "pg_type" {
			out.Node.Types[oid] = key
		}
		return nil
	}
	relation, exists := rows["relation"][p.RelationOID]
	if !exists {
		return out, errors.New("PostgreSQL graph relation record missing")
	}
	kind, err := relation.text("relkind")
	if err != nil || kind != "r" && kind != "p" {
		return out, errors.New("invalid PostgreSQL graph table kind")
	}
	if err = bind("pg_class", p.RelationOID, "@table"); err != nil {
		return out, err
	}
	rowOID, err := relation.integer("reltype")
	if err != nil {
		return out, err
	}
	rowType, exists := rows["type"][rowOID]
	if !exists {
		return out, errors.New("PostgreSQL graph row type missing")
	}
	rowRelation, err := rowType.integer("typrelid")
	if err != nil || rowRelation != p.RelationOID {
		return out, errors.New("PostgreSQL graph row type belongs to another relation")
	}
	arrayOID, err := rowType.integer("typarray")
	if err != nil {
		return out, err
	}
	arrayType, exists := rows["type"][arrayOID]
	if !exists {
		return out, errors.New("PostgreSQL graph row array type missing")
	}
	element, err := arrayType.integer("typelem")
	if err != nil || element != rowOID {
		return out, errors.New("PostgreSQL graph row array has a different element")
	}
	if err = bind("pg_type", rowOID, "@row-type"); err != nil {
		return out, err
	}
	if err = bind("pg_type", arrayOID, "@row-array"); err != nil {
		return out, err
	}
	usedColumns, usedConstraints, usedIndexes, usedTriggers := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	targetColumns, columnIDs, identityColumns := map[int]string{}, map[string]bool{}, map[int]bool{}
	for _, r := range columns {
		if r.entry.OID != p.RelationOID {
			continue
		}
		number, e := r.integer("attnum")
		if e != nil || number <= 0 || number != int64(r.entry.SubID) {
			return out, errors.New("invalid PostgreSQL graph attribute identity")
		}
		owner, e := r.integer("attrelid")
		if e != nil || owner != p.RelationOID {
			return out, errors.New("PostgreSQL graph attribute relation mismatch")
		}
		dropped, e := r.boolean("attisdropped")
		if e != nil {
			return out, e
		}
		if dropped {
			continue
		}
		name, e := r.text("attname")
		if e != nil {
			return out, e
		}
		key, e := pgGraphAlias(names.Columns, usedColumns, name, "column")
		if e != nil || columnIDs[key] {
			return out, errors.New("missing or duplicate PostgreSQL graph column alias")
		}
		columnIDs[key], targetColumns[int(number)] = true, key
		identity, e := r.text("attidentity")
		if e != nil || identity != "" && identity != "a" && identity != "d" {
			return out, errors.New("invalid PostgreSQL graph identity column mode")
		}
		if identity != "" {
			identityColumns[int(number)] = true
		}
	}
	out.Attributes[p.RelationOID] = targetColumns
	out.Node.Variables[1] = targetColumns
	out.Node.CompositeColumns[rowOID] = targetColumns
	for oid, r := range rows["default"] {
		rel, e := r.integer("adrelid")
		if e != nil {
			return out, e
		}
		if rel != p.RelationOID {
			continue
		}
		number, e := r.integer("adnum")
		if e != nil || targetColumns[int(number)] == "" {
			return out, errors.New("PostgreSQL graph default has no live column")
		}
		if err = bind("pg_attrdef", oid, "@default:"+targetColumns[int(number)]); err != nil {
			return out, err
		}
	}
	for oid, r := range rows["constraint"] {
		rel, e := r.integer("conrelid")
		if e != nil {
			return out, e
		}
		if rel != p.RelationOID {
			continue
		}
		kind, e := r.text("contype")
		if e != nil {
			return out, e
		}
		if kind == "t" { // A user constraint trigger owns this catalog row.
			continue
		}
		name, e := r.text("conname")
		if e != nil {
			return out, e
		}
		key, e := pgGraphAlias(names.Constraints, usedConstraints, name, "constraint")
		if e != nil {
			return out, e
		}
		if err = bind("pg_constraint", oid, key); err != nil {
			return out, err
		}
	}
	for oid, r := range rows["index"] {
		rel, e := r.integer("indrelid")
		if e != nil {
			return out, e
		}
		if rel != p.RelationOID {
			continue
		}
		storedOID, e := r.integer("indexrelid")
		if e != nil || storedOID != oid {
			return out, errors.New("PostgreSQL graph index OID mismatch")
		}
		constraint, e := r.integer("constraint_oid")
		if e != nil {
			return out, e
		}
		key := ""
		if constraint != 0 {
			key = out.Objects[PostgreSQLCatalogObject{out.Classes["pg_constraint"], constraint}]
			if key == "" {
				return out, errors.New("PostgreSQL graph constraint index has no owner")
			}
			key = "@constraint-index:" + key
		} else {
			name, e := r.text("name")
			if e != nil {
				return out, e
			}
			key, e = pgGraphAlias(names.Indexes, usedIndexes, name, "index")
			if e != nil {
				return out, e
			}
		}
		if _, exists := rows["relation"][oid]; !exists {
			return out, errors.New("PostgreSQL graph index relation missing")
		}
		if err = bind("pg_class", oid, key); err != nil {
			return out, err
		}
	}
	for oid, r := range rows["trigger"] {
		rel, e := r.integer("tgrelid")
		if e != nil {
			return out, e
		}
		internal, e := r.boolean("tgisinternal")
		if e != nil {
			return out, e
		}
		if internal || rel != p.RelationOID {
			continue
		}
		name, e := r.text("tgname")
		if e != nil {
			return out, e
		}
		key, e := pgGraphAlias(names.Triggers, usedTriggers, name, "trigger")
		if e != nil {
			return out, e
		}
		if err = bind("pg_trigger", oid, key); err != nil {
			return out, err
		}
		constraint, e := r.integer("tgconstraint")
		if e != nil {
			return out, e
		}
		if constraint != 0 {
			owner, exists := rows["constraint"][constraint]
			if !exists {
				return out, errors.New("PostgreSQL graph trigger constraint missing")
			}
			kind, e := owner.text("contype")
			ownerRelation, re := owner.integer("conrelid")
			if e != nil || re != nil || kind != "t" || ownerRelation != p.RelationOID {
				return out, errors.New("PostgreSQL graph trigger constraint belongs to another object")
			}
			if err = bind("pg_constraint", constraint, "@trigger-constraint:"+key); err != nil {
				return out, err
			}
		}
	}
	for oid, r := range rows["trigger"] {
		internal, e := r.boolean("tgisinternal")
		if e != nil {
			return out, e
		}
		if !internal {
			continue
		}
		constraint, e := r.integer("tgconstraint")
		if e != nil {
			return out, e
		}
		owner := out.Objects[PostgreSQLCatalogObject{out.Classes["pg_constraint"], constraint}]
		if owner == "" { // Incoming/external constraints belong to preservation.
			continue
		}
		rel, e := r.integer("tgrelid")
		if e != nil {
			return out, e
		}
		role := fmt.Sprintf("external:%d", rel)
		if rel == p.RelationOID {
			role = "target"
		} else if _, exists := rows["relation"][rel]; !exists {
			return out, errors.New("PostgreSQL graph external trigger relation missing")
		}
		function, e := r.integer("tgfoid")
		if e != nil {
			return out, e
		}
		triggerType, e := r.integer("tgtype")
		if e != nil {
			return out, e
		}
		key := "@internal-trigger:" + Signature([]any{owner, role, function, triggerType})
		if err = bind("pg_trigger", oid, key); err != nil {
			return out, err
		}
	}
	for _, r := range dependencies {
		var raw []map[string]json.RawMessage
		if err = json.Unmarshal(r.data["edges"], &raw); err != nil || raw == nil {
			return out, errors.New("invalid PostgreSQL graph dependency list")
		}
		for _, fields := range raw {
			edge := pgGraphRecord{entry: r.entry, data: fields}
			class, ce := edge.integer("classid")
			object, oe := edge.integer("objid")
			refClass, rce := edge.integer("refclassid")
			refObject, roe := edge.integer("refobjid")
			sub, se := edge.integer("objsubid")
			refSub, rse := edge.integer("refobjsubid")
			kind, ke := edge.text("deptype")
			if errors.Join(ce, oe, rce, roe, se, rse, ke) != nil || object != r.entry.OID || sub < 0 || refSub < 0 {
				return out, errors.New("invalid PostgreSQL graph dependency identity")
			}
			if class != out.Classes["pg_class"] || refClass != out.Classes["pg_class"] || refObject != p.RelationOID || refSub <= 0 || sub != 0 || kind != "i" && kind != "a" {
				continue
			}
			sequenceRelation, exists := rows["relation"][object]
			if !exists {
				return out, errors.New("PostgreSQL graph automatically dependent relation missing")
			}
			relationKind, e := sequenceRelation.text("relkind")
			if e != nil {
				return out, e
			}
			if relationKind != "S" {
				if _, exists := rows["sequence"][object]; exists {
					return out, errors.New("invalid PostgreSQL graph sequence relation kind")
				}
				continue
			}
			sequence, exists := rows["sequence"][object]
			if !exists {
				return out, errors.New("PostgreSQL graph owned sequence catalog row missing")
			}
			sequenceOID, e := sequence.integer("seqrelid")
			if e != nil || sequenceOID != object {
				return out, errors.New("PostgreSQL graph sequence OID mismatch")
			}
			column := targetColumns[int(refSub)]
			if column == "" {
				return out, errors.New("PostgreSQL graph sequence has no live owner column")
			}
			if err = bind("pg_class", object, "@sequence:"+column); err != nil {
				return out, err
			}
			if kind == "i" {
				if !identityColumns[int(refSub)] {
					return out, errors.New("PostgreSQL graph identity sequence has no identity column")
				}
				delete(identityColumns, int(refSub))
			}
		}
	}
	if len(identityColumns) > 0 {
		return out, errors.New("PostgreSQL graph identity sequence ownership missing")
	}
	for oid, r := range rows["constraint"] {
		rel, e := r.integer("conrelid")
		if e != nil {
			return out, e
		}
		if rel == p.RelationOID && out.Objects[PostgreSQLCatalogObject{out.Classes["pg_constraint"], oid}] == "" {
			return out, errors.New("PostgreSQL graph target constraint is unbound")
		}
	}
	if len(usedColumns) != len(names.Columns) || len(usedConstraints) != len(names.Constraints) || len(usedIndexes) != len(names.Indexes) || len(usedTriggers) != len(names.Triggers) {
		return out, errors.New("PostgreSQL graph desired aliases contain absent assets")
	}
	return out, nil
}
