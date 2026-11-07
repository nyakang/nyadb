package schemaddl

import (
	"errors"
	"testing"
)

func TestOpenGaussNullabilityRuleDistinguishesExistingColumnStoreFieldsAndNewFields(t *testing.T) {
	s := nativeDesignTestSnapshot(t)
	id := s.Table.Columns()[0].ID
	for _, params := range [][]string{nil, {"orientation=row"}, {"compression=no", "orientation = COLUMN"}} {
		s.Table.OpenGauss.Parameters = params
		err := OpenGaussNotNullEditable(s, id)
		if (len(params) > 1) != errors.Is(err, ErrOpenGaussNotNull) {
			t.Fatal("native existing-field nullability used the wrong storage rule", params, err)
		}
		if err := OpenGaussNotNullEditable(s, "new:field"); err != nil {
			t.Fatal("new column-store fields could not declare nullability", err)
		}
	}
	s.Exists = false
	if err := OpenGaussNotNullEditable(s, id); err != nil {
		t.Fatal("new table was treated as an existing column-store table", err)
	}
	s.Engine = "postgresql"
	if err := OpenGaussNotNullEditable(s, id); !errors.Is(err, ErrConflict) {
		t.Fatal("native rule accepted another product's snapshot", err)
	}
}

func TestOpenGaussPlannerRejectsExistingColumnStoreNullabilityBeforeDDL(t *testing.T) {
	s := nativeDesignTestSnapshot(t)
	for i := range s.OpenGauss.Entries {
		if s.OpenGauss.Entries[i].Kind == "relation" {
			s.OpenGauss.Entries[i].Data = []byte(`{"relname":"original","relkind":"r","relpersistence":"p","reloptions":["orientation=column"]}`)
		}
	}
	var err error
	s.Revision, err = s.OpenGauss.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	s.Table, _, err = s.OpenGauss.DesignTable()
	if err != nil {
		t.Fatal(err)
	}
	desired := s.Table.Clone()
	f, err := ParseOpenGaussField(desired.Clauses[0].SQL)
	if err != nil {
		t.Fatal(err)
	}
	f.NotNull = !f.NotNull
	desired.Clauses[0].SQL, err = f.SQL()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PlanOpenGauss(s, desired, s.OpenGauss.Namespace, true); !errors.Is(err, ErrOpenGaussNotNull) {
		t.Fatal("planner did not return the designer's native read-only reason", err)
	}
}

func TestOpenGaussRebindingClonesNativeCommentProperties(t *testing.T) {
	comment := "retained"
	definition := Definition{Kind: "index", Name: "idx", SQL: `CREATE INDEX idx ON main.t(id)`, OpenGauss: &OpenGaussDefinitionProperties{OID: 20, Comment: &comment}}
	rebound, err := RebindPostgreSQLDefinition(definition, "main", "t", "probe", "private_idx")
	if err != nil {
		t.Fatal(err)
	}
	*rebound.OpenGauss.Comment = "private"
	if *definition.OpenGauss.Comment != "retained" || rebound.OpenGauss.OID != definition.OpenGauss.OID {
		t.Fatal("private index rebind aliased original native metadata")
	}
	clause := Clause{Kind: "constraint", Name: "fk", SQL: `CONSTRAINT fk FOREIGN KEY(id) REFERENCES main.t(id)`, OpenGauss: &OpenGaussColumnProperties{OID: 21, Comment: &comment}}
	bound, err := RebindPostgreSQLConstraint(clause, "main", "t", "probe", "private_fk")
	if err != nil {
		t.Fatal(err)
	}
	*bound.OpenGauss.Comment = "private constraint"
	if *clause.OpenGauss.Comment != "retained" || bound.OpenGauss.OID != clause.OpenGauss.OID {
		t.Fatal("private constraint rebind aliased original native metadata")
	}
}
