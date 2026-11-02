package engines

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func pgDesiredGraphNames(table schemaddl.Table) schemaddl.PostgreSQLGraphNames {
	names := schemaddl.PostgreSQLGraphNames{Columns: map[string]string{}, Constraints: map[string]string{}, Indexes: map[string]string{}, Triggers: map[string]string{}}
	for _, clause := range table.Clauses {
		if clause.Kind == "column" {
			names.Columns[clause.Name] = clause.ID
		} else if clause.Kind == "constraint" {
			names.Constraints[clause.Name] = clause.ID
		}
	}
	for _, definition := range table.Definitions {
		if definition.Kind == "index" {
			names.Indexes[definition.Name] = definition.Name
		} else if definition.Kind == "trigger" {
			names.Triggers[definition.Name] = definition.Name
		}
	}
	return names
}

func pgGraphNodeHashes(t *testing.T, catalog *schemaddl.PostgreSQLCatalog, bindings schemaddl.PostgreSQLGraphBindings) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, entry := range catalog.Entries {
		class := ""
		fields := []string{}
		switch entry.Kind {
		case "default":
			class, fields = "pg_attrdef", []string{"adbin"}
		case "constraint":
			class, fields = "pg_constraint", []string{"conbin"}
		case "index":
			class, fields = "pg_class", []string{"indexprs", "indpred"}
		case "trigger":
			class, fields = "pg_trigger", []string{"tgqual"}
		default:
			continue
		}
		object := bindings.Objects[schemaddl.PostgreSQLCatalogObject{ClassOID: bindings.Classes[class], ObjectOID: entry.OID}]
		if object == "" {
			continue
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(entry.Data, &data); err != nil {
			t.Fatal(err)
		}
		for _, field := range fields {
			raw, ok := data[field]
			if !ok {
				t.Fatal("expression evidence missing", entry.Kind, field)
			}
			var tree string
			if string(raw) != "null" {
				if err := json.Unmarshal(raw, &tree); err != nil {
					t.Fatal(err)
				}
			}
			node := bindings.Node
			if entry.Kind == "trigger" {
				// OLD and NEW are both explicitly bound to this target's
				// attributes. Ordinary table expressions bind only varno 1.
				node.Variables = map[int]map[int]string{1: bindings.Node.Variables[1], 2: bindings.Node.Variables[1]}
			}
			hash, err := schemaddl.NormalizePostgreSQLNode(tree, node)
			if err != nil {
				t.Fatal("native expression cannot be bound", entry.Kind, field, err)
			}
			result[object+":"+field] = hash
		}
	}
	return result
}

func TestPostgreSQL17DesignGraphIdentityExpressionFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_graph;
CREATE SCHEMA nyadb_graph_external;
CREATE TABLE nyadb_graph_external.parent(id bigint PRIMARY KEY);
CREATE FUNCTION nyadb_graph_external.on_change() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$;
CREATE TABLE nyadb_graph.original(
 id bigint GENERATED ALWAYS AS IDENTITY,
 dropped integer,
 parent_id bigint,
 amount numeric(40,20) DEFAULT 12345678901234567890.12345678901234567890,
 registry regclass DEFAULT 'nyadb_graph_external.parent'::regclass,
 generated numeric GENERATED ALWAYS AS (amount*2) STORED,
 CONSTRAINT graph_pk PRIMARY KEY(id),
 CONSTRAINT graph_fk FOREIGN KEY(parent_id) REFERENCES nyadb_graph_external.parent(id) ON UPDATE CASCADE,
 CONSTRAINT graph_ck CHECK(amount>=0)
);
ALTER TABLE nyadb_graph.original DROP COLUMN dropped;
CREATE INDEX graph_idx ON nyadb_graph.original((amount+1)) INCLUDE(parent_id) WHERE amount>0;
CREATE TRIGGER graph_tr BEFORE UPDATE ON nyadb_graph.original FOR EACH ROW WHEN (OLD.amount IS DISTINCT FROM NEW.amount) EXECUTE FUNCTION nyadb_graph_external.on_change();
CREATE CONSTRAINT TRIGGER graph_ct AFTER UPDATE ON nyadb_graph.original DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.amount>0) EXECUTE FUNCTION nyadb_graph_external.on_change();`)
	original := pgDesignRead(t, s, "nyadb_graph", "original")
	desired := original.Schema.Table.Clone()
	desired.Postgres.Comment = "graph normalization"
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	baseline, err := s.postgresCanonicalDesired(context.Background(), c, contract.SchemaDesignRequest{Original: original, Desired: desired})
	if err != nil {
		t.Fatal(err)
	}
	actualBindings, err := schemaddl.BuildPostgreSQLGraphBindings(*original.Schema.Postgres, pgDesiredGraphNames(desired))
	if err != nil {
		t.Fatal(err)
	}
	probeBindings, err := schemaddl.BuildPostgreSQLGraphBindings(*baseline.Probe.Postgres, baseline.GraphNames)
	if err != nil {
		t.Fatal(err)
	}
	if original.Schema.Postgres.NativeByteOrder != "little" || probeBindings.Node.ByteOrder != "little" {
		t.Fatal("fixture native byte order was not captured")
	}
	actualObjects, probeObjects := []string{}, []string{}
	for _, identity := range actualBindings.Objects {
		actualObjects = append(actualObjects, identity)
	}
	for _, identity := range probeBindings.Objects {
		probeObjects = append(probeObjects, identity)
	}
	slices.Sort(actualObjects)
	slices.Sort(probeObjects)
	if !slices.Equal(actualObjects, probeObjects) {
		t.Fatal("independent probe acquired different logical catalog identities")
	}
	if probeBindings.Objects[schemaddl.PostgreSQLCatalogObject{ClassOID: probeBindings.Classes["pg_class"], ObjectOID: original.Schema.Postgres.RelationOID}] != "" {
		t.Fatal("original context became a probe target asset")
	}
	actualTrees := pgGraphNodeHashes(t, original.Schema.Postgres, actualBindings)
	probeTrees := pgGraphNodeHashes(t, baseline.Probe.Postgres, probeBindings)
	if len(actualTrees) < 12 || schemaddl.Signature(actualTrees) != schemaddl.Signature(probeTrees) {
		for key, hash := range actualTrees {
			if probeTrees[key] != hash {
				t.Log("differing bound expression:", key)
			}
		}
		t.Fatal("independent expression identity differed across catalog OIDs/attribute gaps", len(actualTrees), len(probeTrees))
	}
	actualGraph, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*original.Schema.Postgres, pgDesiredGraphNames(desired))
	if err != nil {
		t.Fatal("original declaration projection unavailable", err)
	}
	probeGraph, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*baseline.Probe.Postgres, baseline.GraphNames)
	if err != nil {
		t.Fatal("independent declaration projection unavailable", err)
	}
	if err := schemaddl.ComparePostgreSQLDeclarationGraphs(actualGraph, probeGraph); err != nil {
		t.Fatal("complete target declaration graph differs", err)
	}
	pgGraphMutationChecks(t, *original.Schema.Postgres, pgDesiredGraphNames(desired), actualGraph)
	// A genuine expression change must not normalize to the old expression.
	changed := original.Schema.Postgres.Clone()
	for i, entry := range changed.Entries {
		if entry.Kind != "constraint" {
			continue
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(entry.Data, &data); err != nil {
			t.Fatal(err)
		}
		if string(data["conname"]) != `"graph_ck"` {
			continue
		}
		data["conbin"] = json.RawMessage(`"{CONST :consttype 16 :constisnull false :constvalue 1 [ 0 0 0 0 0 0 0 0 ] :location -1}"`)
		changed.Entries[i].Data, err = json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
	}
	if schemaddl.Signature(pgGraphNodeHashes(t, changed, actualBindings)) == schemaddl.Signature(actualTrees) {
		t.Fatal("changed CHECK tree became equivalent through identifier mapping")
	}
	pgProbeAbsent(t, c, "nyadb_graph")
	after, err := postgresCatalogSnapshot(context.Background(), c, original.Target)
	if err != nil || after.Schema.Revision != original.Schema.Revision {
		t.Fatal("catalog mapping changed original assets", err)
	}
}

func pgGraphMutationChecks(t *testing.T, catalog schemaddl.PostgreSQLCatalog, names schemaddl.PostgreSQLGraphNames, original schemaddl.PostgreSQLDeclarationGraph) {
	t.Helper()
	for name, edit := range map[string]func(*schemaddl.PostgreSQLCatalog){
		"unknown exact property": func(p *schemaddl.PostgreSQLCatalog) {
			pgEditCatalogField(t, p, "relation", p.RelationOID, 0, "future_property", json.RawMessage(`9007199254740993`))
		},
		"default row missing": func(p *schemaddl.PostgreSQLCatalog) {
			p.Entries = slices.DeleteFunc(p.Entries, func(e schemaddl.PostgreSQLCatalogEntry) bool { return e.Kind == "default" })
		},
		"attribute row missing": func(p *schemaddl.PostgreSQLCatalog) {
			p.Entries = slices.DeleteFunc(p.Entries, func(e schemaddl.PostgreSQLCatalogEntry) bool {
				return e.Kind == "column" && e.OID == p.RelationOID && e.SubID == 4
			})
		},
		"dependency list missing": func(p *schemaddl.PostgreSQLCatalog) {
			p.Entries = slices.DeleteFunc(p.Entries, func(e schemaddl.PostgreSQLCatalogEntry) bool { return e.Kind == "dependency" && e.OID == p.RelationOID })
		},
		"changed nullability": func(p *schemaddl.PostgreSQLCatalog) {
			pgEditCatalogField(t, p, "column", p.RelationOID, 3, "attnotnull", json.RawMessage(`true`))
		},
		"unexpected default marker": func(p *schemaddl.PostgreSQLCatalog) {
			pgEditCatalogField(t, p, "column", p.RelationOID, 4, "atthasdef", json.RawMessage(`false`))
		},
		"relational scope": func(p *schemaddl.PostgreSQLCatalog) { p.DatabaseOID++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := catalog.Clone()
			edit(changed)
			graph, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*changed, names)
			if err == nil {
				err = schemaddl.ComparePostgreSQLDeclarationGraphs(graph, original)
			}
			if err == nil {
				t.Fatal("unrequested change acquired declaration equivalence")
			}
		})
	}
	changed := catalog.Clone()
	pgEditCatalogField(t, changed, "relation", changed.RelationOID, 0, "relfilenode", json.RawMessage(`999999`))
	pgEditCatalogField(t, changed, "relation", changed.RelationOID, 0, "relacl", json.RawMessage(`["reader=r/owner"]`))
	graph, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*changed, names)
	if err != nil || schemaddl.ComparePostgreSQLDeclarationGraphs(graph, original) != nil {
		t.Fatal("opaque/physical state was conflated with independent declaration", err)
	}
	if schemaddl.Signature(graph.Records) == schemaddl.Signature(original.Records) {
		t.Fatal("original properties were lost rather than separately retained")
	}
}

func pgEditCatalogField(t *testing.T, p *schemaddl.PostgreSQLCatalog, kind string, oid int64, sub int, field string, value json.RawMessage) {
	t.Helper()
	for i, e := range p.Entries {
		if e.Kind != kind || e.OID != oid || e.SubID != sub {
			continue
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(e.Data, &data); err != nil {
			t.Fatal(err)
		}
		if value == nil {
			delete(data, field)
		} else {
			data[field] = value
		}
		var err error
		p.Entries[i].Data, err = json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("captured fixture catalog entry absent", kind, oid, sub)
}
