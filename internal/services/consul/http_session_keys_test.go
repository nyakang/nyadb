package consul

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func sessionKeysProjectionFixture(t *testing.T, body string) contract.KVHTTPSessionKeysObservation {
	t.Helper()
	native, err := KeyListRequest("", false)
	if err != nil {
		t.Fatal(err)
	}
	document, err := DocumentRequest(native)
	if err != nil {
		t.Fatal(err)
	}
	r := Response{Request: native, Kind: "kv-read", Protocol: "HTTP/1.1", Status: 200, Attempted: true, Ended: true, Captured: true, EOF: true, Body: []byte(body)}
	if err := projectResponse(&r, Plan{kind: "kv-read", request: native}); err != nil {
		t.Fatal(err)
	}
	return contract.KVHTTPSessionKeysObservation{Scope: contract.KVScope{Namespace: contract.NamespaceRef{ProfileID: 1, Catalog: "kv"}, Generation: 11, Prefix: []byte("configured-prefix/"), Version: "2.0.2", HTTP: contract.KVHTTPIdentity{Protocol: "consul-http", Endpoint: "https://original", Datacenter: "dc1"}}, Request: contract.KVHTTPSessionKeysRequest{ID: sessionToolID, MaxRows: contract.KVHTTPSessionKeysMaxRows, MaxBytes: contract.KVHTTPSessionKeysMaxBytes}, Root: documentResult(document, r)}
}

func TestHTTPSessionKeysProjectionKeepsWholeRootOrderExactValuesAndOriginalScope(t *testing.T) {
	body := `[{"Key":"outside/original","Value":"AP+A","Flags":18446744073709551615,"ModifyIndex":9007199254740993,"Session":"` + sessionToolID + `","Unknown":{"number":18446744073709551615}},{"Key":"configured-prefix/other","Value":null,"Session":null},{"Key":"outside/second","Value":"","Session":"` + sessionToolID + `"}]`
	o := sessionKeysProjectionFixture(t, body)
	original := o.Root.Clone()
	if err := projectSessionKeys(&o); err != nil || !o.ProjectionComplete || !o.Complete || !reflect.DeepEqual(o.Matches, []int{0, 2}) || !reflect.DeepEqual(o.Root, original) || string(o.Scope.Prefix) != "configured-prefix/" {
		t.Fatal("whole-root projection changed scope, order or original evidence", err)
	}
	row := o.Root.HTTP.Entries[o.Matches[0]]
	if row.Flags != math.MaxUint64 || row.ModifyIndex != 9007199254740993 || !bytes.Equal(row.Value, []byte{0, 255, 128}) || !bytes.Contains(o.Root.HTTP.Body, []byte(`"number":18446744073709551615`)) || o.Root.HTTP.Request.Path != "/v1/kv/" || len(o.Root.HTTP.Request.Query) != 1 {
		t.Fatal("original bytes, unsigned values, unknown fields or root request changed")
	}
	clone := o.Clone()
	clone.Scope.Prefix[0] = 'X'
	clone.Matches[0] = 2
	clone.Root.HTTP.Body[0] = 'X'
	clone.Root.HTTP.Entries[0].Value[0] = 77
	if !reflect.DeepEqual(o.Root, original) || o.Matches[0] != 0 || string(o.Scope.Prefix) != "configured-prefix/" {
		t.Fatal("cloned observation aliases original receipt or match indices")
	}
}

func TestHTTPSessionKeysFilteredAndEmptyResponsesRetainOriginalPresence(t *testing.T) {
	for _, filtered := range []*bool{nil, new(false), new(true)} {
		o := sessionKeysProjectionFixture(t, `[]`)
		o.Root.HTTP.Metadata.ACLFiltered = filtered
		if err := projectSessionKeys(&o); err != nil || !o.ProjectionComplete || o.Matches == nil || o.Complete != (filtered == nil || !*filtered) || o.Root.HTTP.Metadata.ACLFiltered != filtered {
			t.Fatal("ACL presence or exact empty root observation changed", err)
		}
		o.Root.HTTP.Status = 404
		o.Root.HTTP.Body = nil
		if err := projectSessionKeys(&o); err != nil || !o.ProjectionComplete || o.Complete != (filtered == nil || !*filtered) {
			t.Fatal("acknowledged original root absence lost its ACL boundary", err)
		}
	}
}

func TestHTTPSessionKeysBudgetsChargeUnmatchedEntriesAndRejectPartialProjection(t *testing.T) {
	rows := []map[string]any{{"Key": "unmatched", "Value": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 2048))}, {"Key": "selected", "Value": "", "Session": sessionToolID}}
	body, _ := json.Marshal(rows)
	o := sessionKeysProjectionFixture(t, string(body))
	if err := projectSessionKeys(&o); err != nil {
		t.Fatal(err)
	}
	fullCost := o.Bytes
	o.Request.MaxRows = 1
	if err := projectSessionKeys(&o); !errors.Is(err, ErrBounds) || o.Matches != nil || o.ProjectionComplete || o.Complete || !o.Root.HTTP.Acknowledged {
		t.Fatal("unmatched root rows escaped budget or became a partial complete set", err)
	}
	o.Request.MaxRows = 2
	o.Request.MaxBytes = fullCost - 1
	if err := projectSessionKeys(&o); !errors.Is(err, ErrBounds) || o.Matches != nil || o.ProjectionComplete || !bytes.Equal(o.Root.HTTP.Body, body) {
		t.Fatal("byte bound discarded original evidence or permitted incomplete projection", err)
	}
	o.Request.MaxBytes = fullCost
	if err := projectSessionKeys(&o); err != nil || o.Bytes != fullCost || !reflect.DeepEqual(o.Matches, []int{1}) {
		t.Fatal("exact complete projection budget rejected", err)
	}
}

func TestHTTPSessionKeysRejectForeignIncompleteAndNonRootObservations(t *testing.T) {
	original := sessionKeysProjectionFixture(t, `[]`)
	for _, change := range []func(*contract.KVHTTPSessionKeysObservation){
		func(o *contract.KVHTTPSessionKeysObservation) { o.Request.ID = "42" },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Request.ID = strings.ToUpper(sessionToolID) },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Request.MaxRows = 0 },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Request.MaxRows = 10001 },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Request.MaxBytes = 0 },
		func(o *contract.KVHTTPSessionKeysObservation) {
			o.Request.MaxBytes = contract.KVHTTPSessionKeysMaxBytes + 1
		},
		func(o *contract.KVHTTPSessionKeysObservation) { o.Scope.HTTP.Protocol = "etcd" },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Scope.Namespace.ProfileID = 0 },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Scope.Generation = 0 },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.Request.Method = "KV/Range" },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.HTTP.Request.Path = "/v1/kv/configured-prefix/" },
		func(o *contract.KVHTTPSessionKeysObservation) {
			o.Root.HTTP.Request.Query = map[string][]string{"keys": {""}}
		},
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.HTTP.Request.Query["limit"] = []string{"1"} },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.HTTP.EOF = false },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.Attempted = false },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.HTTP.Attempted = false },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.HTTP.Ended = false },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.HTTP.Acknowledged = false },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.HTTP.Status = 403 },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.HTTP.ProjectionError = "unavailable" },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.WriteOutcomeUnknown = true },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Root.HTTP.Body = []byte(`null`) },
	} {
		o := original.Clone()
		change(&o)
		if err := projectSessionKeys(&o); err == nil || o.ProjectionComplete || o.Complete || o.Matches != nil {
			t.Fatal("foreign or incomplete receipt granted a whole-root projection")
		}
	}
}
