package consul

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const sessionToolID = "01234567-89ab-cdef-0123-456789abcdef"

func sessionToolRequest(t *testing.T, kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (Request, contract.KVRequest) {
	t.Helper()
	before := input
	document, err := (requestBuilder{}).BuildKVRequest(kind, input)
	if err != nil {
		t.Fatal("request tool failed", kind, err)
	}
	native, canonical, writes, err := decodeDocumentRequest(document)
	if err != nil || canonical != document || writes != (kind != contract.KVToolHTTPSessionInfo) || input != before {
		t.Fatal("generated request changed identity, input or preparation", kind, err)
	}
	return native, document
}

func TestHTTPSessionToolsPreserveOmittedEmptyAndExplicitValues(t *testing.T) {
	request, _ := sessionToolRequest(t, contract.KVToolHTTPSessionCreate, contract.KVRequestToolInput{})
	if request.Method != "PUT" || request.Path != "/v1/session/create" || string(request.Body) != "{}" {
		t.Fatal("omitted session fields were invented")
	}
	input := contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{
		IncludeName: true, IncludeNode: true, TTL: "24h", LockDelay: "0s", Behavior: "release", NodeChecksJSON: "[ ]",
	}}
	request, _ = sessionToolRequest(t, contract.KVToolHTTPSessionCreate, input)
	var fields map[string]json.RawMessage
	if json.Unmarshal(request.Body, &fields) != nil || len(fields) != 6 || string(fields["Name"]) != `""` || string(fields["Node"]) != `""` || string(fields["LockDelay"]) != `"0s"` || string(fields["NodeChecks"]) != "[]" || string(fields["TTL"]) != `"24h"` {
		t.Fatal("explicit empty/zero values lost", string(request.Body))
	}
	input.HTTPSession.Name, input.HTTPSession.Node = "Session 雪", "agent-雪"
	input.HTTPSession.TTL, input.HTTPSession.LockDelay, input.HTTPSession.Behavior = "10s", "60s", "delete"
	input.HTTPSession.NodeChecksJSON = `["serfHealth","中文"]`
	request, _ = sessionToolRequest(t, contract.KVToolHTTPSessionCreate, input)
	if !bytes.Contains(request.Body, []byte(`"Name":"Session 雪"`)) || !bytes.Contains(request.Body, []byte(`"NodeChecks":["serfHealth","中文"]`)) {
		t.Fatal("authored Unicode changed")
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionInfo, contract.KVToolHTTPSessionDestroy} {
		request, _ = sessionToolRequest(t, kind, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: sessionToolID}})
		prefix := "/v1/session/info/"
		if kind == contract.KVToolHTTPSessionDestroy {
			prefix = "/v1/session/destroy/"
		}
		if request.Path != prefix+sessionToolID || request.Body != nil || len(request.Query) != 0 {
			t.Fatal("original UUID or body presence changed", request)
		}
	}
}

func TestHTTPSessionToolsPreserveHTTPKeyBytesAndUnsignedFlags(t *testing.T) {
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
		for _, key := range []string{"key", "nested/中文 ?%#"} {
			for _, value := range [][]byte{{}, {0, 255, 128, 13, 10}} {
				for _, flags := range []string{"", "0", "18446744073709551615"} {
					input := contract.KVRequestToolInput{Key: key, ValueBase64: base64.StdEncoding.EncodeToString(value), Flags: flags, HTTPSession: contract.KVHTTPSessionToolInput{ID: sessionToolID}}
					request, _ := sessionToolRequest(t, kind, input)
					path, _ := KeyPath(key)
					operation := "acquire"
					if kind == contract.KVToolHTTPRelease {
						operation = "release"
					}
					if request.Method != "PUT" || request.Path != path || !bytes.Equal(request.Body, value) || request.Body == nil || request.Query.Get(operation) != sessionToolID || request.Query.Has("flags") != (flags != "") || request.Query.Get("flags") != flags || request.Query.Has("cas") {
						t.Fatal("original HTTP lock request changed", kind, request)
					}
				}
			}
		}
		for _, key := range []string{"/leading", "trailing/", "repeated//key", "./key", "../key", "key/../other", "key/./other"} {
			_, err := (requestBuilder{}).BuildKVRequest(kind, contract.KVRequestToolInput{Key: key, HTTPSession: contract.KVHTTPSessionToolInput{ID: sessionToolID}})
			if !errors.Is(err, ErrUnsupported) {
				t.Fatal("HTTP lock path silently rewritten or substituted", key, err)
			}
		}
	}
}

func TestHTTPSessionToolsRejectInvalidAndHiddenInputs(t *testing.T) {
	for _, session := range []contract.KVHTTPSessionToolInput{
		{ID: sessionToolID}, {Name: "hidden"}, {Node: "hidden"},
		{Name: string([]byte{255}), IncludeName: true}, {Node: "nul\x00", IncludeNode: true},
		{TTL: "0s"}, {TTL: "9s"}, {TTL: "24h1ns"}, {TTL: "10s "},
		{LockDelay: "-1s"}, {LockDelay: "60s1ns"}, {Behavior: "invalid"},
		{NodeChecksJSON: "null"}, {NodeChecksJSON: "[null]"}, {NodeChecksJSON: `[""]`},
		{NodeChecksJSON: `["\ud800"]`}, {NodeChecksJSON: "[\"\xff\"]"},
		{NodeChecksJSON: `{}`}, {NodeChecksJSON: `["a"] true`},
		{NodeChecksJSON: `[` + strings.Repeat(`"a",`, 64) + `"a"]`},
	} {
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPSessionCreate, contract.KVRequestToolInput{HTTPSession: session}); err == nil {
			t.Fatal("invalid session creation accepted", session)
		}
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionInfo, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
		for _, session := range []contract.KVHTTPSessionToolInput{{}, {ID: strings.ToUpper(sessionToolID)}, {ID: sessionToolID, TTL: "10s"}, {ID: sessionToolID, IncludeName: true}, {ID: sessionToolID, NodeChecksJSON: "[]"}} {
			input := contract.KVRequestToolInput{HTTPSession: session}
			if kind == contract.KVToolHTTPAcquire || kind == contract.KVToolHTTPRelease {
				input.Key = "k"
			}
			if _, err := (requestBuilder{}).BuildKVRequest(kind, input); err == nil {
				t.Fatal("invalid UUID/hidden creation input accepted", kind)
			}
		}
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolRead, contract.KVToolList, contract.KVToolRaw, contract.KVToolPut, contract.KVToolDelete, contract.KVToolTransaction} {
		if _, err := (requestBuilder{}).BuildKVRequest(kind, contract.KVRequestToolInput{Key: "k", HTTPSession: contract.KVHTTPSessionToolInput{ID: sessionToolID}}); err == nil {
			t.Fatal("ordinary tool silently dropped session identity", kind)
		}
	}
	for _, input := range []contract.KVRequestToolInput{{Key: "k", CompareIndex: "0"}, {Key: "k", Recursive: true}, {Key: "k", KeysOnly: true}, {Key: "k", TransactionJSON: "[]"}, {Key: "k", Flags: "01"}, {Key: "k", Flags: "18446744073709551616"}, {Key: "k", ValueBase64: "Zg"}, {Key: "k", ValueBase64: "Zh=="}, {Key: "k", ValueBase64: "Zg==\n"}, {Key: ""}} {
		input.HTTPSession.ID = sessionToolID
		for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
			if _, err := (requestBuilder{}).BuildKVRequest(kind, input); err == nil {
				t.Fatal("incompatible lock input accepted", kind, input)
			}
		}
	}
	for _, input := range []contract.KVRequestToolInput{{Key: "hidden"}, {ValueBase64: "AA=="}, {Flags: "0"}, {CompareIndex: "0"}, {KeysOnly: true}, {Recursive: true}, {TransactionJSON: "[]"}} {
		for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionInfo, contract.KVToolHTTPSessionDestroy} {
			if kind != contract.KVToolHTTPSessionCreate {
				input.HTTPSession.ID = sessionToolID
			}
			if _, err := (requestBuilder{}).BuildKVRequest(kind, input); err == nil {
				t.Fatal("session operation ignored hidden KV input", kind)
			}
		}
	}
}

func TestHTTPSessionToolsBoundsAndPublishedDefaults(t *testing.T) {
	for _, input := range []contract.KVRequestToolInput{{Key: strings.Repeat("x", 4097)}, {Key: "k", ValueBase64: base64.StdEncoding.EncodeToString(make([]byte, valueLimit+1))}} {
		input.HTTPSession.ID = sessionToolID
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPAcquire, input); !errors.Is(err, ErrBounds) {
			t.Fatal("lock input bound ignored", err)
		}
	}
	for _, session := range []contract.KVHTTPSessionToolInput{{Name: strings.Repeat("x", 4097), IncludeName: true}, {NodeChecksJSON: strings.Repeat(" ", requestLimit+1)}} {
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPSessionCreate, contract.KVRequestToolInput{HTTPSession: session}); !errors.Is(err, ErrBounds) {
			t.Fatal("session input bound ignored", err)
		}
	}
	sessionToolRequest(t, contract.KVToolHTTPAcquire, contract.KVRequestToolInput{Key: strings.Repeat("x", 4096), ValueBase64: base64.StdEncoding.EncodeToString(make([]byte, valueLimit)), HTTPSession: contract.KVHTTPSessionToolInput{ID: sessionToolID}})
	tools := requestTools("original/")
	if len(tools) != 24 || tools[0].Kind != contract.KVToolRead || tools[5].Kind != contract.KVToolTransaction {
		t.Fatal("original request tools changed")
	}
	want := []contract.KVRequestToolKind{contract.KVToolHTTPSessionInfo, contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease}
	for i, kind := range want {
		if tools[6+i].Kind != kind || tools[6+i].Default.HTTPSession.ID != "" {
			t.Fatal("Session tools missing or template invented a UUID")
		}
	}
	if tools[7].Default.HTTPSession.TTL != "10m" || tools[9].Default.Key != "original/" || tools[10].Default.Key != "original/" {
		t.Fatal("explicit creation default or original prefix suggestion changed")
	}
}
