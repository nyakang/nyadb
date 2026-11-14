package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConsulNativeACLMutationWorkspaceOriginalPreviewFaultsAndParking(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	if secret == "" {
		s, d, _ := consulWorkspaceState(t, p, secret)
		s.SetHTTPACLMutationInput(d, mutationStateInput())
		s.PrepareHTTPACLMutation(d)
		consume(t, s, d)
		assertNativeACLDisabledPreview(t, s, d)
		return
	}
	for _, mode := range []string{"lost", "partial", "invalid", "cancel", "disconnect", "close"} {
		t.Run(mode, func(t *testing.T) {
			proxy, calls, reads, processed := consulOriginalToolFault(t, p, mode, func(r *http.Request) bool { return r.Method == "GET" && r.URL.Path == "/v1/acl/tokens" })
			s, d, m := consulWorkspaceState(t, proxy, secret)
			s.SetHTTPACLMutationInput(d, mutationStateInput())
			s.PrepareHTTPACLMutation(d)
			job := d.job
			await(t, processed)
			active := false
			s.deps.Active = func(*Document) bool { return active }
			switch mode {
			case "cancel":
				s.RequestCancel(d)
			case "disconnect":
				m.Disconnect(p.ID)
				s.DisconnectProfile(p.ID)
			case "close":
				s.Close(d)
			}
			await(t, job.finished)
			if reads.Load() != 1 {
				t.Fatal("original preview retried upstream read")
			}
			if mode == "close" {
				if d.httpACLMutation != nil {
					t.Fatal("closed native preview retained private evidence")
				}
				return
			}
			s.Consume()
			if !d.Busy() || d.httpACLMutation.observation != nil {
				t.Fatal("parked native preview entered active state")
			}
			active = true
			consume(t, s, d)
			x := d.httpACLMutation
			if x.confirmation != nil || x.last != nil || x.observation == nil || len(x.observation.Evidence.Preview.Reads) != 1 || x.observation.Evidence.Preview.Complete || d.hasUnknownWrites() {
				t.Fatal("failed native preview became write authority or lost partial original evidence")
			}
			before := calls.Load()
			s.ConfirmHTTPACLMutation(d, true)
			if d.Busy() || calls.Load() != before {
				t.Fatal("failed preview executed a mutation")
			}
			original := x.observation.Evidence.Clone()
			if mode == "disconnect" {
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal("original reconnect unavailable")
				}
			}
			s.DisconnectProfile(p.ID)
			before = calls.Load()
			s.Recover(d)
			consume(t, s, d)
			if calls.Load() != before || d.NeedsRecovery() || !reflect.DeepEqual(x.observation.Evidence, original) {
				t.Fatal("pure recovery reran preview or changed original partial observations")
			}
		})
	}
}

func TestConsulNativeACLMutationWorkspaceChangedTargetKeepsBothObservations(t *testing.T) {
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
	definition := []byte(`{"Name":"owned-workspace-recheck","Description":"before","Rules":""}`)
	created := nativeWorkspaceACLMutation(t, writer, contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "create", Definition: definition})
	id := nativeWorkspaceACLIdentity(t, created, "ID")
	t.Cleanup(func() {
		nativeWorkspaceACLMutation(t, writer, contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "delete", Identity: id})
	})
	s, d, _ := consulWorkspaceState(t, p, secret)
	s.SetHTTPACLMutationInput(d, contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "delete", Identity: id})
	s.PrepareHTTPACLMutation(d)
	consume(t, s, d)
	if d.httpACLMutation.confirmation == nil {
		t.Fatal("native original target preview unavailable")
	}
	preview := d.httpACLMutation.confirmation.impact.Clone()
	changed, _ := json.Marshal(map[string]string{"Name": "owned-workspace-recheck", "Description": "independent-change", "Rules": ""})
	nativeWorkspaceACLMutation(t, writer, contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "update", Identity: id, Definition: changed})
	s.ConfirmHTTPACLMutation(d, true)
	consume(t, s, d)
	x := d.httpACLMutation
	if x.last != nil || x.observation == nil || !reflect.DeepEqual(x.observation.Evidence.Preview, preview) || len(x.observation.Evidence.Recheck.Reads) == 0 || x.observation.Evidence.Revalidated || x.confirmation != nil || d.hasUnknownWrites() {
		t.Fatal("changed native target dispatched deletion or lost both observations")
	}
	reader := writer.(contract.KVHTTPACLReading)
	plan, err := reader.PrepareHTTPACLRead(contract.KVHTTPACLReadRequest{Resource: "policy", Lookup: "id", Identity: id})
	if err != nil {
		t.Fatal("independent policy read unavailable")
	}
	r, err := reader.ExecuteHTTPACLRead(context.Background(), plan)
	if err != nil || r.Receipt.HTTP == nil || !bytes.Contains(r.Receipt.HTTP.Body, []byte("independent-change")) {
		t.Fatal("recheck refusal did not preserve native target")
	}
}
