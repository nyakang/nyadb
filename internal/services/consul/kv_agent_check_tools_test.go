package consul

import (
	"bytes"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestAgentCheckToolsPreserveOriginalDefinitionsDefaultsIDsAndScope(t *testing.T) {
	builder := requestBuilder{}
	for _, id := range []string{"", `,"ID":""`, `,"ID":null`, `,"ID":"original 雪%?#++"`} {
		raw := " {\n\"Name\":\"original\",\"TTL\":\"10m\",\"Unknown\":18446744073709551615" + id + "\n} "
		request, err := builder.BuildKVRequest(contract.KVToolHTTPAgentCheckRegister, contract.KVRequestToolInput{HTTPAgentCheck: contract.KVHTTPAgentCheckToolInput{DefinitionJSON: raw}})
		if err != nil {
			t.Fatal(err)
		}
		native, _, writes, err := decodeDocumentRequest(request)
		if err != nil || !writes || native.Method != "PUT" || native.Path != "/v1/agent/check/register" || !bytes.Equal(native.Body, []byte(raw)) || len(native.Query) != 0 {
			t.Fatal("check tool rewrote complete native definition/defaults or inherited foreign scope", err)
		}
	}
	for _, id := range []string{"original 雪%?#++", "qualified/id"} {
		request, err := builder.BuildKVRequest(contract.KVToolHTTPAgentCheckDeregister, contract.KVRequestToolInput{HTTPAgentCheck: contract.KVHTTPAgentCheckToolInput{ID: id}})
		if err != nil {
			t.Fatal(err)
		}
		native, _, writes, err := decodeDocumentRequest(request)
		if err != nil || !writes || native.Path != "/v1/agent/check/deregister/"+url.PathEscape(id) || native.Body != nil || len(native.Query) != 0 {
			t.Fatal("check tool changed original removal identity", err)
		}
	}
	tools := requestTools("foreign/prefix/")
	if len(tools) != 24 || tools[22].Kind != contract.KVToolHTTPAgentCheckRegister || tools[23].Kind != contract.KVToolHTTPAgentCheckDeregister || tools[22].Default != (contract.KVRequestToolInput{}) || tools[23].Default != (contract.KVRequestToolInput{}) {
		t.Fatal("check publication inherited prefix or changed existing tools")
	}
}

func TestAgentCheckToolsRejectHiddenAuthRoutingBoundsAndRewrittenIDs(t *testing.T) {
	builder := requestBuilder{}
	base := contract.KVRequestToolInput{HTTPAgentCheck: contract.KVHTTPAgentCheckToolInput{DefinitionJSON: `{"Name":"original","TTL":"10m"}`}}
	for _, mutate := range []func(*contract.KVRequestToolInput){
		func(i *contract.KVRequestToolInput) { i.Key = "hidden" },
		func(i *contract.KVRequestToolInput) { i.HTTPSession.ID = "hidden" },
		func(i *contract.KVRequestToolInput) { i.HTTPHealth.IncludeFilter = true },
		func(i *contract.KVRequestToolInput) { i.HTTPAgent.IncludeFilter = true },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentService.IncludeReplacement = true },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentCheck.ID = "hidden" },
		func(i *contract.KVRequestToolInput) {
			i.HTTPAgentCheck.DefinitionJSON = `{"Name":"x","Token":"hidden"}`
		},
		func(i *contract.KVRequestToolInput) {
			i.HTTPAgentCheck.DefinitionJSON = `{"Name":"x","Partition":"hidden"}`
		},
		func(i *contract.KVRequestToolInput) {
			i.HTTPAgentCheck.DefinitionJSON = `{"Name":"x","Namespace":"hidden"}`
		},
	} {
		input := base
		mutate(&input)
		if _, err := builder.BuildKVRequest(contract.KVToolHTTPAgentCheckRegister, input); err == nil {
			t.Fatal("check tool silently discarded hidden original input")
		}
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolRead, contract.KVToolHTTPAgentSelf, contract.KVToolHTTPSessionInfo, contract.KVToolHTTPHealthState, contract.KVToolHTTPAgentServiceRegister, contract.KVToolHTTPAgentCheckDeregister} {
		if _, err := builder.BuildKVRequest(kind, base); !errors.Is(err, ErrInvalid) {
			t.Fatal("another tool dropped original check input", err)
		}
	}
	for _, id := range []string{"a//b", "a/../b", "/leading", "trailing/"} {
		if _, err := builder.BuildKVRequest(contract.KVToolHTTPAgentCheckDeregister, contract.KVRequestToolInput{HTTPAgentCheck: contract.KVHTTPAgentCheckToolInput{ID: id}}); !errors.Is(err, ErrUnsupported) {
			t.Fatal("check tool rewrote original ID", err)
		}
	}
	base.HTTPAgentCheck.DefinitionJSON = strings.Repeat("x", requestLimit+1)
	if _, err := builder.BuildKVRequest(contract.KVToolHTTPAgentCheckRegister, base); !errors.Is(err, ErrBounds) {
		t.Fatal("complete check definition escaped request budget", err)
	}
}
