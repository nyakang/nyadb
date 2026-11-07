package engines

import (
	"context"
	"database/sql"
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

func gaussAccessMethodFixtureSavepoint(t *testing.T, ctx context.Context, c *sql.Conn, name string) func() {
	t.Helper()
	if _, err := c.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		t.Fatal(err)
	}
	reverted := false
	revert := func() {
		if reverted {
			return
		}
		reverted = true
		if _, err := c.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+name+"; RELEASE SAVEPOINT "+name); err != nil {
			t.Error("native fixture savepoint rollback", err)
		}
	}
	t.Cleanup(revert)
	return revert
}

func TestOpenGaussDesignAccessMethodReplacementsRejectedBeforeParsingFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	if os.Getenv("NYADB_OPENGAUSS_CONTAINER") == "" {
		t.Skip("requires the explicitly owned fixture for native dispatch replacements")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	target.Object.Name = "dispatch_values"
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = []schemaddl.Clause{{ID: "new:id", Kind: "column", Name: "id", SQL: "bigint"}, {ID: "new:label", Kind: "column", Name: "label", SQL: "text"}}
	desired.Definitions = []schemaddl.Definition{{Kind: "index", Name: "by_label", SQL: "CREATE INDEX by_label ON " + qualified(target.Object) + " USING btree(label)"}}
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	if err = openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); err != nil {
		t.Fatal("native btree dispatch", err)
	}
	for _, change := range []struct {
		name, table, property, value string
		oid                          uint64
	}{
		{"insert_binding", "pg_am", "aminsert", "441", 403},
		{"options_binding", "pg_am", "amoptions", "2786", 403},
		{"support_count", "pg_am", "amsupport", "1", 403},
		{"key_type", "pg_am", "amkeytype", "23", 403},
		{"implementation", "pg_proc", "prosrc", "hashinsert", 331},
		{"arguments", "pg_proc", "proargtypes", "25", 331},
		{"result", "pg_proc", "prorettype", "25", 331},
		{"strict", "pg_proc", "proisstrict", "false", 331},
		{"volatility", "pg_proc", "provolatile", "s", 331},
		{"language", "pg_proc", "prolang", "13", 331},
		{"library", "pg_proc", "probin", "external", 331},
		{"defaults", "pg_proc", "pronargdefaults", "1", 331},
		{"variadic", "pg_proc", "provariadic", "25", 331},
	} {
		t.Run(change.name, func(t *testing.T) {
			var old sql.NullString
			var before, after string
			row := " FROM " + change.table + " t WHERE oid=" + fmt.Sprint(change.oid)
			if err := c.QueryRowContext(ctx, "SELECT "+change.property+"::text,row_to_json(t)::text"+row).Scan(&old, &before); err != nil {
				t.Fatal("native dispatch before-image", err)
			}
			update := func(value sql.NullString) string {
				literal := "NULL"
				if value.Valid {
					literal = "'" + strings.ReplaceAll(value.String, "'", "''") + "'"
				}
				return fmt.Sprintf("UPDATE %s SET %s=%s WHERE oid=%d;", change.table, change.property, literal, change.oid)
			}
			t.Cleanup(func() { gaussDesignFixtureRoleSQL(t, update(old)) })
			gaussDesignFixtureRoleSQL(t, update(sql.NullString{String: change.value, Valid: true}))
			if err := openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); !errors.Is(err, errOpenGaussDesignDeclaration) {
				t.Fatal("changed native dispatch was authorized", err)
			}
			if _, err := openGaussCanonicalDesign(ctx, c, contract.SchemaDesignRequest{Original: original, Desired: desired}); !errors.Is(err, errOpenGaussDesignDeclaration) {
				t.Fatal("changed native dispatch reached independent CREATE", err)
			}
			gaussDesignFixtureRoleSQL(t, update(old))
			if err := c.QueryRowContext(ctx, "SELECT row_to_json(t)::text"+row).Scan(&after); err != nil || before != after {
				t.Fatal("native dispatch was not restored exactly", err)
			}
			if err := openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); err != nil {
				t.Fatal("restored native dispatch rejected", err)
			}
		})
	}
	baseline, err := openGaussCanonicalDesign(ctx, c, contract.SchemaDesignRequest{Original: original, Desired: desired})
	if err != nil || baseline.Catalog == nil {
		t.Fatal("restored independent index declaration", err)
	}
	current, err := openGaussReadDesign(ctx, c, target)
	if err != nil || current.Schema.Exists || current.Schema.Revision != original.Schema.Revision {
		t.Fatal("dispatch validation changed the original", err)
	}
	probe := target
	probe.Object.Name = baseline.Catalog.Name
	absent, err := openGaussReadDesign(ctx, c, probe)
	if err != nil || absent.Schema.Exists {
		t.Fatal("dispatch declaration survived rollback", err)
	}
}

func TestOpenGaussDesignAccessMethodNativeInventoryFixture(t *testing.T) {
	_, db, _ := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	var count int
	if err := c.QueryRowContext(ctx, "SELECT count(*) FROM pg_am").Scan(&count); err != nil || count != len(openGaussNativeAccessMethods) {
		t.Fatal("native access method inventory changed", count, err)
	}
	checked := map[uint64]bool{}
	for name, spec := range openGaussNativeAccessMethods {
		t.Run(name, func(t *testing.T) {
			var oid uint64
			var row, rawCallbacks string
			if err := c.QueryRowContext(ctx, openGaussAccessMethodCatalogSQL, name).Scan(&oid, &row, &rawCallbacks); err != nil {
				t.Fatal("native access method", err)
			}
			var callbacks []uint64
			if err := json.Unmarshal([]byte(rawCallbacks), &callbacks); err != nil {
				t.Fatal("native callback identities", err)
			}
			if err := openGaussCheckNativeAccessMethod(ctx, c, name, oid, row, callbacks); err != nil {
				t.Fatal("source and native dispatch disagree", err)
			}
			for _, id := range spec.Routines {
				if id != 0 {
					checked[id] = true
				}
			}
			var expanded map[string]any
			if err := json.Unmarshal([]byte(row), &expanded); err != nil {
				t.Fatal(err)
			}
			expanded["unknown_dispatch"] = 1
			unknown, err := json.Marshal(expanded)
			if err != nil {
				t.Fatal(err)
			}
			if err := openGaussCheckNativeAccessMethod(ctx, c, name, oid, string(unknown), spec.Routines[:]); !errors.Is(err, errOpenGaussDesignDeclaration) {
				t.Fatal("unknown native dispatch property was ignored", err)
			}
		})
	}
	if len(checked) != len(openGaussNativeAccessMethodRoutines) {
		t.Fatal("native method inventory did not verify every callback", len(checked))
	}
}

func TestOpenGaussDesignAccessMethodNativeIndexesCommitFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	target.Object.Name = "native_indexes"
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = []schemaddl.Clause{
		{ID: "new:id", Kind: "column", Name: "id", SQL: "bigint"},
		{ID: "new:label", Kind: "column", Name: "label", SQL: "text"},
		{ID: "new:tags", Kind: "column", Name: "tags", SQL: "integer[]"},
		{ID: "new:location", Kind: "column", Name: "location", SQL: "point"},
	}
	for _, index := range []struct{ method, field string }{{"btree", "label"}, {"hash", "id"}, {"gist", "location"}, {"gin", "tags"}} {
		name := "by_" + index.method
		desired.Definitions = append(desired.Definitions, schemaddl.Definition{Kind: "index", Name: name, SQL: "CREATE INDEX " + quote(name) + " ON " + qualified(target.Object) + " USING " + index.method + "(" + quote(index.field) + ")"})
	}
	out := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired})
	if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil || out.Rows != 0 {
		t.Fatal("native index declaration transaction", out)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+qualified(target.Object)+" VALUES(1,'中文',ARRAY[1,NULL,2],point(3,4))"); err != nil {
		t.Fatal("native index tuple insertion", err)
	}
	var methods, matched int
	if err := db.QueryRowContext(ctx, "SELECT count(DISTINCT a.amname) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_am a ON a.oid=c.relam WHERE i.indrelid=$1::oid AND i.indisvalid AND i.indisready AND i.indisusable", out.Actual.Schema.OpenGauss.RelationOID).Scan(&methods); err != nil || methods != 4 {
		t.Fatal("native index methods were lost", methods, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(target.Object)+" WHERE id=1 AND label='中文' AND tags @> ARRAY[1] AND location~=point(3,4)").Scan(&matched); err != nil || matched != 1 {
		t.Fatal("native indexed values changed", matched, err)
	}
}

func TestOpenGaussDesignAccessMethodStorageReplacementsBeforeParsingFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	if os.Getenv("NYADB_OPENGAUSS_CONTAINER") == "" {
		t.Skip("requires the explicitly owned fixture for native dispatch replacements")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	target.Object.Name = "storage_dispatch"
	original := gaussDesignOriginal(t, s, target)
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	for _, sample := range []struct {
		name, option, index, method, slot, replacement string
	}{
		{"column_default", "orientation=column", "(id)", "psort", "ambuild", "btbuild"},
		{"column_btree_alias", "orientation=column", "USING btree(id)", "cbtree", "ambuild", "btbuild"},
		{"column_unique_constraint", "orientation=column", "", "cbtree", "ambuild", "btbuild"},
		{"column_gin_alias", "orientation=column", "USING gin(tags)", "cgin", "ambuild", "ginbuild"},
		{"ustore_default", "storage_type=ustore", "(id)", "ubtree", "ambuild", "btbuild"},
		{"configured_ustore", "", "(id)", "ubtree", "ambuild", "btbuild"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			desired := original.Schema.Table.Clone()
			desired.Clauses = []schemaddl.Clause{{ID: "new:id", Kind: "column", Name: "id", SQL: "bigint"}, {ID: "new:tags", Kind: "column", Name: "tags", SQL: "integer[]"}}
			if sample.option != "" {
				desired.OpenGauss.Parameters = []string{sample.option}
			}
			if sample.index != "" {
				desired.Definitions = []schemaddl.Definition{{Kind: "index", Name: "by_id", SQL: "CREATE INDEX by_id ON " + qualified(target.Object) + " " + sample.index}}
			} else {
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:unique", Kind: "constraint", Name: "unique_id", SQL: "CONSTRAINT unique_id UNIQUE(id)"})
			}
			if sample.name == "configured_ustore" {
				if _, err := c.ExecContext(ctx, "SET LOCAL enable_default_ustore_table=on"); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := c.ExecContext(ctx, "SET LOCAL enable_default_ustore_table=off"); err != nil {
						t.Error(err)
					}
				}()
			}
			if err := openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); err != nil {
				t.Fatal("native storage dispatch", err)
			}
			var before, old, after string
			if err := c.QueryRowContext(ctx, "SELECT row_to_json(a)::text,"+sample.slot+"::oid::text FROM pg_am a WHERE amname=$1", sample.method).Scan(&before, &old); err != nil {
				t.Fatal(err)
			}
			update := func(value string) string {
				return "UPDATE pg_am SET " + sample.slot + "=" + value + " WHERE amname='" + sample.method + "';"
			}
			t.Cleanup(func() { gaussDesignFixtureRoleSQL(t, update(old)) })
			gaussDesignFixtureRoleSQL(t, update("'"+sample.replacement+"'::regproc::oid"))
			if err := openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); !errors.Is(err, errOpenGaussDesignDeclaration) {
				t.Fatal("changed native storage dispatch authorized", err)
			}
			if _, err := openGaussCanonicalDesign(ctx, c, contract.SchemaDesignRequest{Original: original, Desired: desired}); !errors.Is(err, errOpenGaussDesignDeclaration) {
				t.Fatal("changed native storage dispatch reached independent CREATE", err)
			}
			gaussDesignFixtureRoleSQL(t, update(old))
			if err := c.QueryRowContext(ctx, "SELECT row_to_json(a)::text FROM pg_am a WHERE amname=$1", sample.method).Scan(&after); err != nil || before != after {
				t.Fatal("native storage dispatch not restored exactly", err)
			}
			if err := openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); err != nil {
				t.Fatal("restored storage dispatch rejected", err)
			}
		})
	}
}

func TestOpenGaussDesignAccessMethodUnsupportedStorageRejectedFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	target.Object.Name = "unsupported_index_storage"
	original := gaussDesignOriginal(t, s, target)
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	for _, sample := range []struct{ name, option, method, message string }{
		{"heap_spgist", "storage_type=astore", "spgist", "does not support row store"},
		{"heap_ubtree", "storage_type=astore", "ubtree", "only supported for ustore"},
		{"column_hash", "orientation=column", "hash", "does not support column store"},
		{"ustore_btree", "storage_type=ustore", "btree", "not supported for ustore"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			desired := original.Schema.Table.Clone()
			desired.Clauses = []schemaddl.Clause{{ID: "new:id", Kind: "column", Name: "id", SQL: "bigint"}}
			desired.OpenGauss.Parameters = []string{sample.option}
			key := "id"
			columns := "id bigint"
			if sample.method == "spgist" {
				key = "location"
				columns += ",location point"
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:location", Kind: "column", Name: "location", SQL: "point"})
			}
			index := "CREATE INDEX by_id ON " + qualified(target.Object) + " USING " + sample.method + "(" + key + ")"
			desired.Definitions = []schemaddl.Definition{{Kind: "index", Name: "by_id", SQL: index}}
			if err := openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); !errors.Is(err, errOpenGaussDesignDeclaration) {
				t.Fatal("unsupported storage reached server parsing", err)
			}
			revert := gaussAccessMethodFixtureSavepoint(t, ctx, c, "unsupported_storage")
			if _, err := c.ExecContext(ctx, "CREATE TABLE "+qualified(target.Object)+"("+columns+") WITH("+sample.option+")"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.ExecContext(ctx, index); err == nil || !strings.Contains(err.Error(), sample.message) {
				t.Fatal("native storage rule differs", err)
			}
			revert()
			current, err := openGaussReadDesign(ctx, c, target)
			if err != nil || current.Schema.Exists || current.Schema.Revision != original.Schema.Revision {
				t.Fatal("unsupported native index changed original", err)
			}
		})
	}
}

func TestOpenGaussDesignAccessMethodNativeStorageSelectionFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	target.Object.Name = "native_storage_selection"
	original := gaussDesignOriginal(t, s, target)
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	for _, sample := range []struct{ name, option, index, want string }{
		{"heap", "storage_type=astore", "(id)", "btree"},
		{"column_default", "orientation=column", "(id)", "psort"},
		{"column_btree_alias", "orientation=column", "USING btree(id)", "cbtree"},
		{"column_unique_constraint", "orientation=column", "", "cbtree"},
		{"ustore_default", "storage_type=ustore", "(id)", "ubtree"},
		{"ustore_explicit", "storage_type=ustore", "USING ubtree(id)", "ubtree"},
		{"configured_ustore", "", "(id)", "ubtree"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			desired := original.Schema.Table.Clone()
			desired.Clauses = []schemaddl.Clause{{ID: "new:id", Kind: "column", Name: "id", SQL: "bigint"}}
			if sample.option != "" {
				desired.OpenGauss.Parameters = []string{sample.option}
			}
			if sample.index != "" {
				desired.Definitions = []schemaddl.Definition{{Kind: "index", Name: "by_id", SQL: "CREATE INDEX by_id ON " + qualified(target.Object) + " " + sample.index}}
			} else {
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:unique", Kind: "constraint", Name: "unique_id", SQL: "CONSTRAINT unique_id UNIQUE(id)"})
			}
			revert := gaussAccessMethodFixtureSavepoint(t, ctx, c, "native_storage")
			if sample.name == "configured_ustore" {
				if _, err := c.ExecContext(ctx, "SET LOCAL enable_default_ustore_table=on"); err != nil {
					t.Fatal(err)
				}
			}
			if err := openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); err != nil {
				t.Fatal("native storage selection preflight", err)
			}
			plan, err := schemaddl.PlanOpenGauss(original.Schema, desired, target.Object.Schema, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range plan.SQL {
				if _, err := c.ExecContext(ctx, statement); err != nil {
					t.Fatal("native index creation", err)
				}
			}
			var actual string
			if err := c.QueryRowContext(ctx, "SELECT a.amname FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_am a ON a.oid=c.relam WHERE i.indrelid=$1::regclass AND i.indisvalid AND i.indisready AND i.indisusable", qualified(target.Object)).Scan(&actual); err != nil || actual != sample.want {
				t.Fatal("actual native index selection differs", actual, err)
			}
			if _, err := c.ExecContext(ctx, "INSERT INTO "+qualified(target.Object)+" VALUES(1)"); err != nil {
				t.Fatal("native index tuple insertion", err)
			}
			var count int
			if err := c.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(target.Object)+" WHERE id=1").Scan(&count); err != nil || count != 1 {
				t.Fatal("native index tuple lookup", count, err)
			}
			revert()
			current, err := openGaussReadDesign(ctx, c, target)
			if err != nil || current.Schema.Exists || current.Schema.Revision != original.Schema.Revision {
				t.Fatal("native index declaration survived rollback", err)
			}
		})
	}
}
