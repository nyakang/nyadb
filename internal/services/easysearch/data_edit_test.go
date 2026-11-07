package easysearch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestEasysearchDataEditRejectsTamperedModesAndCrossDocumentSnapshots(t *testing.T) {
	s, l, _ := fakeDataDocument(t)
	page, err := l.page(context.Background(), s.ProfileID(), "nyadb", "own", contract.DataOptions{Object: l.object, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	original := page.RowSnapshots[0]
	for _, change := range []func(*contract.RowSnapshot){
		func(v *contract.RowSnapshot) { *v.Fingerprints[3] = strings.Repeat("a", 64) },
		func(v *contract.RowSnapshot) {
			v.Metadata = strings.Replace(v.Metadata, "9007199254740993", "9007199254740994", 1)
		},
		func(v *contract.RowSnapshot) { v.Ref.Values[1] = []byte("different") },
		func(v *contract.RowSnapshot) { v.Mode = "intent" },
		func(v *contract.RowSnapshot) {
			v.Mode = "values"
			v.Original = []contract.Cell{{Kind: contract.JSON, Text: "{}"}}
		},
	} {
		bad := cloneDocumentSnapshot(original)
		change(bad)
		if _, err = l.prepareEdits("nyadb", []contract.DataEdit{documentEdit("update", bad, "{}")}); !errors.Is(err, ErrInvalid) {
			t.Fatal("tampered edit snapshot accepted", err)
		}
	}
	other := nativeDataLease(t, s, "index", "own")
	if _, err = other.prepareEdits("nyadb", []contract.DataEdit{documentEdit("delete", original, "")}); !errors.Is(err, ErrInvalid) {
		t.Fatal("original edit crossed document lease", err)
	}
	// The input snapshot and native byte buffers are detached before workers run.
	copy := cloneDocumentSnapshot(original)
	plans, err := l.prepareEdits("nyadb", []contract.DataEdit{documentEdit("update", copy, `{"n":9007199254740993}`)})
	if err != nil {
		t.Fatal(err)
	}
	copy.Ref.Values[1][0] = 'x'
	*copy.Fingerprints[3] = "changed"
	if string(plans[0].original.Ref.Values[1]) != "one" || *plans[0].original.Fingerprints[3] != *original.Fingerprints[3] {
		t.Fatal("prepared edit retained caller buffers")
	}
}

func TestEasysearchDataEditRejectsUnsupportedFieldsAndBoundedBatches(t *testing.T) {
	_, l, _ := fakeDataDocument(t)
	for _, value := range []contract.DataValue{
		{Text: "[]"}, {Text: `{"n":1,"n":2}`}, {Text: "{}", Null: true}, {Text: "{}", Default: true}, {Text: "{}", Binary: true}, {Text: "{}", Bytes: []byte("hidden")}, {Text: "{}", Kind: contract.Text, Binary: true},
		{Text: `{"n":"` + strings.Repeat("x", 1<<20) + `"}`},
	} {
		e := documentEdit("insert", nil, "{}")
		e.Values["document"] = value
		if _, err := l.prepareEdits("nyadb", []contract.DataEdit{e}); err == nil {
			t.Fatal("invalid complete source accepted")
		}
	}
	for _, field := range []string{"_index", "n", "_seq_no"} {
		e := documentEdit("insert", nil, "{}")
		e.Values[field] = contract.DataValue{Text: "value"}
		if _, err := l.prepareEdits("nyadb", []contract.DataEdit{e}); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("unsupported source field edit accepted", field, err)
		}
	}
	if _, err := l.prepareEdits("nyadb", make([]contract.DataEdit, 501)); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded batch accepted", err)
	}
	large := documentEdit("insert", nil, `{"n":"`+strings.Repeat("x", 900<<10)+`"}`)
	batch := make([]contract.DataEdit, 20)
	for i := range batch {
		batch[i] = large
	}
	if _, err := l.prepareEdits("nyadb", batch); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded total edit payload accepted", err)
	}
}
