package consul

import (
	"bytes"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestAgentServiceToolsRetainCompleteDefinitionAndEveryReplacementPresence(t *testing.T) {
	builder := requestBuilder{}
	definition := " {\n\"Name\":\"original\",\"ID\":null,\"Checks\":[],\"Meta\":{\"original\":\"中文\"},\"Unknown\":18446744073709551615\n} "
	for _, option := range []struct {
		include bool
		value   string
	}{{false, ""}, {true, ""}, {true, "false"}, {true, "true"}} {
		input := contract.KVRequestToolInput{HTTPAgentService: contract.KVHTTPAgentServiceToolInput{DefinitionJSON: definition, IncludeReplacement: option.include, ReplaceExistingChecks: option.value}}
		request, err := builder.BuildKVRequest(contract.KVToolHTTPAgentServiceRegister, input)
		if err != nil {
			t.Fatal(err)
		}
		native, _, writes, err := decodeDocumentRequest(request)
		if err != nil || !writes || native.Method != "PUT" || native.Path != "/v1/agent/service/register" || !bytes.Equal(native.Body, []byte(definition)) || native.Query.Has("replace-existing-checks") != option.include || native.Query.Get("replace-existing-checks") != option.value || native.Query.Has("dc") {
			t.Fatal("tool changed original definition or replacement presence", err)
		}
	}
	for _, id := range []string{"original 雪%?#++", "qualified/id"} {
		request, err := builder.BuildKVRequest(contract.KVToolHTTPAgentServiceDeregister, contract.KVRequestToolInput{HTTPAgentService: contract.KVHTTPAgentServiceToolInput{ID: id}})
		if err != nil {
			t.Fatal(err)
		}
		native, _, writes, err := decodeDocumentRequest(request)
		if err != nil || !writes || native.Method != "PUT" || native.Path != "/v1/agent/service/deregister/"+url.PathEscape(id) || native.Body != nil || len(native.Query) != 0 {
			t.Fatal("deregistration tool changed original ID", err)
		}
	}
	tools := requestTools("foreign-prefix/")
	if len(tools) != 30 || tools[20].Kind != contract.KVToolHTTPAgentServiceRegister || tools[21].Kind != contract.KVToolHTTPAgentServiceDeregister || tools[20].Default != (contract.KVRequestToolInput{}) || tools[21].Default != (contract.KVRequestToolInput{}) {
		t.Fatal("service tools inherited prefix or changed original ordering")
	}
}

func TestAgentServiceToolsRejectHiddenFieldsRoutingBoundsAndNativeIDRewriting(t *testing.T) {
	builder := requestBuilder{}
	base := contract.KVRequestToolInput{HTTPAgentService: contract.KVHTTPAgentServiceToolInput{DefinitionJSON: `{"Name":"original"}`}}
	for _, mutate := range []func(*contract.KVRequestToolInput){
		func(i *contract.KVRequestToolInput) { i.Key = "hidden" },
		func(i *contract.KVRequestToolInput) { i.HTTPSession.ID = "hidden" },
		func(i *contract.KVRequestToolInput) { i.HTTPHealth.IncludeFilter = true },
		func(i *contract.KVRequestToolInput) { i.HTTPAgent.IncludeFilter = true },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentService.ID = "hidden" },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentService.ReplaceExistingChecks = "true" },
		func(i *contract.KVRequestToolInput) {
			i.HTTPAgentService.IncludeReplacement = true
			i.HTTPAgentService.ReplaceExistingChecks = "TRUE"
		},
		func(i *contract.KVRequestToolInput) {
			i.HTTPAgentService.DefinitionJSON = `{"Name":"x","Token":"hidden"}`
		},
	} {
		input := base
		mutate(&input)
		if _, err := builder.BuildKVRequest(contract.KVToolHTTPAgentServiceRegister, input); err == nil {
			t.Fatal("hidden or unsupported original field dropped")
		}
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolRead, contract.KVToolHTTPAgentSelf, contract.KVToolHTTPSessionInfo, contract.KVToolHTTPHealthState, contract.KVToolHTTPAgentServiceDeregister} {
		if _, err := builder.BuildKVRequest(kind, base); !errors.Is(err, ErrInvalid) {
			t.Fatal("foreign operation dropped service definition", err)
		}
	}
	for _, id := range []string{"a//b", "a/../b", "/leading", "trailing/"} {
		if _, err := builder.BuildKVRequest(contract.KVToolHTTPAgentServiceDeregister, contract.KVRequestToolInput{HTTPAgentService: contract.KVHTTPAgentServiceToolInput{ID: id}}); !errors.Is(err, ErrUnsupported) {
			t.Fatal("original native ID rewritten", err)
		}
	}
	base.HTTPAgentService.DefinitionJSON = strings.Repeat("x", requestLimit+1)
	if _, err := builder.BuildKVRequest(contract.KVToolHTTPAgentServiceRegister, base); !errors.Is(err, ErrBounds) {
		t.Fatal("oversized definition accepted", err)
	}
}
