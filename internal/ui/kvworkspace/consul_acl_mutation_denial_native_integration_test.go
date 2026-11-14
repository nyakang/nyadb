package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConsulNativePublicACLMutationReadOnlyDenialKeepsOriginalPolicy(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	if secret == "" {
		s, d, _ := consulWorkspaceState(t, p, secret)
		s.SetHTTPACLMutationInput(d, mutationStateInput())
		s.PrepareHTTPACLMutation(d)
		consume(t, s, d)
		assertNativeACLDisabledPreview(t, s, d)
		return
	}
	writer := consulRenewalWriter(t, p, secret)
	create := func(resource string, value any) contract.KVHTTPACLMutationResult {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal("owned ACL definition unavailable")
		}
		return nativeWorkspaceACLMutation(t, writer, contract.KVHTTPACLMutationRequest{Resource: resource, Operation: "create", Definition: body})
	}
	access := create("policy", map[string]string{"Name": "owned-workspace-acl-read-only", "Rules": `acl = "read" agent_prefix "" { policy = "read" }`})
	accessID := nativeWorkspaceACLIdentity(t, access, "ID")
	t.Cleanup(func() {
		nativeWorkspaceACLMutation(t, writer, contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "delete", Identity: accessID})
	})
	credential := create("token", map[string]any{"Description": "owned ACL read-only", "Policies": []map[string]string{{"ID": accessID}}})
	accessor := nativeWorkspaceACLIdentity(t, credential, "AccessorID")
	readSecret := nativeWorkspaceACLIdentity(t, credential, "SecretID")
	t.Cleanup(func() {
		nativeWorkspaceACLMutation(t, writer, contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "delete", Identity: accessor})
	})
	target := create("policy", map[string]string{"Name": "owned-workspace-denied-target", "Description": "original unchanged", "Rules": ""})
	id := nativeWorkspaceACLIdentity(t, target, "ID")
	t.Cleanup(func() {
		nativeWorkspaceACLMutation(t, writer, contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "delete", Identity: id})
	})
	s, d, _ := consulWorkspaceState(t, p, readSecret)
	input := contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "delete", Identity: id}
	s.SetHTTPACLMutationInput(d, input)
	s.PrepareHTTPACLMutation(d)
	consume(t, s, d)
	if d.httpACLMutation.confirmation == nil {
		t.Fatal("read-only original policy preview unavailable")
	}
	s.ConfirmHTTPACLMutation(d, true)
	consume(t, s, d)
	x := d.httpACLMutation
	if x.last == nil || x.last.Receipt.HTTP == nil || x.last.Receipt.HTTP.Status != 403 || !x.last.Receipt.HTTP.Acknowledged || !x.last.Receipt.Completed || x.last.Receipt.HTTP.Succeeded || x.last.Receipt.WriteOutcomeUnknown || d.hasUnknownWrites() || x.last.Impact == nil || !x.last.Impact.Revalidated {
		t.Fatal("native ACL write denial lost complete original impact or became unknown")
	}
	original := x.last.Clone()
	s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "policy", Lookup: "id", Identity: id})
	consume(t, s, d)
	if d.httpACL.last == nil || d.httpACL.last.Receipt.HTTP == nil || !bytes.Contains(d.httpACL.last.Receipt.HTTP.Body, []byte("original unchanged")) || !reflect.DeepEqual(*x.last, original) {
		t.Fatal("later read replaced original denied write or policy was changed")
	}
	// Complete refusal grants no reusable write plan. Only a new explicit
	// preview can prepare another operation after the user changes privileges.
	s.ConfirmHTTPACLMutation(d, true)
	if d.Busy() {
		t.Fatal("native denied confirmation replayed")
	}
	reader := writer.(contract.KVHTTPACLReading)
	plan, err := reader.PrepareHTTPACLRead(contract.KVHTTPACLReadRequest{Resource: "policy", Lookup: "id", Identity: id})
	if err != nil {
		t.Fatal("independent original policy read unavailable")
	}
	r, err := reader.ExecuteHTTPACLRead(context.Background(), plan)
	if err != nil || r.Receipt.HTTP == nil || !bytes.Contains(r.Receipt.HTTP.Body, []byte("original unchanged")) {
		t.Fatal("native denied policy deletion changed target")
	}
}
