package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignNativeSQLCastCatalogFixture(t *testing.T) {
	_, db, _ := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	if err = c.QueryRowContext(ctx, "SELECT count(*) FROM pg_cast ca JOIN pg_proc p ON p.oid=ca.castfunc WHERE p.prolang=14").Scan(&count); err != nil || count != len(openGaussNativeSQLCasts) {
		t.Fatal("native SQL cast inventory changed", count, err)
	}
	for oid, cast := range openGaussNativeSQLCasts {
		t.Run(fmt.Sprint(oid), func(t *testing.T) {
			if err := openGaussCheckNativeSQLCast(ctx, c, oid, cast.source, cast.target, cast.routine); err != nil {
				t.Fatal("native SQL forwarder or nested binding", err)
			}
		})
	}
}

// A committed replacement is made by the administrator while the original
// physical transaction remains open. This proves rechecking sees replacements
// at the next preflight; it does not prove a lock between check and execution.
func TestOpenGaussDesignNativeSQLCastCommittedReplacementBeforeParsingFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	if os.Getenv("NYADB_OPENGAUSS_CONTAINER") == "" {
		t.Skip("requires the explicitly owned fixture for catalog replacement")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	schema := quote(target.Object.Schema)
	if _, err := db.ExecContext(ctx, "CREATE SEQUENCE "+schema+".cast_effects; CREATE FUNCTION "+schema+".cast_effect() RETURNS numeric IMMUTABLE LANGUAGE plpgsql AS $$BEGIN PERFORM nextval('"+target.Object.Schema+".cast_effects'); RETURN 1; END$$"); err != nil {
		t.Fatal(err)
	}
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:cast", Kind: "column", Name: "cast_value", SQL: `numeric(40,20) DEFAULT CAST('9007199254740993.12345678901234567890'::nvarchar2 AS numeric)`})
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	if _, err = openGaussCanonicalDesign(ctx, c, contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true}); err != nil {
		t.Fatal("original native declaration", err)
	}
	for _, mutation := range []struct {
		name, table, column, value string
		oid                        uint64
	}{
		{"outer_body", "pg_proc", "prosrc", "select " + schema + ".cast_effect()", 12566},
		{"nested_body", "pg_proc", "prosrc", "select " + schema + ".cast_effect()", 12478},
		{"nested_signature", "pg_proc", "proisstrict", "false", 12478},
		{"nested_arguments", "pg_proc", "proargtypes", "1043", 12478},
		{"outer_language", "pg_proc", "prolang", "12", 12566},
		{"terminal_source", "pg_proc", "prosrc", "varcharin", 3962},
		{"type_input", "pg_type", "typinput", "1046", 3969},
		{"cast_context", "pg_cast", "castcontext", "a", 12567},
		{"cast_function", "pg_cast", "castfunc", "12568", 12567},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			var before, after string
			var old sql.NullString
			row := " FROM " + mutation.table + " WHERE oid=" + fmt.Sprint(mutation.oid)
			if err := c.QueryRowContext(ctx, "SELECT "+mutation.column+"::text"+row).Scan(&old); err != nil || !old.Valid {
				t.Fatal("original native property", err)
			}
			if err := c.QueryRowContext(ctx, "SELECT row_to_json(t)::text FROM (SELECT *"+row+") t").Scan(&before); err != nil {
				t.Fatal(err)
			}
			update := func(value string) string {
				return "UPDATE " + mutation.table + " SET " + mutation.column + "='" + strings.ReplaceAll(value, "'", "''") + "' WHERE oid=" + fmt.Sprint(mutation.oid) + ";"
			}
			// Register restoration before touching the catalog, even when an
			// assertion or a driver error terminates this subtest early.
			t.Cleanup(func() { gaussDesignFixtureRoleSQL(t, update(old.String)) })
			gaussDesignFixtureRoleSQL(t, update(mutation.value))
			if err := openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); !errors.Is(err, errOpenGaussDesignDeclaration) {
				t.Fatal("committed callback replacement was accepted", err)
			}
			if _, err := openGaussCanonicalDesign(ctx, c, contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true}); !errors.Is(err, errOpenGaussDesignDeclaration) {
				t.Fatal("changed callback reached independent CREATE", err)
			}
			var called bool
			if err := c.QueryRowContext(ctx, "SELECT is_called FROM "+schema+".cast_effects").Scan(&called); err != nil || called {
				t.Fatal("preflight evaluated changed user code", called, err)
			}
			gaussDesignFixtureRoleSQL(t, update(old.String))
			if err := c.QueryRowContext(ctx, "SELECT row_to_json(t)::text FROM (SELECT *"+row+") t").Scan(&after); err != nil || after != before {
				t.Fatal("native callback was not restored exactly", err)
			}
			if err := openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); err != nil {
				t.Fatal("restored native conversion rejected", err)
			}
		})
	}
}

func TestOpenGaussDesignNativeSQLCastDeclarationAndRollbackFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	sourceTarget := target
	source := gaussDesignOriginal(t, s, sourceTarget)
	target.Object.Name = "cast_values"
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	for i, field := range []string{
		`numeric(40,20) DEFAULT CAST('9007199254740993.12345678901234567890'::nvarchar2 AS numeric)`,
		`interval DAY TO SECOND(2) DEFAULT CAST('1 day 02:03:04.56789'::varchar AS interval DAY TO SECOND(2))`,
		`smalldatetime DEFAULT CAST('2026-11-07 12:34:56'::timestamp AS smalldatetime)`,
		`polygon DEFAULT CAST('<(0,0),1>'::circle AS polygon)`,
	} {
		desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: fmt.Sprintf("new:cast%d", i), Kind: "column", Name: fmt.Sprintf("cast%d", i), SQL: field})
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
	for i, col := range desired.Clauses[len(original.Schema.Table.Clauses):] {
		one := original.Schema.Table.Clone()
		one.Clauses = append(one.Clauses, col)
		if err = openGaussCheckDesignDeclaration(ctx, c, one, target.Object.Schema); err != nil {
			t.Fatalf("native conversion %d preflight: %v", i, err)
		}
	}
	if err = openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); err != nil {
		t.Fatal("native conversion preflight", err)
	}
	baseline, err := openGaussCanonicalDesign(ctx, c, contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true})
	if err != nil {
		t.Fatal("native conversion independent declaration", err)
	}
	absentTarget := target
	absentTarget.Object.Name = baseline.Catalog.Name
	absent, err := openGaussReadDesign(ctx, c, absentTarget)
	if err != nil || absent.Schema.Exists {
		t.Fatal("native cast declaration survived probe rollback", err)
	}
	current, err := openGaussReadDesign(ctx, c, target)
	if err != nil || current.Schema.Revision != original.Schema.Revision {
		t.Fatal("native cast probe changed original", err)
	}
	plan, err := schemaddl.PlanOpenGauss(original.Schema, desired, target.Object.Schema, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range plan.SQL {
		if _, err = c.ExecContext(ctx, q); err != nil {
			t.Fatal("native conversion DDL", err)
		}
	}
	actual, err := openGaussReadDesign(ctx, c, target)
	if err != nil {
		t.Fatal("native cast catalog after CREATE", err)
	}
	if err = openGaussVerifyDesignCatalog(original, desired, baseline, actual); err != nil {
		t.Fatal("native conversion differs from independent declaration", err)
	}
	if _, err = c.ExecContext(ctx, "INSERT INTO "+qualified(target.Object)+" DEFAULT VALUES"); err != nil {
		t.Fatal("native conversion defaults", err)
	}
	var numeric, interval, datetime string
	var vertices int
	err = c.QueryRowContext(ctx, "SELECT cast0::text,cast1::text,cast2::text,npoints(cast3) FROM "+qualified(target.Object)).Scan(&numeric, &interval, &datetime, &vertices)
	if err != nil || numeric != "9007199254740993.12345678901234567890" || interval != "1 day 02:03:04.57" || datetime != "2026-11-07 12:35:00" || vertices != 12 {
		t.Fatal("native cast values/typmod", numeric, interval, datetime, vertices, err)
	}
	if _, err = c.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal("native conversion rollback", err)
	}
	current, err = openGaussReadDesign(ctx, c, target)
	if err != nil || current.Schema.Exists {
		t.Fatal("rolled-back conversion left its created table", err)
	}
	current, err = openGaussReadDesign(ctx, c, sourceTarget)
	if err != nil || current.Schema.Revision != source.Schema.Revision {
		t.Fatal("native conversion changed the existing source table", err)
	}
}
