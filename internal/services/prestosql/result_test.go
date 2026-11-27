package prestosql

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

func successful(raw string) jdbcplugin.Receipt {
	return jdbcplugin.Receipt{Result: json.RawMessage(raw), Dispatched: true, ExecutorAcknowledged: true}
}

func TestOriginalQueryResultKeepsNumericLexemesNullAndTextIdentity(t *testing.T) {
	r := successful(`{"columns":["id","amount","payload","note"],"rows":[[9223372036854775807,9007199254740993.1234567890123456789,"0x00ff80",null]],"large_value_cells":[],"affected_rows":0,"execution_time_ms":7,"truncated":false}`)
	got, err := decodeResult(r)
	if err != nil || len(got.rows) != 1 || len(got.columns) != 4 || got.duration != 7*time.Millisecond || got.truncated {
		t.Fatal("original result lost", err)
	}
	for i, want := range []string{"9223372036854775807", "9007199254740993.1234567890123456789", `"0x00ff80"`, "null"} {
		if string(got.rows[0][i]) != want {
			t.Fatal("original value converted", i, string(got.rows[0][i]))
		}
	}
	for i := range r.Result {
		r.Result[i] = 'x'
	}
	if string(got.rows[0][0]) != "9223372036854775807" {
		t.Fatal("decoded result aliases caller receipt")
	}
}

func TestOriginalQueryResultRejectsAmbiguousOrIncompleteShapes(t *testing.T) {
	valid := `{"columns":["x"],"rows":[[1]],"large_value_cells":[],"affected_rows":0,"execution_time_ms":1,"truncated":false}`
	for _, mutation := range [][2]string{
		{`"columns":["x"]`, `"columns":null`},
		{`"columns":["x"]`, `"columns":[null]`},
		{`"rows":[[1]]`, `"rows":null`},
		{`"rows":[[1]]`, `"rows":[null]`},
		{`"rows":[[1]]`, `"rows":[[]]`},
		{`"rows":[[1]]`, `"rows":[[1,2]]`},
		{`"rows":[[1]]`, `"rows":[[1]],"rows":[[2]]`},
		{`"large_value_cells":[]`, `"large_value_cells":[{"row_index":0,"column_index":0,"value_ref":"foreign"}]`},
		{`"affected_rows":0`, `"affected_rows":1`},
		{`"affected_rows":0`, `"affected_rows":null`},
		{`"execution_time_ms":1`, `"execution_time_ms":null`},
		{`"execution_time_ms":1`, `"execution_time_ms":-1`},
		{`"execution_time_ms":1`, `"execution_time_ms":9223372036854775807`},
		{`"truncated":false`, `"truncated":null`},
		{`"truncated":false`, `"truncated":"false"`},
		{`"truncated":false`, `"unknown":false`},
	} {
		t.Run(mutation[1], func(t *testing.T) {
			if _, err := decodeResult(successful(strings.Replace(valid, mutation[0], mutation[1], 1))); err == nil {
				t.Fatal("malformed result accepted")
			}
		})
	}
	for _, raw := range []string{valid + `{}`, `{}`, `null`} {
		if _, err := decodeResult(successful(raw)); err == nil {
			t.Fatal("unrecognized envelope accepted")
		}
	}
	for _, mutate := range []func(*jdbcplugin.Receipt){
		func(r *jdbcplugin.Receipt) { r.Dispatched = false },
		func(r *jdbcplugin.Receipt) { r.ExecutorAcknowledged = false },
		func(r *jdbcplugin.Receipt) { r.RemoteFailure = true },
		func(r *jdbcplugin.Receipt) { r.Diagnostic = &jdbcplugin.Diagnostic{Category: "sql"} },
	} {
		r := successful(valid)
		mutate(&r)
		if _, err := decodeResult(r); err == nil {
			t.Fatal("unsuccessful receipt represented as query data")
		}
	}
}

func TestOriginalQueryResultAllowsDuplicateLabelsAndNativeMutationCount(t *testing.T) {
	for _, raw := range []string{
		`{"columns":["x","x"],"rows":[[1,2]],"large_value_cells":[],"affected_rows":0,"execution_time_ms":0,"truncated":true}`,
		`{"columns":[],"rows":[],"large_value_cells":[],"affected_rows":1,"execution_time_ms":0,"truncated":false}`,
		`{"columns":["empty"],"rows":[],"large_value_cells":[],"affected_rows":0,"execution_time_ms":0,"truncated":false}`,
	} {
		if _, err := decodeResult(successful(raw)); err != nil {
			t.Fatal("original valid envelope refused", err)
		}
	}
}
