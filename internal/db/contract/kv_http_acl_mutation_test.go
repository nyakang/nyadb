package contract

import (
	"reflect"
	"testing"
)

func TestHTTPACLMutationRecoveryCloneOwnsPrivateRequestAndReceipt(t *testing.T) {
	result := KVHTTPACLMutationResult{LocalRecordID: "original", Scope: KVScope{Prefix: []byte("owned/")},
		Request:    KVHTTPACLMutationRequest{Resource: "token", Operation: "clone", Identity: "original", Definition: []byte(`{}`), Description: new("description")},
		Receipt:    KVResult{HTTP: &KVHTTPResult{Body: []byte(`{"SecretID":"synthetic"}`), Request: KVHTTPRequest{Body: []byte(`{}`), Query: map[string][]string{"dc": {"original"}}}}},
		Projection: &KVHTTPACLProjection{Rows: []KVHTTPACLRow{{Raw: []byte(`{}`), PresentFields: []string{"SecretID"}, ModifyIndex: new(uint64(7))}}}}
	original := KVRecovery{ACLMutationResult: &result}
	want := original.Clone()
	clone := original.Clone()
	if !reflect.DeepEqual(original, clone) {
		t.Fatal("clone changed original evidence")
	}
	r := clone.ACLMutationResult
	r.Scope.Prefix[0] = 'x'
	r.Request.Definition[0] = 'x'
	*r.Request.Description = "changed"
	r.Receipt.HTTP.Body[0] = 'x'
	r.Receipt.HTTP.Request.Body[0] = 'x'
	r.Receipt.HTTP.Request.Query["dc"][0] = "changed"
	r.Projection.Rows[0].Raw[0] = 'x'
	r.Projection.Rows[0].PresentFields[0] = "changed"
	*r.Projection.Rows[0].ModifyIndex = 1
	if !reflect.DeepEqual(original, want) || original.Last.HTTP != nil || original.Unknown != nil || original.ACLResult != nil {
		t.Fatal("private mutation evidence aliased or entered another receipt cache")
	}
}
