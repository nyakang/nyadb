package consul

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestAgentTTLToolsPreserveOriginalIDsNotePresenceAndCompleteJSON(t *testing.T) {
	builder := requestBuilder{}
	id := "original 雪%?#++"
	for kind, action := range map[contract.KVRequestToolKind]string{contract.KVToolHTTPAgentCheckPass: "pass", contract.KVToolHTTPAgentCheckWarn: "warn", contract.KVToolHTTPAgentCheckFail: "fail"} {
		for _, note := range []struct {
			include bool
			text    string
		}{{}, {true, ""}, {true, "first\nsecond\r\nNUL\x00雪"}, {true, strings.Repeat("雪", 8192)}} {
			encoded, _ := json.Marshal(note.text)
			input := contract.KVRequestToolInput{HTTPAgentTTL: contract.KVHTTPAgentTTLToolInput{ID: id, IncludeNote: note.include, NoteJSON: string(encoded)}}
			if !note.include {
				input.HTTPAgentTTL.NoteJSON = ""
			}
			document, err := builder.BuildKVRequest(kind, input)
			native, _, writes, decodeErr := decodeDocumentRequest(document)
			if err != nil || decodeErr != nil || !writes || native.Method != http.MethodPut || native.Path != "/v1/agent/check/"+action+"/"+url.PathEscape(id) || native.Query.Has("note") != note.include || native.Query.Get("note") != note.text || len(native.Body) != 0 {
				t.Fatal("TTL tool lost original ID, note presence or write semantics", kind, err, decodeErr)
			}
		}
	}
	for _, output := range []string{"", `,"Output":""`, `,"Output":null`, `,"Output":"first\nsecond\u0000雪"`, `,"UnknownNative":18446744073709551615`} {
		raw := " {\n\"Status\":\"warning\"" + output + "\n} "
		request, err := builder.BuildKVRequest(contract.KVToolHTTPAgentCheckUpdate, contract.KVRequestToolInput{HTTPAgentTTL: contract.KVHTTPAgentTTLToolInput{ID: id, DefinitionJSON: raw}})
		native, _, writes, decodeErr := decodeDocumentRequest(request)
		if err != nil || decodeErr != nil || !writes || !bytes.Equal(native.Body, []byte(raw)) || len(native.Query) != 0 {
			t.Fatal("TTL update tool rebuilt complete authored JSON", err, decodeErr)
		}
	}
	tools := requestTools("foreign-prefix")
	for i, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentCheckPass, contract.KVToolHTTPAgentCheckWarn, contract.KVToolHTTPAgentCheckFail, contract.KVToolHTTPAgentCheckUpdate} {
		if len(tools) != 30 || tools[24+i].Kind != kind || tools[24+i].Default != (contract.KVRequestToolInput{}) {
			t.Fatal("TTL tools inherited routing or synthesized native state")
		}
	}
}

func TestAgentTTLToolsRejectHiddenForeignInputsBoundsAndInvalidOriginalIDs(t *testing.T) {
	builder := requestBuilder{}
	base := contract.KVRequestToolInput{HTTPAgentTTL: contract.KVHTTPAgentTTLToolInput{ID: "original"}}
	for _, raw := range []string{`null`, `[]`, `{}`, `true`, `42`, `"unterminated`, `"first" "second"`, `"invalid\x00escape"`, string([]byte{'"', 255, '"'})} {
		input := base
		input.HTTPAgentTTL.IncludeNote, input.HTTPAgentTTL.NoteJSON = true, raw
		if _, err := builder.BuildKVRequest(contract.KVToolHTTPAgentCheckPass, input); !errors.Is(err, ErrInvalid) {
			t.Fatal("TTL note accepted malformed, null or non-string authored JSON", err)
		}
	}
	for _, change := range []func(*contract.KVRequestToolInput){
		func(i *contract.KVRequestToolInput) { i.Key = "hidden" },
		func(i *contract.KVRequestToolInput) { i.KeysOnly = true },
		func(i *contract.KVRequestToolInput) { i.HTTPAgent.Filter = "hidden" },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentCheck.ID = "hidden" },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentService.IncludeReplacement = true },
		func(i *contract.KVRequestToolInput) { i.HTTPSession.ID = "hidden" },
		func(i *contract.KVRequestToolInput) { i.HTTPHealth.IncludeNear = true },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentTTL.NoteJSON = "hidden" },
		func(i *contract.KVRequestToolInput) { i.HTTPAgentTTL.DefinitionJSON = `{"Status":"passing"}` },
	} {
		input := base
		change(&input)
		if _, err := builder.BuildKVRequest(contract.KVToolHTTPAgentCheckPass, input); !errors.Is(err, ErrInvalid) {
			t.Fatal("TTL status tool silently dropped hidden inputs", err)
		}
	}
	for _, tool := range requestTools("")[:24] {
		if _, err := builder.BuildKVRequest(tool.Kind, base); !errors.Is(err, ErrInvalid) {
			t.Fatal("foreign tool accepted hidden TTL fields", tool.Kind, err)
		}
	}
	for _, id := range []string{"", "invalid\nid", "invalid\x00id", string([]byte{255}), "a/../b", "a//b", "/leading", "trailing/"} {
		input := base
		input.HTTPAgentTTL.ID = id
		if _, err := builder.BuildKVRequest(contract.KVToolHTTPAgentCheckPass, input); err == nil {
			t.Fatal("TTL tool silently cleaned original ID", id)
		}
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAgentCheckPass, contract.KVToolHTTPAgentCheckUpdate} {
		input := base
		if kind == contract.KVToolHTTPAgentCheckUpdate {
			input.HTTPAgentTTL.DefinitionJSON = strings.Repeat("x", requestLimit+1)
		} else {
			input.HTTPAgentTTL.IncludeNote, input.HTTPAgentTTL.NoteJSON = true, strings.Repeat("x", requestLimit+1)
		}
		if _, err := builder.BuildKVRequest(kind, input); !errors.Is(err, ErrBounds) {
			t.Fatal("TTL tool escaped original request budget", err)
		}
	}
	for _, note := range []contract.KVHTTPAgentTTLToolInput{{ID: "original", IncludeNote: true}, {ID: "original", NoteJSON: "hidden"}} {
		note.DefinitionJSON = `{"Status":"passing"}`
		if _, err := builder.BuildKVRequest(contract.KVToolHTTPAgentCheckUpdate, contract.KVRequestToolInput{HTTPAgentTTL: note}); !errors.Is(err, ErrInvalid) {
			t.Fatal("update silently dropped original note option", err)
		}
	}
}
