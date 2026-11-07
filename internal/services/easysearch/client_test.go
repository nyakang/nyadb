package easysearch

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeRequestKeepsExactJSONAndNDJSONAndRejectsAmbiguousScripts(t *testing.T) {
	body := "{\"index\":{\"_id\":\"原文\"}}\n{\"n\":9007199254740993,\"d\":1.123456789012345678901234567890}\n"
	r, err := ParseRequest("# native request\nPOST /target/_bulk\n" + body)
	if err != nil || r.ContentType != "application/x-ndjson" || string(r.Body) != body {
		t.Fatal("original NDJSON changed", err)
	}
	for _, input := range []string{
		"GET /\nGET /another",
		"GET https://other.invalid/",
		"GET //other.invalid/",
		"GET /%2e%2e/private",
		"POST /_bulk\n{\"index\":{}}",
		"POST /_bulk\n{\"index\":{},\"index\":{}}\n",
		"POST /_search\n{\"query\":{\"match\":{\"a\":1,\"a\":2}}}",
		"POST /\n{}\nDELETE /original",
	} {
		if _, err := ParseRequest(input); err == nil {
			t.Fatal("ambiguous, redirected or duplicate native request accepted")
		}
	}
	raw := []byte(`{"exact":9007199254740993,"decimal":1.123456789012345678901234567890}`)
	var preserved json.RawMessage
	if err := decodeJSON(raw, &preserved); err != nil || !bytes.Equal(raw, preserved) {
		t.Fatal("exact native JSON changed", err)
	}
	if validSource([]byte(`{"value":"` + strings.Repeat("x", 1<<20) + `"}`)) {
		t.Fatal("oversized complete source accepted")
	}
}

func TestNativePITCleanupRequiresItsOwnAcknowledgement(t *testing.T) {
	if err := pitClosed([]byte(`{"pits":[{"pit_id":"original","successful":true}]}`), "original"); err != nil {
		t.Fatal("native acknowledgement", err)
	}
	for _, body := range []string{
		`{"succeeded":true,"num_freed":1}`,
		`{"pits":[{"pit_id":"another","successful":true}]}`,
		`{"pits":[{"pit_id":"original","successful":false}]}`,
		`{"pits":[{"pit_id":"original"}]}`,
		`{"pits":[{"pit_id":"original","successful":true},{"pit_id":"another","successful":true}]}`,
	} {
		if err := pitClosed([]byte(body), "original"); err == nil {
			t.Fatal("foreign, incomplete or Elasticsearch cleanup receipt accepted")
		}
	}
}
