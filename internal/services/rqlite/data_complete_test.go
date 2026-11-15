package rqlite

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func completeReadResult(columns int, rows ...[]any) nativeResult {
	r := nativeResult{Columns: make([]string, columns), Types: make([]string, columns)}
	for i := range columns {
		r.Columns[i], r.Types[i] = fmt.Sprint(i), "integer"
	}
	for _, row := range rows {
		var values []json.RawMessage
		for _, value := range row {
			b, _ := json.Marshal(value)
			values = append(values, b)
		}
		r.Values = append(r.Values, values)
	}
	return r
}

func TestCompleteExportRejectsMalformedAndInventedBoundedReceipts(t *testing.T) {
	for _, test := range []struct {
		name string
		row  []any
		want error
	}{
		{"null_payload", []any{0, 0, "null", "AA", nil}, errIncomplete},
		{"wrong_position", []any{1, 0, "integer", "31", 1}, errIncomplete},
		{"odd_hex", []any{0, 0, "blob", "A", 1}, errIncomplete},
		{"fractional_length", []any{0, 0, "blob", "AA", 1.5}, errIncomplete},
		{"invalid_text", []any{0, 0, "text", "80", 1}, contract.ErrUnsupported},
		{"missing_small_payload", []any{0, 0, "text", nil, 1}, errIncomplete},
		{"nonfinite_real", []any{0, 0, "real", "496E66", 3}, contract.ErrUnsupported},
		{"oversized_field", []any{0, 0, "blob", nil, contract.DataExportFieldMaxBytes + 1}, errExportValue},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, err := decodeExportRows(receipt{Results: []nativeResult{completeReadResult(5, test.row)}}, 1, 1)
			if !errors.Is(err, test.want) || out != nil {
				t.Fatal("malformed complete export published values", err)
			}
		})
	}
	rows, err := decodeExportRows(receipt{Results: []nativeResult{completeReadResult(5, []any{0, 0, "null", "", nil}, []any{0, 1, "blob", "00FF", 2}, []any{0, 2, "text", "610062", 3})}}, 3, 1)
	if err != nil || !rows[0][0].Null || len(rows[0][1].Bytes) != 2 || rows[0][2].Text != "a\x00b" {
		t.Fatal("valid typed complete export rejected", err)
	}
}

func nonnumericAnalysisReceipt() receipt {
	return receipt{Results: []nativeResult{
		completeReadResult(5, []any{2, 1, 0, 0, 0}),
		completeReadResult(4, []any{1, 1, 1, 1}),
		completeReadResult(3), completeReadResult(3),
		completeReadResult(4, []any{"text", "74657874", 4, 1}),
	}}
}

func TestCompleteAnalysisValidatesEmptyNumericReceiptsAndFullCounts(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*receipt)
	}{
		{"missing_result", func(r *receipt) { r.Results = r.Results[:4] }},
		{"invented_central", func(r *receipt) { r.Results[2] = completeReadResult(3, []any{0, "integer", "31"}) }},
		{"invented_bins", func(r *receipt) { r.Results[3] = completeReadResult(3, []any{0, 0, 1}) }},
		{"missing_distribution", func(r *receipt) { r.Results[4] = completeReadResult(4) }},
		{"excess_frequency", func(r *receipt) { r.Results[4] = completeReadResult(4, []any{"text", "74657874", 4, 2}) }},
		{"nonfinite_nonnumeric", func(r *receipt) { r.Results[0] = completeReadResult(5, []any{2, 1, 0, 0, 1}) }},
		{"imaginary_mode", func(r *receipt) { r.Results[1] = completeReadResult(4, []any{1, 1, 1, 0}) }},
		{"missing_group", func(r *receipt) { r.Results[1] = completeReadResult(4, []any{0, 0, 0, 0}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := nonnumericAnalysisReceipt()
			test.change(&r)
			if _, err := decodeColumnAnalysis(r, contract.StructureColumn{Name: "v"}); !errors.Is(err, errIncomplete) {
				t.Fatal("malformed native analysis accepted", err)
			}
		})
	}
	got, err := decodeColumnAnalysis(nonnumericAnalysisReceipt(), contract.StructureColumn{Name: "v"})
	if err != nil || got.Rows != 2 || got.Null != 1 || got.Numeric != 0 || got.ModeCount != 2 || got.Mean != "" {
		t.Fatal("complete null/text analysis changed", err)
	}
}

func TestExactNumericBinsPreserveDyadicsAndRejectDuplicateOrUnboundedCoefficients(t *testing.T) {
	got, err := decodeNumericBins(receipt{Results: []nativeResult{completeReadResult(3, []any{0, -1, 1}, []any{1, -2, 1})}})
	if err != nil || got[0].Cmp(big.NewRat(1, 2)) != 0 || got[1].Cmp(big.NewRat(1, 4)) != 0 {
		t.Fatal("exact dyadic bins changed", err)
	}
	for _, rows := range [][]any{{2, 0, 1}, {0, -2401, 1}, {1, 2201, 1}, {0, 0, 1.5}} {
		if _, err := decodeNumericBins(receipt{Results: []nativeResult{completeReadResult(3, rows)}}); !errors.Is(err, errIncomplete) {
			t.Fatal("malformed numeric bin accepted", err)
		}
	}
	if _, err := decodeNumericBins(receipt{Results: []nativeResult{completeReadResult(3, []any{0, 0, 1}, []any{0, 0, 2})}}); !errors.Is(err, errIncomplete) {
		t.Fatal("duplicate original coefficient accepted")
	}
	denominator := new(big.Int).Lsh(big.NewInt(3), 1074)
	value := new(big.Rat).SetFrac(big.NewInt(1), denominator)
	if display := numericRatDisplay(value); display == "0" || !strings.Contains(display, "e-") {
		t.Fatal("nonzero recurring subnormal display changed", display)
	}
}

func TestTypedCursorRejectsMalformedSignedBoundariesAndOwnsCopiedInputs(t *testing.T) {
	s := mockSession(t, func(http.ResponseWriter, *http.Request) { t.Error("pure cursor validation performed I/O") }, nil)
	table, _ := dataUnitTable(s)
	options := contract.DataOptions{UseKeyset: true, SortColumn: "note"}
	order, err := dataOrder(table, options)
	if err != nil {
		t.Fatal(err)
	}
	base := contract.DataCursor{Database: "main", Table: "original", Columns: []string{"note", "id"}, Descending: []bool{false, false}, Values: [][]byte{[]byte(`{"kind":"text","text":"a"}`), []byte(`{"kind":"integer","text":"1"}`)}}
	for _, value := range []string{`{`, `{"kind":"integer","text":"9223372036854775808"}`, `{"kind":"floating","text":"Inf"}`, `{"kind":"text","text":"a","truncated":true}`, `{"kind":"null","text":"invented"}`} {
		c := base
		c.Values = [][]byte{[]byte(value), base.Values[1]}
		c.RuleSignature = s.cursorMAC("original-scope", table, options, order, c)
		o := options
		o.After = &c
		if _, err := s.cursorWhere("original-scope", table, o, order, false); err == nil {
			t.Fatal("malformed provider boundary accepted")
		}
	}
	base.RuleSignature = s.cursorMAC("original-scope", table, options, order, base)
	options.After = &base
	captured, err := copyDataOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	base.Values[0][0] = 'x'
	base.Columns[0] = "other"
	if captured.After.Values[0][0] != '{' || captured.After.Columns[0] != "note" {
		t.Fatal("cursor capture shares caller bytes or columns")
	}
	if _, err := s.cursorWhere("other-scope", table, captured, order, false); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("another original document accepted cursor", err)
	}
	if _, err := copyDataFields([]string{strings.Repeat("a", 4097)}); err == nil {
		t.Fatal("unbounded field name was copied")
	}
}
