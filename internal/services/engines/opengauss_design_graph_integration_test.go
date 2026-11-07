package engines

import (
	"context"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignGraphRenamesDropsAndNewStorageFixture(t *testing.T) {
	for _, operation := range []string{"rename_table", "rename_column", "drop_column", "add_toast", "indexed_type"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if operation == "add_toast" {
				if _, err := db.ExecContext(ctx, "DROP TABLE "+qualified(target.Object)+"; CREATE TABLE "+qualified(target.Object)+"(id bigint); INSERT INTO "+qualified(target.Object)+" VALUES(1),(1),(NULL)"); err != nil {
					t.Fatal(err)
				}
			}
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			switch operation {
			case "rename_table":
				desired.Name = "renamed"
			case "rename_column":
				for i, col := range desired.Clauses {
					if col.Kind == "column" && col.Name == "id" {
						desired.Clauses[i].Name = "next_id"
					}
				}
			case "drop_column":
				var fields []schemaddl.Clause
				for _, col := range desired.Clauses {
					if col.Kind != "column" || col.Name != "note" {
						fields = append(fields, col)
					}
				}
				desired.Clauses = fields
			case "add_toast":
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:note", Kind: "column", Name: "note", SQL: "text DEFAULT '完整值'"})
			case "indexed_type":
				for i, col := range desired.Clauses {
					if col.Kind == "column" && col.Name == "id" {
						field, err := schemaddl.ParseOpenGaussField(col.SQL)
						if err != nil {
							t.Fatal(err)
						}
						field.Type = "numeric(30,0)"
						desired.Clauses[i].SQL, err = field.SQL()
						if err != nil {
							t.Fatal(err)
						}
					}
				}
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
			baseline, err := openGaussCanonicalDesign(ctx, c, contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true})
			if err != nil {
				t.Fatal("native independent declaration", err)
			}
			for _, q := range plan.SQL {
				if _, err = c.ExecContext(ctx, q); err != nil {
					t.Fatal("native plan", q, err)
				}
			}
			actualTarget := target
			actualTarget.Object.Name = desired.Name
			actual, err := openGaussReadDesign(ctx, c, actualTarget)
			if err != nil {
				t.Fatal(err)
			}
			if err = openGaussVerifyDesignCatalog(original, desired, baseline, actual); err != nil {
				t.Fatal("native full catalog", err)
			}
			gaussDesignRejectCatalogCorruption(t, original, desired, baseline, actual)
		})
	}
}
