package cassandra

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestAgentValuesPreserveNativePrecisionBinaryAndCQLLiterals(t *testing.T) {
	for _, v := range []struct {
		typ, text string
		kind      contract.CellKind
	}{
		{"bigint", "9007199254740993", contract.Integer},
		{"varint", "1234567890123456789012345678901234567890", contract.Integer},
		{"decimal", "9007199254740993.12345678901234567890", contract.Decimal},
		{"counter", "-11", contract.Integer},
		{"blob", "0x00ff80", contract.Binary},
		{"blob", "0x", contract.Binary},
		{"map<text, int>", "{'a': 1, 'b': 2}", contract.Text},
		{"tuple<int, text>", "(7, 'seven')", contract.Text},
		{"inet", "2001:db8::1", contract.Text},
		{"timestamp", "2026-11-16T00:00:00Z", contract.DateTime},
	} {
		raw, _ := json.Marshal(v.text)
		cell, err := DecodeValue(v.typ, raw)
		if err != nil || cell.Kind != v.kind || cell.Display() != v.text {
			t.Fatal("original native scalar/literal changed", v.typ, cell, err)
		}
	}
	for _, typ := range []string{"blob", "text", "tuple<int, text>"} {
		cell, err := DecodeValue(typ, json.RawMessage("null"))
		if err != nil || cell.Kind != contract.Null {
			t.Fatal("original null became a textual value", err)
		}
	}
}

func TestAgentQueryRetriesDoNotSuppressOriginalTransportSetup(t *testing.T) {
	c := AgentConfig{ContactPoints: []string{"localhost"}, Port: 9042, LocalDatacenter: "dc&=+1", ConnectTimeoutMS: 15000, RequestTimeoutMS: 11000}
	params := connectionParams(c, "secret")
	options, err := url.ParseQuery(params["url_params"].(string))
	if err != nil || options.Get("retry") != "FallthroughRetryPolicy" || options.Has("retries") || options.Get("localdatacenter") != c.LocalDatacenter || options.Get("connecttimeout") != "15000ms" || options.Get("requesttimeout") != "11000ms" {
		t.Fatal("native request retry policy changed transport setup or option boundaries", err)
	}
	if params["password"] != "secret" {
		t.Fatal("secret left original stdin request")
	}
}

func TestAgentValuesRejectGuessedOrMalformedWireTypes(t *testing.T) {
	for _, v := range []struct{ typ, raw string }{
		{"bigint", `9007199254740993`}, {"bigint", `"1.1"`}, {"varint", `"1e20"`},
		{"blob", `"0xfff"`}, {"blob", `"base64:AP8="`}, {"boolean", `true`},
		{"inet", `"not an address"`}, {"custom_udt", `"{'x': 1}"`},
		{"tuple<int, text>", `{"x":1}`}, {"text", `"one" "two"`},
	} {
		if _, err := DecodeValue(v.typ, json.RawMessage(v.raw)); err == nil {
			t.Fatal("unverified native representation became typed data", v.typ, v.raw)
		}
	}
}
