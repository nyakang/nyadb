package engines

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func gaussDesignToastOID(t *testing.T, catalog *schemaddl.OpenGaussCatalog) uint64 {
	t.Helper()
	for _, entry := range catalog.Entries {
		if entry.Kind == "relation" && entry.OID == catalog.RelationOID {
			var row struct {
				Toast uint64 `json:"reltoastrelid"`
			}
			if err := json.Unmarshal(entry.Data, &row); err != nil {
				t.Fatal(err)
			}
			return row.Toast
		}
	}
	t.Fatal("missing original heap relation")
	return 0
}

func TestOpenGaussDesignDefaultRewriteFullValuesAndInternalIdentitiesFixture(t *testing.T) {
	if os.Getenv("NYADB_OPENGAUSS_CONTAINER") == "" {
		t.Skip("requires the owned fixture for physical TOAST verification")
	}
	for _, change := range []struct {
		name, field, value string
		rewrite, null      bool
	}{
		{"short_text", "text DEFAULT 'short'", "short", false, false},
		{"boundary_123", "text DEFAULT '" + strings.Repeat("x", 123) + "'", strings.Repeat("x", 123), false, false},
		{"boundary_124", "text DEFAULT '" + strings.Repeat("x", 124) + "'", strings.Repeat("x", 124), true, false},
		{"long_text", "text DEFAULT '" + strings.Repeat("完整值", 301) + "'", strings.Repeat("完整值", 301), true, false},
		{"null_text", "text DEFAULT NULL", "", false, true},
		{"null_nvarchar", "nvarchar2 DEFAULT NULL", "", false, true},
		{"cast_numeric", `numeric(40,20) DEFAULT CAST('9007199254740993.12345678901234567890'::nvarchar2 AS numeric)`, "9007199254740993.12345678901234567890", false, false},
		{"cast_polygon", `polygon DEFAULT CAST('<(0,0),1>'::circle AS polygon)`, "12", true, false},
	} {
		t.Run(change.name, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
			defer cancel()
			table := qualified(target.Object)
			if _, err := db.ExecContext(ctx, "DROP TABLE "+table+"; CREATE TABLE "+table+"(id bigserial,note text,payload bytea,amount numeric(40,20)); CREATE INDEX retained_id ON "+table+"(id)"); err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			var payload []byte
			for i := range 0x100 {
				digest := sha256.Sum256([]byte(fmt.Sprint(i)))
				text.WriteString(hex.EncodeToString(digest[:]))
				text.WriteString("中文")
				payload = append(payload, digest[:]...)
			}
			for range 2 {
				if _, err := db.ExecContext(ctx, "INSERT INTO "+table+"(id,note,payload,amount) VALUES(1,$1,$2,9007199254740993.12345678901234567890)", text.String(), payload); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, "INSERT INTO "+table+"(id,note,payload,amount) VALUES(2,NULL,NULL,NULL)"); err != nil {
				t.Fatal(err)
			}
			original := gaussDesignOriginal(t, s, target)
			gaussDesignRequireStoredToast(t, original.Schema.OpenGauss)
			desired := original.Schema.Table.Clone()
			desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:value", Kind: "column", Name: "new_value", SQL: change.field})
			c, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
				t.Fatal(err)
			}
			defer rollbackDesign(c)
			r := contract.SchemaDesignRequest{Original: original, Desired: desired}
			baseline, err := openGaussCanonicalDesign(ctx, c, r)
			if err != nil {
				t.Fatal("independent native default rewrite", err)
			}
			rewrite, err := openGaussVerifiedDefaultRewrite(original, desired, baseline)
			if err != nil || rewrite != change.rewrite || baseline.DefaultRewrite == nil {
				t.Fatal("native rewrite classification", rewrite, change.rewrite, err)
			}
			current, err := openGaussReadDesign(ctx, c, target)
			if err != nil || current.Schema.Revision != original.Schema.Revision {
				t.Fatal("default probe changed the original", err)
			}
			probeTarget := target
			probeTarget.Object.Name = baseline.Catalog.Name
			missing, err := openGaussReadDesign(ctx, c, probeTarget)
			if err != nil || missing.Schema.Exists {
				t.Fatal("default probe survived rollback", err)
			}
			// An observation may not authorize another request or prototype.
			foreign := desired.Clone()
			foreign.Clauses[len(foreign.Clauses)-1].Name = "other_value"
			if _, err = openGaussVerifiedDefaultRewrite(original, foreign, baseline); !errors.Is(err, schemaddl.ErrConflict) {
				t.Fatal("rewrite proof authorized another declaration", err)
			}
			foreignOriginal := original.Clone()
			foreignOriginal.Target.Generation++
			if _, err = openGaussVerifiedDefaultRewrite(foreignOriginal, desired, baseline); !errors.Is(err, schemaddl.ErrConflict) {
				t.Fatal("rewrite proof authorized another session generation", err)
			}
			wrong := baseline
			proof := *baseline.DefaultRewrite
			proof.Catalog = "different native prototype"
			wrong.DefaultRewrite = &proof
			if _, err = openGaussVerifiedDefaultRewrite(original, desired, wrong); !errors.Is(err, schemaddl.ErrConflict) {
				t.Fatal("rewrite proof accepted an unrelated catalog", err)
			}
			if _, err = c.ExecContext(ctx, "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			out := s.openGaussDesignApply(ctx, r)
			if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil || out.Rows != 3 {
				t.Fatal("verified native default transaction", out)
			}
			if err = openGaussVerifyDesignCatalog(original, desired, baseline, *out.Actual); err != nil {
				t.Fatal("complete default/TOAST catalog", err)
			}
			oldToast, nextToast := gaussDesignToastOID(t, original.Schema.OpenGauss), gaussDesignToastOID(t, out.Actual.Schema.OpenGauss)
			if oldToast == 0 || (oldToast != nextToast) != change.rewrite {
				t.Fatal("native TOAST identity transition", oldToast, nextToast, change.rewrite)
			}
			gaussDesignRequireStoredToast(t, out.Actual.Schema.OpenGauss)
			if change.rewrite {
				gaussDesignFixtureRoleSQL(t, fmt.Sprintf("DO $$BEGIN IF EXISTS(SELECT 1 FROM pg_class WHERE oid=%d) THEN RAISE EXCEPTION 'old TOAST survived heap rewrite'; END IF; END$$;", oldToast))
			}
			if change.rewrite {
				missingProof := baseline
				missingProof.DefaultRewrite = nil
				if err = openGaussVerifyDesignCatalog(original, desired, missingProof, *out.Actual); err == nil || !strings.Contains(err.Error(), "retained catalog identity changed") {
					t.Fatal("default rewrite without independent evidence was accepted", err)
				}
			}
			var duplicates, nulls, correct int
			if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE id=1 AND note=$1 AND payload=$2 AND amount=9007199254740993.12345678901234567890", text.String(), payload).Scan(&duplicates); err != nil || duplicates != 2 {
				t.Fatal("rewritten full values or duplicate count", duplicates, err)
			}
			if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE id=2 AND note IS NULL AND payload IS NULL AND amount IS NULL").Scan(&nulls); err != nil || nulls != 1 {
				t.Fatal("rewritten original NULL values", nulls, err)
			}
			query := "SELECT count(*) FROM " + table + " WHERE new_value::text=$1"
			if change.name == "cast_polygon" {
				query = "SELECT count(*) FROM " + table + " WHERE npoints(new_value)::text=$1"
			}
			if change.null {
				err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE new_value IS NULL").Scan(&correct)
			} else {
				err = db.QueryRowContext(ctx, query, change.value).Scan(&correct)
			}
			if err != nil || correct != 3 {
				t.Fatal("new default complete values", correct, err)
			}
			gaussDesignRejectCatalogCorruption(t, original, desired, baseline, *out.Actual)
		})
	}
}

func gaussDesignRequireStoredToast(t *testing.T, catalog *schemaddl.OpenGaussCatalog) {
	t.Helper()
	oid := gaussDesignToastOID(t, catalog)
	for _, entry := range catalog.Entries {
		if entry.Kind != "relation" || entry.OID != oid {
			continue
		}
		var row struct {
			Name string `json:"relname"`
		}
		if err := json.Unmarshal(entry.Data, &row); err != nil || row.Name == "" {
			t.Fatal("native TOAST relation name", err)
		}
		gaussDesignFixtureRoleSQL(t, "DO $$BEGIN IF NOT EXISTS(SELECT 1 FROM pg_toast."+quote(row.Name)+") THEN RAISE EXCEPTION 'fixture has no out-of-line values'; END IF; END$$;")
		return
	}
	t.Fatal("missing physical TOAST relation")
}

func TestOpenGaussDesignDefaultRewriteMixedNativeCastsCommitFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	note := strings.Repeat("完整值", 1001)
	if _, err := db.ExecContext(ctx, "UPDATE "+qualified(target.Object)+" SET note=$1", note); err != nil {
		t.Fatal(err)
	}
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
	out := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired})
	if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil || out.Rows != 1 {
		t.Fatal("mixed native conversion and TOAST transaction", out)
	}
	var numeric, interval, datetime, retained string
	var vertices int
	err := db.QueryRowContext(ctx, "SELECT cast0::text,cast1::text,cast2::text,npoints(cast3),note FROM "+qualified(target.Object)+" WHERE id=1").Scan(&numeric, &interval, &datetime, &vertices, &retained)
	if err != nil || numeric != "9007199254740993.12345678901234567890" || interval != "1 day 02:03:04.57" || datetime != "2026-11-07 12:35:00" || vertices != 12 || retained != note {
		t.Fatal("mixed native default/retained values", numeric, interval, datetime, vertices, len(retained), err)
	}
}

func TestOpenGaussDesignDefaultRewriteLossyValuesRestoreToastFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	for i, col := range desired.Clauses {
		if col.Kind == "column" && col.Name == "note" {
			desired.Clauses[i].SQL = "varchar(1)"
		}
	}
	desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:long", Kind: "column", Name: "long_value", SQL: "text DEFAULT '" + strings.Repeat("原生值", 300) + "'"})
	out := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired})
	if out.State != "rolled_back" || !errors.Is(out.Error, schemaddl.ErrDataChanged) || out.Verified || out.Actual != nil {
		t.Fatal("lossy rewrite was committed or left replayable", out)
	}
	actual := gaussDesignOriginal(t, s, target)
	if actual.Schema.Revision != original.Schema.Revision || gaussDesignToastOID(t, actual.Schema.OpenGauss) != gaussDesignToastOID(t, original.Schema.OpenGauss) {
		t.Fatal("rollback lost the original catalog/TOAST identity")
	}
	var note string
	if err := db.QueryRowContext(ctx, "SELECT note FROM "+qualified(target.Object)).Scan(&note); err != nil || note != "原始" {
		t.Fatal("rewrite rollback changed original values", note, err)
	}
}
