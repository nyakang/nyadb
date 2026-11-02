package schemaddl

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func pgContextFixture(t *testing.T) (PostgreSQLCatalog, PostgreSQLGraphNames) {
	t.Helper()
	p, names := pgGraphFixture()
	pgGraphChange(t, &p, "column", 650, "attisdropped", false)
	pgGraphChange(t, &p, "trigger", 661, "tgfoid", 1644)
	pgGraphChange(t, &p, "trigger", 661, "tgtype", 5)
	p.Entries = append(p.Entries,
		pgRecord("routine", 700, 0, `{"oid":700,"prosrc":"private original SQL","future":9007199254740993}`),
		pgRecord("policy", 710, 0, `{"polrelid":700,"polqual":"private policy"}`),
		pgRecord("rewrite", 711, 0, `{"ev_class":650,"ev_action":"private rewrite"}`),
		pgRecord("publication_relation", 712, 0, `{"prrelid":700,"prattrs":[1,3]}`),
		pgRecord("security_label:1259", 650, 1, `{"labels":[{"provider":"fixture","label":"private label"}]}`),
		pgRecord("dependency", 700, 0, `{"edges":[{"classid":1255,"objid":700,"objsubid":0,"refclassid":1259,"refobjid":700,"refobjsubid":3,"deptype":"n","future":9007199254740993},{"classid":1259,"objid":700,"objsubid":0,"refclassid":2615,"refobjid":11,"refobjsubid":0,"deptype":"n"}]}`),
	)
	return p, names
}

func TestPostgreSQLUnselectedContextPreservesTypedIdentityAndAllProperties(t *testing.T) {
	p, names := pgContextFixture(t)
	before := Signature(p)
	after := p.Clone()
	slices.Reverse(after.Entries)
	// Target declarations are checked by the separate independent graph;
	// same numeric OID in an unselected catalog cannot acquire that permission.
	pgGraphChange(t, after, "relation", 700, "comment", "requested target comment")
	if err := pgVerifyUnselectedCatalog(p, names, *after, names); err != nil {
		t.Fatal("unchanged context rejected", err)
	}
	if Signature(p) != before {
		t.Fatal("context proof mutated captured evidence")
	}
	for _, sample := range []struct {
		name, kind, field string
		oid               int64
		value             any
	}{
		{"class collision", "routine", "prosrc", 700, "private changed SQL"},
		{"exact unknown number", "routine", "future", 700, json.Number("9007199254740992")},
		{"incoming FK", "constraint", "confdeltype", 660, "c"},
		{"incoming trigger on target", "trigger", "tgenabled", 661, "D"},
		{"external physical identity", "relation", "relfilenode", 650, 8000},
		{"policy on target", "policy", "polqual", 710, "private changed policy"},
		{"external rewrite", "rewrite", "ev_action", 711, "private changed rewrite"},
		{"publication", "publication_relation", "prattrs", 712, []int{1}},
		{"dropped residual", "column", "attlen", 700, 100},
		{"unknown label payload", "security_label:1259", "future", 650, "private changed label"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			changed := p.Clone()
			if sample.name == "dropped residual" {
				for i, e := range changed.Entries {
					if e.Kind == "column" && e.OID == 700 && e.SubID == 2 {
						var fields map[string]json.RawMessage
						_ = json.Unmarshal(e.Data, &fields)
						fields[sample.field], _ = json.Marshal(sample.value)
						changed.Entries[i].Data, _ = json.Marshal(fields)
					}
				}
			} else {
				pgGraphChange(t, changed, sample.kind, sample.oid, sample.field, sample.value)
			}
			if err := pgVerifyUnselectedCatalog(p, names, *changed, names); !errors.Is(err, ErrConflict) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "900719925474099") {
				t.Fatal("changed context accepted or payload logged", err)
			}
		})
	}
}

func TestPostgreSQLUnselectedContextRejectsMissingNewAndReboundRecords(t *testing.T) {
	p, names := pgContextFixture(t)
	for _, kind := range []string{"routine", "constraint", "trigger", "policy", "rewrite", "publication_relation", "security_label:1259"} {
		t.Run(kind, func(t *testing.T) {
			missing := p.Clone()
			missing.Entries = slices.DeleteFunc(missing.Entries, func(e PostgreSQLCatalogEntry) bool {
				return e.Kind == kind && (e.OID == 700 && kind == "routine" || e.OID == 660 || e.OID == 661 || e.OID == 710 || e.OID == 711 || e.OID == 712 || e.OID == 650 && strings.HasPrefix(kind, "security_label:"))
			})
			if err := pgVerifyUnselectedCatalog(p, names, *missing, names); !errors.Is(err, ErrConflict) {
				t.Fatal("missing context accepted", err)
			}
		})
	}
	added := p.Clone()
	added.Entries = append(added.Entries, pgRecord("future_catalog", 8888, 0, `{"future":true}`))
	if err := pgVerifyUnselectedCatalog(p, names, *added, names); !errors.Is(err, ErrConflict) {
		t.Fatal("new unselected object accepted", err)
	}
	// An original external object cannot become target-owned merely through
	// an actual binding or a desired alias with the same name.
	rebound := p.Clone()
	pgGraphChange(t, rebound, "constraint", 660, "conrelid", 700)
	nextNames := names
	nextNames.Constraints = map[string]string{}
	for name, id := range names.Constraints {
		nextNames.Constraints[name] = id
	}
	nextNames.Constraints["incoming_fk"] = "new-fk"
	if err := pgVerifyUnselectedCatalog(p, names, *rebound, nextNames); !errors.Is(err, ErrConflict) {
		t.Fatal("original context acquired target authorization", err)
	}
}

func TestPostgreSQLUnselectedDependenciesPreserveIncomingEdgesMultiplicityAndWrappers(t *testing.T) {
	p, names := pgContextFixture(t)
	changeEdges := func(t *testing.T, p *PostgreSQLCatalog, change func(map[string]json.RawMessage, []map[string]json.RawMessage)) {
		t.Helper()
		for i, e := range p.Entries {
			if e.Kind != "dependency" || e.OID != 700 {
				continue
			}
			var fields map[string]json.RawMessage
			var edges []map[string]json.RawMessage
			_ = json.Unmarshal(e.Data, &fields)
			_ = json.Unmarshal(fields["edges"], &edges)
			change(fields, edges)
			p.Entries[i].Data, _ = json.Marshal(fields)
			return
		}
		t.Fatal("dependency fixture missing")
	}
	for name, mutate := range map[string]func(map[string]json.RawMessage, []map[string]json.RawMessage){
		"incoming field": func(f map[string]json.RawMessage, e []map[string]json.RawMessage) {
			e[0]["future"] = json.RawMessage(`9007199254740992`)
			f["edges"], _ = json.Marshal(e)
		},
		"incoming deleted": func(f map[string]json.RawMessage, e []map[string]json.RawMessage) {
			f["edges"], _ = json.Marshal(e[1:])
		},
		"incoming duplicate": func(f map[string]json.RawMessage, e []map[string]json.RawMessage) {
			f["edges"], _ = json.Marshal(append(e, e[0]))
		},
		"incoming retargeted": func(f map[string]json.RawMessage, e []map[string]json.RawMessage) {
			e[0]["refobjsubid"] = json.RawMessage(`1`)
			f["edges"], _ = json.Marshal(e)
		},
		"wrapper": func(f map[string]json.RawMessage, e []map[string]json.RawMessage) {
			f["private future"] = json.RawMessage(`"private wrapper"`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := p.Clone()
			changeEdges(t, changed, mutate)
			if err := pgVerifyUnselectedCatalog(p, names, *changed, names); !errors.Is(err, ErrConflict) || strings.Contains(err.Error(), "private") {
				t.Fatal("incoming dependency loss accepted or payload logged", err)
			}
		})
	}
	ordered := p.Clone()
	changeEdges(t, ordered, func(f map[string]json.RawMessage, e []map[string]json.RawMessage) {
		slices.Reverse(e)
		f["edges"], _ = json.Marshal(e)
	})
	if err := pgVerifyUnselectedCatalog(p, names, *ordered, names); err != nil {
		t.Fatal("dependency order changed preservation", err)
	}
}

func TestPostgreSQLUnselectedContextRejectsInvalidEvidenceAndWrongScope(t *testing.T) {
	p, names := pgContextFixture(t)
	malformed := p.Clone()
	pgGraphChange(t, malformed, "column", 650, "attrelid", 700)
	if err := pgVerifyUnselectedCatalog(*malformed, names, *malformed, names); err == nil {
		t.Fatal("malformed original external attribute accepted as unchanged")
	}
	for _, mutate := range []func(*PostgreSQLCatalog){
		func(p *PostgreSQLCatalog) { p.DatabaseOID++ },
		func(p *PostgreSQLCatalog) { p.NamespaceOID++ },
		func(p *PostgreSQLCatalog) { p.RelationOID++ },
		func(p *PostgreSQLCatalog) { p.ServerVersion++ },
		func(p *PostgreSQLCatalog) { p.NativeByteOrder = "big" },
		func(p *PostgreSQLCatalog) { p.Entries = append(p.Entries, p.Entries[0]) },
		func(p *PostgreSQLCatalog) { p.Entries = append(p.Entries, pgRecord("future", 1<<32, 0, `{}`)) },
		func(p *PostgreSQLCatalog) {
			p.Entries = append(p.Entries, pgRecord("security_label:private", 650, 0, `{}`))
		},
		func(p *PostgreSQLCatalog) {
			p.Entries = append(p.Entries, pgRecord("dependency", 1234, 0, `{"edges":[]}`))
		},
		func(p *PostgreSQLCatalog) {
			p.Entries = append(p.Entries, pgRecord("dependency", 1234, 0, `{"edges":[{"classid":1255,"objid":1235,"refclassid":1259,"refobjid":700,"objsubid":0,"refobjsubid":0,"deptype":"n"}]}`))
		},
	} {
		changed := p.Clone()
		mutate(changed)
		if err := pgVerifyUnselectedCatalog(p, names, *changed, names); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid context accepted or payload logged", err)
		}
	}
}
