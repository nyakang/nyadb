package schemaddl

import (
	"encoding/json"
	"slices"
	"testing"
)

func pgGraphFixture() (PostgreSQLCatalog, PostgreSQLGraphNames) {
	p := PostgreSQLCatalog{ServerVersion: 170000, NativeByteOrder: "little", Database: "fixture", Namespace: "fixture", DatabaseOID: 10, NamespaceOID: 11, RelationOID: 700}
	for name, oid := range map[string]int64{"pg_class": 1259, "pg_type": 1247, "pg_attrdef": 2604, "pg_constraint": 2606, "pg_trigger": 2620} {
		b, _ := json.Marshal(map[string]string{"name": name})
		p.Entries = append(p.Entries, PostgreSQLCatalogEntry{Kind: "catalog_class", OID: oid, Data: b})
	}
	p.Entries = append(p.Entries,
		pgRecord("relation", 700, 0, `{"relkind":"r","reltype":700,"relname":"probe"}`),
		pgRecord("type", 700, 0, `{"typrelid":700,"typarray":701}`),
		pgRecord("type", 701, 0, `{"typelem":700}`),
		pgRecord("column", 700, 1, `{"attnum":1,"attname":"id","attisdropped":false,"attidentity":"a"}`),
		pgRecord("column", 700, 2, `{"attnum":2,"attname":"........pg.dropped.2........","attisdropped":true}`),
		pgRecord("column", 700, 3, `{"attnum":3,"attname":"value","attisdropped":false,"attidentity":""}`),
		pgRecord("default", 700, 0, `{"adrelid":700,"adnum":3}`),
		pgRecord("constraint", 700, 0, `{"conrelid":700,"conname":"probe_pk","contype":"p"}`),
		pgRecord("constraint", 702, 0, `{"conrelid":700,"conname":"probe_fk","contype":"f"}`),
		pgRecord("constraint", 703, 0, `{"conrelid":700,"conname":"probe_ct","contype":"t"}`),
		pgRecord("index", 800, 0, `{"indrelid":700,"constraint_oid":700}`),
		pgRecord("relation", 800, 0, `{"relkind":"i"}`),
		pgRecord("index", 801, 0, `{"indrelid":700,"constraint_oid":0,"name":"probe_idx"}`),
		pgRecord("relation", 801, 0, `{"relkind":"i"}`),
		pgRecord("relation", 900, 0, `{"relkind":"S"}`),
		pgRecord("sequence", 900, 0, `{"seqrelid":900}`),
		pgRecord("trigger", 900, 0, `{"tgrelid":700,"tgisinternal":true,"tgconstraint":702,"tgfoid":1644,"tgtype":5}`),
		pgRecord("trigger", 901, 0, `{"tgrelid":650,"tgisinternal":true,"tgconstraint":702,"tgfoid":1654,"tgtype":9}`),
		pgRecord("trigger", 1000, 0, `{"tgrelid":700,"tgisinternal":false,"tgconstraint":0,"tgname":"probe_tr"}`),
		pgRecord("trigger", 1001, 0, `{"tgrelid":700,"tgisinternal":false,"tgconstraint":703,"tgname":"probe_ct"}`),
		pgRecord("dependency", 900, 0, `{"edges":[{"classid":1259,"objid":900,"objsubid":0,"refclassid":1259,"refobjid":700,"refobjsubid":1,"deptype":"i"},{"classid":2620,"objid":900,"objsubid":0,"refclassid":2606,"refobjid":702,"refobjsubid":0,"deptype":"i"}]}`),
		// Incoming FK and original/context records must not be selected as
		// newly declared target assets, even when a trigger is on the target.
		pgRecord("relation", 650, 0, `{"relkind":"r","reltype":651,"relname":"original"}`),
		pgRecord("type", 651, 0, `{"typrelid":650,"typarray":652}`),
		pgRecord("type", 652, 0, `{"typelem":651}`),
		pgRecord("constraint", 660, 0, `{"conrelid":650,"conname":"incoming_fk","contype":"f"}`),
		pgRecord("trigger", 661, 0, `{"tgrelid":700,"tgisinternal":true,"tgconstraint":660}`),
		pgRecord("column", 650, 1, `{"attnum":1,"attname":"context"}`),
	)
	for i, entry := range p.Entries {
		field := map[string]string{"relation": "oid", "type": "oid", "default": "oid", "constraint": "oid", "trigger": "oid", "index": "indexrelid", "column": "attrelid"}[entry.Kind]
		if field == "" {
			continue
		}
		var data map[string]json.RawMessage
		_ = json.Unmarshal(entry.Data, &data)
		data[field], _ = json.Marshal(entry.OID)
		p.Entries[i].Data, _ = json.Marshal(data)
	}
	names := PostgreSQLGraphNames{Columns: map[string]string{"id": "id-stable", "value": "value-stable"}, Constraints: map[string]string{"probe_pk": "pk-stable", "probe_fk": "fk-stable"}, Indexes: map[string]string{"probe_idx": "user-index"}, Triggers: map[string]string{"probe_tr": "user-trigger", "probe_ct": "constraint-trigger"}}
	return p, names
}

func pgGraphChange(t *testing.T, p *PostgreSQLCatalog, kind string, oid int64, field string, value any) {
	t.Helper()
	for i, e := range p.Entries {
		if e.Kind == kind && e.OID == oid {
			var data map[string]json.RawMessage
			if err := json.Unmarshal(e.Data, &data); err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			data[field] = b
			p.Entries[i].Data, err = json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("fixture record absent", kind, oid)
}

func TestPostgreSQLGraphBindsCatalogClassesGeneratedAssetsAndConstraintTriggers(t *testing.T) {
	p, names := pgGraphFixture()
	original := Signature(p)
	b, err := BuildPostgreSQLGraphBindings(p, names)
	if err != nil {
		t.Fatal(err)
	}
	for identity, wanted := range map[PostgreSQLCatalogObject]string{
		{1259, 700}: "@table", {1247, 700}: "@row-type", {1247, 701}: "@row-array", {2604, 700}: "@default:@column:value-stable", {2606, 700}: "@constraint:pk-stable", {1259, 800}: "@constraint-index:@constraint:pk-stable", {1259, 801}: "@index:user-index", {1259, 900}: "@sequence:@column:id-stable", {2620, 1000}: "@trigger:user-trigger", {2606, 703}: "@trigger-constraint:@trigger:constraint-trigger",
	} {
		if b.Objects[identity] != wanted {
			t.Fatal("typed object acquired the wrong identity", identity, b.Objects[identity], wanted)
		}
	}
	if b.Objects[PostgreSQLCatalogObject{2620, 900}] == "" || b.Objects[PostgreSQLCatalogObject{2620, 901}] == "" || b.Objects[PostgreSQLCatalogObject{2620, 900}] == b.Objects[PostgreSQLCatalogObject{2620, 901}] {
		t.Fatal("FK target/external triggers were conflated")
	}
	for _, identity := range []PostgreSQLCatalogObject{{1259, 650}, {1247, 651}, {1247, 652}, {2606, 660}, {2620, 661}} {
		if b.Objects[identity] != "" {
			t.Fatal("context or incoming relationship acquired target identity", identity)
		}
	}
	if len(b.Objects) != 14 || b.Node.Types[700] != "@row-type" || b.Node.Relations[700] != "@table" || b.Attributes[700][3] != "@column:value-stable" || len(b.Node.Variables) != 1 || b.Node.CompositeColumns[700][3] != b.Attributes[700][3] {
		t.Fatal("incomplete generated-object or expression bindings", len(b.Objects))
	}
	b.Node.Types[700] = "edited"
	b.Attributes[700][1] = "edited"
	if Signature(p) != original || names.Columns["id"] != "id-stable" {
		t.Fatal("bindings shared or changed captured source")
	}
}

func TestPostgreSQLGraphIsIndependentOfCatalogOrdering(t *testing.T) {
	p, names := pgGraphFixture()
	a, err := BuildPostgreSQLGraphBindings(p, names)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(p.Entries)
	b, err := BuildPostgreSQLGraphBindings(p, names)
	if err != nil || !equalPGGraphObjects(a.Objects, b.Objects) || Signature(a.Node) != Signature(b.Node) {
		t.Fatal("catalog ordering changed identity evidence", err)
	}
}

func TestPostgreSQLGraphAcceptsNativeOIDDecimalStringsAndRejectsLossyNumbers(t *testing.T) {
	p, names := pgGraphFixture()
	pgGraphChange(t, &p, "relation", 700, "reltype", "700")
	pgGraphChange(t, &p, "type", 700, "typrelid", "700")
	pgGraphChange(t, &p, "type", 700, "typarray", "701")
	pgGraphChange(t, &p, "type", 701, "typelem", "700")
	if _, err := BuildPostgreSQLGraphBindings(p, names); err != nil {
		t.Fatal("native PostgreSQL OID strings rejected", err)
	}
	for _, raw := range []string{`1.5`, `7e2`, `"700 "`, `null`, `true`, `[]`, `"9223372036854775808"`} {
		r := pgGraphRecord{entry: PostgreSQLCatalogEntry{Kind: "relation"}, data: map[string]json.RawMessage{"reltype": json.RawMessage(raw)}}
		if _, err := r.integer("reltype"); err == nil {
			t.Fatal("invalid/lossy catalog identity accepted", raw)
		}
	}
}

func equalPGGraphObjects(a, b map[PostgreSQLCatalogObject]string) bool {
	if len(a) != len(b) {
		return false
	}
	for object, key := range a {
		if b[object] != key {
			return false
		}
	}
	return true
}

func TestPostgreSQLGraphRejectsIncompleteAmbiguousOrWrongOwnershipWithoutPartialEvidence(t *testing.T) {
	for name, change := range map[string]func(*testing.T, *PostgreSQLCatalog, *PostgreSQLGraphNames){
		"no target": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) { p.RelationOID = 0 },
		"no class": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			p.Entries = slices.DeleteFunc(p.Entries, func(e PostgreSQLCatalogEntry) bool { return e.Kind == "catalog_class" && e.OID == 1247 })
		},
		"wrong row owner": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			pgGraphChange(t, p, "type", 700, "typrelid", 650)
		},
		"wrong array element": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			pgGraphChange(t, p, "type", 701, "typelem", 651)
		},
		"wrong target stored OID": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			pgGraphChange(t, p, "relation", 700, "oid", 650)
		},
		"wrong attribute relation": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			pgGraphChange(t, p, "column", 700, "attrelid", 650)
		},
		"wrong stored index OID": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			pgGraphChange(t, p, "index", 800, "indexrelid", 801)
		},
		"missing column alias": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) { delete(n.Columns, "value") },
		"duplicate column ID": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			n.Columns["value"] = n.Columns["id"]
		},
		"extra column alias":       func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) { n.Columns["absent"] = "absent" },
		"missing constraint alias": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) { delete(n.Constraints, "probe_pk") },
		"duplicate constraint ID": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			n.Constraints["probe_fk"] = n.Constraints["probe_pk"]
		},
		"wrong default column": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			pgGraphChange(t, p, "default", 700, "adnum", 2)
		},
		"wrong constraint index": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			pgGraphChange(t, p, "index", 800, "constraint_oid", 660)
		},
		"missing index relation": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			p.Entries = slices.DeleteFunc(p.Entries, func(e PostgreSQLCatalogEntry) bool { return e.Kind == "relation" && e.OID == 801 })
		},
		"wrong trigger constraint": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			pgGraphChange(t, p, "trigger", 1001, "tgconstraint", 660)
		},
		"orphan constraint trigger": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			p.Entries = slices.DeleteFunc(p.Entries, func(e PostgreSQLCatalogEntry) bool { return e.Kind == "trigger" && e.OID == 1001 })
			delete(n.Triggers, "probe_ct")
		},
		"ambiguous internal trigger": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			p.Entries = append(p.Entries, pgRecord("trigger", 9999, 0, `{"tgrelid":700,"tgisinternal":true,"tgconstraint":702,"tgfoid":1644,"tgtype":5}`))
		},
		"missing ownership": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			p.Entries = slices.DeleteFunc(p.Entries, func(e PostgreSQLCatalogEntry) bool { return e.Kind == "dependency" })
		},
		"missing sequence record": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			p.Entries = slices.DeleteFunc(p.Entries, func(e PostgreSQLCatalogEntry) bool { return e.Kind == "sequence" })
		},
		"wrong sequence OID": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			pgGraphChange(t, p, "sequence", 900, "seqrelid", 901)
		},
		"wrong sequence kind": func(t *testing.T, p *PostgreSQLCatalog, n *PostgreSQLGraphNames) {
			pgGraphChange(t, p, "relation", 900, "relkind", "r")
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, n := pgGraphFixture()
			change(t, &p, &n)
			b, err := BuildPostgreSQLGraphBindings(p, n)
			if err == nil || b.Objects != nil || b.Node.Types != nil {
				t.Fatal("invalid graph published partial identity evidence", err)
			}
		})
	}
}
