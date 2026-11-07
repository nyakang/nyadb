package engines

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignValueProofCompleteDefaultsSerialsAndGeneratedFixture(t *testing.T) {
	for _, storage := range []string{"row", "column", "partition"} {
		t.Run(storage, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			name := qualified(target.Object)
			if storage != "row" {
				q := "DROP TABLE " + name + "; CREATE TABLE " + name + "(id bigint,note text,amount numeric(40,20))"
				if storage == "column" {
					q += " WITH(orientation=column,compression=middle)"
				} else {
					q += " PARTITION BY RANGE(id)(PARTITION p1 VALUES LESS THAN(10),PARTITION p2 VALUES LESS THAN(MAXVALUE))"
				}
				if _, err := db.ExecContext(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, "ALTER TABLE "+name+" ADD COLUMN payload bytea"); err != nil {
				t.Fatal(err)
			}
			for _, row := range []struct {
				id      int
				note    string
				amount  any
				payload []byte
			}{
				{2, strings.Repeat("完整值", 1001), "9007199254740993.12345678901234567890", bytes.Repeat([]byte{0, 255}, 2048)},
				{11, "second", nil, nil},
				{12, "", "2.00000000000000000000", []byte{}},
			} {
				if _, err := db.ExecContext(ctx, "INSERT INTO "+name+"(id,note,amount,payload) VALUES($1,$2,$3::numeric,$4)", row.id, row.note, row.amount, row.payload); err != nil {
					t.Fatal(err)
				}
			}
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:constant", Kind: "column", Name: "constant", SQL: "numeric(40,20) DEFAULT 9007199254740993.12345678901234567890"})
			if storage == "row" {
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:serial", Kind: "column", Name: "new_id", SQL: "bigserial"}, schemaddl.Clause{ID: "new:generated", Kind: "column", Name: "new_generated", SQL: "numeric(40,20) GENERATED ALWAYS AS(amount*3+new_id) STORED"})
			}
			plan, err := schemaddl.PlanOpenGauss(original.Schema, desired, target.Object.Schema, true)
			if err != nil {
				t.Fatal(err)
			}
			c, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; LOCK TABLE "+name+" IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			defer rollbackDesign(c)
			r := contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true}
			baseline, err := openGaussCanonicalDesign(ctx, c, r)
			if err != nil {
				t.Fatal(err)
			}
			definition, err := original.Schema.OpenGauss.CopyDefinition()
			if err != nil {
				t.Fatal(err)
			}
			states, err := openGaussCopySequenceStates(ctx, c, definition)
			if err != nil {
				t.Fatal(err)
			}
			before, err := openGaussRetainedDesignDigest(ctx, c, target.Object, original.Schema.Table, desired, false)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := openGaussCaptureValueProof(ctx, c, r, baseline, before.Rows)
			if err != nil {
				t.Fatal("independent full values", err)
			}
			unchanged, err := openGaussCopySequenceStates(ctx, c, definition)
			if err != nil || schemaddl.Signature(states) != schemaddl.Signature(unchanged) {
				t.Fatal("projection consumed an original sequence", err)
			}
			for _, q := range plan.SQL {
				if _, err = c.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			actual, err := openGaussReadDesign(ctx, c, target)
			if err != nil {
				t.Fatal(err)
			}
			if err = openGaussVerifyDesignCatalog(original, desired, baseline, actual); err != nil {
				t.Fatal("full native catalog", err)
			}
			after, err := openGaussRetainedDesignDigest(ctx, c, target.Object, original.Schema.Table, desired, true)
			if err != nil || before != after {
				t.Fatal("retained original values", err)
			}
			if err = openGaussVerifyValueProof(ctx, c, r, baseline, actual, proof); err != nil {
				t.Fatal("full after values", err)
			}
			if _, err = c.ExecContext(ctx, "SAVEPOINT nyadb_value_fault; UPDATE "+name+" SET constant=0 WHERE id=2"); err != nil {
				t.Fatal(err)
			}
			if err = openGaussVerifyValueProof(ctx, c, r, baseline, actual, proof); !errors.Is(err, schemaddl.ErrDataChanged) {
				t.Fatal("wrong new values accepted", err)
			}
			if _, err = c.ExecContext(ctx, "ROLLBACK TO SAVEPOINT nyadb_value_fault; RELEASE SAVEPOINT nyadb_value_fault"); err != nil {
				t.Fatal(err)
			}
			if storage == "row" {
				if _, err = c.ExecContext(ctx, "SAVEPOINT nyadb_value_fault; ALTER TABLE "+name+" DROP COLUMN new_generated RESTRICT; ALTER TABLE "+name+" ADD COLUMN new_generated numeric(40,20); UPDATE "+name+" SET new_generated=0"); err != nil {
					t.Fatal(err)
				}
				if err = openGaussVerifyValueProof(ctx, c, r, baseline, actual, proof); !errors.Is(err, schemaddl.ErrDataChanged) {
					t.Fatal("unproved generated values accepted", err)
				}
				if _, err = c.ExecContext(ctx, "ROLLBACK TO SAVEPOINT nyadb_value_fault; RELEASE SAVEPOINT nyadb_value_fault"); err != nil {
					t.Fatal(err)
				}
			}
			unchanged, err = openGaussCopySequenceStates(ctx, c, definition)
			if err != nil || schemaddl.Signature(states) != schemaddl.Signature(unchanged) {
				t.Fatal("verification consumed an original sequence", err)
			}
			var leaked int
			if err = c.QueryRowContext(ctx, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND (c.relname LIKE '__nyadb_gauss_values_%' OR c.relname LIKE '__nyadb_gauss_design_%')", target.Object.Schema).Scan(&leaked); err != nil || leaked != 0 {
				t.Fatal("private projection survived rollback", leaked, err)
			}
		})
	}
}

func TestOpenGaussDesignValueProofNoRetainedInputsAndRejectsVolatileDefaultFixture(t *testing.T) {
	for _, operation := range []string{"replace_all", "volatile"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			if operation == "replace_all" {
				desired.Clauses = []schemaddl.Clause{{ID: "new:serial", Kind: "column", Name: "id", SQL: "bigserial"}, {ID: "new:generated", Kind: "column", Name: "next_id", SQL: "bigint GENERATED ALWAYS AS(id+1) STORED"}}
				desired.Definitions = nil
			} else {
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:random", Kind: "column", Name: "random_value", SQL: "double precision DEFAULT random()"})
			}
			plan, err := schemaddl.PlanOpenGauss(original.Schema, desired, target.Object.Schema, true)
			if err != nil {
				t.Fatal(err)
			}
			c, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog"); err != nil {
				t.Fatal(err)
			}
			defer rollbackDesign(c)
			r := contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true}
			baseline, err := openGaussCanonicalDesign(ctx, c, r)
			if err != nil {
				t.Fatal(err)
			}
			before, err := openGaussRetainedDesignDigest(ctx, c, target.Object, original.Schema.Table, desired, false)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := openGaussCaptureValueProof(ctx, c, r, baseline, before.Rows)
			if operation == "volatile" {
				if !errors.Is(err, errOpenGaussValueRepeatability) {
					t.Fatal("volatile default acquired a repeatable proof", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range plan.SQL {
				if _, err = c.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			actual, err := openGaussReadDesign(ctx, c, target)
			if err != nil {
				t.Fatal(err)
			}
			if err = openGaussVerifyValueProof(ctx, c, r, baseline, actual, proof); err != nil {
				t.Fatal(err)
			}
		})
	}
}
