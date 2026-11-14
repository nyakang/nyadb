package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConsulNativeACLMutationDocumentOriginalReceiptsRecoveryAndProcessedFaults(t *testing.T) {
	base, cfg, secret := nativeClient(t)
	build := nativeCatalogRequest(t)
	if cfg.Auth != "token" {
		s, err := OpenNativeSession(context.Background(), 1, cfg, secret)
		if err != nil {
			t.Fatal("owned anonymous document session unavailable")
		}
		defer s.Close()
		l := aclDocumentLease(t, s, 1)
		p := aclMutationPlan(t, l, aclTokenCreate())
		r, err := l.ExecuteHTTPACLMutation(context.Background(), p)
		if err == nil || r.Receipt.HTTP == nil || r.Receipt.HTTP.Status != 401 || r.Receipt.WriteOutcomeUnknown || l.LastResult().HTTP != nil {
			t.Fatal("native ACL-disabled refusal lost original private receipt")
		}
		return
	}
	for _, action := range []string{"create", "update", "delete", "clone"} {
		for _, mode := range []string{"success", "lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "close"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				resource := ACLReadToken
				if action == "update" {
					resource = ACLReadPolicy
				} else if action == "delete" {
					resource = ACLReadRole
				}
				name := "owned-document-" + action + "-" + mode
				sourceID := ""
				removeSource := action != "create"
				if removeSource {
					fields := map[string]any{"Description": "owned original"}
					identityField := "AccessorID"
					if resource != ACLReadToken {
						fields["Name"], identityField = name, "ID"
					}
					raw := nativeACLFixtureCall(t, base, "PUT", "/v1/acl/"+string(resource), fields)
					sourceID = nativeACLString(t, raw, identityField)
					t.Cleanup(func() {
						if removeSource {
							nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/"+string(resource)+"/"+url.PathEscape(sourceID), nil)
						}
					})
				}
				input := contract.KVHTTPACLMutationRequest{Resource: string(resource), Operation: action, Identity: sourceID}
				switch action {
				case "create":
					input.Definition = []byte(` {"Description":"owned actual create"} `)
				case "update":
					input.Definition, _ = json.Marshal(map[string]string{"Name": name, "Description": "owned actual update", "Rules": `acl = "read"`})
				case "clone":
					input.Description = new("owned actual clone")
				}
				request, err := aclNativeMutationRequest(input)
				if err != nil {
					t.Fatal("original native document input invalid")
				}
				relay, writes, processed := nativeACLMutationFault(t, cfg, secret, mode, request)
				s, err := OpenNativeSession(context.Background(), 1, relay, secret)
				if err != nil {
					t.Fatal("owned original document session unavailable")
				}
				t.Cleanup(func() { s.Close() })
				l := aclDocumentLease(t, s, 1)
				p := aclMutationPlan(t, l, input)
				pending := aclMutationPlan(t, l, input)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type completion struct {
					result contract.KVHTTPACLMutationResult
					err    error
				}
				done := make(chan completion, 1)
				go func() { r, e := l.ExecuteHTTPACLMutation(ctx, p); done <- completion{r, e} }()
				var observed []byte
				select {
				case observed = <-processed:
				case <-time.After(20 * time.Second):
					t.Fatal("original native document mutation was not processed")
				}
				identity := sourceID
				if action == "create" || action == "clone" {
					identity = nativeACLString(t, observed, "AccessorID")
					t.Cleanup(func() { nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/token/"+identity, nil) })
				} else if action == "delete" {
					removeSource = false
				}
				closed := make(chan struct{})
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					go func() { l.Close(); close(closed) }()
				}
				var out completion
				select {
				case out = <-done:
				case <-time.After(20 * time.Second):
					t.Fatal("original document executor did not acknowledge loss or cancellation")
				}
				if mode == "close" {
					wait(t, closed)
				}
				original := out.result.Clone()
				receipt := original.Receipt
				if receipt.HTTP == nil || !receipt.Attempted || !receipt.HTTP.Ended || writes.Load() != 1 || !reflect.DeepEqual(original.Request, input) || original.LocalRecordID != p.LocalRecordID() || l.LastResult().HTTP != nil || l.recoveryOwned().Unknown != nil {
					t.Fatal("original private document receipt changed, escaped or replayed")
				}
				if mode == "success" {
					if out.err != nil || !receipt.Completed || !receipt.HTTP.Succeeded || receipt.WriteOutcomeUnknown || !bytes.Equal(receipt.HTTP.Body, observed) || (original.Projection == nil) != (action == "delete") {
						t.Fatal("complete native mutation lost original ACK or projection")
					}
					if resource == ACLReadToken && (!original.Projection.ContainsSensitiveValues || nativeACLString(t, receipt.HTTP.Body, "SecretID") != nativeACLString(t, observed, "SecretID")) {
						t.Fatal("original generated token receipt or secret classification changed")
					}
				} else if out.err == nil || receipt.Completed || receipt.HTTP.Acknowledged || !receipt.WriteOutcomeUnknown || original.Projection != nil {
					t.Fatal("processed unknown document mutation promoted to success")
				}
				if mode == "partial" && (receipt.HTTP.Captured || receipt.HTTP.EOF || !bytes.Equal(receipt.HTTP.Body, observed[:len(observed)/2])) {
					t.Fatal("original partial mutation response lost boundary")
				}
				if mode != "close" {
					if !reflect.DeepEqual(l.LastHTTPACLMutation(), original) {
						t.Fatal("parked original mutation evidence lost")
					}
					recovery, err := s.RecoverKV(l)
					if err != nil {
						t.Fatal("original document recovery failed")
					}
					next := recovery.Lease.(*documentLease)
					defer next.Close()
					if recovery.ACLMutationResult == nil || !reflect.DeepEqual(*recovery.ACLMutationResult, original) || recovery.Unknown != nil || recovery.Last.HTTP != nil || !reflect.DeepEqual(next.LastHTTPACLMutation(), original) || writes.Load() != 1 {
						t.Fatal("recovery replayed or mixed original private mutation")
					}
					if _, err := next.ExecuteHTTPACLMutation(context.Background(), pending); !errors.Is(err, contract.ErrKVPlanUsed) {
						t.Fatal("recovery reused original write authority")
					}
					aclDocumentRead(t, next, contract.KVHTTPACLReadRequest{Resource: string(resource), Lookup: "list"})
					executeDocument(t, next, Request{Method: "GET", Path: "/v1/kv/", Query: url.Values{"recurse": {""}}})
					if !reflect.DeepEqual(next.LastHTTPACLMutation(), original) {
						t.Fatal("later ACL/KV read replaced original mutation")
					}
					if mode != "success" {
						if _, err := next.PrepareHTTPACLMutation(input); !errors.Is(err, contract.ErrKVWriteUnknown) {
							t.Fatal("recovered read cleared ACL unknown barrier")
						}
						generic, _ := DocumentRequest(putRequest("owned-document-later", nil))
						if _, err := next.Prepare(generic); !errors.Is(err, contract.ErrKVWriteUnknown) {
							t.Fatal("recovered read cleared generic unknown barrier")
						}
					}
				} else if l.LastHTTPACLMutation().Receipt.HTTP != nil || l.cacheBytes != 0 {
					t.Fatal("closed document retained private cache")
				}
				read := build(ACLItemReadRequest(resource, identity))
				if action == "delete" {
					nativeACLRead(t, base, read, 404)
				} else {
					actual := nativeACLRead(t, base, read, 200)
					if resource == ACLReadToken && nativeACLString(t, actual.Body, "SecretID") != nativeACLString(t, observed, "SecretID") {
						t.Fatal("native state differs from original upstream secret receipt")
					}
					if nativeACLString(t, actual.Body, "Description") != "owned actual "+action {
						t.Fatal("relay fault did not follow actual mutation")
					}
				}
				if writes.Load() != 1 {
					t.Fatal("original document mutation replayed")
				}
			})
		}
	}
}
