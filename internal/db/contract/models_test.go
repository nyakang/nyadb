package contract_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestTypedSnapshotRoundTripPreservesValues(t *testing.T) {
	values := []contract.Cell{{Kind: contract.Null}, {Kind: contract.Text, Text: "NULL"}, {Kind: contract.Text}, {Kind: contract.Binary, Bytes: []byte{}}, {Kind: contract.Binary, Bytes: []byte{0, 255, 128}}, {Kind: contract.Integer, Text: "9223372036854775807"}, {Kind: contract.Decimal, Text: "12345678901234567890.00000000000000000001"}, {Kind: contract.JSON, Text: `{"n":9223372036854775807}`}, {Kind: contract.Text, Text: "preview", Truncated: true}}
	object := contract.ObjectRef{ProfileID: 2, Catalog: "db", Schema: "public", Kind: "table", Name: "items"}
	for _, mode := range []string{"values", "fingerprints"} {
		original := contract.RowSnapshot{Mode: mode, Ref: contract.RowRef{Object: object, Columns: []string{"id"}, TypedValues: values[6:7], Values: [][]byte{[]byte("9223372036854775807")}}, Original: values, Fingerprints: []*string{nil, func() *string { v := "aabbcc"; return &v }()}, Metadata: "signature"}
		encoded, e := json.Marshal(original)
		if e != nil {
			t.Fatal(e)
		}
		var decoded contract.RowSnapshot
		if e = json.Unmarshal(encoded, &decoded); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(original, decoded) {
			t.Fatalf("lossy %s snapshot roundtrip", mode)
		}
	}
	copy := values[4].Clone()
	copy.Bytes[0] = 9
	if bytes.Equal(copy.Bytes, values[4].Bytes) {
		t.Fatal("binary aliases original")
	}
	if values[3].Clone().Bytes == nil {
		t.Fatal("empty binary became nil")
	}
}
