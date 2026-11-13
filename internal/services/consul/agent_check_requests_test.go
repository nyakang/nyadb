package consul

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAgentCheckRequestsRetainOriginalDefinitionsIDsDefaultsAndUnknownFields(t *testing.T) {
	for _, field := range []string{"", `,"ID":""`, `,"ID":null`, `,"ID":"original 雪%?#++"`} {
		raw := []byte(" \n{\"Name\":\"original\",\"TTL\":\"10m\",\"Notes\":\"中文\",\"UnknownNative\":18446744073709551615" + field + "}\n ")
		original := bytes.Clone(raw)
		request, err := AgentCheckRegisterRequest(raw)
		if err != nil || !bytes.Equal(request.Body, original) || request.Method != http.MethodPut || request.Path != "/v1/agent/check/register" || len(request.Query) != 0 {
			t.Fatal("original check definition or defaults changed", err)
		}
		raw[0] = 'x'
		if !bytes.Equal(request.Body, original) {
			t.Fatal("check request retained caller-owned mutable bytes")
		}
		document, err := DocumentRequest(request)
		native, canonical, writes, decodeErr := decodeDocumentRequest(document)
		if err != nil || decodeErr != nil || !writes || canonical != document || !bytes.Equal(native.Body, original) {
			t.Fatal("original check document codec lost authored bytes", err, decodeErr)
		}
	}
	for _, id := range []string{"original 雪%?#++", "qualified/check", "name:check"} {
		request, err := AgentCheckDeregisterRequest(id)
		if err != nil || request.Method != http.MethodPut || request.Path != "/v1/agent/check/deregister/"+url.PathEscape(id) || request.Body != nil || len(request.Query) != 0 {
			t.Fatal("original check ID changed", id, err)
		}
		if _, err := DocumentRequest(request); err != nil {
			t.Fatal("original deregistration document rejected", err)
		}
	}
}

func TestAgentCheckRequestsRejectForeignRoutingHiddenOptionsAndPathCleanup(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `[]`, `{"Name":""}`, `{"Name":null}`, `{"Name":"original","name":"other"}`, `{"Name":"x","Namespace":""}`, `{"Name":"x","nested":{"Partition":"default"}}`, `{"Name":"x","Token":"hidden"}`, `{"Name":"x","Meta":{"Token":"hidden"}}`} {
		if _, err := AgentCheckRegisterRequest([]byte(raw)); err == nil {
			t.Fatal("invalid/foreign original check definition silently accepted", raw)
		}
	}
	for _, id := range []string{"a//b", "a/../b", "/leading", "trailing/"} {
		if _, err := AgentCheckDeregisterRequest(id); !errors.Is(err, ErrUnsupported) {
			t.Fatal("original check ID silently cleaned", id, err)
		}
	}
	for _, id := range []string{"", "bad\x00id", strings.Repeat("x", 4097)} {
		if _, err := AgentCheckDeregisterRequest(id); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid original check ID accepted", err)
		}
	}
	request, _ := AgentCheckRegisterRequest([]byte(`{"Name":"original","TTL":"10m"}`))
	for _, query := range []url.Values{{"dc": {"dc1"}}, {"replace-existing-checks": {"true"}}, {"index": {"1"}}, {"consistent": {""}}, {"ns": {""}}, {"partition": {"default"}}, {"filter": {""}}} {
		request.Query = query
		if _, err := DocumentRequest(request); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsupported check routing/read/replacement option accepted", query, err)
		}
	}
}

func TestAgentCheckCompleteNativeACKsDoNotGuessOtherWriteOutcomes(t *testing.T) {
	for _, kind := range []string{"agent-check-register", "agent-check-deregister"} {
		for _, status := range []int{200, 400, 403, 404, 408, 500} {
			for _, body := range [][]byte{nil, []byte("null"), []byte("false"), []byte("true"), []byte("error")} {
				r := Response{Status: status, Body: bytes.Clone(body)}
				err := projectResponse(&r, Plan{kind: kind})
				knownDenial := status == 403 || kind == "agent-check-deregister" && status == 404
				success := status == 200 && len(body) == 0
				if r.Acknowledged != (knownDenial || success) || r.Succeeded != success || (err == nil) != success || !bytes.Equal(r.Body, body) {
					t.Fatal("native check outcome guessed or original body changed", kind, status, string(body), err)
				}
			}
		}
	}
}
