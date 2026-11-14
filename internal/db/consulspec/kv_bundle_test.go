package consulspec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"math"
	"strings"
	"testing"
)

func testObservation(key string, exists bool, value []byte, flags, index uint64) contract.KVResult {
	status := 404
	var entries []contract.KVHTTPEntry
	if exists {
		status = 200
		entries = []contract.KVHTTPEntry{{Key: key, Value: value, ValuePresent: true, Flags: flags, ModifyIndex: index, CreateIndex: 1, PresentFields: []string{"Key", "Value", "Flags", "ModifyIndex", "CreateIndex"}}}
	}
	return contract.KVResult{HTTP: &contract.KVHTTPResult{Status: status, Request: contract.KVHTTPRequest{Method: "GET", Path: "/v1/kv/" + key}, Captured: true, EOF: true, Ended: true, Acknowledged: true, Succeeded: true, Entries: entries}}
}
func testBundle() Bundle {
	return Bundle{Format: "nyadb-consul-kv-bundle", Version: 1, Prefix: "key", ScopeKind: "key", Entries: []BundleEntry{{Key: "key", Value: "AP8=", Flags: "18446744073709551615", CreateIndex: "9007199254740993", ModifyIndex: "18446744073709551615"}}}
}
func TestBundleExactUnsignedValuesBinaryAndNullValidation(t *testing.T) {
	b := testBundle()
	raw, _ := json.Marshal(b)
	decoded, err := DecodeBundle(raw)
	if err != nil || decoded.Entries[0].Flags != "18446744073709551615" {
		t.Fatal("native uint64 lost", err)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"AP8="`, `null`, 1), strings.Replace(string(raw), `"AP8="`, `"AP9="`, 1), strings.Replace(string(raw), `"18446744073709551615"`, `18446744073709551615`, 1), strings.Replace(string(raw), `"version":1`, `"version":1,"extra":true`, 1), strings.Replace(string(raw), `"entries":[`, `"entries":[null,`, 1)} {
		if _, err = DecodeBundle([]byte(bad)); err == nil {
			t.Fatal("invalid bundle accepted", bad)
		}
	}
	r := testObservation("key", true, []byte{0, 255}, math.MaxUint64, math.MaxUint64)
	exported, err := ExportBundle(contract.KVScope{}, "key", false, r)
	if err != nil || exported.Entries[0].Flags != b.Entries[0].Flags || exported.Entries[0].Value != "AP8=" {
		t.Fatal("original native binary export changed", err)
	}
	r.HTTP.Metadata.ACLFiltered = new(true)
	if _, err = ExportBundle(contract.KVScope{}, "key", false, r); err == nil {
		t.Fatal("filtered export claimed complete")
	}
}
func TestBundleImportPoliciesLockedTargetsAndIndependentCASBatches(t *testing.T) {
	b := testBundle()
	observed := testObservation("key", true, []byte("different"), 1, math.MaxUint64)
	for _, tc := range []struct {
		policy, op string
		allowed    bool
	}{{"abort", "conflict", false}, {"skip", "skipped", true}, {"cas", "update", true}} {
		p, err := PlanImport(b, tc.policy, []contract.KVResult{observed})
		if err != nil || p.Rows[0].Operation != tc.op || p.CanApply != tc.allowed {
			t.Fatal("policy changed", tc.policy, err)
		}
		if tc.policy == "cas" {
			if len(p.Requests) != 1 || !strings.Contains(p.Requests[0].JSON, "body_base64") {
				t.Fatal("CAS request missing")
			}
		}
	}
	observed.HTTP.Entries[0].Session = "original-session"
	p, err := PlanImport(b, "cas", []contract.KVResult{observed})
	if err != nil || p.Rows[0].Operation != "locked" || len(p.Requests) != 0 {
		t.Fatal("session-held target written")
	}
	b.ScopeKind = "prefix"
	b.Prefix = "key"
	b.Entries = nil
	var reads []contract.KVResult
	for i := range 65 {
		key := fmt.Sprintf("key%d", i)
		e := testBundle().Entries[0]
		e.Key = key
		b.Entries = append(b.Entries, e)
		reads = append(reads, testObservation(key, false, nil, 0, 0))
	}
	p, err = PlanImport(b, "abort", reads)
	if err != nil || len(p.Requests) != 2 {
		t.Fatal("native operation batches not independent", err)
	}
}
func TestSearchCompleteOriginalUTF8LimitsCancellationAndNoncanonicalRange(t *testing.T) {
	r := testObservation("key", true, []byte("hello"), 0, 1)
	r.HTTP.Request.Query = map[string][]string{"recurse": {""}}
	r.HTTP.Entries = append(r.HTTP.Entries, contract.KVHTTPEntry{Key: "key-binary", Value: []byte{255}, ValuePresent: true})
	search := SearchRequest{Prefix: "key", Query: "HELLO", Values: true, Limit: 1, MaxScan: 10}
	result, err := Search(search, r, nil)
	if err != nil || result.Matched != 1 || result.Scanned != 2 || !bytes.Equal(result.Matches[0].Entry.Value, []byte("hello")) {
		t.Fatal("text search changed native values", err)
	}
	_, err = Search(search, r, func(int) error { return context.Canceled })
	if err != context.Canceled {
		t.Fatal("search cancellation ignored")
	}
	request, err := ReadRequest("key//../", true)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Path string
		Body string `json:"body_base64"`
	}
	json.Unmarshal([]byte(request.JSON), &envelope)
	if envelope.Path != "/v1/txn" {
		t.Fatal("original noncanonical prefix was cleaned")
	}
}
