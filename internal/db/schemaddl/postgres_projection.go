package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PostgreSQLGraphReference permits references to original assets present in an
// independent probe. Those assets do not become selected declaration records.
type PostgreSQLGraphReference struct {
	Catalog PostgreSQLCatalog
	Names   PostgreSQLGraphNames
}

type PostgreSQLGraphRecord struct {
	Kind, Object, Attribute string
	Declaration             json.RawMessage
	// Opaque properties and physical state stay available for the separate
	// original-catalog proof. A declaration match cannot authorize their loss.
	Opaque, Physical json.RawMessage
}

type PostgreSQLDeclarationGraph struct {
	Scope   PostgreSQLCatalog
	Records []PostgreSQLGraphRecord
}

type pgGraphProjector struct {
	bindings        PostgreSQLGraphBindings
	refs            map[PostgreSQLCatalogObject]string
	columns         map[int64]map[int]string
	entries         map[string]map[int64]pgGraphRecord
	counts, maximum map[int64]int
	defaults        map[int64]map[int]int64
}

// ProjectPostgreSQLDeclarationGraph compares independently captured target
// semantics without broad textual/OID substitutions. Unknown fields remain
// declaration evidence; only named derived, opaque and physical properties
// are classified separately. It is not the complete original-catalog proof.
func ProjectPostgreSQLDeclarationGraph(p PostgreSQLCatalog, names PostgreSQLGraphNames, references ...PostgreSQLGraphReference) (out PostgreSQLDeclarationGraph, err error) {
	defer func() {
		if err != nil {
			out = PostgreSQLDeclarationGraph{}
		}
	}()
	b, err := BuildPostgreSQLGraphBindings(p, names)
	if err != nil {
		return out, err
	}
	g := pgGraphProjector{bindings: b, refs: map[PostgreSQLCatalogObject]string{}, columns: map[int64]map[int]string{}, entries: map[string]map[int64]pgGraphRecord{}, counts: map[int64]int{}, maximum: map[int64]int{}, defaults: map[int64]map[int]int64{}}
	for object, identity := range b.Objects {
		g.refs[object] = identity
	}
	for relation, columns := range b.Attributes {
		g.columns[relation] = columns
	}
	for _, reference := range references {
		if !pgGraphSameScope(p, reference.Catalog) {
			return out, errors.New("PostgreSQL graph reference has a different original scope")
		}
		rb, e := BuildPostgreSQLGraphBindings(reference.Catalog, reference.Names)
		if e != nil {
			return out, e
		}
		for object, identity := range rb.Objects {
			if current := g.refs[object]; current != "" && current != identity {
				return out, errors.New("ambiguous PostgreSQL graph reference identity")
			}
			g.refs[object] = identity
			if object.ClassOID == b.Classes["pg_class"] {
				g.bindings.Node.Relations[object.ObjectOID] = identity
			} else if object.ClassOID == b.Classes["pg_type"] {
				g.bindings.Node.Types[object.ObjectOID] = identity
			}
		}
		for relation, columns := range rb.Attributes {
			if current, ok := g.columns[relation]; ok && Signature(current) != Signature(columns) {
				return out, errors.New("ambiguous PostgreSQL graph reference attributes")
			}
			g.columns[relation] = columns
		}
		for typ, columns := range rb.Node.CompositeColumns {
			g.bindings.Node.CompositeColumns[typ] = columns
		}
	}
	var records []pgGraphRecord
	for _, entry := range p.Entries {
		r := pgGraphRecord{entry: entry}
		if err = json.Unmarshal(entry.Data, &r.data); err != nil {
			return out, err
		}
		records = append(records, r)
		if entry.SubID == 0 {
			if g.entries[entry.Kind] == nil {
				g.entries[entry.Kind] = map[int64]pgGraphRecord{}
			}
			g.entries[entry.Kind][entry.OID] = r
		}
	}
	// Index and sequence attributes have physical positions independent of
	// base-table attnums. External attributes retain qualified physical IDs.
	for _, r := range records {
		if r.entry.Kind == "default" && b.Objects[PostgreSQLCatalogObject{b.Classes["pg_attrdef"], r.entry.OID}] != "" {
			owner, oe := r.integer("adrelid")
			number, ne := r.integer("adnum")
			if oe != nil || ne != nil || number <= 0 {
				return out, errors.New("invalid PostgreSQL projected default ownership")
			}
			if g.defaults[owner] == nil {
				g.defaults[owner] = map[int]int64{}
			}
			if g.defaults[owner][int(number)] != 0 {
				return out, errors.New("duplicate PostgreSQL projected column default")
			}
			g.defaults[owner][int(number)] = r.entry.OID
		}
		if r.entry.Kind != "column" {
			continue
		}
		number, e := r.integer("attnum")
		owner, oe := r.integer("attrelid")
		if e != nil || oe != nil || number != int64(r.entry.SubID) || number <= 0 || owner != r.entry.OID {
			return out, errors.New("invalid PostgreSQL projected attribute identity")
		}
		g.counts[owner]++
		if int(number) > g.maximum[owner] {
			g.maximum[owner] = int(number)
		}
		dropped, e := r.boolean("attisdropped")
		if e != nil {
			return out, e
		}
		if dropped {
			continue
		}
		if g.columns[owner] == nil {
			g.columns[owner] = map[int]string{}
		}
		if g.columns[owner][int(number)] == "" {
			object := g.refs[PostgreSQLCatalogObject{b.Classes["pg_class"], owner}]
			if object != "" {
				g.columns[owner][int(number)] = "@attribute:" + Signature([]any{object, number})
			} else {
				g.columns[owner][int(number)] = "@external-attribute:" + Signature([]any{b.Classes["pg_class"], owner, number})
			}
		}
	}
	out.Scope = *p.Clone()
	out.Scope.RelationOID, out.Scope.Entries = 0, nil
	seen, dependencies := map[string]bool{}, map[string][]map[string]json.RawMessage{}
	add := func(record PostgreSQLGraphRecord) error {
		key := record.Kind + ":" + Signature([]string{record.Object, record.Attribute})
		if seen[key] {
			return errors.New("duplicate PostgreSQL projected record identity")
		}
		seen[key] = true
		out.Records = append(out.Records, record)
		return nil
	}
	for _, r := range records {
		if r.entry.Kind == "dependency" {
			if err = g.dependencies(r, dependencies); err != nil {
				return out, err
			}
			continue
		}
		class := pgGraphRecordClass(r.entry.Kind)
		object := b.Objects[PostgreSQLCatalogObject{b.Classes[class], r.entry.OID}]
		if class == "" || object == "" {
			continue
		}
		attribute := ""
		if r.entry.Kind == "inheritance" {
			sequence, e := r.integer("inhseqno")
			if e != nil || sequence <= 0 || sequence != int64(r.entry.SubID) {
				return out, errors.New("invalid PostgreSQL projected inheritance position")
			}
			attribute = "@parent-position:" + strconv.FormatInt(sequence, 10)
		}
		if r.entry.Kind == "column" {
			dropped, e := r.boolean("attisdropped")
			if e != nil {
				return out, e
			}
			if dropped {
				continue // Only the separate original proof can permit this loss.
			}
			attribute = g.columns[r.entry.OID][r.entry.SubID]
			if attribute == "" {
				return out, errors.New("PostgreSQL projected column has no identity")
			}
		}
		for _, field := range pgGraphRequiredFields(r.entry.Kind) {
			if _, exists := r.data[field]; !exists {
				return out, fmt.Errorf("PostgreSQL projected %s evidence lacks field %s", r.entry.Kind, field)
			}
		}
		if err = g.normalize(r, object, attribute); err != nil {
			return out, err
		}
		opaque, physical := map[string]json.RawMessage{}, map[string]json.RawMessage{}
		for _, field := range pgGraphOpaqueFields(r.entry.Kind) {
			if value, exists := r.data[field]; exists {
				opaque[field] = value
				delete(r.data, field)
			}
		}
		for _, field := range pgGraphPhysicalFields(r.entry.Kind) {
			if value, exists := r.data[field]; exists {
				physical[field] = value
				delete(r.data, field)
			}
		}
		decl, e := pgGraphCanonicalJSON(r.data)
		if e != nil {
			return out, e
		}
		opaqueJSON, e := pgGraphCanonicalJSON(opaque)
		if e != nil {
			return out, e
		}
		physicalJSON, e := pgGraphCanonicalJSON(physical)
		if e != nil {
			return out, e
		}
		if err = add(PostgreSQLGraphRecord{r.entry.Kind, object, attribute, decl, opaqueJSON, physicalJSON}); err != nil {
			return out, err
		}
	}
	for object, edges := range dependencies {
		sort.Slice(edges, func(i, j int) bool { return Signature(edges[i]) < Signature(edges[j]) })
		// pg_depend has no unique edge constraint. Preserve multiplicity;
		// neither silently deduplicate native edges nor reject valid catalogs.
		data, e := pgGraphCanonicalJSON(map[string]any{"edges": edges})
		if e != nil {
			return out, e
		}
		if err = add(PostgreSQLGraphRecord{Kind: "dependency", Object: object, Declaration: data}); err != nil {
			return out, err
		}
	}
	for _, object := range b.Objects {
		if len(dependencies[object]) == 0 {
			return out, errors.New("PostgreSQL projected object dependency evidence is missing")
		}
	}
	sort.Slice(out.Records, func(i, j int) bool {
		a, b := out.Records[i], out.Records[j]
		return a.Kind+":"+a.Object+":"+a.Attribute < b.Kind+":"+b.Object+":"+b.Attribute
	})
	return out, nil
}

// ComparePostgreSQLDeclarationGraphs checks the independent intended graph.
// Opaque/physical records are deliberately left for original preservation;
// callers must separately verify them and retained values before committing.
func ComparePostgreSQLDeclarationGraphs(actual, expected PostgreSQLDeclarationGraph) error {
	if _, err := actual.Scope.Fingerprint(); err != nil {
		return err
	}
	if _, err := expected.Scope.Fingerprint(); err != nil {
		return err
	}
	if !pgGraphSameScope(actual.Scope, expected.Scope) || len(actual.Records) == 0 || len(actual.Records) != len(expected.Records) {
		return errors.New("PostgreSQL declaration graph scope or record count differs")
	}
	byKey := map[string]PostgreSQLGraphRecord{}
	for _, record := range expected.Records {
		if record.Kind == "" || record.Object == "" || !pgGraphValidDeclaration(record.Declaration) {
			return errors.New("invalid PostgreSQL expected declaration graph record")
		}
		key := record.Kind + ":" + Signature([]string{record.Object, record.Attribute})
		if _, exists := byKey[key]; exists {
			return errors.New("duplicate PostgreSQL expected graph record")
		}
		byKey[key] = record
	}
	for _, record := range actual.Records {
		if record.Kind == "" || record.Object == "" || !pgGraphValidDeclaration(record.Declaration) {
			return errors.New("invalid PostgreSQL actual declaration graph record")
		}
		key := record.Kind + ":" + Signature([]string{record.Object, record.Attribute})
		wanted, exists := byKey[key]
		if !exists {
			return errors.New("unexpected PostgreSQL declaration graph object")
		}
		delete(byKey, key)
		got, err := pgGraphCanonicalJSON(record.Declaration)
		if err != nil {
			return err
		}
		want, err := pgGraphCanonicalJSON(wanted.Declaration)
		if err != nil {
			return err
		}
		if string(got) != string(want) {
			var a, b map[string]json.RawMessage
			if json.Unmarshal(record.Declaration, &a) != nil || json.Unmarshal(wanted.Declaration, &b) != nil {
				return errors.New("malformed PostgreSQL declaration graph record")
			}
			var fields []string
			for field, value := range a {
				if string(value) != string(b[field]) {
					fields = append(fields, field)
				}
			}
			for field := range b {
				if _, exists := a[field]; !exists {
					fields = append(fields, field)
				}
			}
			sort.Strings(fields)
			return fmt.Errorf("PostgreSQL declaration graph %s differs in fields %s", record.Kind, strings.Join(fields, ", "))
		}
	}
	return nil
}

func pgGraphValidDeclaration(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	return utf8.Valid(raw) && json.Valid(raw) && json.Unmarshal(raw, &fields) == nil && len(fields) > 0
}

func pgGraphSameScope(a, b PostgreSQLCatalog) bool {
	a.RelationOID, b.RelationOID, a.Entries, b.Entries = 0, 0, nil, nil
	return Signature(a) == Signature(b)
}

func pgGraphCanonicalJSON(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	var canonical any
	if err = d.Decode(&canonical); err != nil {
		return nil, err
	}
	return json.Marshal(canonical)
}

func pgGraphRecordClass(kind string) string {
	switch kind {
	case "relation", "column", "index", "sequence", "inheritance", "partition":
		return "pg_class"
	case "type":
		return "pg_type"
	case "default":
		return "pg_attrdef"
	case "constraint":
		return "pg_constraint"
	case "trigger":
		return "pg_trigger"
	}
	return ""
}

func (g pgGraphProjector) oid(r pgGraphRecord, field, class string) error {
	if _, exists := r.data[field]; !exists {
		return nil
	}
	n, err := r.integer(field)
	if err != nil || n < 0 || n > 1<<32-1 {
		return fmt.Errorf("invalid PostgreSQL projected %s OID field %s", r.entry.Kind, field)
	}
	if identity := g.refs[PostgreSQLCatalogObject{g.bindings.Classes[class], n}]; identity != "" {
		r.data[field], err = json.Marshal(map[string]string{"catalog_reference": identity})
	} else {
		r.data[field] = json.RawMessage(strconv.FormatInt(n, 10))
	}
	return err
}

func (g pgGraphProjector) attribute(r pgGraphRecord, field string, relation int64) error {
	n, err := r.integer(field)
	if err != nil {
		return err
	}
	if n == 0 {
		r.data[field] = json.RawMessage("0")
		return nil
	}
	identity := g.columns[relation][int(n)]
	if n < 0 || identity == "" {
		return fmt.Errorf("unbound PostgreSQL projected %s attribute field %s", r.entry.Kind, field)
	}
	r.data[field], err = json.Marshal(map[string]string{"attribute_reference": identity})
	return err
}

func pgGraphVector(raw json.RawMessage) ([]int64, bool, error) {
	if string(raw) == "null" {
		return nil, true, nil
	}
	var atoms []json.RawMessage
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, false, err
		}
		for _, atom := range strings.Fields(text) {
			atoms = append(atoms, json.RawMessage(atom))
		}
	} else if err := json.Unmarshal(raw, &atoms); err != nil || atoms == nil {
		return nil, false, errors.New("invalid PostgreSQL projected vector")
	}
	values := make([]int64, 0, len(atoms))
	for _, atom := range atoms {
		r := pgGraphRecord{data: map[string]json.RawMessage{"value": atom}}
		n, err := r.integer("value")
		if err != nil {
			return nil, false, err
		}
		values = append(values, n)
	}
	return values, false, nil
}

func (g pgGraphProjector) vector(r pgGraphRecord, field string, relation int64, attributes bool) error {
	raw, exists := r.data[field]
	if !exists {
		return nil
	}
	values, null, err := pgGraphVector(raw)
	if err != nil {
		return fmt.Errorf("invalid PostgreSQL projected %s vector field %s", r.entry.Kind, field)
	}
	if null {
		return nil
	}
	out := make([]any, 0, len(values))
	for _, n := range values {
		if attributes && n != 0 {
			identity := g.columns[relation][int(n)]
			if n < 0 || identity == "" {
				return fmt.Errorf("unbound PostgreSQL projected %s vector field %s", r.entry.Kind, field)
			}
			out = append(out, map[string]string{"attribute_reference": identity})
		} else {
			out = append(out, n)
		}
	}
	r.data[field], err = json.Marshal(out)
	return err
}

func (g pgGraphProjector) expression(r pgGraphRecord, field string, relation int64, trigger bool) error {
	raw, exists := r.data[field]
	if !exists {
		return nil
	}
	if string(raw) == "null" {
		return nil
	}
	var tree string
	if err := json.Unmarshal(raw, &tree); err != nil {
		return fmt.Errorf("invalid PostgreSQL projected %s expression field %s", r.entry.Kind, field)
	}
	node := g.bindings.Node
	node.Variables = map[int]map[int]string{1: g.columns[relation]}
	if trigger {
		node.Variables[2] = g.columns[relation]
	}
	hash, err := NormalizePostgreSQLNode(tree, node)
	if err != nil {
		return fmt.Errorf("unprovable PostgreSQL projected %s expression field %s: %w", r.entry.Kind, field, err)
	}
	r.data[field], err = json.Marshal(map[string]string{"expression_signature": hash})
	return err
}

func (g pgGraphProjector) dependencies(r pgGraphRecord, out map[string][]map[string]json.RawMessage) error {
	var edges []map[string]json.RawMessage
	if err := json.Unmarshal(r.data["edges"], &edges); err != nil || edges == nil {
		return errors.New("invalid PostgreSQL projected dependency evidence")
	}
	for _, fields := range edges {
		edge := pgGraphRecord{entry: r.entry, data: fields}
		class, ce := edge.integer("classid")
		object, oe := edge.integer("objid")
		refclass, rce := edge.integer("refclassid")
		refobject, roe := edge.integer("refobjid")
		if errors.Join(ce, oe, rce, roe) != nil || object != r.entry.OID {
			return errors.New("invalid PostgreSQL projected dependency identity")
		}
		identity := g.bindings.Objects[PostgreSQLCatalogObject{class, object}]
		if identity == "" {
			continue
		}
		for _, field := range []string{"objsubid", "refobjsubid", "deptype"} {
			if _, exists := fields[field]; !exists {
				return errors.New("incomplete PostgreSQL projected dependency evidence")
			}
		}
		sub, se := edge.integer("objsubid")
		refsub, rse := edge.integer("refobjsubid")
		if se != nil || rse != nil || sub < 0 || refsub < 0 {
			return errors.New("invalid PostgreSQL projected dependency attribute")
		}
		if class == g.bindings.Classes["pg_class"] && sub > 0 {
			if err := g.attribute(edge, "objsubid", object); err != nil {
				return err
			}
		}
		if refclass == g.bindings.Classes["pg_class"] && refsub > 0 {
			if err := g.attribute(edge, "refobjsubid", refobject); err != nil {
				return err
			}
		}
		fields["classid"] = json.RawMessage(strconv.FormatInt(class, 10))
		fields["refclassid"] = json.RawMessage(strconv.FormatInt(refclass, 10))
		fields["objid"], _ = json.Marshal(map[string]string{"catalog_reference": identity})
		if ref := g.refs[PostgreSQLCatalogObject{refclass, refobject}]; ref != "" {
			fields["refobjid"], _ = json.Marshal(map[string]string{"catalog_reference": ref})
		} else {
			fields["refobjid"] = json.RawMessage(strconv.FormatInt(refobject, 10))
		}
		canonical, err := pgGraphCanonicalJSON(fields)
		if err != nil {
			return err
		}
		var result map[string]json.RawMessage
		if err = json.Unmarshal(canonical, &result); err != nil {
			return err
		}
		out[identity] = append(out[identity], result)
	}
	return nil
}

func (g pgGraphProjector) normalize(r pgGraphRecord, object, attribute string) error {
	// Resolve owning relation numbers before typed fields become markers.
	relation := r.entry.OID
	ownerField := map[string]string{"default": "adrelid", "constraint": "conrelid", "index": "indrelid", "trigger": "tgrelid", "partition": "partrelid"}[r.entry.Kind]
	if ownerField != "" {
		var err error
		relation, err = r.integer(ownerField)
		if err != nil {
			return err
		}
	}
	referenceRelation := int64(0)
	if r.entry.Kind == "constraint" {
		var err error
		referenceRelation, err = r.integer("confrelid")
		if err != nil {
			return err
		}
	}
	for field, class := range pgGraphOIDFields(r.entry.Kind) {
		if err := g.oid(r, field, class); err != nil {
			return err
		}
	}
	for _, field := range pgGraphExpressionFields(r.entry.Kind) {
		if err := g.expression(r, field, relation, r.entry.Kind == "trigger"); err != nil {
			return err
		}
	}
	switch r.entry.Kind {
	case "relation", "type", "index", "constraint", "trigger":
		for _, field := range pgGraphObjectNames(r.entry.Kind) {
			if _, exists := r.data[field]; exists {
				r.data[field], _ = json.Marshal(map[string]string{"object_name": object})
			}
		}
	case "column":
		if err := g.attribute(r, "attnum", relation); err != nil {
			return err
		}
		if g.bindings.Objects[PostgreSQLCatalogObject{g.bindings.Classes["pg_class"], relation}] != "@table" {
			// Index attribute names cache the original column/expression name;
			// native column rename does not rewrite this physical descriptor.
			r.data["attname"], _ = json.Marshal(map[string]string{"attribute_name": attribute})
		}
	case "default":
		if err := g.attribute(r, "adnum", relation); err != nil {
			return err
		}
	}
	if r.entry.Kind == "relation" {
		number, err := r.integer("relnatts")
		if err != nil || number < 0 || int(number) != g.counts[r.entry.OID] || int(number) != g.maximum[r.entry.OID] {
			return errors.New("PostgreSQL projected relation attribute evidence is incomplete")
		}
		kind, err := r.text("relkind")
		if err != nil {
			return err
		}
		if kind == "p" {
			if _, exists := g.entries["partition"][r.entry.OID]; !exists {
				return errors.New("PostgreSQL partition key evidence is missing")
			}
		}
		if _, exists := r.data["__nyadb_ordered_attributes"]; exists {
			return errors.New("PostgreSQL catalog field conflicts with projected attribute evidence")
		}
		numbers := make([]int, 0, len(g.columns[r.entry.OID]))
		for number := range g.columns[r.entry.OID] {
			numbers = append(numbers, number)
		}
		sort.Ints(numbers)
		ordered := make([]string, 0, len(numbers))
		for _, number := range numbers {
			ordered = append(ordered, g.columns[r.entry.OID][number])
		}
		r.data["__nyadb_ordered_attributes"], _ = json.Marshal(ordered)
	}
	for _, field := range pgGraphAttributeVectors(r.entry.Kind) {
		owner := relation
		if field == "confkey" {
			owner = referenceRelation
		}
		if err := g.vector(r, field, owner, true); err != nil {
			return err
		}
	}
	for _, field := range pgGraphNumericVectors(r.entry.Kind) {
		if err := g.vector(r, field, 0, false); err != nil {
			return err
		}
	}
	if r.entry.Kind == "column" {
		hasDefault, err := r.boolean("atthasdef")
		if err != nil || hasDefault != (g.defaults[relation][r.entry.SubID] != 0) {
			return errors.New("PostgreSQL column default expression evidence is incomplete")
		}
		if raw, exists := r.data["identity_sequence"]; exists && string(raw) != "null" {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
				return errors.New("invalid PostgreSQL projected identity sequence")
			}
			sequence := pgGraphRecord{entry: r.entry, data: fields}
			if err := g.oid(sequence, "seqrelid", "pg_class"); err != nil {
				return err
			}
			fields["name"], _ = json.Marshal(map[string]string{"object_name": "@sequence:" + attribute})
			var err error
			r.data["identity_sequence"], err = pgGraphCanonicalJSON(fields)
			if err != nil {
				return err
			}
		}
	}
	if r.entry.Kind == "trigger" {
		internal, err := r.boolean("tgisinternal")
		if err != nil {
			return err
		}
		if internal {
			// A user can disable RI triggers independently of the editable
			// FK declaration. Their state requires original preservation.
			for _, field := range []string{"tgenabled", "enabled"} {
				if value, exists := r.data[field]; exists {
					if _, conflict := r.data["original_"+field]; conflict {
						return errors.New("PostgreSQL trigger field conflicts with original state evidence")
					}
					r.data["original_"+field] = value
					delete(r.data, field)
				}
			}
		}
	}
	for _, field := range pgGraphDerivedFields(r.entry.Kind) {
		delete(r.data, field)
	}
	return nil
}

func pgGraphRequiredFields(kind string) []string {
	fields := map[string]string{
		"relation":    "oid relname relnamespace reltype relkind relam reltablespace reloptions relpersistence relreplident relrowsecurity relforcerowsecurity relispartition relpartbound relnatts",
		"column":      "attrelid attname attnum atttypid atttypmod attndims attcollation attnotnull attidentity attgenerated atthasdef attisdropped attislocal attinhcount attstorage attcompression attstattarget attoptions attfdwoptions attacl",
		"type":        "oid typname typnamespace typrelid typelem typarray typbasetype typtype typlen typbyval typalign typstorage typinput typoutput typreceive typsend typcollation",
		"default":     "oid adrelid adnum adbin",
		"constraint":  "oid conname connamespace contype conrelid contypid conindid confrelid conkey confkey conbin condeferrable condeferred convalidated confupdtype confdeltype confmatchtype conislocal coninhcount connoinherit conparentid",
		"index":       "indexrelid indrelid indnatts indnkeyatts indisunique indisprimary indisexclusion indisvalid indisready indislive indkey indcollation indclass indoption indexprs indpred constraint_oid",
		"trigger":     "oid tgrelid tgname tgfoid tgtype tgenabled tgisinternal tgconstrrelid tgconstrindid tgconstraint tgdeferrable tginitdeferred tgnargs tgattr tgargs tgqual tgparentid",
		"sequence":    "seqrelid seqtypid seqstart seqincrement seqmax seqmin seqcache seqcycle",
		"inheritance": "inhrelid inhparent inhseqno",
		"partition":   "partrelid partstrat partnatts partdefid partattrs partclass partcollation partexprs",
	}[kind]
	return strings.Fields(fields)
}

func pgGraphOIDFields(kind string) map[string]string {
	fields := map[string]map[string]string{
		"relation":    {"oid": "pg_class", "reltype": "pg_type", "reloftype": "pg_type", "relrewrite": "pg_class"},
		"column":      {"attrelid": "pg_class", "atttypid": "pg_type"},
		"type":        {"oid": "pg_type", "typrelid": "pg_class", "typelem": "pg_type", "typarray": "pg_type", "typbasetype": "pg_type"},
		"default":     {"oid": "pg_attrdef", "adrelid": "pg_class"},
		"constraint":  {"oid": "pg_constraint", "conrelid": "pg_class", "contypid": "pg_type", "conindid": "pg_class", "confrelid": "pg_class", "conparentid": "pg_constraint", "relation_oid": "pg_class"},
		"index":       {"indexrelid": "pg_class", "indrelid": "pg_class", "constraint_oid": "pg_constraint", "relation_oid": "pg_class"},
		"trigger":     {"oid": "pg_trigger", "tgrelid": "pg_class", "tgconstrrelid": "pg_class", "tgconstrindid": "pg_class", "tgconstraint": "pg_constraint", "tgparentid": "pg_trigger", "relation_oid": "pg_class", "constraint_oid": "pg_constraint"},
		"sequence":    {"seqrelid": "pg_class", "seqtypid": "pg_type"},
		"inheritance": {"inhrelid": "pg_class", "inhparent": "pg_class"},
		"partition":   {"partrelid": "pg_class", "partdefid": "pg_class"},
	}[kind]
	return fields
}

func pgGraphExpressionFields(kind string) []string {
	return map[string][]string{"relation": {"relpartbound"}, "default": {"adbin"}, "constraint": {"conbin"}, "index": {"indexprs", "indpred"}, "trigger": {"tgqual"}, "type": {"typdefaultbin"}, "partition": {"partexprs"}}[kind]
}

func pgGraphObjectNames(kind string) []string {
	return map[string][]string{"relation": {"relname"}, "type": {"typname"}, "constraint": {"conname", "name"}, "index": {"name"}, "trigger": {"tgname", "name"}}[kind]
}

func pgGraphAttributeVectors(kind string) []string {
	return map[string][]string{"constraint": {"conkey", "confkey", "confdelsetcols"}, "index": {"indkey"}, "trigger": {"tgattr"}, "partition": {"partattrs"}}[kind]
}

func pgGraphNumericVectors(kind string) []string {
	return map[string][]string{"constraint": {"conpfeqop", "conppeqop", "conffeqop", "conexclop"}, "index": {"indcollation", "indclass", "indoption"}, "partition": {"partclass", "partcollation"}}[kind]
}

func pgGraphOpaqueFields(kind string) []string {
	return map[string][]string{"relation": {"relowner", "owner_name", "relacl", "comment", "relreplident", "relrowsecurity", "relforcerowsecurity"}, "column": {"attacl", "attoptions", "attfdwoptions", "comment"}, "type": {"typowner", "typacl", "comment"}, "constraint": {"comment"}, "index": {"comment", "indisclustered", "indisreplident"}, "trigger": {"comment", "original_tgenabled", "original_enabled"}}[kind]
}

func pgGraphPhysicalFields(kind string) []string {
	return map[string][]string{"relation": {"relfilenode", "reltoastrelid", "relhasindex", "relhasrules", "relhastriggers", "relnatts"}, "column": {"attcacheoff", "atthasmissing", "attmissingval"}, "index": {"indcheckxmin"}}[kind]
}

func pgGraphDerivedFields(kind string) []string {
	return map[string][]string{"relation": {"partition_key", "partition_bound"}, "column": {"type_sql", "default_sql", "collation_sql"}, "default": {"definition"}, "constraint": {"definition"}, "index": {"definition"}, "trigger": {"definition"}}[kind]
}

// Clone keeps projected graphs safe to capture in an immutable worker request.
func (g PostgreSQLDeclarationGraph) Clone() PostgreSQLDeclarationGraph {
	g.Scope = *g.Scope.Clone()
	g.Records = slices.Clone(g.Records)
	for i := range g.Records {
		g.Records[i].Declaration = slices.Clone(g.Records[i].Declaration)
		g.Records[i].Opaque = slices.Clone(g.Records[i].Opaque)
		g.Records[i].Physical = slices.Clone(g.Records[i].Physical)
	}
	return g
}
