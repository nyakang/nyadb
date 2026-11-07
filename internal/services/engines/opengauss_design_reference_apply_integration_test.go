package engines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignApplyExternalReferencesPreserveParentAndBindNativeActionsFixture(t *testing.T) {
	for _, operation := range []string{"simple", "mixed_integer", "mixed_numeric", "composite_reordered", "retained", "change_key_type", "removed", "replace_action", "invalid_value"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			parent := quote(target.Object.Schema) + ".other"
			setup := "ALTER TABLE " + parent + " ADD PRIMARY KEY(id); INSERT INTO " + parent + " VALUES(1)"
			if operation == "composite_reordered" {
				setup = "DROP TABLE " + parent + "; CREATE TABLE " + parent + "(a bigint,b text,PRIMARY KEY(a,b)); INSERT INTO " + parent + " VALUES(1,'key')"
			}
			if operation == "retained" || operation == "change_key_type" || operation == "removed" || operation == "replace_action" {
				setup += "; ALTER TABLE " + qualified(target.Object) + " ADD COLUMN parent_id bigint; ALTER TABLE " + qualified(target.Object) + " ADD CONSTRAINT parent_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id); UPDATE " + qualified(target.Object) + " SET parent_id=1"
			}
			if _, err := db.ExecContext(ctx, setup); err != nil {
				t.Fatal(err)
			}
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			comment := "parent-locked design"
			desired.OpenGauss.Comment = &comment
			switch operation {
			case "simple", "mixed_integer", "mixed_numeric", "invalid_value":
				declaration := "bigint DEFAULT 1"
				if operation == "mixed_integer" {
					declaration = "integer DEFAULT 1"
				} else if operation == "mixed_numeric" {
					declaration = "numeric(20,0) DEFAULT 1"
				}
				if operation == "invalid_value" {
					declaration = "bigint DEFAULT 999"
				}
				desired.Clauses = append(desired.Clauses,
					schemaddl.Clause{ID: "new:parent", Kind: "column", Name: "parent_id", SQL: declaration},
					schemaddl.Clause{ID: "new:fk", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id)"},
				)
			case "composite_reordered":
				desired.Clauses = append(desired.Clauses,
					schemaddl.Clause{ID: "new:parent_b", Kind: "column", Name: "parent_b", SQL: "text DEFAULT 'key'"},
					schemaddl.Clause{ID: "new:parent_a", Kind: "column", Name: "parent_a", SQL: "bigint DEFAULT 1"},
					schemaddl.Clause{ID: "new:fk", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(parent_b,parent_a) REFERENCES " + parent + "(b,a) ON DELETE CASCADE ON UPDATE RESTRICT DEFERRABLE INITIALLY DEFERRED"},
				)
			case "removed":
				var kept []schemaddl.Clause
				for _, clause := range desired.Clauses {
					if clause.Name != "parent_fk" {
						kept = append(kept, clause)
					}
				}
				desired.Clauses = kept
			case "change_key_type":
				for i, clause := range desired.Clauses {
					if clause.Name == "parent_id" {
						desired.Clauses[i].SQL = "integer"
					}
				}
			case "replace_action":
				for i, clause := range desired.Clauses {
					if clause.Name == "parent_fk" {
						desired.Clauses[i].SQL += " ON DELETE CASCADE"
					}
				}
			}
			out := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired})
			if operation == "invalid_value" {
				if out.Error == nil || out.State != "rolled_back" || out.Verified || out.Actual != nil {
					t.Fatal("invalid FK values committed or rollback was unacknowledged", out)
				}
			} else if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
				t.Fatal("verified native external reference design", operation, out)
			}
			var count, anchors int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+parent).Scan(&count); err != nil || count != 1 {
				t.Fatal("reference design changed parent values", count, err)
			}
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname LIKE '__nyadb_gauss_ref_%'", target.Object.Schema).Scan(&anchors); err != nil || anchors != 0 {
				t.Fatal("reference locks leaked private relations", anchors, err)
			}
			if operation == "composite_reordered" || operation == "replace_action" {
				if _, err := db.ExecContext(ctx, "DELETE FROM "+parent); err != nil {
					t.Fatal("native cascade action", err)
				}
				if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(target.Object)).Scan(&count); err != nil || count != 0 {
					t.Fatal("native FK action was not bound to original child", count, err)
				}
			}
		})
	}
}

func TestOpenGaussDesignReferenceLocksRejectParentChangeWhileWaitingFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	parent := quote(target.Object.Schema) + ".other"
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+parent+" ADD PRIMARY KEY(id)"); err != nil {
		t.Fatal(err)
	}
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses,
		schemaddl.Clause{ID: "new:parent", Kind: "column", Name: "parent_id", SQL: "bigint"},
		schemaddl.Clause{ID: "new:fk", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id)"},
	)
	parentTarget := target
	parentTarget.Object.Name = "other"
	locker := openGaussTableLocker(t, db, parentTarget)
	done := make(chan contract.SchemaDesignOutcome, 1)
	go func() {
		done <- s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired})
	}()
	openGaussViewWaiting(t, db, parentTarget.Object)
	if _, err := locker.ExecContext(ctx, "COMMENT ON TABLE "+parent+" IS 'concurrent parent change'; COMMIT"); err != nil {
		t.Fatal(err)
	}
	out := openGaussTableAwait(t, done)
	if out.State != "rolled_back" || !errors.Is(out.Error, schemaddl.ErrConflict) || out.Verified || out.Actual != nil {
		t.Fatal("parent change while locking bypassed independent baseline", out)
	}
	var columns int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_attribute WHERE attrelid=$1::regclass AND attname='parent_id' AND NOT attisdropped", qualified(target.Object)).Scan(&columns); err != nil || columns != 0 {
		t.Fatal("conflicting parent changed child before verification", columns, err)
	}
	if ops := s.SchemaDesign(); ops.Apply != nil || ops.CanApply != nil && ops.CanApply(target) {
		t.Fatal("private reference executor prematurely exposed public Apply")
	}
}

func TestOpenGaussDesignApplyExternalReferenceWithOnlyReferencesPrivilegeFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	role := fmt.Sprintf("nyadb_apply_ref_%d", time.Now().UnixNano())
	password := os.Getenv("NYADB_OPENGAUSS_PASSWORD")
	if password == "" || os.Getenv("NYADB_OPENGAUSS_CONTAINER") == "" {
		t.Fatal("restricted Apply requires the explicit disposable native fixture")
	}
	gaussDesignFixtureRoleSQL(t, "CREATE ROLE "+quote(role)+" WITH LOGIN PASSWORD '"+strings.ReplaceAll(password, "'", "''")+"';")
	t.Cleanup(func() { gaussDesignFixtureRoleSQL(t, "DROP OWNED BY "+quote(role)+"; DROP ROLE "+quote(role)+";") })
	parent := quote(target.Object.Schema) + ".other"
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+parent+" ADD PRIMARY KEY(id); INSERT INTO "+parent+" VALUES(1); CREATE SEQUENCE "+quote(target.Object.Schema)+".private_parent_seq OWNED BY "+parent+".id; ALTER TABLE "+parent+" ALTER COLUMN id SET DEFAULT nextval('"+quote(target.Object.Schema)+".private_parent_seq'::regclass); GRANT USAGE,CREATE ON SCHEMA "+quote(target.Object.Schema)+" TO "+quote(role)+"; GRANT REFERENCES(id) ON "+parent+" TO "+quote(role)); err != nil {
		t.Fatal(err)
	}
	profile := s.backupProfile
	profile.Username = role
	opened, err := OpenSession(ctx, profile, password)
	if err != nil {
		t.Fatal(err)
	}
	restricted := opened.(*Session)
	defer restricted.Close()
	restrictedTarget := contract.SchemaDesignTarget{Object: restricted.Namespace(target.Object.Schema).Object("table", "owned_child"), Generation: restricted.Generation()}
	if _, err = restricted.db.ExecContext(ctx, "CREATE TABLE "+qualified(restrictedTarget.Object)+"(id bigint PRIMARY KEY); INSERT INTO "+qualified(restrictedTarget.Object)+" VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	var selectAllowed bool
	if err = restricted.db.QueryRowContext(ctx, "SELECT has_table_privilege($1::regclass,'SELECT')", parent).Scan(&selectAllowed); err != nil || selectAllowed {
		t.Fatal("restricted reference actor acquired parent SELECT", selectAllowed, err)
	}
	original := gaussDesignOriginal(t, restricted, restrictedTarget)
	desired := original.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses,
		schemaddl.Clause{ID: "new:parent", Kind: "column", Name: "parent_id", SQL: "bigint DEFAULT 1"},
		schemaddl.Clause{ID: "new:fk", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id)"},
	)
	out := restricted.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired})
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
		t.Fatal("REFERENCES-only native Apply", out)
	}
	var id int
	if err = restricted.db.QueryRowContext(ctx, "SELECT parent_id FROM "+qualified(restrictedTarget.Object)).Scan(&id); err != nil || id != 1 {
		t.Fatal("native restricted FK validation/values", id, err)
	}
}

func TestOpenGaussDesignReferenceLocksSurviveCanonicalRollbackUntilTransactionAcknowledgementFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	parent := quote(target.Object.Schema) + ".other"
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+parent+" ADD PRIMARY KEY(id)"); err != nil {
		t.Fatal(err)
	}
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses,
		schemaddl.Clause{ID: "new:parent", Kind: "column", Name: "parent_id", SQL: "bigint"},
		schemaddl.Clause{ID: "new:fk", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id)"},
	)
	r := contract.SchemaDesignRequest{Original: original, Desired: desired}
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	references, err := openGaussLockDesignReferences(ctx, c, r)
	if err != nil || len(references) != 1 {
		t.Fatal("native transaction-held reference locks", err)
	}
	canonical, err := openGaussCanonicalDesign(ctx, c, r)
	if err != nil {
		t.Fatal("canonical savepoint under independent native reference locks", err)
	}
	if err = openGaussVerifyDesignReferences(ctx, c, r, references, canonical, nil); err != nil {
		t.Fatal("canonical rollback lost reference lock or parent context", err)
	}
	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = writer.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(writer)
	done := make(chan error, 1)
	go func() {
		_, err := writer.ExecContext(ctx, "ALTER TABLE "+parent+" ADD COLUMN after_release bigint; COMMIT")
		done <- err
	}()
	parentObject := target.Object
	parentObject.Name = "other"
	openGaussViewWaiting(t, db, parentObject)
	select {
	case err := <-done:
		t.Fatal("reference writer completed before transaction acknowledgement", err)
	default:
	}
	if err = rollbackDesign(c); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("reference writer after transaction acknowledgement", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestOpenGaussDesignReferenceTransitionRejectsUnknownParentPropertiesAndEffectsFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	parent := quote(target.Object.Schema) + ".other"
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+parent+" ADD PRIMARY KEY(id); INSERT INTO "+parent+" VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses,
		schemaddl.Clause{ID: "new:parent", Kind: "column", Name: "parent_id", SQL: "bigint DEFAULT 1"},
		schemaddl.Clause{ID: "new:fk", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id)"},
	)
	r := contract.SchemaDesignRequest{Original: original, Desired: desired}
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on; LOCK TABLE "+qualified(target.Object)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	references, err := openGaussLockDesignReferences(ctx, c, r)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := openGaussCanonicalDesign(ctx, c, r)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := schemaddl.PlanOpenGauss(original.Schema, desired, target.Object.Schema, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range plan.SQL {
		if _, err = c.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	actual, err := openGaussReadDesign(ctx, c, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = openGaussVerifyDesignReferences(ctx, c, r, references, canonical, &actual); err != nil {
		t.Fatal("independent actual reference transition", err)
	}
	parentTarget := target
	parentTarget.Object.Name = "other"
	after, err := openGaussReadDesignReference(ctx, c, parentTarget)
	if err != nil {
		t.Fatal(err)
	}
	ref := schemaddl.PostgreSQLTableReference{Schema: target.Object.Schema, Name: "other"}
	baseline, err := schemaddl.CaptureOpenGaussReferenceBaseline(original.Schema, desired, references[ref])
	if err != nil {
		t.Fatal(err)
	}
	beforeSignature := schemaddl.Signature(after)
	checked := map[string]bool{}
	for i, entry := range after.Entries {
		corrupt := after.Clone()
		var raw map[string]json.RawMessage
		if json.Unmarshal(entry.Data, &raw) != nil {
			t.Fatal("native parent record")
		}
		raw["unknown_reference_transition_property"] = json.RawMessage(`90071992547409931234567890.12345678901234567890`)
		corrupt.Entries[i].Data, _ = json.Marshal(raw)
		if _, err = corrupt.Fingerprint(); err != nil {
			t.Fatal(err)
		}
		if err = schemaddl.VerifyOpenGaussReferenceTransition(baseline, original.Schema, desired, canonical.References[ref], actual.Schema.OpenGauss, corrupt); err == nil {
			t.Fatal("unknown parent property hidden by FK transition", entry.Kind)
		}
		checked[entry.Kind] = true
		if entry.Kind != "dependencies" {
			continue
		}
		var edges []map[string]json.RawMessage
		if json.Unmarshal(raw["rows"], &edges) != nil {
			t.Fatal("native parent dependencies")
		}
		delete(raw, "unknown_reference_transition_property")
		for n := range edges {
			edges[n]["unknown_edge_property"] = json.RawMessage(`90071992547409931234567891`)
		}
		raw["rows"], _ = json.Marshal(edges)
		corrupt.Entries[i].Data, _ = json.Marshal(raw)
		if _, err = corrupt.Fingerprint(); err != nil {
			t.Fatal(err)
		}
		if err = schemaddl.VerifyOpenGaussReferenceTransition(baseline, original.Schema, desired, canonical.References[ref], actual.Schema.OpenGauss, corrupt); err == nil {
			t.Fatal("unknown dependency properties hidden by selected FK effects")
		}
	}
	for _, kind := range []string{"relation", "incoming_constraint", "trigger", "dependencies"} {
		if !checked[kind] {
			t.Fatal("native adversarial reference coverage missing", kind)
		}
	}
	foreign := desired.Clone()
	foreign.Name = "different_request"
	if !errors.Is(schemaddl.VerifyOpenGaussReferenceTransition(baseline, original.Schema, foreign, canonical.References[ref], actual.Schema.OpenGauss, after), schemaddl.ErrConflict) || beforeSignature != schemaddl.Signature(after) {
		t.Fatal("reference transition reused request or aliased original metadata")
	}
}

func TestOpenGaussDesignReferenceLocksCancellationAndRevocationAwaitRollbackFixture(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			setup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			parent := quote(target.Object.Schema) + ".other"
			_, err := db.ExecContext(setup, "ALTER TABLE "+parent+" ADD PRIMARY KEY(id)")
			stop()
			if err != nil {
				t.Fatal(err)
			}
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			desired.Clauses = append(desired.Clauses,
				schemaddl.Clause{ID: "new:parent", Kind: "column", Name: "parent_id", SQL: "bigint"},
				schemaddl.Clause{ID: "new:fk", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id)"},
			)
			parentTarget := target
			parentTarget.Object.Name = "other"
			locker := openGaussTableLocker(t, db, parentTarget)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan contract.SchemaDesignOutcome, 1)
			go func() {
				done <- s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired})
			}()
			openGaussViewWaiting(t, db, parentTarget.Object)
			if revoke {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			out := openGaussTableAwait(t, done)
			if out.State != "rolled_back" && out.State != "uncertain" || out.Error == nil || out.Verified || out.Actual != nil || !revoke && !errors.Is(out.Error, context.Canceled) {
				t.Fatal("reference-lock cancellation returned before settlement", out)
			}
			if out.State == "uncertain" && !s.isClosed() {
				t.Fatal("uncertain reference-lock transaction kept its session")
			}
			if err := rollbackDesign(locker); err != nil {
				t.Fatal(err)
			}
			verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer verifyCancel()
			var anchors, added int
			if err := db.QueryRowContext(verifyCtx, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname LIKE '__nyadb_gauss_ref_%'", target.Object.Schema).Scan(&anchors); err != nil || anchors != 0 {
				t.Fatal("cancelled reference lock left native anchors", anchors, err)
			}
			if err := db.QueryRowContext(verifyCtx, "SELECT count(*) FROM pg_attribute WHERE attrelid=$1::oid AND attname='parent_id' AND NOT attisdropped", original.Schema.OpenGauss.RelationOID).Scan(&added); err != nil || added != 0 {
				t.Fatal("cancelled reference lock changed original columns", added, err)
			}
		})
	}
}
