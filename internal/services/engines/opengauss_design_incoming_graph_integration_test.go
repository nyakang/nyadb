package engines

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignIncomingParentGraphNativeTransitionsFixture(t *testing.T) {
	for _, operation := range []string{"type", "alias", "alias_metadata", "metadata", "rename_table", "rename_parent", "parent_slot_hole", "child_slot_hole", "cross_namespace", "child_defaults", "not_valid", "composite_reordered", "multiple", "cycle", "cycle_both_keys", "cycle_drop_outgoing", "constraint_comment"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
			defer cancel()
			parent, child := qualified(target.Object), quote(target.Object.Schema)+".other"
			if operation == "cross_namespace" {
				childSchema := target.Object.Schema + "_children"
				child = quote(childSchema) + ".other"
				if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+quote(childSchema)+"; CREATE TABLE "+child+"(id bigint)"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
					defer stop()
					if _, err := db.ExecContext(cleanup, "DROP SCHEMA "+quote(childSchema)+" CASCADE"); err != nil {
						t.Error(err)
					}
				})
			}
			setup := "ALTER TABLE " + child + " ADD COLUMN parent_id bigint; ALTER TABLE " + child + " ADD CONSTRAINT incoming_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id) ON DELETE CASCADE ON UPDATE RESTRICT DEFERRABLE INITIALLY DEFERRED; INSERT INTO " + child + " VALUES(9,1)"
			if operation == "child_slot_hole" {
				setup = "ALTER TABLE " + child + " ADD COLUMN gap text; ALTER TABLE " + child + " ADD COLUMN parent_id bigint; ALTER TABLE " + child + " DROP COLUMN gap; ALTER TABLE " + child + " ADD CONSTRAINT incoming_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id); INSERT INTO " + child + " VALUES(9,1)"
			}
			if operation == "not_valid" {
				setup = "ALTER TABLE " + child + " ADD COLUMN parent_id bigint; INSERT INTO " + child + " VALUES(9,999); ALTER TABLE " + child + " ADD CONSTRAINT incoming_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id) NOT VALID"
			}
			if operation == "parent_slot_hole" {
				setup = "DROP TABLE " + parent + "; CREATE TABLE " + parent + "(gap text,id bigint PRIMARY KEY); ALTER TABLE " + parent + " DROP COLUMN gap; INSERT INTO " + parent + " VALUES(1); " + setup
			}
			if operation == "composite_reordered" {
				setup = "DROP TABLE " + parent + "; CREATE TABLE " + parent + "(id bigint,gap text,key text,PRIMARY KEY(id,key)); ALTER TABLE " + parent + " DROP COLUMN gap; INSERT INTO " + parent + " VALUES(1,'native'); DROP TABLE " + child + "; CREATE TABLE " + child + "(child_key text,child_id bigint,CONSTRAINT incoming_fk FOREIGN KEY(child_key,child_id) REFERENCES " + parent + "(key,id) MATCH FULL DEFERRABLE INITIALLY DEFERRED); INSERT INTO " + child + " VALUES('native',1)"
			}
			if operation == "cycle" || operation == "cycle_both_keys" || operation == "cycle_drop_outgoing" {
				setup = "ALTER TABLE " + child + " ADD PRIMARY KEY(id); INSERT INTO " + child + " VALUES(9); ALTER TABLE " + parent + " ADD COLUMN partner_id bigint DEFAULT 9; ALTER TABLE " + parent + " ADD CONSTRAINT outgoing_fk FOREIGN KEY(partner_id) REFERENCES " + child + "(id); ALTER TABLE " + child + " ADD COLUMN parent_id bigint; UPDATE " + child + " SET parent_id=1; ALTER TABLE " + child + " ADD CONSTRAINT incoming_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id)"
			}
			if _, err := db.ExecContext(ctx, setup); err != nil {
				t.Fatal(err)
			}
			if operation == "multiple" {
				if _, err := db.ExecContext(ctx, "ALTER TABLE "+child+" ADD CONSTRAINT incoming_second FOREIGN KEY(parent_id) REFERENCES "+parent+"(id) ON DELETE RESTRICT; CREATE TABLE "+quote(target.Object.Schema)+".third(parent_id bigint REFERENCES "+parent+"(id))"); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "constraint_comment" {
				if _, err := db.ExecContext(ctx, "COMMENT ON CONSTRAINT incoming_fk ON "+child+" IS '保留原始外键注释'"); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "child_defaults" {
				if _, err := db.ExecContext(ctx, "CREATE SEQUENCE "+quote(target.Object.Schema)+".incoming_side_effect; ALTER TABLE "+child+" ALTER COLUMN parent_id SET DEFAULT nextval('"+quote(target.Object.Schema)+".incoming_side_effect')"); err != nil {
					t.Fatal(err)
				}
			}
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			if operation == "cycle_drop_outgoing" {
				var retained []schemaddl.Clause
				for _, clause := range desired.Clauses {
					if clause.Kind != "constraint" || clause.Name != "outgoing_fk" {
						retained = append(retained, clause)
					}
				}
				desired.Clauses = retained
			}
			if operation == "alias_metadata" {
				comment := "metadata with equal native type alias"
				desired.OpenGauss.Comment = &comment
			}
			switch operation {
			case "metadata":
				comment := "retained incoming foreign keys"
				desired.OpenGauss.Comment = &comment
			case "rename_table":
				desired.Name = "renamed_parent"
			default:
				for i, clause := range desired.Clauses {
					if clause.Kind != "column" || clause.Name != "id" && !(operation == "cycle_both_keys" && clause.Name == "partner_id") {
						continue
					}
					if operation == "rename_parent" {
						desired.Clauses[i].Name = "renamed_key"
						continue
					}
					field, err := schemaddl.ParseOpenGaussField(clause.SQL)
					if err != nil {
						t.Fatal(err)
					}
					field.Type = "integer"
					if operation == "alias" || operation == "alias_metadata" {
						field.Type = "int8"
					}
					desired.Clauses[i].SQL, err = field.SQL()
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			c, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on; LOCK TABLE "+parent+" IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			defer rollbackDesign(c)
			request := contract.SchemaDesignRequest{Original: original, Desired: desired}
			canonical, err := openGaussCanonicalDesign(ctx, c, request)
			if err != nil {
				t.Fatal("incoming canonical graph", err)
			}
			plan, err := openGaussEffectiveDesignPlan(original, desired, canonical, false)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "alias" && len(plan.SQL) != 0 {
				t.Fatal("equal native alias descriptor rebuilt incoming keys", plan.SQL)
			}
			for _, statement := range plan.SQL {
				if _, err = c.ExecContext(ctx, statement); err != nil {
					t.Fatal("native parent transition", err)
				}
			}
			actualTarget := target
			actualTarget.Object.Name = desired.Name
			actual, err := openGaussReadDesign(ctx, c, actualTarget)
			if err != nil {
				t.Fatal(err)
			}
			children, err := openGaussReadDesignIncomingActual(ctx, c, original, canonical)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "constraint_comment" {
				for oid, probe := range canonical.IncomingProbes {
					childCatalog := children[oid]
					input := schemaddl.Signature(childCatalog)
					statements, err := schemaddl.OpenGaussIncomingCommentRestoration(probe.Proof, original.Schema, desired, actual.Schema.OpenGauss, childCatalog)
					if err != nil || len(statements) != 1 || schemaddl.Signature(childCatalog) != input {
						t.Fatal("lost comment restoration was incomplete or mutated its evidence", statements, err)
					}
					corrupt := childCatalog.Clone()
					var constraintOID uint64
					for _, entry := range corrupt.Entries {
						if entry.Kind != "constraint" {
							continue
						}
						var row struct {
							Name string `json:"conname"`
						}
						if json.Unmarshal(entry.Data, &row) == nil && row.Name == "incoming_fk" {
							constraintOID = entry.OID
						}
					}
					if constraintOID == 0 {
						t.Fatal("missing actual incoming identity for comment fault")
					}
					for i, entry := range corrupt.Entries {
						if entry.Kind != "comments" {
							continue
						}
						var aggregate map[string]json.RawMessage
						var rows []map[string]json.RawMessage
						if json.Unmarshal(entry.Data, &aggregate) != nil || json.Unmarshal(aggregate["rows"], &rows) != nil {
							t.Fatal("invalid native comments")
						}
						object, _ := json.Marshal(constraintOID)
						class, _ := json.Marshal(corrupt.Classes["pg_constraint"])
						rows = append(rows, map[string]json.RawMessage{"classoid": class, "objoid": object, "objsubid": json.RawMessage(`0`), "description": json.RawMessage(`"unexpected concurrent comment"`)})
						aggregate["rows"], _ = json.Marshal(rows)
						corrupt.Entries[i].Data, _ = json.Marshal(aggregate)
					}
					if _, err = schemaddl.OpenGaussIncomingCommentRestoration(probe.Proof, original.Schema, desired, actual.Schema.OpenGauss, corrupt); err == nil {
						t.Fatal("incoming restoration overwrote a conflicting comment")
					}
				}
			}
			children, err = openGaussRestoreDesignIncomingComments(ctx, c, request, canonical, actual, children)
			if err != nil {
				t.Fatal("preserve original incoming comments", err)
			}
			if err = openGaussVerifyDesignCatalogWithIncoming(original, desired, canonical, actual, children); err != nil {
				t.Fatal("actual native incoming parent graph", err)
			}
			for oid, probe := range canonical.IncomingProbes {
				verify := func(parent, child *schemaddl.OpenGaussCatalog) error {
					ref := schemaddl.PostgreSQLTableReference{Schema: child.Namespace, Name: child.Name}
					if operation == "cycle" || operation == "cycle_both_keys" || operation == "cycle_drop_outgoing" {
						before := canonical.Incoming[oid].Baseline.Catalog()
						baseline, err := schemaddl.CaptureOpenGaussReferenceBaseline(original.Schema, desired, before)
						if err != nil {
							return err
						}
						return schemaddl.VerifyOpenGaussReferenceTransitionWithIncoming(baseline, original.Schema, desired, canonical.References[ref], parent, child, probe.Proof)
					}
					return schemaddl.VerifyOpenGaussIncomingTransition(probe.Proof, original.Schema, desired, parent, child)
				}
				if err = verify(actual.Schema.OpenGauss, children[oid]); err != nil {
					t.Fatal("actual native incoming child transition", err)
				}
				if operation == "metadata" || operation == "type" || operation == "cycle_both_keys" || operation == "cycle_drop_outgoing" {
					gaussDesignCheckIncomingUnknownRecords(t, actual.Schema.OpenGauss, children[oid], verify)
				}
			}
			if err = rollbackDesign(c); err != nil {
				t.Fatal(err)
			}
			type childValues struct {
				object contract.ObjectRef
				names  []string
				digest postgresDesignDigest
			}
			var retainedChildren []childValues
			for _, evidence := range canonical.Incoming {
				catalog := evidence.Baseline.Catalog()
				value := childValues{object: target.Object}
				value.object.Schema, value.object.Name = catalog.Namespace, catalog.Name
				for _, entry := range catalog.Entries {
					if entry.Kind != "column" || entry.OID != catalog.RelationOID {
						continue
					}
					var column struct {
						Name    string `json:"attname"`
						Dropped bool   `json:"attisdropped"`
					}
					if json.Unmarshal(entry.Data, &column) != nil {
						t.Fatal("invalid child column for full-value verification")
					}
					if !column.Dropped {
						value.names = append(value.names, quote(column.Name))
					}
				}
				value.digest, err = openGaussBinaryDigest(ctx, c, value.object, value.names)
				if err != nil {
					t.Fatal("capture complete retained child values", err)
				}
				retainedChildren = append(retainedChildren, value)
			}
			input := schemaddl.Signature(request)
			out := s.openGaussDesignApply(ctx, request)
			if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
				t.Fatal("private incoming transaction did not commit with full proofs", out)
			}
			if schemaddl.Signature(request) != input {
				t.Fatal("private incoming apply mutated captured request")
			}
			for _, value := range retainedChildren {
				got, err := openGaussBinaryDigest(ctx, c, value.object, value.names)
				if err != nil || got != value.digest {
					t.Fatal("private incoming apply changed complete typed child values", err)
				}
			}
			var count int
			if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(actualTarget.Object)).Scan(&count); err != nil || count != 1 {
				t.Fatal("private incoming apply changed retained parent rows", count, err)
			}
			if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+child).Scan(&count); err != nil || count != 1 {
				t.Fatal("private incoming apply changed child rows", count, err)
			}
			if operation == "child_defaults" {
				var called bool
				if err = db.QueryRowContext(ctx, "SELECT is_called FROM "+quote(target.Object.Schema)+".incoming_side_effect").Scan(&called); err != nil || called {
					t.Fatal("private incoming apply evaluated child default", called, err)
				}
			}
			if ops := s.SchemaDesign(); ops.Apply != nil || ops.CanApply != nil && ops.CanApply(target) {
				t.Fatal("parent graph evidence opened public Apply")
			}
		})
	}
}

func gaussDesignCheckIncomingUnknownRecords(t *testing.T, parent, child *schemaddl.OpenGaussCatalog, verify func(*schemaddl.OpenGaussCatalog, *schemaddl.OpenGaussCatalog) error) {
	t.Helper()
	input := schemaddl.Signature(child)
	checked := map[string]bool{}
	for i, entry := range child.Entries {
		corrupt, corruptParent := child.Clone(), parent.Clone()
		var row map[string]json.RawMessage
		if json.Unmarshal(entry.Data, &row) != nil {
			t.Fatal("incoming child raw record")
		}
		row["unknown_incoming_transition_property"] = json.RawMessage(`90071992547409931234567890.12345678901234567890`)
		corrupt.Entries[i].Data, _ = json.Marshal(row)
		if entry.Kind == "constraint" {
			// Coordinate parent/child records so their independent join still
			// matches; expected effect comparison must reject the new field.
			for n, parentEntry := range corruptParent.Entries {
				if parentEntry.Kind == "incoming_constraint" && parentEntry.OID == entry.OID {
					corruptParent.Entries[n].Data = append(json.RawMessage{}, corrupt.Entries[i].Data...)
				}
			}
		}
		if _, err := corrupt.Fingerprint(); err != nil {
			t.Fatal(err)
		}
		if err := verify(corruptParent, corrupt); err == nil {
			t.Fatal("unknown child record hidden by incoming transition", entry.Kind)
		}
		checked[entry.Kind] = true
		if entry.Kind == "dependencies" {
			delete(row, "unknown_incoming_transition_property")
			var edges []map[string]json.RawMessage
			if json.Unmarshal(row["rows"], &edges) != nil {
				t.Fatal("incoming dependencies")
			}
			for n := range edges {
				edges[n]["unknown_incoming_edge_property"] = json.RawMessage(`90071992547409931234567891`)
			}
			row["rows"], _ = json.Marshal(edges)
			corrupt.Entries[i].Data, _ = json.Marshal(row)
			if _, err := corrupt.Fingerprint(); err != nil {
				t.Fatal(err)
			}
			if err := verify(parent, corrupt); err == nil {
				t.Fatal("unknown dependency field hidden by independent FK rebind")
			}
		}
	}
	for _, kind := range []string{"relation", "column", "constraint", "trigger", "dependencies", "comments"} {
		if !checked[kind] {
			t.Fatal("incoming adversarial fixture misses raw catalog kind", kind)
		}
	}
	if schemaddl.Signature(child) != input {
		t.Fatal("incoming verification mutated captured child")
	}
}
