package consul

import (
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestAgentToolsPreserveOriginalLocalFilterPresenceAndRejectForeignInputs(t *testing.T) {
	b := requestBuilder{}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentSelf, contract.KVToolHTTPAgentServices, contract.KVToolHTTPAgentChecks} {
		for _, a := range []contract.KVHTTPAgentToolInput{{}, {IncludeFilter: true}, {IncludeFilter: true, Filter: "Service == `原服务`"}} {
			input := contract.KVRequestToolInput{HTTPAgent: a}
			r, err := b.BuildKVRequest(kind, input)
			if kind == contract.KVToolHTTPAgentSelf && a.IncludeFilter {
				if !errors.Is(err, ErrInvalid) {
					t.Fatal("agent self acquired a hidden filter")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			native, _, writes, err := decodeDocumentRequest(r)
			if err != nil || writes || native.Method != "GET" || native.Query.Has("filter") != a.IncludeFilter || native.Query.Get("filter") != a.Filter || native.Query.Has("dc") || native.Query.Has("index") {
				t.Fatal("agent tool changed native local-read semantics", err)
			}
			for _, foreign := range []func(*contract.KVRequestToolInput){
				func(i *contract.KVRequestToolInput) { i.Key = "foreign/" },
				func(i *contract.KVRequestToolInput) { i.HTTPHealth.Index = "0" },
				func(i *contract.KVRequestToolInput) { i.HTTPSession.ID = "foreign" },
				func(i *contract.KVRequestToolInput) { i.Recursive = true },
			} {
				bad := input
				foreign(&bad)
				if _, err := b.BuildKVRequest(kind, bad); err == nil {
					t.Fatal("Agent tool silently ignored foreign input")
				}
			}
		}
	}
	for _, filter := range []string{strings.Repeat("x", 4097), "invalid\nfilter", string([]byte{0xff})} {
		if _, err := b.BuildKVRequest(contract.KVToolHTTPAgentChecks, contract.KVRequestToolInput{HTTPAgent: contract.KVHTTPAgentToolInput{IncludeFilter: true, Filter: filter}}); err == nil {
			t.Fatal("invalid native Agent filter accepted")
		}
	}
	input := contract.KVRequestToolInput{HTTPAgent: contract.KVHTTPAgentToolInput{IncludeFilter: true}}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolPut, contract.KVToolHTTPHealthState, contract.KVToolHTTPSessionCreate} {
		if _, err := b.BuildKVRequest(kind, input); !errors.Is(err, ErrInvalid) {
			t.Fatal("foreign tool discarded explicit Agent semantics", err)
		}
	}
	tools := requestTools("original/")
	for i, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentSelf, contract.KVToolHTTPAgentServices, contract.KVToolHTTPAgentChecks} {
		if len(tools) != 20 || tools[17+i].Kind != kind || tools[17+i].Default != (contract.KVRequestToolInput{}) {
			t.Fatal("Agent tools changed existing ordering or inherited prefix")
		}
	}
}
