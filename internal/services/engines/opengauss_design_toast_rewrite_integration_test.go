package engines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func gaussFixedReplacement(original contract.SchemaDesignSnapshot, retained bool) schemaddl.Table {
	desired := original.Schema.Table.Clone()
	desired.Clauses = nil
	desired.Definitions = nil
	if retained {
		for _, col := range original.Schema.Table.Columns() {
			if col.Name == "id" {
				desired.Clauses = append(desired.Clauses, col)
			}
		}
		desired.Definitions = original.Schema.Table.Clone().Definitions
	} else {
		desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:id", Kind: "column", Name: "id", SQL: "bigserial"})
	}
	desired.Clauses = append(desired.Clauses,
		schemaddl.Clause{ID: "new:next", Kind: "column", Name: "next_id", SQL: "bigint GENERATED ALWAYS AS(id+1) STORED"},
		schemaddl.Clause{ID: "new:null", Kind: "column", Name: "null_value", SQL: "bigint GENERATED ALWAYS AS(NULL::bigint) STORED"},
	)
	return desired
}

func gaussToastRewriteClone(baseline openGaussDesignBaseline) openGaussDesignBaseline {
	copy := baseline
	if baseline.ToastRewrite == nil {
		return copy
	}
	proof := *baseline.ToastRewrite
	proof.Before = proof.Before.Clone()
	proof.Columns = map[string]openGaussToastRewriteObservation{}
	for id, observation := range baseline.ToastRewrite.Columns {
		observation.After = observation.After.Clone()
		proof.Columns[id] = observation
	}
	copy.ToastRewrite = &proof
	return copy
}

func gaussToastRewriteOID(t *testing.T, catalog *schemaddl.OpenGaussCatalog) uint64 {
	t.Helper()
	_, toast, err := openGaussHeapStorage(catalog)
	if err != nil {
		t.Fatal(err)
	}
	return toast
}

func TestOpenGaussDesignToastRewriteFullReplacementAndProofBindingFixture(t *testing.T) {
	for _, operation := range []string{"replace_all", "retained_id", "unlogged"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			prefix := "CREATE TABLE "
			if operation == "unlogged" {
				prefix = "CREATE UNLOGGED TABLE "
			}
			table := qualified(target.Object)
			if _, err := db.ExecContext(ctx, "DROP TABLE "+table+"; "+prefix+table+"(id bigserial,note text,payload bytea); CREATE INDEX retained_id ON "+table+"(id)"); err != nil {
				t.Fatal(err)
			}
			for _, id := range []int{1, 1, 2} {
				if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES($1,$2,$3)", id, strings.Repeat("原始完整值", 1600), []byte(strings.Repeat("bytes", 1600))); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, "UPDATE "+table+" SET note=NULL,payload=NULL WHERE id=2"); err != nil {
				t.Fatal(err)
			}
			original := gaussDesignOriginal(t, s, target)
			desired := gaussFixedReplacement(original, operation == "retained_id")
			request := contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true}
			beforeRequest := schemaddl.Signature(request)
			c, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
				t.Fatal(err)
			}
			defer rollbackDesign(c)
			baseline, err := openGaussCanonicalDesign(ctx, c, request)
			if err != nil || baseline.ToastRewrite == nil || len(baseline.ToastRewrite.Columns) != 2 {
				t.Fatal("independent fixed-width TOAST prototype", err)
			}
			_, toast, err := openGaussHeapStorage(baseline.Catalog)
			if err != nil || toast != 0 {
				t.Fatal("fixed-width declaration unexpectedly has TOAST", toast, err)
			}
			prototypes, removed, err := openGaussVerifiedToastRewrite(original, desired, baseline)
			if err != nil || removed != (operation == "retained_id") || !removed && len(prototypes) == 0 {
				t.Fatal("independent native TOAST object bindings", err)
			}
			current, err := openGaussReadDesign(ctx, c, target)
			if err != nil || current.Schema.Revision != original.Schema.Revision {
				t.Fatal("carrier modified the original catalog", err)
			}
			carrier := target
			carrier.Object.Name = baseline.ToastRewrite.Before.Name
			missing, err := openGaussReadDesign(ctx, c, carrier)
			if err != nil || missing.Schema.Exists {
				t.Fatal("empty carrier survived acknowledged rollback", err)
			}
			if _, err = c.ExecContext(ctx, "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			out := s.openGaussDesignApply(ctx, request)
			if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil || out.Rows != 3 {
				t.Fatal("complete fixed-width replacement", out)
			}
			if schemaddl.Signature(request) != beforeRequest {
				t.Fatal("storage observer changed the owning request")
			}
			if out.Actual.Schema.OpenGauss.RelationOID != original.Schema.OpenGauss.RelationOID || gaussToastRewriteOID(t, out.Actual.Schema.OpenGauss) == gaussDesignToastOID(t, original.Schema.OpenGauss) {
				t.Fatal("original heap or rebuilt TOAST identity is incorrect")
			}
			if err := openGaussVerifyDesignCatalog(original, desired, baseline, *out.Actual); err != nil {
				t.Fatal("independent complete replacement catalog", err)
			}
			var rows, nulls, wrong int
			if err := db.QueryRowContext(ctx, "SELECT count(*),sum(CASE WHEN null_value IS NULL THEN 1 ELSE 0 END),sum(CASE WHEN next_id<>id+1 THEN 1 ELSE 0 END) FROM "+table).Scan(&rows, &nulls, &wrong); err != nil || rows != 3 || nulls != 3 || wrong != 0 {
				t.Fatal("native generated values, NULLs or cardinality changed", rows, nulls, wrong, err)
			}
			if operation == "retained_id" {
				var duplicates int
				if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE id=1").Scan(&duplicates); err != nil || duplicates != 2 {
					t.Fatal("retained values or duplicate multiplicity changed", duplicates, err)
				}
			} else {
				var min, max, distinct int
				if err := db.QueryRowContext(ctx, "SELECT min(id),max(id),count(DISTINCT id) FROM "+table).Scan(&min, &max, &distinct); err != nil || min != 1 || max != 3 || distinct != 3 {
					t.Fatal("complete native owned sequence values", min, max, distinct, err)
				}
			}
			missingProof := baseline
			missingProof.ToastRewrite = nil
			if err := openGaussVerifyDesignCatalog(original, desired, missingProof, *out.Actual); err == nil {
				t.Fatal("missing independent storage prototype was authorized", err)
			}
			if operation == "replace_all" {
				gaussToastRewriteRejectForeignProofs(t, original, desired, baseline)
				gaussDesignRejectCatalogCorruption(t, original, desired, baseline, *out.Actual)
			}
			if operation == "retained_id" {
				for _, entry := range original.Schema.OpenGauss.Entries {
					if entry.Kind != "index" || entry.OID == gaussDesignToastOID(t, original.Schema.OpenGauss) {
						continue
					}
					found := false
					for _, next := range out.Actual.Schema.OpenGauss.Entries {
						found = found || next.Kind == "index" && next.OID == entry.OID
					}
					// Only the user index on the original table is retained; its
					// internal TOAST index belongs to the rebuilt storage tree.
					var index struct {
						Owner uint64 `json:"indrelid"`
					}
					if json.Unmarshal(entry.Data, &index) != nil {
						t.Fatal("native index ownership")
					}
					if index.Owner == original.Schema.OpenGauss.RelationOID && !found {
						t.Fatal("retained user index identity changed", entry.OID)
					}
				}
			}
		})
	}
}

func gaussToastRewriteRejectForeignProofs(t *testing.T, original contract.SchemaDesignSnapshot, desired schemaddl.Table, baseline openGaussDesignBaseline) {
	t.Helper()
	for _, sample := range []struct {
		name   string
		change func(*openGaussDesignBaseline)
	}{
		{"request", func(b *openGaussDesignBaseline) { b.ToastRewrite.Request = "another request" }},
		{"declaration_catalog", func(b *openGaussDesignBaseline) { b.ToastRewrite.Catalog = "another declaration" }},
		{"before_catalog", func(b *openGaussDesignBaseline) { b.ToastRewrite.BeforeRevision = "another carrier" }},
		{"missing_observation", func(b *openGaussDesignBaseline) { delete(b.ToastRewrite.Columns, "new:next") }},
		{"column", func(b *openGaussDesignBaseline) {
			x := b.ToastRewrite.Columns["new:next"]
			x.Column = "another generated field"
			b.ToastRewrite.Columns["new:next"] = x
		}},
		{"after_catalog", func(b *openGaussDesignBaseline) {
			x := b.ToastRewrite.Columns["new:next"]
			x.Revision = "another storage rewrite"
			b.ToastRewrite.Columns["new:next"] = x
		}},
		{"original_handle", func(b *openGaussDesignBaseline) {
			b.ToastRewrite.Before = original.Schema.OpenGauss.Clone()
			b.ToastRewrite.BeforeRevision = original.Schema.Revision
			b.ToastRewrite.BeforeSignature = schemaddl.Signature(b.ToastRewrite.Before)
		}},
		{"foreign_role", func(b *openGaussDesignBaseline) {
			x := b.ToastRewrite.Columns["new:next"]
			x.After.RoleOID++
			x.Revision, _ = x.After.Fingerprint()
			x.Signature = schemaddl.Signature(x.After)
			b.ToastRewrite.Columns["new:next"] = x
		}},
		{"foreign_namespace", func(b *openGaussDesignBaseline) {
			x := b.ToastRewrite.Columns["new:next"]
			x.After.Namespace = "another_schema"
			x.Revision, _ = x.After.Fingerprint()
			x.Signature = schemaddl.Signature(x.After)
			b.ToastRewrite.Columns["new:next"] = x
		}},
		{"unproved_rewrite", func(b *openGaussDesignBaseline) {
			x := b.ToastRewrite.Columns["new:next"]
			x.After = b.ToastRewrite.Before.Clone()
			x.Revision = b.ToastRewrite.BeforeRevision
			x.Signature = schemaddl.Signature(x.After)
			b.ToastRewrite.Columns["new:next"] = x
		}},
		{"before_physical_record", func(b *openGaussDesignBaseline) {
			gaussToastRewriteChangeRecord(t, b.ToastRewrite.Before, "relation", b.ToastRewrite.Before.RelationOID, "relfilenode", uint64(1))
			b.ToastRewrite.BeforeRevision, _ = b.ToastRewrite.Before.Fingerprint()
		}},
		{"after_physical_record", func(b *openGaussDesignBaseline) {
			x := b.ToastRewrite.Columns["new:next"]
			gaussToastRewriteChangeRecord(t, x.After, "relation", x.After.RelationOID, "relfilenode", uint64(1))
			x.Revision, _ = x.After.Fingerprint()
			b.ToastRewrite.Columns["new:next"] = x
		}},
		{"unchanged_heap", func(b *openGaussDesignBaseline) {
			x := b.ToastRewrite.Columns["new:next"]
			file, _, err := openGaussHeapStorage(b.ToastRewrite.Before)
			if err != nil {
				t.Fatal(err)
			}
			gaussToastRewriteChangeRecord(t, x.After, "relation", x.After.RelationOID, "relfilenode", file)
			x.Revision, _ = x.After.Fingerprint()
			x.Signature = schemaddl.Signature(x.After)
			b.ToastRewrite.Columns["new:next"] = x
		}},
		{"foreign_creator", func(b *openGaussDesignBaseline) {
			x := b.ToastRewrite.Columns["new:next"]
			table, _, err := x.After.DesignTable()
			if err != nil {
				t.Fatal(err)
			}
			graph, err := schemaddl.NormalizeOpenGaussDesignGraph(*x.After, schemaddl.OpenGaussNamesForTable(table))
			if err != nil {
				t.Fatal(err)
			}
			for key, object := range graph.Objects {
				if strings.HasPrefix(key, "object:") && strings.HasPrefix(object, "@table:reltoastrelid") {
					gaussToastRewriteChangeRecord(t, x.After, "object", graph.Identities[key], "creator", x.After.RoleOID+1)
				}
			}
			x.Revision, _ = x.After.Fingerprint()
			x.Signature = schemaddl.Signature(x.After)
			b.ToastRewrite.Columns["new:next"] = x
		}},
		{"foreign_carrier_owner", func(b *openGaussDesignBaseline) {
			owner, err := openGaussHeapOwner(b.ToastRewrite.Before)
			if err != nil {
				t.Fatal(err)
			}
			gaussToastRewriteChangeRecord(t, b.ToastRewrite.Before, "relation", b.ToastRewrite.Before.RelationOID, "relowner", owner+1)
			b.ToastRewrite.BeforeRevision, _ = b.ToastRewrite.Before.Fingerprint()
			b.ToastRewrite.BeforeSignature = schemaddl.Signature(b.ToastRewrite.Before)
		}},
		{"foreign_rewritten_owner", func(b *openGaussDesignBaseline) {
			x := b.ToastRewrite.Columns["new:next"]
			owner, err := openGaussHeapOwner(x.After)
			if err != nil {
				t.Fatal(err)
			}
			gaussToastRewriteChangeRecord(t, x.After, "relation", x.After.RelationOID, "relowner", owner+1)
			x.Revision, _ = x.After.Fingerprint()
			x.Signature = schemaddl.Signature(x.After)
			b.ToastRewrite.Columns["new:next"] = x
		}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			copy := gaussToastRewriteClone(baseline)
			sample.change(&copy)
			if _, _, err := openGaussVerifiedToastRewrite(original, desired, copy); err == nil {
				t.Fatal("foreign storage evidence was authorized")
			}
		})
	}
	foreign := original.Clone()
	foreign.Target.Generation++
	if _, _, err := openGaussVerifiedToastRewrite(foreign, desired, baseline); !errors.Is(err, schemaddl.ErrConflict) {
		t.Fatal("storage proof accepted another generation", err)
	}
	foreignDesired := desired.Clone()
	foreignDesired.Clauses[1].SQL = "bigint GENERATED ALWAYS AS(id+2) STORED"
	if _, _, err := openGaussVerifiedToastRewrite(original, foreignDesired, baseline); !errors.Is(err, schemaddl.ErrConflict) {
		t.Fatal("storage proof accepted another expression", err)
	}
}

func gaussToastRewriteChangeRecord(t *testing.T, catalog *schemaddl.OpenGaussCatalog, kind string, oid uint64, key string, value any) {
	t.Helper()
	found := false
	for i, entry := range catalog.Entries {
		if entry.Kind != kind || entry.OID != oid {
			continue
		}
		var row map[string]json.RawMessage
		if err := json.Unmarshal(entry.Data, &row); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		row[key] = encoded
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		catalog.Entries[i].Data = data
		found = true
	}
	if !found {
		t.Fatal("missing native record to mutate", kind, oid)
	}
}

func TestOpenGaussDesignToastRewritePreservesForeignOwnerAndBindsCreatorFixture(t *testing.T) {
	if os.Getenv("NYADB_OPENGAUSS_CONTAINER") == "" {
		t.Skip("requires owned role fixture for creator transition")
	}
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	role := fmt.Sprintf("gauss_storage_owner_%d", time.Now().UnixNano())
	password := os.Getenv("NYADB_OPENGAUSS_PASSWORD")
	if password == "" {
		t.Fatal("owned role requires the private fixture credential")
	}
	gaussDesignFixtureRoleSQL(t, "CREATE ROLE "+quote(role)+" WITH LOGIN PASSWORD '"+strings.ReplaceAll(password, "'", "''")+"'; GRANT USAGE,CREATE ON SCHEMA "+quote(target.Object.Schema)+" TO "+quote(role)+";")
	t.Cleanup(func() {
		gaussDesignFixtureRoleSQL(t, "DROP OWNED BY "+quote(role)+" CASCADE; DROP ROLE "+quote(role)+";")
	})
	gaussDesignFixtureRoleSQL(t, "GRANT "+quote(role)+" TO "+quote(s.backupProfile.Username)+";")
	if _, err := db.ExecContext(ctx, "DROP TABLE "+qualified(target.Object)+"; CREATE TABLE "+qualified(target.Object)+"(id integer,note text); INSERT INTO "+qualified(target.Object)+" VALUES(1,'original creator'); ALTER TABLE "+qualified(target.Object)+" OWNER TO "+quote(role)); err != nil {
		t.Fatal("independently transferred original ownership", err)
	}
	// Native pg_roles hides other identities from a membership-only actor;
	// pg_get_userbyid consequently returns NULL. Do not invent owner metadata.
	var hadCreateRole bool
	if err := db.QueryRowContext(ctx, "SELECT rolcreaterole FROM pg_roles WHERE rolname=current_user").Scan(&hadCreateRole); err != nil || hadCreateRole {
		t.Fatal("requires the owned ordinary fixture actor", hadCreateRole, err)
	}
	if invisible, err := s.openGaussDesignSnapshot(ctx, target); err == nil || invisible.Schema.OpenGauss == nil || !invisible.Schema.OpenGauss.CanManage {
		t.Fatal("hidden foreign owner acquired a complete blueprint", err)
	}
	gaussDesignFixtureRoleSQL(t, "ALTER ROLE "+quote(s.backupProfile.Username)+" CREATEROLE;")
	t.Cleanup(func() { gaussDesignFixtureRoleSQL(t, "ALTER ROLE "+quote(s.backupProfile.Username)+" NOCREATEROLE;") })
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = []schemaddl.Clause{{ID: "new:id", Kind: "column", Name: "id", SQL: "integer DEFAULT 23"}, {ID: "new:next", Kind: "column", Name: "next_id", SQL: "bigint GENERATED ALWAYS AS(id+1) STORED"}}
	desired.Definitions = nil
	out := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true})
	if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil || out.Rows != 1 || out.Actual.Schema.Table.OpenGauss.Owner != role {
		t.Fatal("foreign owner storage rewrite", out)
	}
	var next int
	if err := db.QueryRowContext(ctx, "SELECT next_id FROM "+qualified(target.Object)).Scan(&next); err != nil || next != 24 {
		t.Fatal("owned fixed-width values", next, err)
	}
	var beforeCreator, afterCreator uint64
	toastObject := func(catalog *schemaddl.OpenGaussCatalog) uint64 {
		for _, entry := range catalog.Entries {
			if entry.Kind != "index" {
				continue
			}
			var index struct {
				Owner uint64 `json:"indrelid"`
			}
			if json.Unmarshal(entry.Data, &index) == nil && index.Owner == gaussDesignToastOID(t, catalog) {
				return entry.OID
			}
		}
		t.Fatal("missing native TOAST index")
		return 0
	}
	readCreator := func(catalog *schemaddl.OpenGaussCatalog, index uint64) uint64 {
		for _, entry := range catalog.Entries {
			if entry.Kind == "object" && entry.OID == index {
				var object struct{ Creator uint64 }
				if json.Unmarshal(entry.Data, &object) != nil {
					t.Fatal("native object creator")
				}
				return object.Creator
			}
		}
		t.Fatal("missing native object metadata")
		return 0
	}
	beforeCreator = readCreator(original.Schema.OpenGauss, toastObject(original.Schema.OpenGauss))
	afterCreator = readCreator(out.Actual.Schema.OpenGauss, toastObject(out.Actual.Schema.OpenGauss))
	owner, err := openGaussHeapOwner(original.Schema.OpenGauss)
	if err != nil || beforeCreator != original.Schema.OpenGauss.RoleOID || beforeCreator == afterCreator || afterCreator != owner {
		t.Fatal("creator was not independently bound to the captured heap owner", beforeCreator, afterCreator, owner, err)
	}
}

func TestOpenGaussDesignToastRewriteCancellationAndLostRollbackReceiptFixture(t *testing.T) {
	for _, sample := range []struct{ name, query string }{
		{"cancel_carrier", "SAVEPOINT nyadb_gauss_toast_carrier"},
		{"cancel_column", "SAVEPOINT nyadb_gauss_toast_column"},
		{"lost_column_rollback", "ROLLBACK TO SAVEPOINT nyadb_gauss_toast_column"},
		{"lost_carrier_release", "RELEASE SAVEPOINT nyadb_gauss_toast_carrier"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			original := gaussDesignOriginal(t, s, target)
			desired := gaussFixedReplacement(original, false)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			fired := false
			gaussDesignInstallExecutionGate(t, s, sample.query, func(context.Context) error {
				fired = true
				if strings.HasPrefix(sample.name, "cancel_") {
					cancel()
					return nil
				}
				return io.ErrUnexpectedEOF
			})
			out := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true})
			if !fired || out.Error == nil || out.Verified || out.Actual != nil || out.State != "rolled_back" && out.State != "uncertain" {
				t.Fatal("private storage probe remained replayable", fired, out)
			}
			if strings.HasPrefix(sample.name, "lost_") && (out.State != "uncertain" || !errors.Is(out.Error, ErrUncertain) || !errors.Is(out.Error, io.ErrUnexpectedEOF) || !s.isClosed()) {
				t.Fatal("lost cleanup receipt did not retire the owning session", out)
			}
			if strings.HasPrefix(sample.name, "cancel_") && !errors.Is(out.Error, context.Canceled) {
				t.Fatal("probe cancellation lost its cause", out)
			}
			c, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			fresh, err := openGaussReadDesign(context.Background(), c, target)
			if err != nil || fresh.Schema.Revision != original.Schema.Revision {
				t.Fatal("probe cleanup changed the original catalog", err)
			}
			var carrierCount, rowCount int
			if err := c.QueryRowContext(context.Background(), "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname LIKE '__nyadb_gauss_storage_%'", target.Object.Schema).Scan(&carrierCount); err != nil || carrierCount != 0 {
				t.Fatal("cancelled storage carrier survived cleanup", carrierCount, err)
			}
			if err := c.QueryRowContext(context.Background(), "SELECT count(*) FROM "+qualified(target.Object)+" WHERE note='原始' AND amount=9007199254740993.12345678901234567890").Scan(&rowCount); err != nil || rowCount != 1 {
				t.Fatal("probe cleanup changed original full values", rowCount, err)
			}
		})
	}
}
