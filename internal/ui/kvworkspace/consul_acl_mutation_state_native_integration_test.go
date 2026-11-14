package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func nativeWorkspaceACLMutation(t *testing.T, l contract.KVRequestLease, input contract.KVHTTPACLMutationRequest) contract.KVHTTPACLMutationResult {
	t.Helper()
	facet, ok := l.(contract.KVHTTPACLMutating)
	if !ok {
		t.Fatal("original native ACL mutation facet unavailable")
	}
	plan, err := facet.PrepareHTTPACLMutation(input)
	if err != nil {
		t.Fatal("original native ACL fixture plan unavailable")
	}
	r, err := facet.ExecuteHTTPACLMutation(context.Background(), plan)
	if err != nil || !r.Receipt.Completed || r.Receipt.WriteOutcomeUnknown {
		t.Fatal("original native ACL fixture request failed")
	}
	return r
}
func nativeWorkspaceACLIdentity(t *testing.T, r contract.KVHTTPACLMutationResult, field string) string {
	t.Helper()
	var value map[string]json.RawMessage
	var id string
	if r.Receipt.HTTP == nil || json.Unmarshal(r.Receipt.HTTP.Body, &value) != nil || json.Unmarshal(value[field], &id) != nil || id == "" {
		t.Fatal("original ACL mutation identity unavailable")
	}
	return id
}

func TestConsulNativePrivateACLMutationWorkspaceSecretsConfirmationAndPureRecovery(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	s, d, m := consulWorkspaceState(t, p, secret)
	originalEditor := d.Request()
	input := contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "create", Definition: []byte(` {"Description":"owned workspace token"} `)}
	s.SetHTTPACLMutationInput(d, input)
	s.PrepareHTTPACLMutation(d)
	consume(t, s, d)
	if secret == "" {
		assertNativeACLDisabledPreview(t, s, d)
		return
	}
	if d.httpACLMutation == nil || d.httpACLMutation.confirmation == nil || d.Busy() || d.httpACLMutationContent() {
		t.Fatal("native mutation preparation bypassed confirmation")
	}
	s.ConfirmHTTPACLMutation(d, false)
	if d.Busy() {
		t.Fatal("unchecked native mutation executed")
	}
	s.ConfirmHTTPACLMutation(d, true)
	s.ConfirmHTTPACLMutation(d, true)
	consume(t, s, d)
	if !d.httpACLMutationContent() || d.last != nil || d.unknown != nil || d.Request() != originalEditor {
		t.Fatal("original private native mutation entered generic document")
	}
	original := d.httpACLMutation.last.Clone()
	if secret == "" {
		if original.Receipt.HTTP == nil || original.Receipt.HTTP.Status != 401 || d.hasUnknownWrites() {
			t.Fatal("ACL-disabled mutation refusal changed")
		}
		return
	}
	if original.Receipt.HTTP == nil || !original.Receipt.Completed || d.hasUnknownWrites() || original.Projection == nil || !original.Projection.ContainsSensitiveValues {
		t.Fatal("native token mutation receipt lost generated secret classification")
	}
	id := nativeWorkspaceACLIdentity(t, original, "AccessorID")
	generated := nativeWorkspaceACLIdentity(t, original, "SecretID")
	writer := consulRenewalWriter(t, p, secret)
	t.Cleanup(func() {
		nativeWorkspaceACLMutation(t, writer, contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "delete", Identity: id})
	})
	if _, ok := s.RevealedHTTPACLMutationReceipt(d); ok {
		t.Fatal("generated native secret automatically revealed")
	}
	if !s.RevealHTTPACLMutationReceipt(d, original.LocalRecordID) {
		t.Fatal("native original reveal unavailable")
	}
	revealed, ok := s.RevealedHTTPACLMutationReceipt(d)
	if !ok || !bytes.Contains(revealed.Receipt.HTTP.Body, []byte(generated)) {
		t.Fatal("explicit native reveal changed generated secret")
	}
	s.HideHTTPACLMutationReceipt(d)
	s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "id", Identity: id})
	consume(t, s, d)
	if !reflect.DeepEqual(*d.httpACLMutation.last, original) || d.httpACL.last.Projection == nil || !bytes.Contains(d.httpACL.last.Receipt.HTTP.Body, []byte(generated)) {
		t.Fatal("later native token read lost original private write")
	}
	for range 2 {
		m.Disconnect(p.ID)
		s.Consume()
		if err := m.Connect(context.Background(), p, secret); err != nil {
			t.Fatal("original native reconnect failed")
		}
		s.Recover(d)
		consume(t, s, d)
		if d.NeedsRecovery() || !reflect.DeepEqual(*d.httpACLMutation.last, original) || d.scope.Generation <= original.Scope.Generation || d.Request() != originalEditor || !reflect.DeepEqual(d.httpACLMutation.input, input) {
			t.Fatal("native pure recovery replayed or relabeled original secret receipt")
		}
		if _, ok := s.RevealedHTTPACLMutationReceipt(d); ok {
			t.Fatal("native recovery revealed old secret")
		}
	}
}

func TestConsulNativePrivateACLMutationWorkspaceProcessedFaultsParkingAndWriteBarrier(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	if secret == "" {
		s, d, _ := consulWorkspaceState(t, p, secret)
		runACLMutation(t, s, d, mutationStateInput())
		consume(t, s, d)
		assertNativeACLDisabledPreview(t, s, d)
		return
	}
	writer := consulRenewalWriter(t, p, secret)
	for _, mode := range []string{"lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "disconnect", "replace", "parked", "close"} {
		t.Run(mode, func(t *testing.T) {
			definition, _ := json.Marshal(map[string]string{"Name": "owned-workspace-" + mode, "Description": "original", "Rules": ""})
			created := nativeWorkspaceACLMutation(t, writer, contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "create", Definition: definition})
			id := nativeWorkspaceACLIdentity(t, created, "ID")
			t.Cleanup(func() {
				nativeWorkspaceACLMutation(t, writer, contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "delete", Identity: id})
			})
			proxy, calls, writes, processed := consulOriginalToolFault(t, p, mode, func(r *http.Request) bool { return r.Method == "PUT" && r.URL.Path == "/v1/acl/policy/"+id })
			s, d, m := consulWorkspaceState(t, proxy, secret)
			originalEditor := d.Request()
			definition, _ = json.Marshal(map[string]string{"Name": "owned-workspace-" + mode, "Description": "actual original update", "Rules": `acl = "read"`})
			input := contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "update", Identity: id, Definition: definition}
			before := calls.Load()
			s.SetHTTPACLMutationInput(d, input)
			s.PrepareHTTPACLMutation(d)
			consume(t, s, d)
			if d.httpACLMutation.confirmation == nil || calls.Load() <= before || writes.Load() != 0 {
				t.Fatal("native private preview failed or dispatched a mutation")
			}
			s.ConfirmHTTPACLMutation(d, true)
			job, lease := d.job, d.lease
			await(t, processed)
			active := false
			s.deps.Active = func(*Document) bool { return active }
			switch mode {
			case "cancel":
				s.RequestCancel(d)
			case "disconnect":
				m.Disconnect(p.ID)
				s.DisconnectProfile(p.ID)
			case "replace":
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal("original replacement failed")
				}
			case "close":
				s.Close(d)
			}
			await(t, job.finished)
			reader := writer.(contract.KVHTTPACLReading)
			plan, err := reader.PrepareHTTPACLRead(contract.KVHTTPACLReadRequest{Resource: "policy", Lookup: "id", Identity: id})
			if err != nil {
				t.Fatal("independent policy read unavailable")
			}
			actual, err := reader.ExecuteHTTPACLRead(context.Background(), plan)
			if err != nil || !bytes.Contains(actual.Receipt.HTTP.Body, []byte("actual original update")) || writes.Load() != 1 {
				t.Fatal("fault did not follow exactly one actual ACL mutation")
			}
			if mode == "close" {
				if d.httpACLMutation != nil || d.Editor != nil || lease.CheckKVLease() == nil {
					t.Fatal("native close retained private mutation resources")
				}
				return
			}
			s.Consume()
			if d.job != job || d.httpACLMutationContent() || !d.Busy() {
				t.Fatal("parked original ACL mutation consumed")
			}
			active = true
			consume(t, s, d)
			if !d.httpACLMutationContent() || !d.hasUnknownWrites() || !d.httpACLMutation.last.Receipt.WriteOutcomeUnknown || d.httpACLMutation.last.Receipt.HTTP.Acknowledged || d.last != nil || d.unknown != nil {
				t.Fatal("native processed ACL write lost independent unknown receipt")
			}
			original := d.httpACLMutation.last.Clone()
			if mode == "disconnect" {
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal("original native reconnect unavailable")
				}
			}
			if !d.NeedsRecovery() {
				s.DisconnectProfile(p.ID)
			}
			before = calls.Load()
			s.Recover(d)
			consume(t, s, d)
			if d.NeedsRecovery() || calls.Load() != before || !reflect.DeepEqual(*d.httpACLMutation.last, original) || d.Request() != originalEditor {
				t.Fatal("pure native ACL recovery replayed or changed original receipt")
			}
			s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "policy", Lookup: "id", Identity: id})
			consume(t, s, d)
			consulWorkspaceInput(t, s, d, consul.Request{Method: "GET", Path: "/v1/kv/", Query: map[string][]string{"recurse": {""}}})
			s.Run(d)
			consume(t, s, d)
			if !d.hasUnknownWrites() || !reflect.DeepEqual(*d.httpACLMutation.last, original) || d.unknown != nil {
				t.Fatal("later native reads erased ACL unknown barrier")
			}
			before = calls.Load()
			s.PrepareHTTPACLMutation(d)
			consume(t, s, d)
			s.ConfirmHTTPACLMutation(d, true)
			consulWorkspaceInput(t, s, d, consul.Request{Method: "PUT", Path: "/v1/kv/owned-workspace-after-acl", Body: []byte("never")})
			s.Run(d)
			s.Confirm(d, true)
			if d.Busy() || calls.Load() != before || writes.Load() != 1 {
				t.Fatal("later reads permitted original or generic write replay")
			}
		})
	}
}

func assertNativeACLDisabledPreview(t *testing.T, s *State, d *Document) {
	t.Helper()
	x := d.httpACLMutation
	if x == nil || x.confirmation != nil || x.observation == nil || x.last != nil || d.hasUnknownWrites() || len(x.observation.Evidence.Preview.Reads) != 1 {
		t.Fatal("ACL-disabled preview did not retain original refusal without write authority")
	}
	h := x.observation.Evidence.Preview.Reads[0].Result.Receipt.HTTP
	if h == nil || h.Status != 401 {
		t.Fatal("ACL-disabled preview refusal absent")
	}
	s.ConfirmHTTPACLMutation(d, true)
	if d.Busy() {
		t.Fatal("ACL-disabled preview allowed mutation")
	}
}
