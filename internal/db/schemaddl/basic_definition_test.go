package schemaddl

import (
	"strings"
	"testing"
)

func TestBasicIndexKeepsQualifiedIdentityAndColumnOrder(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgresql"} {
		f, err := ParseBasicDefinition(engine, "index", `CREATE UNIQUE INDEX "idx" ON "main"."t" ("a", "b" DESC)`)
		if err != nil {
			t.Fatal(err)
		}
		f.Columns[0].Name = `a"quoted`
		raw, err := f.SQL(engine)
		if err != nil || !strings.Contains(raw, `("a""quoted", "b" DESC)`) {
			t.Fatal(raw, err)
		}
		parsed, err := ParseBasicDefinition(engine, "index", raw)
		if err != nil || parsed.Schema != "main" || parsed.Table != "t" || !parsed.Unique {
			t.Fatal(parsed, err)
		}
	}
}

func TestBasicDefinitionsDoNotDiscardAdvancedOptions(t *testing.T) {
	for _, raw := range []string{`CREATE INDEX i ON t (lower(a))`, `CREATE INDEX i ON t(a) WHERE a>0`, `CREATE INDEX i ON t(a COLLATE nocase)`, `CREATE INDEX i ON t(a); DROP TABLE t`, `CREATE INDEX i ON t(a) /* retained */`} {
		if _, err := ParseBasicDefinition("sqlite", "index", raw); err == nil {
			t.Fatal("advanced SQL became editable ordinary form", raw)
		}
	}
	f, err := ParseBasicDefinition("postgresql", "constraint", `CONSTRAINT fk FOREIGN KEY (a,b) REFERENCES other.parent(x,y) ON DELETE CASCADE ON UPDATE SET NULL`)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.SQL("postgresql")
	if err != nil || !strings.Contains(raw, `REFERENCES "other"."parent" ("x", "y")`) {
		t.Fatal(raw, err)
	}
	if _, err := f.SQL("sqlite"); err == nil {
		t.Fatal("SQLite accepted cross-schema foreign key")
	}
	f.ReferencedColumns = f.ReferencedColumns[:1]
	if _, err := f.SQL("postgresql"); err == nil {
		t.Fatal("mismatched foreign key accepted")
	}
}

func TestPostgresOrdinaryBtreeIndexKeepsMethodAndRejectsAdvancedClauses(t *testing.T) {
	f, e := ParseBasicDefinition("postgresql", "index", `CREATE INDEX idx ON public.t USING btree (a DESC,b)`)
	if e != nil {
		t.Fatal(e)
	}
	f.Schema = "other"
	raw, e := f.SQL("postgresql")
	if e != nil || !strings.Contains(raw, `ON "other"."t" USING btree ("a" DESC, "b")`) {
		t.Fatal(raw, e)
	}
	for _, raw := range []string{`CREATE INDEX idx ON t USING hash(a)`, `CREATE INDEX idx ON t USING btree(a) INCLUDE(b)`, `CREATE INDEX idx ON t(a NULLS FIRST)`, `CONSTRAINT fk FOREIGN KEY(a) REFERENCES p(a) DEFERRABLE`} {
		kind := "index"
		if strings.HasPrefix(raw, "CONSTRAINT") {
			kind = "constraint"
		}
		if _, e := ParseBasicDefinition("postgresql", kind, raw); e == nil {
			t.Fatal("advanced property was discarded", raw)
		}
	}
}

func TestBasicPostgresNamesFoldOnlyUnquotedIdentifiers(t *testing.T) {
	f, e := ParseBasicDefinition("postgresql", "index", `CREATE INDEX IDX ON PUBLIC.T (ID, "ExactCase")`)
	if e != nil || f.Name != "idx" || f.Schema != "public" || f.Table != "t" || f.Columns[0].Name != "id" || f.Columns[1].Name != "ExactCase" {
		t.Fatal(f, e)
	}
}
