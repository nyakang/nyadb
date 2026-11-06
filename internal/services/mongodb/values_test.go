package mongodb

import (
	"bytes"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestExtendedJSONRetainsBSONTypesAndMissingFields(t *testing.T) {
	text := `{"_id":{"$oid":"507f1f77bcf86cd799439011"},"big":{"$numberLong":"9007199254740993"},"small":{"$numberInt":"2"},"decimal":{"$numberDecimal":"123456789.1234567890123456789"},"date":{"$date":{"$numberLong":"1700000000123"}},"binary":{"$binary":{"base64":"AP8=","subType":"80"}},"nested":{"null":null,"array":[{"$numberDouble":"NaN"},true]}}`
	raw, err := parseDocument(text, contract.DataEditMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := extendedJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := parseDocument(canonical, contract.DataEditMaxBytes)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("BSON roundtrip changed", err)
	}
	if raw.Lookup("big").Type != bson.TypeInt64 || raw.Lookup("small").Type != bson.TypeInt32 || raw.Lookup("decimal").Type != bson.TypeDecimal128 || raw.Lookup("date").Type != bson.TypeDateTime || raw.Lookup("missing").Type != 0 || raw.Lookup("nested").Document().Lookup("null").Type != bson.TypeNull {
		t.Fatal("type or missing/null lost")
	}
	for _, input := range []string{`{"_id":1,"_id":2}`, `{"x":{"a":1,"a":2}}`, `{"a":1} {"b":2}`, `[]`, `null`, `{"a":`} {
		if _, err = parseDocument(input, contract.DataEditMaxBytes); err == nil {
			t.Fatal("invalid/ambiguous JSON accepted", input)
		}
	}
}
func TestSnapshotsRejectTamperingAndCrossSessionReplay(t *testing.T) {
	p, _ := connections.Defaults(connections.MongoDB)
	p.ID = 7
	s := &Session{profile: p}
	s.key[0] = 1
	raw, _ := parseDocument(`{"_id":1,"x":null}`, 1024)
	o := s.Namespace("db").Object("collection", "c")
	snap, err := s.snapshot(o, raw)
	if err != nil || s.validateSnapshot(o, snap) != nil {
		t.Fatal(err)
	}
	other := &Session{profile: p}
	other.key[0] = 2
	if other.validateSnapshot(o, snap) == nil {
		t.Fatal("old session snapshot accepted")
	}
	altered := *snap
	altered.Ref = snap.Ref
	altered.Ref.Values = [][]byte{[]byte("forged")}
	if s.validateSnapshot(o, &altered) == nil {
		t.Fatal("forged identity accepted")
	}
	changed, _ := parseDocument(`{"_id":1}`, 1024)
	if matchesSnapshot(changed, snap) {
		t.Fatal("missing equals null")
	}
	if _, err = s.prepareEdits(7, "db", "c", []contract.DataEdit{{Kind: "update", Original: snap, Values: map[string]contract.DataValue{documentField: {Kind: contract.JSON, Text: `{"_id":2}`}}}}); err == nil {
		t.Fatal("_id mutation accepted")
	}
}
func TestCommandsNeverAcceptUnacknowledgedWritesOrTransactionInjection(t *testing.T) {
	for _, text := range []string{`{"insert":"c","documents":[],"writeConcern":{"w":0}}`, `{"ping":1,"$db":"another"}`, `{"ping":1,"lsid":{}}`, `{"commitTransaction":1}`, `{"getMore":12}`} {
		if _, _, _, err := commandDocument(text); err == nil {
			t.Fatal("unsafe command accepted", text)
		}
	}
	if _, _, cursor, err := commandDocument(`{"find":"c","filter":{"name":"a;--b"}}`); err != nil || !cursor {
		t.Fatal("native command not retained", err)
	}
}
