package engines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func gaussDesignOriginal(t *testing.T, s *Session, target contract.SchemaDesignTarget) contract.SchemaDesignSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	original, err := s.openGaussDesignSnapshot(ctx, target)
	if err != nil || original.Schema.Engine != "opengauss" || original.Schema.Table.OpenGauss == nil || original.Schema.Table.Postgres != nil {
		t.Fatal("native design snapshot", err)
	}
	return original
}

func TestOpenGaussDesignDraftHasNativeColumnsDefinitionsAndDDLFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	name := qualified(target.Object)
	if _, err := db.ExecContext(ctx, "COMMENT ON TABLE "+name+" IS 'native table'; COMMENT ON COLUMN "+name+".note IS 'native field'; CREATE INDEX note_idx ON "+name+"(note); ALTER TABLE "+name+" ADD CONSTRAINT amount_check CHECK(amount>=0)"); err != nil {
		t.Fatal(err)
	}
	original := gaussDesignOriginal(t, s, target)
	if original.Schema.DDL == "" || !strings.Contains(original.Schema.DDL, "CREATE TABLE") || len(original.Schema.Table.Columns()) != 4 || len(original.Schema.Table.Definitions) != 1 {
		t.Fatal("incomplete native declaration", original.Schema.Table)
	}
	if *original.Schema.Table.OpenGauss.Comment != "native table" || original.Schema.Table.OpenGauss.Persistence != "p" {
		t.Fatal("native table metadata", original.Schema.Table.OpenGauss)
	}
	for _, c := range original.Schema.Table.Columns() {
		f, err := schemaddl.ParseOpenGaussField(c.SQL)
		if err != nil {
			t.Fatal(err)
		}
		if c.Name == "id" && (!f.NotNull || !f.HasDefault || !strings.Contains(f.Default, "original_id_seq")) {
			t.Fatal("native serial/nullability lost", f)
		}
		if c.Name == "twice" && (f.Generation == "" || f.GenerationStorage != "STORED") {
			t.Fatal("native generated declaration lost", f)
		}
		if c.Name == "note" && (c.OpenGauss.Comment == nil || *c.OpenGauss.Comment != "native field") {
			t.Fatal("native column comment lost", c)
		}
	}
	if p, err := schemaddl.PlanOpenGauss(original.Schema, original.Schema.Table, target.Object.Schema, true); err != nil || p.Mode != "noop" {
		t.Fatal("read-only native draft is not stable", p, err)
	}
}

func TestOpenGaussDesignPlanUsesNativeAlterAndPreservesStorageFixture(t *testing.T) {
	for _, storage := range []string{"row", "column", "partition"} {
		t.Run(storage, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			name := qualified(target.Object)
			if storage != "row" {
				q := "DROP TABLE " + name + "; CREATE TABLE " + name + "(id bigint,note text)"
				if storage == "column" {
					q += " WITH(orientation=column,compression=middle)"
				} else {
					q += " PARTITION BY RANGE(id)(PARTITION p1 VALUES LESS THAN(10),PARTITION p2 VALUES LESS THAN(MAXVALUE))"
				}
				q += "; INSERT INTO " + name + " VALUES(1,'原始'),(11,'second')"
				if _, err := db.ExecContext(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			comment := "native edited 中文 '"
			desired.OpenGauss.Comment = &comment
			for i, c := range desired.Clauses {
				if c.Kind == "column" && c.Name == "note" {
					desired.Clauses[i].SQL = "varchar(512) NOT NULL DEFAULT 'next'"
					if storage == "column" {
						desired.Clauses[i].SQL = "varchar(512) DEFAULT 'next'"
					}
				}
			}
			if storage == "column" {
				unsupported := desired.Clone()
				for i, c := range unsupported.Clauses {
					if c.Kind == "column" && c.Name == "note" {
						unsupported.Clauses[i].SQL = "varchar(512) NOT NULL DEFAULT 'next'"
					}
				}
				if _, err := schemaddl.PlanOpenGauss(original.Schema, unsupported, target.Object.Schema, true); err == nil {
					t.Fatal("native unsupported column-store nullability reached DDL")
				}
			}
			desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:extra", Kind: "column", Name: "extra", SQL: "numeric(40,20) DEFAULT 9007199254740993.12345678901234567890"})
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
				t.Fatal("native ALTER baseline", err)
			}
			for _, q := range plan.SQL {
				if _, err = c.ExecContext(ctx, q); err != nil {
					t.Fatal("native plan", q, err)
				}
			}
			actual, err := openGaussReadDesign(ctx, c, target)
			if err != nil {
				t.Fatal(err)
			}
			if err = openGaussVerifyDesignCatalog(original, desired, baseline, actual); err != nil {
				t.Fatal("native complete ALTER catalog", err)
			}
			if actual.Schema.Table.OpenGauss.Comment == nil || *actual.Schema.Table.OpenGauss.Comment != comment || len(actual.Schema.Table.Columns()) != len(desired.Columns()) || schemaddl.Signature(actual.Schema.Table.OpenGauss.Parameters) != schemaddl.Signature(original.Schema.Table.OpenGauss.Parameters) {
				t.Fatal("native alter lost storage/declaration", actual.Schema.Table)
			}
			var value string
			if err = c.QueryRowContext(ctx, "SELECT extra::text FROM "+name+" WHERE id=1").Scan(&value); err != nil || value != "9007199254740993.12345678901234567890" {
				t.Fatal("native new default precision", value, err)
			}
			gaussDesignRejectCatalogCorruption(t, original, desired, baseline, actual)
		})
	}
}

func TestOpenGaussDesignPlanCreatesNativeSequencesGeneratedFieldsAndIndexesFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	target.Object.Name = "created"
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = []schemaddl.Clause{
		{ID: "new:id", Kind: "column", Name: "id", SQL: "bigserial NOT NULL"},
		{ID: "new:amount", Kind: "column", Name: "amount", SQL: "numeric(40,20) DEFAULT 9007199254740993.12345678901234567890"},
		{ID: "new:twice", Kind: "column", Name: "twice", SQL: "numeric(40,20) GENERATED ALWAYS AS(amount*2) STORED"},
		{ID: "new:primary", Kind: "constraint", Name: "created_pkey", SQL: "CONSTRAINT created_pkey PRIMARY KEY(id)"},
		{ID: "new:check", Kind: "constraint", Name: "positive", SQL: "CONSTRAINT positive CHECK(amount>=0)"},
	}
	desired.Definitions = []schemaddl.Definition{{Name: "created_amount_idx", Kind: "index", SQL: "CREATE INDEX created_amount_idx ON " + qualified(target.Object) + "(amount)"}}
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
		t.Fatal("native CREATE baseline", err)
	}
	for _, q := range plan.SQL {
		if _, err = c.ExecContext(ctx, q); err != nil {
			t.Fatal("native CREATE", q, err)
		}
	}
	var id int64
	var twice string
	if err = c.QueryRowContext(ctx, "INSERT INTO "+qualified(target.Object)+" DEFAULT VALUES RETURNING id,twice::text").Scan(&id, &twice); err != nil || id != 1 || twice != "18014398509481986.24691357802469135780" {
		t.Fatal("native sequence/generated values", id, twice, err)
	}
	actual, err := openGaussReadDesign(ctx, c, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = openGaussVerifyDesignCatalog(original, desired, baseline, actual); err != nil {
		t.Fatal("native complete CREATE catalog", err)
	}
	gaussDesignRejectCatalogCorruption(t, original, desired, baseline, actual)
	definition, err := actual.Schema.OpenGauss.CopyDefinition()
	if err != nil || len(definition.Sequences) != 1 || len(actual.Schema.Table.Definitions) != 1 {
		t.Fatal("native created object identities", err, definition)
	}
}

func gaussDesignRejectCatalogCorruption(t *testing.T, original contract.SchemaDesignSnapshot, desired schemaddl.Table, baseline openGaussDesignBaseline, actual contract.SchemaDesignSnapshot) {
	t.Helper()
	prior := schemaddl.Signature(original)
	prototype := schemaddl.Signature(baseline)
	result := schemaddl.Signature(actual)
	for i, entry := range actual.Schema.OpenGauss.Entries {
		if entry.Kind == "copy_definition" || entry.Kind == "design_definition" {
			continue // synthesized presentation rows, not native catalog records
		}
		if entry.Kind == "column" {
			var column struct {
				Dropped bool `json:"attisdropped"`
			}
			if err := json.Unmarshal(entry.Data, &column); err != nil {
				t.Fatal(err)
			}
			if column.Dropped {
				continue // intentionally removed fields do not retain properties
			}
		}
		t.Run(fmt.Sprintf("reject_%s_%d_%d", entry.Kind, entry.OID, entry.SubID), func(t *testing.T) {
			x := actual.Clone()
			decoder := json.NewDecoder(strings.NewReader(string(entry.Data)))
			decoder.UseNumber()
			var fields map[string]any
			if err := decoder.Decode(&fields); err != nil {
				t.Fatal(err)
			}
			fields["unrecognized_native_property"] = json.Number("9007199254740993")
			var err error
			x.Schema.OpenGauss.Entries[i].Data, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			x.Schema.Revision, err = x.Schema.OpenGauss.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			if err = openGaussVerifyDesignCatalog(original, desired, baseline, x); err == nil {
				t.Fatal("unexpected native property was accepted")
			}
		})
	}
	if prior != schemaddl.Signature(original) || prototype != schemaddl.Signature(baseline) || result != schemaddl.Signature(actual) {
		t.Fatal("catalog verification modified captured evidence")
	}
	gaussDesignRejectRetainedDefaultReplacement(t, original, desired, baseline, actual)
	for _, corruption := range []struct {
		name   string
		change func(*contract.SchemaDesignSnapshot)
	}{
		{"target_catalog", func(x *contract.SchemaDesignSnapshot) { x.Target.Object.Catalog += "_other" }},
		{"target_schema", func(x *contract.SchemaDesignSnapshot) { x.Target.Object.Schema += "_other" }},
		{"database_identity", func(x *contract.SchemaDesignSnapshot) { x.Schema.OpenGauss.DatabaseOID++ }},
		{"namespace_identity", func(x *contract.SchemaDesignSnapshot) { x.Schema.OpenGauss.NamespaceOID++ }},
		{"role_identity", func(x *contract.SchemaDesignSnapshot) { x.Schema.OpenGauss.RoleOID++ }},
		{"create_permission", func(x *contract.SchemaDesignSnapshot) { x.Schema.OpenGauss.CanCreate = !x.Schema.OpenGauss.CanCreate }},
		{"management_permission", func(x *contract.SchemaDesignSnapshot) { x.Schema.OpenGauss.CanManage = false }},
		{"enabled_hooks", func(x *contract.SchemaDesignSnapshot) { x.Schema.OpenGauss.Hooks = true }},
	} {
		t.Run("reject_"+corruption.name, func(t *testing.T) {
			x := actual.Clone()
			corruption.change(&x)
			var err error
			x.Schema.Revision, err = x.Schema.OpenGauss.Fingerprint()
			if err == nil {
				err = openGaussVerifyDesignCatalog(original, desired, baseline, x)
			}
			if err == nil {
				t.Fatal("native result changed the captured scope or authority")
			}
		})
	}
	t.Run("catalog_record_order_is_not_identity", func(t *testing.T) {
		x := actual.Clone()
		entries := x.Schema.OpenGauss.Entries
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
		if err := openGaussVerifyDesignCatalog(original, desired, baseline, x); err != nil {
			t.Fatal("native catalog identity depends on entry ordering", err)
		}
	})
}

func gaussDesignRejectRetainedDefaultReplacement(t *testing.T, original contract.SchemaDesignSnapshot, desired schemaddl.Table, baseline openGaussDesignBaseline, actual contract.SchemaDesignSnapshot) {
	t.Helper()
	if !original.Schema.Exists {
		return
	}
	for i, entry := range actual.Schema.OpenGauss.Entries {
		if entry.Kind != "default" {
			continue
		}
		var d struct {
			Owner  uint64 `json:"adrelid"`
			Number int    `json:"adnum"`
		}
		if err := json.Unmarshal(entry.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Owner != actual.Schema.OpenGauss.RelationOID {
			continue
		}
		unchanged := false
		for _, old := range original.Schema.Table.Columns() {
			if old.OpenGauss == nil || old.OpenGauss.Number != d.Number {
				continue
			}
			for _, next := range desired.Columns() {
				unchanged = unchanged || old.ID == next.ID && old.SQL == next.SQL
			}
		}
		if !unchanged {
			continue
		}
		t.Run("reject_retained_default_identity", func(t *testing.T) {
			x := actual.Clone()
			newOID := entry.OID + 1000000
			x.Schema.OpenGauss.Entries[i].OID = newOID
			for j, other := range x.Schema.OpenGauss.Entries {
				if other.Kind != "dependencies" {
					continue
				}
				decoder := json.NewDecoder(strings.NewReader(string(other.Data)))
				decoder.UseNumber()
				var fields map[string]any
				if err := decoder.Decode(&fields); err != nil {
					t.Fatal(err)
				}
				rows, _ := fields["rows"].([]any)
				for _, row := range rows {
					v := row.(map[string]any)
					class, err := gaussRecordUint(v["classid"])
					if err != nil {
						t.Fatal(err)
					}
					oid, err := gaussRecordUint(v["objid"])
					if err != nil {
						t.Fatal(err)
					}
					if class == x.Schema.OpenGauss.Classes["pg_attrdef"] && oid == entry.OID {
						v["objid"] = json.Number(strconv.FormatUint(newOID, 10))
					}
				}
				var err error
				x.Schema.OpenGauss.Entries[j].Data, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
			}
			var err error
			x.Schema.Revision, err = x.Schema.OpenGauss.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			if err = openGaussVerifyDesignCatalog(original, desired, baseline, x); err == nil || !strings.Contains(err.Error(), "retained catalog identity changed") {
				t.Fatal("coordinated default/dependency replacement did not fail identity preservation", err)
			}
		})
		return
	}
}

func TestOpenGaussDesignDeclarationChecksNativeCallbacksBeforeParsingFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	schema := quote(target.Object.Schema)
	if _, err := db.ExecContext(ctx, "CREATE SEQUENCE "+schema+".effects; CREATE FUNCTION "+schema+".side_effect() RETURNS int IMMUTABLE LANGUAGE plpgsql AS $$BEGIN PERFORM nextval('"+target.Object.Schema+".effects'); RETURN 1; END$$"); err != nil {
		t.Fatal(err)
	}
	original := gaussDesignOriginal(t, s, target)
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	if err = openGaussCheckDesignDeclaration(ctx, c, original.Schema.Table, target.Object.Schema); err != nil {
		t.Fatal("native builtin original", err)
	}
	addition := original.Schema.Table.Clone()
	addition.Clauses = append(addition.Clauses, schemaddl.Clause{ID: "new:addition", Kind: "column", Name: "addition", SQL: "numeric(40,20) GENERATED ALWAYS AS(amount+id+1) STORED"})
	if err = openGaussCheckDesignDeclaration(ctx, c, addition, target.Object.Schema); err != nil {
		t.Fatal("core addition was rejected by unrelated SQL overloads", err)
	}
	for _, body := range []string{
		"integer DEFAULT " + schema + ".side_effect()",
		"integer GENERATED ALWAYS AS(" + schema + ".side_effect()) STORED",
		"integer DEFAULT 1::" + schema + ".custom_type",
		"integer DEFAULT 1 OPERATOR(" + schema + ".+) 2",
	} {
		d := original.Schema.Table.Clone()
		d.Clauses = append(d.Clauses, schemaddl.Clause{ID: "new:unsafe", Kind: "column", Name: "unsafe", SQL: body})
		if err = openGaussCheckDesignDeclaration(ctx, c, d, target.Object.Schema); err == nil {
			t.Fatal("unsafe native callbacks were authorized", body)
		}
	}
	if _, err = c.ExecContext(ctx, "SET LOCAL search_path="+schema+",pg_catalog; CREATE TYPE "+schema+".cast_target AS ENUM('one'); CREATE FUNCTION "+schema+".cast_effect(integer) RETURNS "+schema+".cast_target IMMUTABLE LANGUAGE plpgsql AS $$BEGIN PERFORM nextval('"+target.Object.Schema+".effects'); RETURN 'one'; END$$; CREATE CAST(integer AS "+schema+".cast_target) WITH FUNCTION "+schema+".cast_effect(integer) AS IMPLICIT; SET LOCAL search_path=pg_catalog"); err != nil {
		t.Fatal(err)
	}
	if err = openGaussCheckDesignDeclaration(ctx, c, addition, target.Object.Schema); !errors.Is(err, errOpenGaussDesignDeclaration) {
		t.Fatal("implicit user cast outside written operand types was authorized", err)
	}
	var called bool
	if err = c.QueryRowContext(ctx, "SELECT is_called FROM "+schema+".effects").Scan(&called); err != nil || called {
		t.Fatal("declaration validation executed user code", called, err)
	}
}

func TestOpenGaussDesignIndependentDeclarationNormalizesAndRollsBackFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:extra", Kind: "column", Name: "extra", SQL: "DECIMAL(40,20) DEFAULT 9007199254740993.12345678901234567890"})
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	definition, err := original.Schema.OpenGauss.CopyDefinition()
	if err != nil {
		t.Fatal(err)
	}
	states, err := openGaussCopySequenceStates(ctx, c, definition)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := openGaussCanonicalDesign(ctx, c, contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true})
	if err != nil {
		t.Fatal("independent native declaration", err)
	}
	if _, err = schemaddl.NormalizeOpenGaussDesignGraph(*original.Schema.OpenGauss, schemaddl.OpenGaussNamesForTable(original.Schema.Table)); err != nil {
		t.Fatal("original native graph", err)
	}
	if _, err = schemaddl.NormalizeOpenGaussDesignGraph(*baseline.Catalog, baseline.Names); err != nil {
		t.Fatal("independent native graph", err)
	}
	if baseline.Table.Name != desired.Name || len(baseline.Table.Columns()) != 5 || baseline.Table.Columns()[4].ID != "new:extra" || !strings.HasPrefix(baseline.Table.Columns()[4].SQL, "numeric(40,20)") {
		t.Fatal("native normalization/stable identity", baseline.Table)
	}
	if baseline.Catalog == nil || !strings.HasPrefix(baseline.Catalog.Name, "__nyadb_gauss_design_") {
		t.Fatal("missing private native catalog")
	}
	absentTarget := target
	absentTarget.Object.Name = baseline.Catalog.Name
	absent, err := openGaussReadDesign(ctx, c, absentTarget)
	if err != nil || absent.Schema.Exists {
		t.Fatal("private declaration survived its rollback", err)
	}
	current, err := openGaussReadDesign(ctx, c, target)
	if err != nil || current.Schema.Revision != original.Schema.Revision {
		t.Fatal("probe changed original native catalog", err)
	}
	after, err := openGaussCopySequenceStates(ctx, c, definition)
	if err != nil || schemaddl.Signature(states) != schemaddl.Signature(after) {
		t.Fatal("probe advanced original owned sequence", err)
	}
}

func TestOpenGaussDesignRetainedValuesDetectNativeRoundingAndTruncationFixture(t *testing.T) {
	for _, change := range []struct {
		name, field, typ string
		loss             bool
	}{
		{"lossless", "note", "varchar(5000)", false},
		{"truncated", "note", "varchar(3)", true},
		{"rounded", "amount", "numeric(20,2)", true},
	} {
		t.Run(change.name, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			name := qualified(target.Object)
			if _, err := db.ExecContext(ctx, "DROP TABLE "+name+"; CREATE TABLE "+name+"(id bigint,note text,amount numeric(40,20)); INSERT INTO "+name+" VALUES(1,repeat('长',2001),9007199254740993.12345678901234567890),(1,repeat('长',2001),9007199254740993.12345678901234567890),(2,NULL,NULL)"); err != nil {
				t.Fatal(err)
			}
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			for i, col := range desired.Clauses {
				if col.Kind == "column" && col.Name == change.field {
					desired.Clauses[i].SQL = change.typ
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
			if err = openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); err != nil {
				t.Fatal(err)
			}
			before, err := openGaussRetainedDesignDigest(ctx, c, target.Object, original.Schema.Table, desired, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range plan.SQL {
				if _, err = c.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			after, err := openGaussRetainedDesignDigest(ctx, c, target.Object, original.Schema.Table, desired, true)
			if err != nil {
				t.Fatal(err)
			}
			if before.Rows != 3 || after.Rows != 3 || (before != after) != change.loss {
				t.Fatal("native retained-value proof", change.name, before, after)
			}
			if err = rollbackDesign(c); err != nil {
				t.Fatal(err)
			}
			var length int
			var amount string
			if err = c.QueryRowContext(ctx, "SELECT length(note),amount::text FROM "+name+" WHERE id=1 LIMIT 1").Scan(&length, &amount); err != nil || length != 2001 || amount != "9007199254740993.12345678901234567890" {
				t.Fatal("rollback did not retain complete original values", length, amount, err)
			}
		})
	}
}
