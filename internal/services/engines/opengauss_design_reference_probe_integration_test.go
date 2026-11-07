package engines

import (
	"context"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignCanonicalReferencesCaptureAndRollbackNativeParentEffectsFixture(t *testing.T) {
	for _, operation := range []string{"simple", "composite_reordered"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			parent := quote(target.Object.Schema) + ".other"
			setup := "ALTER TABLE " + parent + " ADD PRIMARY KEY(id)"
			if operation == "composite_reordered" {
				setup = "DROP TABLE " + parent + "; CREATE TABLE " + parent + "(a bigint,b text,PRIMARY KEY(a,b))"
			}
			if _, err := db.ExecContext(ctx, setup); err != nil {
				t.Fatal(err)
			}
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			if operation == "simple" {
				desired.Clauses = append(desired.Clauses,
					schemaddl.Clause{ID: "new:parent", Kind: "column", Name: "parent_id", SQL: "bigint"},
					schemaddl.Clause{ID: "new:fk", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id)"},
				)
			} else {
				desired.Clauses = append(desired.Clauses,
					schemaddl.Clause{ID: "new:parent_b", Kind: "column", Name: "parent_b", SQL: "text"},
					schemaddl.Clause{ID: "new:parent_a", Kind: "column", Name: "parent_a", SQL: "bigint"},
					schemaddl.Clause{ID: "new:fk", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(parent_b,parent_a) REFERENCES " + parent + "(b,a) ON DELETE CASCADE ON UPDATE RESTRICT DEFERRABLE INITIALLY DEFERRED"},
				)
			}
			c, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
				t.Fatal(err)
			}
			defer rollbackDesign(c)
			parentTarget := target
			parentTarget.Object.Name = "other"
			before, err := openGaussReadDesignReference(ctx, c, parentTarget)
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := openGaussCanonicalDesign(ctx, c, contract.SchemaDesignRequest{Original: original, Desired: desired})
			if err != nil || len(baseline.References) != 1 {
				t.Fatal("native canonical reference proof", err)
			}
			after, err := openGaussReadDesignReference(ctx, c, parentTarget)
			beforeRevision, e1 := before.Fingerprint()
			afterRevision, e2 := after.Fingerprint()
			if err != nil || e1 != nil || e2 != nil || beforeRevision != afterRevision {
				t.Fatal("independent FK left parent triggers, dependencies or identity changes", err, e1, e2)
			}
			ref := schemaddl.PostgreSQLTableReference{Schema: target.Object.Schema, Name: "other"}
			proof, ok := baseline.References[ref]
			if !ok || schemaddl.VerifyOpenGaussReferenceProbeReleased(proof, original.Schema, desired, after, "rollback") != nil {
				t.Fatal("canonical parent proof did not match acknowledged rollback")
			}
			var last int
			if err = c.QueryRowContext(ctx, "SELECT last_value FROM "+quote(target.Object.Schema)+".original_id_seq").Scan(&last); err != nil || last != 1 {
				t.Fatal("independent reference declaration consumed the original sequence", last, err)
			}
			if ops := s.SchemaDesign(); ops.Apply != nil || ops.CanApply != nil && ops.CanApply(target) {
				t.Fatal("prototype-only reference proof opened public Apply")
			}
		})
	}
}
