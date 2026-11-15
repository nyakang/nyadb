package rqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func dataUnitTable(s *Session) (tableSnapshot, *contract.RowSnapshot) {
	t := tableSnapshot{version: 1, key: []string{"id"}, rowIDKey: "id", structure: &contract.TableStructure{
		Object: contract.ObjectRef{ProfileID: s.ProfileID(), Catalog: "main", Kind: "table", Name: "original"}, Name: "original", Database: "main", Kind: "table", Editable: true,
		Columns: []contract.StructureColumn{{Name: "id", Type: "INTEGER", ValueKind: contract.Integer}, {Name: "note", Type: "TEXT", Nullable: true, ValueKind: contract.Text}, {Name: "raw", Type: "BLOB", Nullable: true, ValueKind: contract.Binary}},
	}}
	row := dataRow{cells: []contract.Cell{{Kind: contract.Integer, Text: "1"}, {Kind: contract.Text, Text: "a\x00b"}, {Kind: contract.Binary, Bytes: []byte{0, 255}}}}
	for _, v := range row.cells {
		typ, raw := string(v.Kind), []byte(v.Text)
		if v.Kind == contract.Binary {
			typ, raw = "blob", v.Bytes
		}
		h := sha256.Sum256(append([]byte(typ+"\x00"), raw...))
		fp := hex.EncodeToString(h[:])
		row.fp = append(row.fp, &fp)
	}
	return t, s.dataSnapshot(t, row)
}

func TestDataBatchRejectsMalformedOriginalSnapshotsAndUnsupportedWrites(t *testing.T) {
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) { t.Error("pure validation performed I/O") }, nil)
	table, original := dataUnitTable(s)
	for _, change := range []func(*contract.RowSnapshot){
		func(o *contract.RowSnapshot) { o.Mode = "unknown" },
		func(o *contract.RowSnapshot) { o.Fingerprints = o.Fingerprints[:1] },
		func(o *contract.RowSnapshot) { o.Columns[0] = "wrong" },
		func(o *contract.RowSnapshot) { o.Ref.Object.ProfileID++ },
		func(o *contract.RowSnapshot) { o.Ref.TypedValues[0].Truncated = true },
		func(o *contract.RowSnapshot) { o.Ref.Values[0] = []byte("another identity") },
		func(o *contract.RowSnapshot) { fp := strings.Repeat("q", 64); o.Fingerprints[0] = &fp },
		func(o *contract.RowSnapshot) {
			o.Mode = "values"
			o.Original = []contract.Cell{{Kind: contract.Integer, Text: "1"}}
		},
	} {
		copy := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{{Kind: "delete", Original: original}}}).Edits
		change(copy[0].Original)
		if _, err := s.dataEditBatch(table, copy); err == nil {
			t.Fatal("malformed original payload was admitted")
		}
	}
	for _, edit := range []contract.DataEdit{
		{Kind: "copy", Original: original},
		{Kind: "insert", Original: original},
		{Kind: "delete", Original: original, Values: map[string]contract.DataValue{"note": {Text: "unexpected"}}},
		{Kind: "update", Original: original},
		{Kind: "update", Original: original, Values: map[string]contract.DataValue{"note": {Kind: contract.Binary, Text: "ambiguous", Bytes: []byte{1}}}},
		{Kind: "update", Original: original, Values: map[string]contract.DataValue{"note": {Kind: contract.Floating, Text: "NaN"}}},
		{Kind: "update", Original: original, Values: map[string]contract.DataValue{"note": {Null: true, Default: true}}},
	} {
		if _, err := s.dataEditBatch(table, []contract.DataEdit{edit}); err == nil {
			t.Fatal("unsupported typed edit was admitted")
		}
	}
	valid := []contract.DataEdit{{Kind: "update", Original: original, Values: map[string]contract.DataValue{"note": {Text: "complete"}}}}
	first, err := s.dataEditBatch(table, valid)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.dataEditBatch(table, valid)
	if err != nil {
		t.Fatal(err)
	}
	if first.statements[0] == second.statements[0] {
		t.Fatal("separate original transactions reused a TEMP guard identity")
	}
}

func TestNativeDataReceiptMustIncludeEveryOriginalGuardAndMutation(t *testing.T) {
	for _, test := range []struct {
		name, body string
		want       error
		uncertain  bool
		status     int
	}{
		{"complete", `{"results":[{}, {"rows_affected":1}, {"rows_affected":1,"last_insert_id":9007199254740993}, {"rows_affected":1}, {}]}`, nil, false, 200},
		{"missing_guard", `{"results":[{}, {}, {"rows_affected":1,"last_insert_id":1}, {"rows_affected":1}, {}]}`, contract.ErrDataCommitUncertain, true, 200},
		{"missing_mutation", `{"results":[{}, {"rows_affected":1}, {}, {"rows_affected":1}, {}]}`, contract.ErrDataCommitUncertain, true, 200},
		{"missing_generated_identity", `{"results":[{}, {"rows_affected":1}, {"rows_affected":1}, {"rows_affected":1}, {}]}`, contract.ErrDataCommitUncertain, true, 200},
		{"negative_count", `{"results":[{}, {"rows_affected":1}, {"rows_affected":-1,"last_insert_id":1}, {"rows_affected":1}, {}]}`, contract.ErrDataCommitUncertain, true, 200},
		{"rolled_back", `{"results":[{}, {"rows_affected":1}, {"rows_affected":1,"last_insert_id":1}, {"error":"CHECK constraint failed: ok=1"}]}`, contract.ErrDataConflict, false, 200},
		{"top_level_commit_error", `{"error":"commit failure"}`, contract.ErrDataCommitUncertain, true, 200},
		{"denied", `permission denied`, nil, false, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/db/request" || !r.URL.Query().Has("transaction") || r.URL.Query().Get("retries") != "0" {
					t.Error("original atomic transaction path changed")
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}, nil)
			table, _ := dataUnitTable(s)
			edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "9007199254740993"}}}}
			batch, err := s.dataEditBatch(table, edits)
			if err != nil {
				t.Fatal(err)
			}
			out, err := s.executeDataBatch(context.Background(), table, edits, batch)
			if test.want != nil && !errors.Is(err, test.want) || test.uncertain != out.CommitUncertain || test.uncertain != out.Incomplete {
				t.Fatalf("original result classification: %v (%+v)", err, out)
			}
			if test.status == 200 && out.NativeJSON != test.body {
				t.Fatal("exact original native receipt was reconstructed")
			}
			if test.name == "complete" {
				if err != nil || !out.CommitConfirmed || out.Applied != 1 || out.Outcomes[0].GeneratedID != 9007199254740993 {
					t.Fatal("complete original commit or large identifier lost")
				}
			} else if out.CommitConfirmed || out.Applied != 0 {
				t.Fatal("incomplete/failed original transaction counted as committed")
			}
		})
	}
}

func TestDataSaveCopiesOriginalValuesFingerprintsAndBoundsBeforeAdmission(t *testing.T) {
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) { t.Error("bounded or blocked edit dispatched") }, nil)
	table, original := dataUnitTable(s)
	l, err := s.ReserveDataDocument(table.structure.Object)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	d := l.(*dataDocument)
	originalResult := &contract.DataEditResult{CommitUncertain: true, Incomplete: true, NativeJSON: `{"original":"unknown"}`, Outcomes: []contract.DataEditOutcome{{Kind: "delete", Original: original}}}
	d.unknown = copyDataResult(originalResult)
	*original.Fingerprints[0] = "changed"
	original.Ref.Values[0][0] = '2'
	if d.unknown.Outcomes[0].Original.Ref.Values[0][0] != '1' || *d.unknown.Outcomes[0].Original.Fingerprints[0] == "changed" {
		t.Fatal("unknown original receipt retained caller-owned keys or fingerprints")
	}
	blocked, err := d.Runtime().Saver(context.Background(), s.ProfileID(), "main", "original", []contract.DataEdit{{Kind: "insert"}})
	if !errors.Is(err, contract.ErrDataCommitUncertain) || blocked.NativeJSON != originalResult.NativeJSON {
		t.Fatal("unknown original write was not retained")
	}
	blocked.Outcomes[0].Original.Ref.TypedValues[0].Text = "changed"
	if d.unknown.Outcomes[0].Original.Ref.TypedValues[0].Text != "1" {
		t.Fatal("published unknown result aliases the provider's original cache")
	}
	if err := dataEditBounds(make([]contract.DataEdit, contract.DataMaxPageSize+1)); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("oversized edit batch was admitted")
	}
	if _, err := json.Marshal(blocked); err != nil {
		t.Fatal(err)
	}
}
