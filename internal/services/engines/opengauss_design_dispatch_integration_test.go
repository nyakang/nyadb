package engines

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignDispatchCoreClassesCollationsAndRejectsForeignNamesFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:label", Kind: "column", Name: "label", SQL: `text COLLATE PG_CATALOG."C" DEFAULT '中文'`})
	desired.Definitions = append(desired.Definitions, schemaddl.Definition{Name: "by_note", Kind: "index", SQL: "CREATE INDEX by_note ON " + qualified(target.Object) + ` USING btree(note COLLATE pg_catalog."C" pg_catalog.text_ops)`})
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
		t.Fatal("native index callbacks and collation", err)
	}
	for _, definition := range []string{
		"CREATE INDEX by_note ON " + qualified(target.Object) + " USING btree(note " + quote(target.Object.Schema) + ".foreign_class)",
		"CREATE INDEX by_note ON " + qualified(target.Object) + " USING btree(note COLLATE " + quote(target.Object.Schema) + ".foreign_collation)",
		"CREATE INDEX by_note ON " + qualified(target.Object) + " USING missing_method(note)",
	} {
		bad := desired.Clone()
		bad.Definitions[len(bad.Definitions)-1].SQL = definition
		if err = openGaussCheckDesignDeclaration(ctx, c, bad, target.Object.Schema); !errors.Is(err, errOpenGaussDesignDeclaration) {
			t.Fatal("unverified native dispatch name was authorized", definition, err)
		}
	}
	// A core SET placeholder in an unrelated >= overload must not authorize
	// storing SET values or using the polymorphic type as a declared field.
	polymorphic := desired.Clone()
	polymorphic.Clauses = append(polymorphic.Clauses, schemaddl.Clause{ID: "new:set", Kind: "column", Name: "set_value", SQL: "pg_catalog.anyset"})
	if err = openGaussCheckDesignDeclaration(ctx, c, polymorphic, target.Object.Schema); !errors.Is(err, errOpenGaussDesignDeclaration) {
		t.Fatal("polymorphic operator signature authorized a SET field", err)
	}
	if _, err = c.ExecContext(ctx, "SET LOCAL search_path=public,pg_catalog"); err != nil {
		t.Fatal(err)
	}
	if err = openGaussCheckDesignDeclaration(ctx, c, desired, target.Object.Schema); !errors.Is(err, errOpenGaussDesignDeclaration) {
		t.Fatal("declaration allowed another namespace to shadow core callbacks", err)
	}
}
