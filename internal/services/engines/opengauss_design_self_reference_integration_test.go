package engines

import (
	"context"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignSelfReferenceNativeTransitionsFixture(t *testing.T) {
	for _, operation := range []string{"add", "retained", "remove", "change_child_type", "change_parent_type", "rename_table", "rename_child", "rename_parent", "dropped_slot", "parent_slot_hole", "composite_reordered", "create", "invalid_value"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			name := qualified(target.Object)
			if operation == "parent_slot_hole" {
				if _, err := db.ExecContext(ctx, "DROP TABLE "+name+"; CREATE TABLE "+name+"(id bigint PRIMARY KEY,gap bigint,key bigint UNIQUE,parent_id bigint,CONSTRAINT self_fk FOREIGN KEY(parent_id) REFERENCES "+name+"(key)); ALTER TABLE "+name+" DROP COLUMN gap; INSERT INTO "+name+" VALUES(1,7,7)"); err != nil {
					t.Fatal(err)
				}
			} else if operation == "composite_reordered" {
				if _, err := db.ExecContext(ctx, "DROP TABLE "+name+"; CREATE TABLE "+name+"(id bigint PRIMARY KEY,a text,b bigint,f_b bigint,f_a text,UNIQUE(a,b)); INSERT INTO "+name+" VALUES(1,'key',7,7,'key')"); err != nil {
					t.Fatal(err)
				}
			} else if operation != "add" && operation != "dropped_slot" && operation != "create" && operation != "invalid_value" {
				if _, err := db.ExecContext(ctx, "ALTER TABLE "+name+" ADD COLUMN parent_id bigint; ALTER TABLE "+name+" ADD CONSTRAINT self_fk FOREIGN KEY(parent_id) REFERENCES "+name+"(id); UPDATE "+name+" SET parent_id=1"); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "dropped_slot" {
				if _, err := db.ExecContext(ctx, "ALTER TABLE "+name+" ADD COLUMN gap bigint; ALTER TABLE "+name+" DROP COLUMN gap"); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "create" {
				target.Object.Name = "created"
				name = qualified(target.Object)
			}
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			switch operation {
			case "add", "dropped_slot", "invalid_value":
				declaration := "bigint DEFAULT 1"
				if operation == "invalid_value" {
					declaration = "bigint DEFAULT 999"
				}
				desired.Clauses = append(desired.Clauses,
					schemaddl.Clause{ID: "new:parent", Kind: "column", Name: "parent_id", SQL: declaration},
					schemaddl.Clause{ID: "new:self", Kind: "constraint", Name: "self_fk", SQL: "CONSTRAINT self_fk FOREIGN KEY(parent_id) REFERENCES " + name + "(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED"},
				)
			case "remove":
				var kept []schemaddl.Clause
				for _, clause := range desired.Clauses {
					if clause.Name != "self_fk" {
						kept = append(kept, clause)
					}
				}
				desired.Clauses = kept
			case "change_child_type", "change_parent_type":
				column := "parent_id"
				if operation == "change_parent_type" {
					column = "id"
				}
				for i, clause := range desired.Clauses {
					if clause.Name == column {
						field, err := schemaddl.ParseOpenGaussField(clause.SQL)
						if err != nil {
							t.Fatal(err)
						}
						field.Type = "integer"
						desired.Clauses[i].SQL, err = field.SQL()
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			case "rename_table":
				desired.Name = "renamed"
			case "rename_child", "rename_parent":
				column := "parent_id"
				if operation == "rename_parent" {
					column = "id"
				}
				for i, clause := range desired.Clauses {
					if clause.Name == column {
						desired.Clauses[i].Name = "renamed_field"
					}
				}
			case "composite_reordered":
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:self", Kind: "constraint", Name: "self_fk", SQL: "CONSTRAINT self_fk FOREIGN KEY(f_b,f_a) REFERENCES " + name + "(b,a) ON DELETE RESTRICT ON UPDATE CASCADE DEFERRABLE INITIALLY DEFERRED"})
			case "create":
				desired.Clauses = []schemaddl.Clause{
					{ID: "new:id", Kind: "column", Name: "id", SQL: "bigint NOT NULL"},
					{ID: "new:parent", Kind: "column", Name: "parent_id", SQL: "bigint"},
					{ID: "new:pk", Kind: "constraint", Name: "new_pk", SQL: "CONSTRAINT new_pk PRIMARY KEY(id)"},
					{ID: "new:self", Kind: "constraint", Name: "self_fk", SQL: "CONSTRAINT self_fk FOREIGN KEY(parent_id) REFERENCES " + name + "(id)"},
				}
			}
			if operation == "retained" || operation == "parent_slot_hole" {
				comment := "native self-reference retained"
				desired.OpenGauss.Comment = &comment
			}
			before := schemaddl.Signature(original)
			out := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired})
			if operation == "invalid_value" {
				if out.State != "rolled_back" || out.Error == nil || out.Verified || out.Actual != nil {
					t.Fatal("invalid self-reference was not rolled back", out)
				}
			} else if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
				t.Fatal("native self-reference transition", operation, out)
			}
			if before != schemaddl.Signature(original) {
				t.Fatal("self-reference execution changed captured original")
			}
			result := target.Object
			result.Name = desired.Name
			var count, anchors int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(result)).Scan(&count); err != nil || count != map[bool]int{true: 0, false: 1}[operation == "create"] {
				t.Fatal("self-reference changed retained rows", count, err)
			}
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname LIKE '__nyadb_gauss_ref_%'", target.Object.Schema).Scan(&anchors); err != nil || anchors != 0 {
				t.Fatal("self-reference created external reference anchors", anchors, err)
			}
			if ops := s.SchemaDesign(); ops.Apply != nil || ops.CanApply != nil && ops.CanApply(target) {
				t.Fatal("private self-reference verification opened public Apply")
			}
		})
	}
}
