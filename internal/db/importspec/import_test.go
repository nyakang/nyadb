package importspec_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/importspec"
)

func TestEngineInferenceKeepsExactNumericAndBinaryTargets(t *testing.T) {
	source := []contract.DataImportColumn{{Name: "number", Type: "DECIMAL(65,30)"}, {Name: "bytes", Type: "LONGBLOB"}, {Name: "id", Type: "BIGINT"}}
	for _, engine := range []string{"sqlite", "postgresql"} {
		cols := importspec.AdaptInference(engine, source)
		want := "TEXT"
		if engine == "postgresql" {
			want = "NUMERIC"
		}
		if cols[0].Type != want || source[0].Type != "DECIMAL(65,30)" {
			t.Fatal(cols, source)
		}
		meta, err := importspec.Columns(engine, cols)
		if err != nil || meta[1].ValueKind != contract.Binary {
			t.Fatal(meta, err)
		}
		if _, err = importspec.DDL(engine, "target", "quoted\"table", cols); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImportRejectsGeneratedFieldsAndPartialKeys(t *testing.T) {
	cols := []contract.StructureColumn{{Name: "id", Type: "INTEGER"}, {Name: "generated", Generated: true}}
	for _, engine := range []string{"postgresql", "sqlite"} {
		for _, request := range []contract.DataImportRequest{
			{Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"generated": {Text: "1"}}}}},
			{Mode: "update", MatchColumns: []string{"id"}, Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Null: true}}}}},
			{Mode: "delete"},
		} {
			if err := importspec.Validate(engine, cols, request); err == nil {
				t.Fatal("invalid import accepted", engine, request)
			}
		}
	}
}
