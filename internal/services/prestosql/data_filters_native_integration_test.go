package prestosql

import (
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// This separate real fixture verifies every advertised filter mode and numeric
// ordering, including 2 versus 10 so a lexical sort cannot pass accidentally.
func TestPrestoSQLPrivateFiltersNativeAllModesAndNumericOrder(t *testing.T) {
	f, p, c, _ := nativeProvider(t)
	s, q, ctx := nativeDataSession(t, p, c)
	const name = "filter_data65"
	queryNative(t, q, "", `CREATE TABLE memory.default.filter_data65 (id bigint, note varchar)`)
	_, r, label := nativeDataDocument(t, s, name)
	written := nativeDataInsert(t, ctx, r, label, name, []contract.DataEdit{
		{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "1"}, "note": {Kind: contract.Null}}},
		{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "2"}, "note": {Kind: contract.Text, Text: "b'quote"}}},
		{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "10"}, "note": {Kind: contract.Text, Text: "a%b"}}},
		{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "20"}, "note": {Kind: contract.Text, Text: "a_b"}}},
	})
	ids := func(page *contract.DataPage) []string {
		out := []string{}
		for _, row := range page.Rows {
			out = append(out, row[0].Value.Text)
		}
		return out
	}
	var proofs []map[string]any
	for _, test := range []struct {
		mode, column, value string
		want                []string
	}{
		{"equal", "id", "10", []string{"10"}},
		{"not_equal", "id", "10", []string{"1", "2", "20"}},
		{"less", "id", "10", []string{"1", "2"}},
		{"greater", "id", "2", []string{"10", "20"}},
		{"like", "note", "a%", []string{"10", "20"}},
		{"is_null", "note", "", []string{"1"}},
		{"is_not_null", "note", "", []string{"2", "10", "20"}},
	} {
		page, err := r.AdvancedLoader(ctx, 65, label, name, contract.DataOptions{Limit: 10, FilterColumn: test.column, FilterMode: test.mode, FilterValue: test.value, SortColumn: "id"})
		if err != nil || page == nil || !reflect.DeepEqual(ids(page), test.want) {
			t.Fatal("original filter mode", test.mode, err, page)
		}
		proofs = append(proofs, map[string]any{"mode": test.mode, "page": page})
	}
	first, err := r.AdvancedLoader(ctx, 65, label, name, contract.DataOptions{Limit: 2, SortColumn: "id"})
	if err != nil || !first.HasMore || !reflect.DeepEqual(ids(first), []string{"1", "2"}) {
		t.Fatal("native numeric first page", err)
	}
	second, err := r.AdvancedLoader(ctx, 65, label, name, contract.DataOptions{Limit: 2, Offset: 2, SortColumn: "id"})
	if err != nil || second.HasMore || !reflect.DeepEqual(ids(second), []string{"10", "20"}) {
		t.Fatal("native numeric second page", err)
	}
	descending, err := r.AdvancedLoader(ctx, 65, label, name, contract.DataOptions{Limit: 10, SortColumn: "id", SortDescending: true})
	if err != nil || !reflect.DeepEqual(ids(descending), []string{"20", "10", "2", "1"}) {
		t.Fatal("native numeric descending order", err)
	}
	multiple, err := r.AdvancedLoader(ctx, 65, label, name, contract.DataOptions{Limit: 10, Filters: []contract.DataFilterRule{{Column: "id", Mode: "greater", Value: "2"}, {Column: "note", Mode: "equal", Value: "a_b"}}})
	if err != nil || !reflect.DeepEqual(ids(multiple), []string{"20"}) {
		t.Fatal("native combined typed filters", err)
	}
	providerProof(t, f, "data-filters.json", map[string]any{"write": written, "filter_modes": proofs, "first": first, "second": second, "descending": descending, "combined": multiple, "explicit_null": first.Rows[0][1]})
	queryNative(t, q, "", `DROP TABLE memory.default.filter_data65`)
	_ = s.Close()
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
}
