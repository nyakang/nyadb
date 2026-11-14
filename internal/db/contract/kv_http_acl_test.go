package contract

import (
	"reflect"
	"testing"
)

func TestHTTPACLRecoveryCloneSeparatesOriginalSecretEvidence(t *testing.T) {
	request := KVHTTPACLReadRequest{Resource: "token", Lookup: "list", Policy: new("policy"), Role: new("role"), AuthMethod: new("auth"), ServiceName: new("service"), Index: new(uint64(17)), Wait: new("1s")}
	result := KVHTTPACLReadResult{Scope: KVScope{Generation: 9, Prefix: []byte("original/")}, Request: request,
		Receipt:    KVResult{HTTP: &KVHTTPResult{Body: []byte(`{"secret":"test"}`), Request: KVHTTPRequest{Query: map[string][]string{"index": {"17"}}}}},
		Projection: &KVHTTPACLProjection{Rows: []KVHTTPACLRow{{Raw: []byte(`{"secret":"test"}`), PresentFields: []string{"SecretID"}, NullFields: []string{"Description"}, CreateIndex: new(uint64(11)), ModifyIndex: new(uint64(17))}}}}
	original := KVRecovery{ACLResult: &result}
	clone := original.Clone()
	if !reflect.DeepEqual(original, clone) {
		t.Fatal("ACL recovery clone changed original evidence")
	}
	copy := clone.ACLResult
	copy.Scope.Prefix[0] = 'x'
	copy.Receipt.HTTP.Body[0] = 'x'
	copy.Receipt.HTTP.Request.Query["index"][0] = "changed"
	copy.Projection.Rows[0].Raw[0] = 'x'
	copy.Projection.Rows[0].PresentFields[0] = "changed"
	copy.Projection.Rows[0].NullFields[0] = "changed"
	*copy.Projection.Rows[0].CreateIndex = 1
	*copy.Projection.Rows[0].ModifyIndex = 1
	for _, field := range []*string{copy.Request.Policy, copy.Request.Role, copy.Request.AuthMethod, copy.Request.ServiceName, copy.Request.Wait} {
		*field = "changed"
	}
	*copy.Request.Index = 1
	if string(result.Scope.Prefix) != "original/" || result.Receipt.HTTP.Body[0] != '{' || result.Receipt.HTTP.Request.Query["index"][0] != "17" || result.Projection.Rows[0].Raw[0] != '{' || result.Projection.Rows[0].PresentFields[0] != "SecretID" || result.Projection.Rows[0].NullFields[0] != "Description" || *result.Projection.Rows[0].CreateIndex != 11 || *result.Projection.Rows[0].ModifyIndex != 17 || *result.Request.Policy != "policy" || *result.Request.Role != "role" || *result.Request.AuthMethod != "auth" || *result.Request.ServiceName != "service" || *result.Request.Wait != "1s" || *result.Request.Index != 17 {
		t.Fatal("cloned ACL recovery aliases original request, raw bytes or projection")
	}
	if original.Last.HTTP != nil || original.Unknown != nil {
		t.Fatal("private ACL evidence entered generic recovery receipts")
	}
}
