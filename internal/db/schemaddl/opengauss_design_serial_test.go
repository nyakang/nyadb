package schemaddl

import (
	"strings"
	"testing"
)

func TestOpenGaussDesignSerialUsesFreshOwnedSequenceAndExplicitBackfill(t *testing.T) {
	for _, typ := range []string{"bigserial", "serial", "smallserial"} {
		t.Run(typ, func(t *testing.T) {
			original := nativeDesignTestSnapshot(t)
			desired := original.Table.Clone()
			desired.Clauses = append(desired.Clauses, Clause{ID: "new:serial", Kind: "column", Name: "fresh", SQL: typ})
			plan, err := PlanOpenGauss(original, desired, "public", true)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(plan.SQL, "\n")
			if strings.Contains(joined, "ADD COLUMN \"fresh\" "+typ) || strings.Contains(joined, "setval(") || strings.Contains(joined, "IF NOT EXISTS") || !strings.Contains(joined, "OWNED BY \"public\".\"original\".\"fresh\"") || !strings.Contains(joined, "UPDATE \"public\".\"original\" SET \"fresh\"=DEFAULT") {
				t.Fatal("serial expansion reused a sequence or skipped ownership/backfill", joined)
			}
			setDefault, update, setNotNull := -1, -1, -1
			for i, statement := range plan.SQL {
				if strings.Contains(statement, "SET DEFAULT") {
					setDefault = i
				}
				if strings.HasPrefix(statement, "UPDATE ") {
					update = i
				}
				if strings.Contains(statement, "SET NOT NULL") {
					setNotNull = i
				}
			}
			if setDefault < 0 || setDefault >= update || update >= setNotNull {
				t.Fatal("serial values were not populated before nullability validation", plan)
			}
		})
	}
}

func TestOpenGaussDesignReplacingEveryFieldKeepsAColumnAndReleasesReusedName(t *testing.T) {
	original := nativeDesignTestSnapshot(t)
	desired := original.Table.Clone()
	desired.Clauses = []Clause{{ID: "new:id", Kind: "column", Name: "id", SQL: "bigserial"}}
	plan, err := PlanOpenGauss(original, desired, "public", true)
	if err != nil {
		t.Fatal(err)
	}
	add, drop := -1, -1
	for i, statement := range plan.SQL {
		if strings.Contains(statement, "ADD COLUMN") {
			add = i
		}
		if strings.Contains(statement, "DROP COLUMN") {
			drop = i
		}
	}
	if add < 0 || drop < add || !strings.Contains(plan.SQL[0], "RENAME COLUMN \"id\" TO \"__nyadb_retired_") || len(plan.Dropped) != 1 || plan.Dropped[0] != "id" {
		t.Fatal("replacement passed through a zero-column or duplicate-name state", plan)
	}
}

func TestOpenGaussDesignSerialNameDoesNotReuseAnEditorFieldID(t *testing.T) {
	original := nativeDesignTestSnapshot(t)
	col := Clause{ID: "new:1", Kind: "column", Name: "first", SQL: "bigserial"}
	first, err := gaussDesignAddField(original, col, "public", original.Table.Name)
	if err != nil {
		t.Fatal(err)
	}
	col.Name = "second"
	second, err := gaussDesignAddField(original, col, "public", original.Table.Name)
	if err != nil || first[0] == second[0] {
		t.Fatal("a reused editor ID selected an existing sequence", first, second, err)
	}
	col.Name = "first"
	changed := original.Clone()
	changed.Revision = "next captured revision"
	next, err := gaussDesignAddField(changed, col, "public", changed.Table.Name)
	if err != nil || first[0] == next[0] {
		t.Fatal("a later replacement selected the old sequence", first, next, err)
	}
}
