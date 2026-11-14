package agentbridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNativeQueryPreservesExactValuesAndCompleteSidecars(t *testing.T) {
	raw := json.RawMessage(`{"columns":["id","decimal","binary","empty","null","text"],"column_types":["BIGINT","NUMERIC(38,20)","VARBINARY","BLOB","VARCHAR","VARCHAR"],"rows":[[9007199254740993,123456789012345678.12345678901234567890,"0x00ff805c270a","0x",null,"NULL"]],"affected_rows":0,"execution_time_ms":3,"truncated":false,"messages":[{"native":"complete"}],"spatial_values":[]}`)
	s, err := DecodeQuery(Receipt{Result: raw, ExecutorAcknowledged: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	row := s.SQL.Sets[0].Rows[0]
	if row[0].Kind != contract.Integer || row[0].Text != "9007199254740993" || row[1].Text != "123456789012345678.12345678901234567890" || row[2].Display() != "0x00ff805c270a" || row[3].Kind != contract.Binary || row[3].Bytes == nil || len(row[3].Bytes) != 0 || row[4].Kind != contract.Null || row[5].Kind != contract.Text {
		t.Fatal("native scalar identity lost")
	}
	raw[0] = ' '
	if !strings.Contains(string(s.Native), "messages") || s.Native[0] != '{' || s.SQL.NativeJSON != string(s.Native) {
		t.Fatal("original complete snapshot aliased or sidecars discarded")
	}
}

func TestNativeQueryRejectsAmbiguousShapesAndDeferredValues(t *testing.T) {
	for _, raw := range []string{
		` `,
		`{"columns":null,"column_types":[],"rows":[],"affected_rows":0,"execution_time_ms":0,"truncated":false}`,
		`{"columns":[],"column_types":[],"rows":[],"affected_rows":null,"execution_time_ms":0,"truncated":false}`,
		`{"columns":[],"columns":[],"column_types":[],"rows":[],"affected_rows":0,"execution_time_ms":0,"truncated":false}`,
		`{"columns":["x"],"column_types":[],"rows":[[1]],"affected_rows":0,"execution_time_ms":0,"truncated":false}`,
		`{"columns":["x"],"column_types":["BLOB"],"rows":[[{"largeValueId":"original"}]],"affected_rows":0,"execution_time_ms":0,"truncated":false}`,
		`[]`,
	} {
		s, err := DecodeQuery(Receipt{Result: json.RawMessage(raw), ExecutorAcknowledged: true}, nil)
		if err == nil || string(s.Native) != raw {
			t.Fatal("ambiguous/deferred original value became complete data")
		}
	}
}
