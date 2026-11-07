package engines

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// The written text field has no int16 declaration. Native implicit casts can
// still reach int16, so its callbacks must be checked before any CREATE probe.
// Catalog replacements are confined to the disposable, explicitly owned server.
func TestOpenGaussDesignImplicitTypeCallbacksBeforeNativeParsingFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	if os.Getenv("NYADB_OPENGAUSS_CONTAINER") == "" {
		t.Skip("requires the owned fixture for native type callback replacements")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	schema := quote(target.Object.Schema)
	if _, err := db.ExecContext(ctx, "CREATE SEQUENCE "+schema+".implicit_effects; CREATE FUNCTION "+schema+".implicit_effect(integer) RETURNS integer IMMUTABLE LANGUAGE plpgsql AS $$BEGIN PERFORM nextval('"+target.Object.Schema+".implicit_effects'); RETURN $1; END$$"); err != nil {
		t.Fatal(err)
	}
	var callback uint64
	if err := db.QueryRowContext(ctx, "SELECT p.oid::bigint FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND p.proname='implicit_effect'", target.Object.Schema).Scan(&callback); err != nil || callback < 16384 {
		t.Fatal("owned replacement callback", callback, err)
	}
	target.Object.Name = "implicit_types"
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:text", Kind: "column", Name: "label", SQL: "text DEFAULT 'unchanged'"})
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
		t.Fatal("native text declaration", err)
	}
	type mutation struct {
		name, property string
		routine        uint64
	}
	var changes []mutation
	for _, property := range []string{"typinput", "typoutput", "typreceive", "typsend", "typmodin", "typmodout", "typanalyze"} {
		changes = append(changes, mutation{property, property, callback})
	}
	for _, property := range []string{"typinput", "typoutput"} {
		changes = append(changes, mutation{"missing_" + property, property, 0})
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			var before, after string
			var old uint64
			if err := c.QueryRowContext(ctx, "SELECT "+change.property+"::oid::bigint,row_to_json(t)::text FROM pg_type t WHERE oid=34").Scan(&old, &before); err != nil {
				t.Fatal("native int16 before-image", err)
			}
			update := func(oid uint64) string {
				return fmt.Sprintf("UPDATE pg_type SET %s=%d WHERE oid=34;", change.property, oid)
			}
			t.Cleanup(func() { gaussDesignFixtureRoleSQL(t, update(old)) })
			gaussDesignFixtureRoleSQL(t, update(change.routine))
			if err := openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); !errors.Is(err, errOpenGaussDesignDeclaration) {
				t.Fatal("implicit-only type callback reached native parsing", err)
			}
			if _, err := openGaussCanonicalDesign(ctx, c, contract.SchemaDesignRequest{Original: original, Desired: desired}); !errors.Is(err, errOpenGaussDesignDeclaration) {
				t.Fatal("implicit-only type callback authorized an independent CREATE", err)
			}
			var called bool
			if err := c.QueryRowContext(ctx, "SELECT is_called FROM "+schema+".implicit_effects").Scan(&called); err != nil || called {
				t.Fatal("implicit callback was evaluated", called, err)
			}
			gaussDesignFixtureRoleSQL(t, update(old))
			if err := c.QueryRowContext(ctx, "SELECT row_to_json(t)::text FROM pg_type t WHERE oid=34").Scan(&after); err != nil || before != after {
				t.Fatal("implicit native type was not restored exactly", err)
			}
			if err := openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); err != nil {
				t.Fatal("restored native implicit type was rejected", err)
			}
		})
	}
	baseline, err := openGaussCanonicalDesign(ctx, c, contract.SchemaDesignRequest{Original: original, Desired: desired})
	if err != nil || baseline.Catalog == nil {
		t.Fatal("restored native declaration", err)
	}
	current, err := openGaussReadDesign(ctx, c, target)
	if err != nil || current.Schema.Exists || current.Schema.Revision != original.Schema.Revision {
		t.Fatal("implicit type probes changed the original", err)
	}
	probe := target
	probe.Object.Name = baseline.Catalog.Name
	absent, err := openGaussReadDesign(ctx, c, probe)
	if err != nil || absent.Schema.Exists || !strings.HasPrefix(probe.Object.Name, "__nyadb_gauss_design_") {
		t.Fatal("independent declaration was not rolled back", err)
	}
}

func TestOpenGaussDesignImplicitArrayTypesPreserveNativeDeclarationsFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	target.Object.Name = "implicit_arrays"
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	for i, declaration := range []string{"integer[]", "numeric(40,20)[]", "text[]"} {
		name := fmt.Sprintf("array%d", i)
		desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:" + name, Kind: "column", Name: name, SQL: declaration})
	}
	bad := desired.Clone()
	bad.Clauses = append(bad.Clauses, schemaddl.Clause{ID: "new:set", Kind: "column", Name: "set_value", SQL: "pg_catalog.anyset"})
	if rejected := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: bad}); rejected.Error == nil || rejected.Verified || rejected.State == "committed" {
		t.Fatal("implicit SET placeholder authorized a stored field", rejected)
	}
	out := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired})
	if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil || out.Rows != 0 {
		t.Fatal("native array declaration", out)
	}
	for i, col := range out.Actual.Schema.Table.Columns() {
		field, err := schemaddl.ParseOpenGaussField(col.SQL)
		if err != nil || field.Type != desired.Clauses[i].SQL {
			t.Fatal("array declaration/typmod changed", field.Type, desired.Clauses[i].SQL, err)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+qualified(target.Object)+" VALUES(ARRAY[1,NULL,2],ARRAY[9007199254740993.12345678901234567890,NULL]::numeric(40,20)[],ARRAY['中文',NULL,''])"); err != nil {
		t.Fatal("native array values", err)
	}
	var numeric, text string
	var nulls bool
	err := db.QueryRowContext(ctx, "SELECT array1[1]::text,array2[1],array0[2] IS NULL AND array1[2] IS NULL AND array2[2] IS NULL AND array2[3]='' FROM "+qualified(target.Object)).Scan(&numeric, &text, &nulls)
	if err != nil || numeric != "9007199254740993.12345678901234567890" || text != "中文" || !nulls {
		t.Fatal("array element precision/text/NULL changed", numeric, text, nulls, err)
	}
}
