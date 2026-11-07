package schemaddl

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func nativeDesignTestSnapshot(t *testing.T) Snapshot {
	t.Helper()
	s := nativeCopyTestSnapshot(t)
	s.OpenGauss.CanCreate = true
	s.Table, _, _ = s.OpenGauss.DesignTable()
	var err error
	s.Revision, err = s.OpenGauss.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOpenGaussDesignNativeDraftRetainsMetadataAndClones(t *testing.T) {
	s := nativeDesignTestSnapshot(t)
	comment := "native 中文 comment"
	s.Table.OpenGauss.Comment = &comment
	s.Table.OpenGauss.Parameters = []string{"orientation=row", "compression=no"}
	s.Table.Clauses[0].OpenGauss.Comment = &comment
	x := s.Clone()
	*x.Table.OpenGauss.Comment = "different"
	x.Table.OpenGauss.Parameters[0] = "orientation=column"
	*x.Table.Clauses[0].OpenGauss.Comment = "changed column"
	if *s.Table.OpenGauss.Comment != comment || *s.Table.Clauses[0].OpenGauss.Comment != comment || s.Table.OpenGauss.Parameters[0] != "orientation=row" {
		t.Fatal("native asynchronous draft shares mutable metadata")
	}
	if s.Table.Postgres != nil || s.Table.Clauses[0].Postgres != nil {
		t.Fatal("native catalog acquired PostgreSQL identity")
	}
}

func TestOpenGaussDesignPlansQualifiedFieldChangesAndSeparateRenames(t *testing.T) {
	s := nativeDesignTestSnapshot(t)
	d := s.Table.Clone()
	d.Clauses[0].SQL = "bigint NOT NULL DEFAULT 7"
	d.Clauses = append(d.Clauses, Clause{ID: "new:note", Kind: "column", Name: `q"; DROP TABLE other; --`, SQL: "text"})
	p, err := PlanOpenGauss(s, d, "public", true)
	if err != nil || p.Mode != "alter" {
		t.Fatal(p, err)
	}
	sql := strings.Join(p.SQL, "\n")
	if !strings.Contains(sql, `ALTER TABLE "public"."original" ALTER COLUMN "id" SET DEFAULT 7`) || !strings.Contains(sql, Quote(d.Clauses[1].Name)) || strings.Contains(sql, "CASCADE") {
		t.Fatal(sql)
	}
	d = s.Table.Clone()
	d.Name = "renamed"
	p, err = PlanOpenGauss(s, d, "public", true)
	if err != nil || p.Mode != "rename" || len(p.SQL) != 1 || p.RenameFrom != "original" || p.RenameTo != "renamed" {
		t.Fatal(p, err)
	}
	d.Clauses[0].SQL = "integer"
	if _, err = PlanOpenGauss(s, d, "public", true); err == nil {
		t.Fatal("rename mixed with a type conversion")
	}
	d = s.Table.Clone()
	d.Clauses[0].Name = "next_id"
	p, err = PlanOpenGauss(s, d, "public", true)
	if err != nil || p.Mode != "rename_column" || len(p.SQL) != 1 {
		t.Fatal(p, err)
	}
}

func TestOpenGaussDesignCreatesNativeTableAndRejectsUnconfirmedDrop(t *testing.T) {
	s := nativeDesignTestSnapshot(t)
	s.OpenGauss.RelationOID = 0
	s.OpenGauss.Entries = s.OpenGauss.Entries[:1]
	s.Exists = false
	s.Table, _, _ = s.OpenGauss.DesignTable()
	s.Revision, _ = s.OpenGauss.Fingerprint()
	d := s.Table.Clone()
	d.Name = "new_table"
	d.Clauses = []Clause{{ID: "new:id", Kind: "column", Name: "id", SQL: "bigserial NOT NULL"}, {ID: "new:pk", Kind: "constraint", Name: "new_pkey", SQL: `CONSTRAINT "new_pkey" PRIMARY KEY (id)`}}
	d.OpenGauss.Parameters = []string{"orientation=row", "compression=no"}
	p, err := PlanOpenGauss(s, d, "public", true)
	if err != nil || p.Mode != "create" || !strings.Contains(p.SQL[0], `CREATE TABLE "public"."new_table"`) || !strings.Contains(p.SQL[0], "WITH (orientation=row,compression=no)") {
		t.Fatal(p, err)
	}
	s.OpenGauss.CanCreate = false
	s.Revision, _ = s.OpenGauss.Fingerprint()
	if _, err = PlanOpenGauss(s, d, "public", true); err == nil {
		t.Fatal("new table accepted without native schema CREATE")
	}
	s = nativeDesignTestSnapshot(t)
	d = s.Table.Clone()
	d.Clauses = []Clause{{ID: "new:text", Kind: "column", Name: "text", SQL: "text"}}
	if _, err = PlanOpenGauss(s, d, "public", false); err == nil {
		t.Fatal("unconfirmed native column deletion planned")
	}
	p, err = PlanOpenGauss(s, d, "public", true)
	if err != nil || len(p.Dropped) != 1 || p.Dropped[0] != "id" || !strings.Contains(strings.Join(p.SQL, "\n"), "DROP COLUMN \"id\" RESTRICT") {
		t.Fatal(p, err)
	}
}

func TestOpenGaussDesignRejectsTamperingCrossProductAndUnsupportedNativeSemantics(t *testing.T) {
	s := nativeDesignTestSnapshot(t)
	x := s.Clone()
	x.Table.Clauses[0].SQL = "text"
	if _, err := PlanOpenGauss(x, s.Table, "public", true); !errors.Is(err, ErrConflict) {
		t.Fatal("caller draft became original evidence", err)
	}
	x = s.Clone()
	x.OpenGauss.Entries[1].Data = json.RawMessage(`{"replaced":true}`)
	if _, err := PlanOpenGauss(x, s.Table, "public", true); !errors.Is(err, ErrConflict) {
		t.Fatal("mutated native catalog became original", err)
	}
	for _, body := range []string{"bigint GENERATED ALWAYS AS IDENTITY", "bigint GENERATED ALWAYS AS (1) VIRTUAL", "text; DROP TABLE other", "text, hidden int"} {
		d := s.Table.Clone()
		d.Clauses[0].SQL = body
		if _, err := PlanOpenGauss(s, d, "public", true); err == nil {
			t.Fatal("unsupported/injected declaration planned", body)
		}
	}
	d := s.Table.Clone()
	d.OpenGauss.Parameters = []string{"orientation=column"}
	if _, err := PlanOpenGauss(s, d, "public", true); err == nil {
		t.Fatal("storage conversion silently accepted")
	}
	x = s.Clone()
	x.Engine = "postgresql"
	if _, err := PlanOpenGauss(x, s.Table, "public", true); err == nil {
		t.Fatal("native DDL accepted a PostgreSQL snapshot")
	}
}

func TestOpenGaussDesignMetadataUpdatesPreserveConstraintIdentityAndRejectUnboundIDs(t *testing.T) {
	s := nativeDesignTestSnapshot(t)
	d, err := s.OpenGauss.CopyDefinition()
	if err != nil {
		t.Fatal(err)
	}
	d.Constraints = []OpenGaussCopyObject{{OID: 7, Name: "id_positive", Kind: "c", SQL: "CHECK (id > 0)"}}
	for i := range s.OpenGauss.Entries {
		if s.OpenGauss.Entries[i].Kind == "copy_definition" {
			s.OpenGauss.Entries[i].Data, _ = json.Marshal(d)
		}
	}
	s.Revision, _ = s.OpenGauss.Fingerprint()
	s.Table, _, _ = s.OpenGauss.DesignTable()
	desired := s.Table.Clone()
	comment := "constraint comment"
	desired.Clauses[1].OpenGauss.Comment = &comment
	p, err := PlanOpenGauss(s, desired, "public", true)
	if err != nil || len(p.SQL) != 1 || !strings.HasPrefix(p.SQL[0], "COMMENT ON CONSTRAINT") {
		t.Fatal("metadata-only edit recreated a native constraint", p, err)
	}
	for _, mutate := range []func(*Table){
		func(t *Table) { t.Clauses[1].OpenGauss = nil },
		func(t *Table) { t.Clauses[1].OpenGauss.OID++ },
		func(t *Table) { t.Clauses[1].ID = "constraint:unknown" },
		func(t *Table) { t.Clauses[0].OpenGauss.Number++ },
	} {
		x := s.Table.Clone()
		mutate(&x)
		if _, err := PlanOpenGauss(s, x, "public", true); !errors.Is(err, ErrConflict) {
			t.Fatal("unbound native identity was planned", err)
		}
	}
}
