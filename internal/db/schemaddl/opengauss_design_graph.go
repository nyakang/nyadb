package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Name joins are explicit evidence supplied by the captured declaration.
// Ephemeral names and raw numeric OIDs never infer a stable field identity.
type OpenGaussDesignNames struct {
	Columns, Constraints, Definitions map[string]string
	ReferenceRelations                map[uint64]string
	ReferenceColumns                  map[uint64]map[int]string
	IncomingConstraints               map[uint64]string
	ReferenceSequences                []OpenGaussCopySequence
}

func OpenGaussNamesForTable(t Table) OpenGaussDesignNames {
	n := OpenGaussDesignNames{Columns: map[string]string{}, Constraints: map[string]string{}, Definitions: map[string]string{}}
	for _, c := range t.Clauses {
		if c.Kind == "column" {
			n.Columns[c.Name] = c.ID
		} else if c.Kind == "constraint" {
			n.Constraints[c.Name] = c.ID
		}
	}
	for _, d := range t.Definitions {
		n.Definitions[d.Kind+":"+d.Name] = d.Name
	}
	return n
}

type OpenGaussDesignGraph struct {
	Records    map[string]map[string]any
	Columns    map[string]string
	Objects    map[string]string
	Identities map[string]uint64
}

type gaussObject struct{ class, oid uint64 }

func gaussJSON(raw json.RawMessage) (map[string]any, error) {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	var v map[string]any
	if err := d.Decode(&v); err != nil || v == nil {
		return nil, ErrConflict
	}
	return v, nil
}
func gaussNumber(v any) (uint64, error) {
	s := ""
	switch n := v.(type) {
	case json.Number:
		s = string(n)
	case string:
		s = n
	default:
		return 0, ErrConflict
	}
	return strconv.ParseUint(s, 10, 64)
}

// NormalizeOpenGaussDesignGraph retains unknown fields. Only typed catalog
// identities, explicitly bound field numbers, parser display information and
// documented physical statistics acquire normalization.
func NormalizeOpenGaussDesignGraph(p OpenGaussCatalog, names OpenGaussDesignNames) (out OpenGaussDesignGraph, err error) {
	phase := "scope"
	defer func() {
		if err != nil {
			err = fmt.Errorf("native graph %s: %w", phase, err)
		}
	}()
	out = OpenGaussDesignGraph{Records: map[string]map[string]any{}, Columns: map[string]string{}, Objects: map[string]string{}, Identities: map[string]uint64{}}
	if _, err = p.Fingerprint(); err != nil || p.RelationOID == 0 {
		return out, errors.Join(err, ErrConflict)
	}
	for _, name := range []string{"pg_class", "pg_type", "pg_attrdef", "pg_constraint", "pg_trigger", "pg_rewrite", "pg_rlspolicy", "pg_partition", "pg_namespace"} {
		if p.Classes[name] == 0 {
			return out, ErrConflict
		}
	}
	definition, err := p.CopyDefinition()
	if err != nil {
		return out, err
	}
	objects := map[gaussObject]string{}
	attributes := map[uint64]map[int]string{}
	bind := func(class string, oid uint64, id string) error {
		if oid == 0 || id == "" {
			return ErrConflict
		}
		key := gaussObject{p.Classes[class], oid}
		if previous := objects[key]; previous != "" && previous != id {
			return ErrConflict
		}
		objects[key] = id
		return nil
	}
	if err = bind("pg_class", p.RelationOID, "@table"); err != nil {
		return out, err
	}
	for oid, id := range names.ReferenceRelations {
		if err = bind("pg_class", oid, id); err != nil {
			return out, err
		}
	}
	for oid, columns := range names.ReferenceColumns {
		if oid == p.RelationOID || names.ReferenceRelations[oid] == "" {
			return out, ErrConflict
		}
		attributes[oid] = map[int]string{}
		for number, id := range columns {
			if number <= 0 || id == "" {
				return out, ErrConflict
			}
			attributes[oid][number] = "@column:" + id
		}
	}
	for oid, id := range names.IncomingConstraints {
		if id == "" {
			return out, ErrConflict
		}
		if err = bind("pg_constraint", oid, "@incoming-constraint:"+id); err != nil {
			return out, err
		}
	}
	attributes[p.RelationOID] = map[int]string{}
	for _, col := range definition.Columns {
		id := names.Columns[col.Name]
		if id == "" || out.Columns[id] != "" {
			return out, ErrConflict
		}
		attributes[p.RelationOID][col.Number] = "@column:" + id
		out.Columns[id] = col.Name
	}
	for _, c := range definition.Constraints {
		id := names.Constraints[c.Name]
		if id == "" {
			return out, ErrConflict
		}
		if err = bind("pg_constraint", c.OID, "@constraint:"+id); err != nil {
			return out, err
		}
		if c.Kind != "f" && c.IndexOID != 0 {
			if err = bind("pg_class", c.IndexOID, "@constraint-index:"+id); err != nil {
				return out, err
			}
		}
	}
	for _, idx := range definition.Indexes {
		if objects[gaussObject{p.Classes["pg_class"], idx.OID}] != "" {
			continue
		}
		id := names.Definitions["index:"+idx.Name]
		if id == "" {
			return out, ErrConflict
		}
		if err = bind("pg_class", idx.OID, "@index:"+id); err != nil {
			return out, err
		}
	}
	for _, seq := range definition.Sequences {
		column := attributes[p.RelationOID][seq.Column]
		if column == "" {
			return out, ErrConflict
		}
		if err = bind("pg_class", seq.OID, "@sequence:"+column); err != nil {
			return out, err
		}
	}
	for _, list := range [][]OpenGaussCopyObject{definition.Triggers, definition.Rules} {
		for _, obj := range list {
			if obj.Internal {
				continue
			}
			id := names.Definitions[obj.Kind+":"+obj.Name]
			if id == "" {
				return out, ErrConflict
			}
			class := "pg_trigger"
			if obj.Kind == "rule" {
				class = "pg_rewrite"
			}
			if err = bind(class, obj.OID, "@"+obj.Kind+":"+id); err != nil {
				return out, err
			}
		}
	}
	var records []struct {
		entry OpenGaussCatalogEntry
		data  map[string]any
	}
	for _, e := range p.Entries {
		phase = fmt.Sprintf("binding %s:%d", e.Kind, e.OID)
		v, err := gaussJSON(e.Data)
		if err != nil {
			return out, err
		}
		records = append(records, struct {
			entry OpenGaussCatalogEntry
			data  map[string]any
		}{e, v})
		if e.Kind == "relation" && e.OID == p.RelationOID {
			for _, field := range []string{"reltype", "reltoastrelid", "reltoastidxid", "reldeltarelid", "reldeltaidx", "relcudescrelid", "relcudescidx"} {
				oid, err := gaussNumber(v[field])
				if err != nil {
					return out, err
				}
				if oid == 0 {
					continue
				}
				class := "pg_class"
				if field == "reltype" {
					class = "pg_type"
				}
				if err = bind(class, oid, "@table:"+field); err != nil {
					return out, err
				}
			}
		}
		if e.Kind == "default" {
			relation, err := gaussNumber(v["adrelid"])
			if err != nil {
				return out, err
			}
			number, err := gaussNumber(v["adnum"])
			if err != nil {
				return out, err
			}
			if relation == p.RelationOID {
				if err = bind("pg_attrdef", e.OID, "@default:"+attributes[relation][int(number)]); err != nil {
					return out, err
				}
			}
		}
		if e.Kind == "partition" {
			name, ok := v["relname"].(string)
			if !ok {
				return out, ErrConflict
			}
			kind, _ := v["parttype"].(string)
			if kind == "r" {
				name = "@table"
			}
			if err = bind("pg_partition", e.OID, "@partition:"+kind+":"+name); err != nil {
				return out, err
			}
			for _, field := range []string{"reltoastrelid", "reltoastidxid"} {
				if value, ok := v[field]; ok {
					oid, err := gaussNumber(value)
					if err != nil {
						return out, err
					}
					if oid != 0 {
						if err = bind("pg_class", oid, "@partition-storage:"+kind+":"+name+":"+field); err != nil {
							return out, err
						}
					}
				}
			}
		}
		if e.Kind == "policy" {
			name, ok := v["polname"].(string)
			if !ok {
				return out, ErrConflict
			}
			if err = bind("pg_rlspolicy", e.OID, "@policy:"+name); err != nil {
				return out, err
			}
		}
	}
	// Native array/composite identities and implicit FK triggers belong to the
	// captured relation/constraint, not to an ephemeral OID assigned by CREATE.
	for pass := 0; pass < len(records); pass++ {
		bound := len(objects)
		for _, r := range records {
			e, v := r.entry, r.data
			phase = fmt.Sprintf("closure %s:%d", e.Kind, e.OID)
			if e.Kind == "relation" {
				parent := objects[gaussObject{p.Classes["pg_class"], e.OID}]
				if parent != "" {
					for _, field := range []string{"reltype", "reltoastrelid", "reltoastidxid", "reldeltarelid", "reldeltaidx", "relcudescrelid", "relcudescidx"} {
						oid, err := gaussNumber(v[field])
						if err != nil {
							return out, err
						}
						class := "pg_class"
						if field == "reltype" {
							class = "pg_type"
						}
						if oid != 0 && objects[gaussObject{p.Classes[class], oid}] == "" {
							if err = bind(class, oid, parent+":"+field); err != nil {
								return out, err
							}
						}
					}
				}
			}
		}
		if len(objects) == bound {
			break
		}
	}
	// Row-type and array inference follows the complete explicit relation
	// graph, so a type row visited first cannot replace a reltype anchor.
	for pass := 0; pass < len(records); pass++ {
		bound := len(objects)
		for _, r := range records {
			e, v := r.entry, r.data
			phase = fmt.Sprintf("type/trigger closure %s:%d", e.Kind, e.OID)
			if e.Kind == "type" {
				if objects[gaussObject{p.Classes["pg_type"], e.OID}] != "" {
					continue
				}
				for _, field := range []string{"typrelid", "typelem"} {
					oid, err := gaussNumber(v[field])
					if err != nil {
						return out, err
					}
					if oid == 0 {
						continue
					}
					class := "pg_class"
					if field == "typelem" {
						class = "pg_type"
					}
					if id := objects[gaussObject{p.Classes[class], oid}]; id != "" {
						if err = bind("pg_type", e.OID, "@type:"+field+":"+id); err != nil {
							return out, err
						}
						break
					}
				}
			}
			if e.Kind == "trigger" && v["tgisinternal"] == true {
				// Implicit triggers retain their owning constraint and routine.
				constraint, err := gaussNumber(v["tgconstraint"])
				if err != nil {
					return out, err
				}
				id := objects[gaussObject{p.Classes["pg_constraint"], constraint}]
				if id == "" {
					id = "external:" + strconv.FormatUint(constraint, 10)
				}
				function, err := gaussNumber(v["tgfoid"])
				if err != nil {
					return out, err
				}
				if err = bind("pg_trigger", e.OID, "@internal-trigger:"+id+":"+strconv.FormatUint(function, 10)); err != nil {
					return out, err
				}
			}
		}
		if len(objects) == bound {
			break
		}
	}
	// Resolve all explicit relation pointers before falling back to a
	// captured physical index's parent. Catalog order and OID allocation must
	// not decide which stable name an internal index receives.
	for _, r := range records {
		if r.entry.Kind != "index" || objects[gaussObject{p.Classes["pg_class"], r.entry.OID}] != "" {
			continue
		}
		parent, err := gaussNumber(r.data["indrelid"])
		if err != nil {
			return out, err
		}
		if alias := objects[gaussObject{p.Classes["pg_class"], parent}]; strings.HasPrefix(alias, "@table:rel") || strings.HasPrefix(alias, "@partition-storage:") {
			if err = bind("pg_class", r.entry.OID, "@physical-index:"+alias); err != nil {
				return out, err
			}
		}
	}
	reference := func(v any, class string) (any, error) {
		oid, err := gaussNumber(v)
		if err != nil {
			return nil, err
		}
		if id := objects[gaussObject{p.Classes[class], oid}]; id != "" {
			return id, nil
		}
		return json.Number(strconv.FormatUint(oid, 10)), nil
	}
	for _, r := range records {
		if r.entry.Kind != "column" || r.entry.OID == p.RelationOID || r.data["attisdropped"] == true {
			continue
		}
		if owner := objects[gaussObject{p.Classes["pg_class"], r.entry.OID}]; owner != "" {
			name, ok := r.data["attname"].(string)
			if !ok {
				return out, ErrConflict
			}
			if attributes[r.entry.OID] == nil {
				attributes[r.entry.OID] = map[int]string{}
			}
			id := "@physical-column:" + name
			if owner == "@table:reldeltarelid" {
				if names.Columns[name] == "" {
					return out, ErrConflict
				}
				id = "@column:" + names.Columns[name]
			}
			attributes[r.entry.OID][r.entry.SubID] = id
		}
	}
	for _, r := range records {
		if r.entry.Kind != "default" {
			continue
		}
		owner, err := gaussNumber(r.data["adrelid"])
		if err != nil {
			return out, err
		}
		number, err := gaussNumber(r.data["adnum"])
		if err != nil {
			return out, err
		}
		if owner != p.RelationOID {
			alias := objects[gaussObject{p.Classes["pg_class"], owner}]
			attribute := attributes[owner][int(number)]
			if alias != "" && attribute != "" {
				if err = bind("pg_attrdef", r.entry.OID, "@default:"+alias+":"+attribute); err != nil {
					return out, err
				}
			}
		}
	}
	node := OpenGaussNodeBindings{Types: map[int64]string{}, Relations: map[int64]string{}, Variables: map[int]map[int]string{1: attributes[p.RelationOID], 2: attributes[p.RelationOID]}, ByteOrder: p.NativeByteOrder, SequenceNames: map[string]string{}}
	for object, id := range objects {
		if object.class == p.Classes["pg_type"] {
			node.Types[int64(object.oid)] = id
		}
		if object.class == p.Classes["pg_class"] {
			node.Relations[int64(object.oid)] = id
		}
	}
	classForKind := map[string]string{"relation": "pg_class", "column": "pg_class", "index": "pg_class", "object": "pg_class", "type": "pg_type", "default": "pg_attrdef", "constraint": "pg_constraint", "incoming_constraint": "pg_constraint", "trigger": "pg_trigger", "rule": "pg_rewrite", "partition": "pg_partition", "policy": "pg_rlspolicy", "namespace": "pg_namespace"}
	sequenceSources := append(append([]OpenGaussCopySequence{}, definition.Sequences...), names.ReferenceSequences...)
	for _, seq := range sequenceSources {
		if alias := objects[gaussObject{p.Classes["pg_class"], seq.OID}]; alias != "" {
			node.SequenceNames[seq.Schema+"."+seq.Name] = alias
		}
	}
	for _, r := range records {
		e, v := r.entry, r.data
		phase = fmt.Sprintf("record %s:%d", e.Kind, e.OID)
		if e.Kind == "copy_definition" || e.Kind == "design_definition" {
			continue
		}
		class := classForKind[e.Kind]
		id := objects[gaussObject{p.Classes[class], e.OID}]
		if class == "" && e.OID == p.RelationOID {
			id = "@table"
		}
		if id == "" {
			id = strconv.FormatUint(e.OID, 10)
		}
		key := e.Kind + ":" + id
		if e.Kind == "object" {
			key += fmt.Sprintf(":%d", e.SubID)
		}
		if strings.HasPrefix(id, "@") {
			identityField := map[string]string{"relation": "relname", "type": "typname", "constraint": "conname", "incoming_constraint": "conname", "trigger": "tgname", "rule": "rulename", "partition": "relname"}[e.Kind]
			if identityField != "" {
				v[identityField] = id
			}
		}
		if e.Kind == "column" {
			if v["attisdropped"] == true {
				continue
			}
			attr := attributes[e.OID][e.SubID]
			if attr == "" {
				attr = strconv.Itoa(e.SubID)
			}
			key += ":" + attr
			delete(v, "attcacheoff")
			delete(v, "attinitdefval")
		}
		if e.Kind == "relation" || e.Kind == "partition" {
			for _, f := range []string{"relfilenode", "relpages", "reltuples", "relallvisible", "relfrozenxid", "relfrozenxid64", "relminmxid"} {
				delete(v, f)
			}
			if e.Kind == "relation" {
				for _, f := range []string{"relhasindex", "relhasrules", "relhastriggers", "relhassubclass"} {
					delete(v, f)
				}
			}
		}
		if e.Kind == "object" {
			delete(v, "mtime")
			delete(v, "changecsn")
		}
		for field, class := range gaussDesignReferenceFields(e.Kind) {
			value, ok := v[field]
			if !ok {
				continue
			}
			x, err := reference(value, class)
			if err != nil {
				return out, err
			}
			v[field] = x
		}
		if e.Kind == "column" {
			if attr := attributes[e.OID][e.SubID]; attr != "" {
				v["attnum"] = attr
				v["attname"] = attr
			}
		}
		if e.Kind == "default" {
			number, err := gaussNumber(v["adnum"])
			if err != nil {
				return out, err
			}
			raw, err := gaussJSON(e.Data)
			if err != nil {
				return out, err
			}
			owner, err := gaussNumber(raw["adrelid"])
			if err != nil {
				return out, err
			}
			v["adnum"] = attributes[owner][int(number)]
			if raw, ok := v["adsrc"].(string); ok && raw != "" {
				v["adsrc"] = gaussCopySQLSignature(raw)
				for _, seq := range sequenceSources {
					if _, err := gaussCopyDefault(OpenGaussCopyColumn{Default: raw}, seq, seq.Schema, seq.Name); err == nil {
						v["adsrc"] = objects[gaussObject{p.Classes["pg_class"], seq.OID}]
					}
				}
			}
		}
		for field, class := range gaussDesignVectorFields(e.Kind) {
			value, ok := v[field]
			if !ok || value == nil {
				continue
			}
			if class == "columns" {
				owner := p.RelationOID
				ownerField := map[string]string{"index": "indrelid", "constraint": "conrelid", "incoming_constraint": "conrelid", "trigger": "tgrelid"}[e.Kind]
				if ownerField != "" {
					raw, e := gaussJSON(e.Data)
					if e != nil {
						return out, e
					}
					owner, err = gaussNumber(raw[ownerField])
					if err != nil {
						return out, err
					}
				}
				if e.Kind == "incoming_constraint" && attributes[owner] == nil {
					continue // unbound external records retain exact native values
				}
				v[field], err = gaussDesignAttributeVector(value, attributes[owner])
				if err != nil {
					return out, fmt.Errorf("native vector %s=%v on relation %d (%v): %w", field, value, owner, attributes[owner], err)
				}
			} else {
				v[field], err = gaussDesignReferenceVector(value, class, reference)
			}
			if err != nil {
				return out, fmt.Errorf("native vector %s=%v: %w", field, value, err)
			}
		}
		if e.Kind == "constraint" || e.Kind == "incoming_constraint" {
			raw, e := gaussJSON(e.Data)
			if e != nil {
				return out, e
			}
			if raw["contype"] == "f" {
				parent, e := gaussNumber(raw["confrelid"])
				if e != nil {
					return out, e
				}
				// Self-reference parent slots have the same explicit stable
				// column IDs as child slots, including dropped-slot holes.
				if attributes[parent] != nil {
					v["confkey"], err = gaussDesignAttributeVector(raw["confkey"], attributes[parent])
					if err != nil {
						return out, err
					}
				}
			}
		}
		for _, field := range gaussDesignNodeFields(e.Kind) {
			value, ok := v[field]
			if !ok || value == nil {
				continue
			}
			raw, ok := value.(string)
			if !ok {
				return out, ErrConflict
			}
			if raw == "" {
				continue
			}
			if e.Kind == "rule" {
				continue
			} // retained complete raw action trees
			v[field], err = NormalizeOpenGaussNode(raw, node)
			if err != nil {
				return out, fmt.Errorf("native %s %s: %w", e.Kind, field, err)
			}
		}
		if rows, ok := v["rows"].([]any); ok {
			for _, raw := range rows {
				row, ok := raw.(map[string]any)
				if !ok {
					return out, ErrConflict
				}
				switch e.Kind {
				case "dependencies", "shared_dependencies":
					for _, prefix := range []string{"", "ref"} {
						classOID, err := gaussNumber(row[prefix+"classid"])
						if err != nil {
							return out, err
						}
						objectOID, err := gaussNumber(row[prefix+"objid"])
						if err != nil {
							return out, err
						}
						if alias := objects[gaussObject{classOID, objectOID}]; alias != "" {
							row[prefix+"objid"] = alias
						}
						if classOID == p.Classes["pg_class"] {
							if sub, ok := row[prefix+"objsubid"]; ok {
								n, err := gaussNumber(sub)
								if err != nil {
									return out, err
								}
								if attr := attributes[objectOID][int(n)]; attr != "" {
									row[prefix+"objsubid"] = attr
								}
							}
						}
					}
				case "comments", "security_labels":
					classOID, err := gaussNumber(row["classoid"])
					if err != nil {
						return out, err
					}
					objectOID, err := gaussNumber(row["objoid"])
					if err != nil {
						return out, err
					}
					if alias := objects[gaussObject{classOID, objectOID}]; alias != "" {
						row["objoid"] = alias
					}
					if classOID == p.Classes["pg_class"] {
						n, err := gaussNumber(row["objsubid"])
						if err != nil {
							return out, err
						}
						if attr := attributes[objectOID][int(n)]; attr != "" {
							row["objsubid"] = attr
						}
					}
				case "inheritance":
					for _, f := range []string{"inhrelid", "inhparent"} {
						row[f], err = reference(row[f], "pg_class")
						if err != nil {
							return out, err
						}
					}
				case "publications":
					row["prrelid"], err = reference(row["prrelid"], "pg_class")
					if err != nil {
						return out, err
					}
				}
			}
			sort.Slice(rows, func(i, j int) bool {
				a, _ := json.Marshal(rows[i])
				b, _ := json.Marshal(rows[j])
				return string(a) < string(b)
			})
		}
		if out.Records[key] != nil {
			return out, errors.New("ambiguous native normalized catalog identity")
		}
		out.Records[key] = v
		out.Objects[key] = id
		out.Identities[key] = e.OID
	}
	return out, nil
}

func gaussDesignReferenceFields(kind string) map[string]string {
	switch kind {
	case "relation":
		return map[string]string{"relnamespace": "pg_namespace", "reltype": "pg_type", "reloftype": "pg_type", "reltoastrelid": "pg_class", "reltoastidxid": "pg_class", "reldeltarelid": "pg_class", "reldeltaidx": "pg_class", "relcudescrelid": "pg_class", "relcudescidx": "pg_class"}
	case "column":
		return map[string]string{"attrelid": "pg_class", "atttypid": "pg_type"}
	case "type":
		return map[string]string{"typrelid": "pg_class", "typelem": "pg_type", "typarray": "pg_type", "typbasetype": "pg_type"}
	case "default":
		return map[string]string{"adrelid": "pg_class"}
	case "index":
		return map[string]string{"indexrelid": "pg_class", "indrelid": "pg_class"}
	case "constraint", "incoming_constraint":
		return map[string]string{"conrelid": "pg_class", "conindid": "pg_class", "confrelid": "pg_class", "contypid": "pg_type"}
	case "trigger":
		return map[string]string{"tgrelid": "pg_class", "tgconstrrelid": "pg_class", "tgconstrindid": "pg_class", "tgconstraint": "pg_constraint"}
	case "rule":
		return map[string]string{"ev_class": "pg_class"}
	case "partition":
		return map[string]string{"parentid": "pg_class", "indextblid": "pg_partition", "reltoastrelid": "pg_class", "reltoastidxid": "pg_class"}
	case "policy":
		return map[string]string{"polrelid": "pg_class"}
	case "object":
		return map[string]string{"object_oid": "pg_class"}
	}
	return nil
}
func gaussDesignVectorFields(kind string) map[string]string {
	switch kind {
	case "constraint", "incoming_constraint":
		return map[string]string{"conkey": "columns"}
	case "index":
		return map[string]string{"indkey": "columns", "indclass": "pg_opclass", "indcollation": "pg_collation"}
	case "trigger":
		return map[string]string{"tgattr": "columns"}
	case "partition":
		return map[string]string{"partkey": "columns"}
	}
	return nil
}
func gaussDesignNodeFields(kind string) []string {
	switch kind {
	case "default":
		return []string{"adbin"}
	case "index":
		return []string{"indexprs", "indpred"}
	case "constraint":
		return []string{"conbin"}
	case "trigger":
		return []string{"tgqual"}
	case "policy":
		return []string{"polqual"}
	}
	return nil
}
func gaussDesignAttributeVector(value any, columns map[int]string) (any, error) {
	var items []any
	switch v := value.(type) {
	case []any:
		items = v
	case string:
		for _, part := range strings.Fields(v) {
			items = append(items, json.Number(part))
		}
	default:
		return nil, ErrConflict
	}
	for i, item := range items {
		n, err := gaussNumber(item)
		if err != nil {
			return nil, err
		}
		if alias := columns[int(n)]; alias != "" {
			items[i] = alias
		} else if n != 0 {
			return nil, ErrConflict
		}
	}
	return items, nil
}
func gaussDesignReferenceVector(value any, class string, reference func(any, string) (any, error)) (any, error) {
	var items []any
	switch v := value.(type) {
	case []any:
		items = v
	case string:
		for _, part := range strings.Fields(v) {
			items = append(items, json.Number(part))
		}
	default:
		return nil, ErrConflict
	}
	for i, item := range items {
		v, err := reference(item, class)
		if err != nil {
			return nil, err
		}
		items[i] = v
	}
	return items, nil
}
