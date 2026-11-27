package prestosql

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOriginalCursorRejectsAmbiguousProgress(t *testing.T) {
	valid := `{"columns":["x"],"rows":[[1]],"large_value_cells":[],"affected_rows":0,"execution_time_ms":1,"truncated":false,"has_more":true,"session_id":"12345678-1234-1234-1234-123456789abc"}`
	page, err := decodeCursor(successful(valid))
	if err != nil || !page.more || len(page.result.rows) != 1 {
		t.Fatal("valid original cursor refused", err)
	}
	for _, change := range [][2]string{
		{`"has_more":true`, `"has_more":null`},
		{`"has_more":true`, `"has_more":"true"`},
		{`"has_more":true`, `"has_more":false`},
		{`"rows":[[1]]`, `"rows":[]`},
		{`"rows":[[1]]`, `"rows":[[1],[2]]`},
		{`"truncated":false`, `"truncated":true`},
		{`"session_id":"12345678-1234-1234-1234-123456789abc"`, `"session_id":null`},
		{`"session_id":"12345678-1234-1234-1234-123456789abc"`, `"session_id":"foreign"`},
		{`"has_more":true`, `"has_more":true,"has_more":false`},
	} {
		if _, err = decodeCursor(successful(strings.Replace(valid, change[0], change[1], 1))); err == nil {
			t.Fatal("invalid cursor admitted", change[1])
		}
	}
	final := strings.Replace(valid, `"has_more":true`, `"has_more":false`, 1)
	final = strings.Replace(final, `"session_id":"12345678-1234-1234-1234-123456789abc"`, `"session_id":null`, 1)
	if page, err = decodeCursor(successful(final)); err != nil || page.more || page.id != "" {
		t.Fatal("completed cursor refused", err)
	}
}

func TestOriginalFieldRejectsInconsistentBoundaries(t *testing.T) {
	encoded := func(v string) json.RawMessage { raw, _ := json.Marshal(v); return raw }
	for _, tc := range []struct {
		value  string
		binary bool
		offset int64
		limit  int
	}{
		{`["猫",2]`, false, 0, 2},
		{`["猫猫",1]`, false, 0, 1},
		{`[null,1]`, false, 0, 1},
		{`["",-1]`, false, 0, 1},
		{`["",1]`, false, 2, 1},
		{`["AP+B",2]`, true, 0, 2},
		{`["/x==",1]`, true, 0, 1},
		{`["AA==",1,0]`, true, 0, 1},
	} {
		if _, err := decodeField(encoded(tc.value), tc.binary, tc.offset, tc.limit); err == nil {
			t.Fatal("inconsistent field accepted", tc)
		}
	}
	null, err := decodeField(encoded(`[null,0]`), false, 0, 1)
	if err != nil || !null.Null {
		t.Fatal("SQL NULL lost", err)
	}
	empty, err := decodeField(encoded(`["",0]`), true, 0, 1)
	if err != nil || empty.Null || empty.Text != "0x" {
		t.Fatal("empty binary lost", err)
	}
}
