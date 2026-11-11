package consul

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestRequestToolsPreserveBytesUnsignedPresenceAndExactPaths(t *testing.T) {
	b := requestBuilder{}
	for _, key := range []string{"plain/key", "unicode/中文 ?%#", "prefix//exact/", "./key", "../key"} {
		for _, value := range [][]byte{{}, {0, 255, 128}, []byte("\r\n中文")} {
			for _, number := range []string{"", "0", "18446744073709551615"} {
				input := contract.KVRequestToolInput{Key: key, ValueBase64: base64.StdEncoding.EncodeToString(value), Flags: number, CompareIndex: number}
				request, err := b.BuildKVRequest(contract.KVToolPut, input)
				if err != nil {
					t.Fatal(err)
				}
				native, canonical, write, err := decodeDocumentRequest(request)
				if err != nil || canonical != request || !write {
					t.Fatal("generated request changed during original preparation", err)
				}
				if canonicalHTTPKey(key) {
					path, _ := KeyPath(key)
					if native.Path != path || !bytes.Equal(native.Body, value) || native.Body == nil || native.Query.Get("flags") != number || native.Query.Has("flags") != (number != "") || native.Query.Get("cas") != number || native.Query.Has("cas") != (number != "") {
						t.Fatal("HTTP bytes, optional zero, or path changed")
					}
				} else {
					var ops []TxnOperation
					if native.Path != "/v1/txn" || json.Unmarshal(native.Body, &ops) != nil || len(ops) != 1 || ops[0].KV.Key != key || !bytes.Equal(*ops[0].KV.Value, value) || (ops[0].KV.Flags != nil) != (number != "") || (ops[0].KV.Index != nil) != (number != "") {
						t.Fatal("native transaction fallback changed absolute identity")
					}
					if number == "18446744073709551615" && (*ops[0].KV.Flags != math.MaxUint64 || *ops[0].KV.Index != math.MaxUint64) {
						t.Fatal("unsigned precision lost")
					}
				}
			}
		}
	}
}

func TestRequestToolsReadListRawDeleteAndOriginalTransaction(t *testing.T) {
	b := requestBuilder{}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolRead, contract.KVToolList, contract.KVToolRaw, contract.KVToolDelete} {
		for _, key := range []string{"absolute/key", "absolute//", ""} {
			input := contract.KVRequestToolInput{Key: key}
			if kind == contract.KVToolList {
				input.KeysOnly = true
			}
			if kind == contract.KVToolDelete && key == "" {
				input.Recursive = true
			}
			r, err := b.BuildKVRequest(kind, input)
			if kind == contract.KVToolRaw && !canonicalHTTPKey(key) || key == "" && (kind == contract.KVToolRead || kind == contract.KVToolRaw) {
				if err == nil {
					t.Fatal("raw path rewrite or empty key accepted")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			native, _, write, err := decodeDocumentRequest(r)
			if err != nil || write != (kind == contract.KVToolDelete) || kind == contract.KVToolRaw && !native.Query.Has("raw") || !canonicalHTTPKey(key) && native.Path != "/v1/txn" {
				t.Fatal("request semantics changed", kind, err)
			}
		}
	}
	for _, raw := range []string{` [ { "KV" : { "Verb":"get", "Key":"exact//key" } } ] `, `[{"KV":{"Verb":"cas","Key":"exact//key","Value":"AP8=","Flags":18446744073709551615,"Index":0}}]`} {
		r, err := b.BuildKVRequest(contract.KVToolTransaction, contract.KVRequestToolInput{TransactionJSON: raw})
		if err != nil {
			t.Fatal(err)
		}
		native, _, _, _ := decodeDocumentRequest(r)
		if string(native.Body) != raw {
			t.Fatal("original transaction bytes replaced")
		}
	}
}

func TestRequestToolsRejectAmbiguousHiddenInputsAndBounds(t *testing.T) {
	b := requestBuilder{}
	for _, text := range []string{"00", "+1", "-1", " 1", "1 ", "1e3", "18446744073709551616"} {
		for _, field := range []string{"flags", "index"} {
			input := contract.KVRequestToolInput{Key: "key"}
			if field == "flags" {
				input.Flags = text
			} else {
				input.CompareIndex = text
			}
			if _, err := b.BuildKVRequest(contract.KVToolPut, input); err == nil {
				t.Fatal("noncanonical unsigned number accepted", text)
			}
		}
	}
	for _, item := range []struct {
		kind  contract.KVRequestToolKind
		input contract.KVRequestToolInput
	}{
		{contract.KVToolPut, contract.KVRequestToolInput{Key: "key", ValueBase64: "AB=="}},
		{contract.KVToolPut, contract.KVRequestToolInput{Key: "key", ValueBase64: "AA==\n"}},
		{contract.KVToolPut, contract.KVRequestToolInput{Key: "key", KeysOnly: true}},
		{contract.KVToolDelete, contract.KVRequestToolInput{Key: "prefix", CompareIndex: "0", Recursive: true}},
		{contract.KVToolRead, contract.KVRequestToolInput{Key: "key", Flags: "0"}},
		{contract.KVToolRead, contract.KVRequestToolInput{Key: "key", TransactionJSON: "[]"}},
		{contract.KVToolList, contract.KVRequestToolInput{Key: "key", Recursive: true}},
		{contract.KVToolTransaction, contract.KVRequestToolInput{Key: "hidden", TransactionJSON: `[{"KV":{"Verb":"get","Key":"k"}}]`}},
		{contract.KVToolTransaction, contract.KVRequestToolInput{TransactionJSON: `[{"KV":{"Verb":"get","Verb":"set","Key":"k","Value":""}}]`}},
		{contract.KVToolTransaction, contract.KVRequestToolInput{TransactionJSON: `[{"KV":{"Verb":"delete-cas","Key":"k","Index":null}}]`}},
		{contract.KVToolRead, contract.KVRequestToolInput{Key: "nul\x00key"}},
		{"foreign", contract.KVRequestToolInput{Key: "key"}},
	} {
		if _, err := b.BuildKVRequest(item.kind, item.input); err == nil {
			t.Fatal("incompatible input accepted", item.kind, item.input)
		}
	}
	for _, input := range []contract.KVRequestToolInput{{Key: strings.Repeat("x", 4097)}, {Key: "k", ValueBase64: base64.StdEncoding.EncodeToString(make([]byte, valueLimit+1))}} {
		if _, err := b.BuildKVRequest(contract.KVToolPut, input); !errors.Is(err, ErrBounds) {
			t.Fatal("bounds ignored", err)
		}
	}
	if _, err := b.BuildKVRequest(contract.KVToolPut, contract.KVRequestToolInput{Key: strings.Repeat("x", 4096), ValueBase64: base64.StdEncoding.EncodeToString(make([]byte, valueLimit))}); err != nil {
		t.Fatal("boundary rejected", err)
	}
}
