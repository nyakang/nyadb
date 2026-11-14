package consul

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestAgentMaintenanceToolsPreserveNativeBooleanReasonPresenceAndControlText(t *testing.T) {
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentNodeMaintenance, contract.KVToolHTTPAgentServiceMaintenance} {
		for _, enable := range []string{"1", "t", "T", "TRUE", "true", "True", "0", "f", "F", "FALSE", "false", "False"} {
			for _, reason := range []struct {
				include     bool
				json, value string
			}{{}, {include: true}, {true, `""`, ""}, {true, `"  first\nsecond\r\nNUL\u0000雪 %?#++  "`, "  first\nsecond\r\nNUL\x00雪 %?#++  "}, {true, strconv.Quote(strings.Repeat("雪", 4096)), strings.Repeat("雪", 4096)}} {
				input := contract.KVRequestToolInput{HTTPAgentMaintenance: contract.KVHTTPAgentMaintenanceToolInput{Enable: enable, IncludeReason: reason.include, ReasonJSON: reason.json}}
				path := "/v1/agent/maintenance"
				if kind == contract.KVToolHTTPAgentServiceMaintenance {
					input.HTTPAgentMaintenance.ID = "original 雪%?#++"
					path = "/v1/agent/service/maintenance/" + url.PathEscape(input.HTTPAgentMaintenance.ID)
				}
				before := input
				doc, err := (requestBuilder{}).BuildKVRequest(kind, input)
				r, canonical, writes, decodeErr := decodeDocumentRequest(doc)
				if err != nil || decodeErr != nil || !writes || canonical != doc || r.Method != "PUT" || r.Path != path || len(r.Body) != 0 || r.Query.Get("enable") != enable || r.Query.Has("reason") != reason.include || r.Query.Get("reason") != reason.value || input != before {
					t.Fatal("maintenance tool changed original native request or reason presence", kind, enable, err, decodeErr)
				}
			}
		}
	}
	tools := requestTools("foreign-prefix/")
	if len(tools) != 35 || tools[28].Kind != contract.KVToolHTTPAgentNodeMaintenance || tools[29].Kind != contract.KVToolHTTPAgentServiceMaintenance || tools[28].Default.HTTPAgentMaintenance.Enable != "true" || tools[29].Default.HTTPAgentMaintenance.ID != "" {
		t.Fatal("maintenance tools inherited foreign targets or defaults")
	}
}

func TestAgentMaintenanceToolsRejectHiddenScopesInvalidBooleansAndIncompleteReasons(t *testing.T) {
	base := contract.KVRequestToolInput{HTTPAgentMaintenance: contract.KVHTTPAgentMaintenanceToolInput{Enable: "true"}}
	for _, change := range []func(*contract.KVRequestToolInput){
		func(i *contract.KVRequestToolInput) { i.Key = "foreign" },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentTTL.ID = "foreign" },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentService.IncludeReplacement = true },
		func(i *contract.KVRequestToolInput) { i.HTTPHealth.Filter = "foreign" },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentMaintenance.Enable = "" },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentMaintenance.Enable = " true" },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentMaintenance.Enable = "2" },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentMaintenance.ID = "foreign-service" },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentMaintenance.ReasonJSON = `"hidden"` },
		func(i *contract.KVRequestToolInput) {
			i.HTTPAgentMaintenance.IncludeReason = true
			i.HTTPAgentMaintenance.ReasonJSON = "null"
		},
		func(i *contract.KVRequestToolInput) {
			i.HTTPAgentMaintenance.IncludeReason = true
			i.HTTPAgentMaintenance.ReasonJSON = "{}"
		},
		func(i *contract.KVRequestToolInput) {
			i.HTTPAgentMaintenance.IncludeReason = true
			i.HTTPAgentMaintenance.ReasonJSON = `"incomplete`
		},
	} {
		input := base
		change(&input)
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPAgentNodeMaintenance, input); !errors.Is(err, ErrInvalid) {
			t.Fatal("maintenance silently dropped hidden or invalid input", input, err)
		}
	}
	for _, id := range []string{"", "invalid\nid", "a//b", "a/../b", "/leading", "trailing/"} {
		input := base
		input.HTTPAgentMaintenance.ID = id
		if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPAgentServiceMaintenance, input); err == nil {
			t.Fatal("maintenance tool normalized an unsupported original ID", id)
		}
	}
	input := base
	input.HTTPAgentMaintenance.IncludeReason = true
	input.HTTPAgentMaintenance.ReasonJSON = strconv.Quote(strings.Repeat("x", requestLimit))
	if _, err := (requestBuilder{}).BuildKVRequest(contract.KVToolHTTPAgentNodeMaintenance, input); !errors.Is(err, ErrBounds) {
		t.Fatal("maintenance tool accepted an incomplete oversized reason", err)
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolRead, contract.KVToolHTTPAgentServiceRegister, contract.KVToolHTTPAgentCheckPass, contract.KVToolHTTPSessionCreate} {
		if _, err := (requestBuilder{}).BuildKVRequest(kind, base); !errors.Is(err, ErrInvalid) {
			t.Fatal("other tool silently discarded maintenance input", kind, err)
		}
	}
}
