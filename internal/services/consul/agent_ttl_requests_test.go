package consul

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func ttlTestNote(value string) *string { return &value }

func TestAgentTTLRequestsRetainOriginalIDNotePresenceAndCompleteUpdates(t *testing.T) {
	id := "original 雪%?#++"
	for action, build := range map[string]func(string, *string) (Request, error){"pass": AgentCheckPassRequest, "warn": AgentCheckWarnRequest, "fail": AgentCheckFailRequest} {
		for _, authored := range []*string{nil, new(string), ttlTestNote("original 雪%?#++"), ttlTestNote(strings.Repeat("x", 8192)), ttlTestNote("original first\nsecond\r\nNUL\x00雪")} {
			request, err := build(id, authored)
			want := ""
			if authored != nil {
				want = *authored
			}
			if err != nil || request.Method != http.MethodPut || request.Path != "/v1/agent/check/"+action+"/"+url.PathEscape(id) || request.Query.Has("note") != (authored != nil) || request.Query.Get("note") != want || request.Body != nil {
				t.Fatal("TTL request changed original ID/note presence/body", action, err)
			}
			if authored != nil {
				*authored = "later edited note"
				if request.Query.Get("note") != want {
					t.Fatal("original TTL request retained mutable note input")
				}
			}
			document, err := DocumentRequest(request)
			native, canonical, writes, decodeErr := decodeDocumentRequest(document)
			if err != nil || decodeErr != nil || !writes || canonical != document || native.Query.Has("note") != request.Query.Has("note") || native.Query.Get("note") != want {
				t.Fatal("TTL document codec lost original presence/authority", err, decodeErr)
			}
		}
	}
	for _, field := range []string{"", `,"Output":""`, `,"Output":null`, `,"Output":"original 中文"`, `,"UnknownNative":18446744073709551615`} {
		raw := []byte(" {\n\"Status\":\"passing\"" + field + "\n} ")
		original := bytes.Clone(raw)
		request, err := AgentCheckUpdateRequest(id, raw)
		if err != nil || request.Method != http.MethodPut || request.Path != "/v1/agent/check/update/"+url.PathEscape(id) || !bytes.Equal(request.Body, original) || len(request.Query) != 0 {
			t.Fatal("TTL update rebuilt complete original JSON", err)
		}
		raw[0] = 'x'
		if !bytes.Equal(request.Body, original) {
			t.Fatal("TTL update retained caller-owned bytes")
		}
	}
}

func TestAgentTTLRequestsRejectForeignRoutingMalformedUpdatesAndPathCleanup(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}`, `{"Status":null}`, `{"Status":"Passing"}`, `{"Status":"unknown"}`, `{"Status":"passing","status":"warning"}`, `{"Status":"passing","Output":2}`, `{"Status":"passing","Output":"x","output":null}`, `{"Status":"passing","Token":"hidden"}`, `{"Status":"passing","nested":{"Namespace":"hidden"}}`} {
		if _, err := AgentCheckUpdateRequest("original", []byte(raw)); err == nil {
			t.Fatal("unsupported/ambiguous TTL update silently accepted", raw)
		}
	}
	for _, build := range []func(string, *string) (Request, error){AgentCheckPassRequest, AgentCheckWarnRequest, AgentCheckFailRequest} {
		for _, id := range []string{"a//b", "a/../b", "/leading", "trailing/"} {
			if _, err := build(id, nil); !errors.Is(err, ErrUnsupported) {
				t.Fatal("original TTL ID was silently cleaned", err)
			}
		}
		if _, err := build("original", ttlTestNote(strings.Repeat("x", requestLimit+1))); !errors.Is(err, ErrBounds) {
			t.Fatal("TTL note exceeded complete request budget", err)
		}
		request, _ := build("original", nil)
		for _, query := range []url.Values{{"dc": {"dc1"}}, {"note": {"first", "second"}}, {"replace-existing-checks": {"true"}}, {"consistent": {""}}, {"token": {"hidden"}}, {"note": {string([]byte{0xff})}}} {
			request.Query = query
			if _, err := DocumentRequest(request); !errors.Is(err, ErrInvalid) {
				t.Fatal("foreign TTL query silently dropped", query, err)
			}
		}
		request.Query, request.Method = nil, http.MethodGet
		if _, err := DocumentRequest(request); err == nil {
			t.Fatal("old handler GET comment created TTL write authority")
		}
		request.Method, request.Body = http.MethodPut, []byte("hidden body")
		if _, err := DocumentRequest(request); err == nil {
			t.Fatal("status endpoint ignored authored body")
		}
	}
	for _, action := range []string{"pass", "warn", "fail", "update"} {
		request := Request{Method: http.MethodPut, Path: "/v1/agent/check/" + action + "/" + url.PathEscape("invalid\nid")}
		if action == "update" {
			request.Body = []byte(`{"Status":"passing"}`)
		}
		if _, err := DocumentRequest(request); !errors.Is(err, ErrInvalid) {
			t.Fatal("raw TTL text bypassed original ID control validation", action, err)
		}
	}
	request, _ := AgentCheckUpdateRequest("original", []byte(`{"Status":"passing"}`))
	request.Query = url.Values{"note": {"hidden"}}
	if _, err := DocumentRequest(request); err == nil {
		t.Fatal("update endpoint silently ignored status note")
	}
}

func TestAgentTTLReceiptsRetainEmptyACKsProvedDenialsAndUnknownErrors(t *testing.T) {
	for _, kind := range []string{"agent-check-pass", "agent-check-warn", "agent-check-fail", "agent-check-update"} {
		for _, status := range []int{200, 400, 403, 404, 408, 500} {
			for _, body := range [][]byte{nil, []byte("null"), []byte("true"), []byte("false"), []byte("original error")} {
				r := Response{Status: status, Body: bytes.Clone(body)}
				err := projectResponse(&r, Plan{kind: kind})
				known := status == 403 || status == 404
				success := status == 200 && len(body) == 0
				if r.Acknowledged != (known || success) || r.Succeeded != success || (err == nil) != success || !bytes.Equal(r.Body, body) {
					t.Fatal("original TTL ACK/error guessed a write outcome", kind, status, string(body), err)
				}
			}
		}
	}
}
