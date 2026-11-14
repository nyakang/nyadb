package consul

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const (
	impactPolicy    = "11111111-2222-4333-8444-000000000001"
	impactRole      = "11111111-2222-4333-8444-000000000002"
	impactDirect    = "11111111-2222-4333-8444-000000000003"
	impactInherited = "11111111-2222-4333-8444-000000000004"
	impactBoth      = "11111111-2222-4333-8444-000000000005"
	impactStale     = "11111111-2222-4333-8444-000000000006"
)

func impactInput(resource, id string) contract.KVHTTPACLImpactRequest {
	return contract.KVHTTPACLImpactRequest{Resource: resource, Identity: id, MaxReads: 128, MaxRows: 10000, MaxBytes: 32 << 20}
}

func impactRow(id string, policy, role bool) map[string]any {
	r := map[string]any{"AccessorID": id, "CreateIndex": 7, "ModifyIndex": 8, "Hash": "unchanged"}
	if policy {
		r["Policies"] = []map[string]string{{"ID": impactPolicy}}
	}
	if role {
		r["Roles"] = []map[string]string{{"ID": impactRole}}
	}
	return r
}

func impactPolicyFixture(t *testing.T, change *atomic.Bool) (*documentLease, *atomic.Int32) {
	t.Helper()
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Get("policy") != "" && r.URL.Query().Get("role") != "" {
			t.Error("impact combined native membership filters or performed a write")
		}
		linked := !change.Load()
		switch r.URL.Path {
		case "/v1/acl/policy/" + impactPolicy:
			io.WriteString(w, `{"ID":"`+impactPolicy+`","Name":"owned-policy","ModifyIndex":8,"Hash":"unchanged"}`)
		case "/v1/acl/tokens":
			if r.URL.Query().Get("policy") == impactPolicy {
				json.NewEncoder(w).Encode([]any{impactRow(impactDirect, linked, false), impactRow(impactBoth, linked, true), impactRow(impactStale, false, false)})
			} else if r.URL.Query().Get("role") == impactRole {
				json.NewEncoder(w).Encode([]any{impactRow(impactInherited, false, true), impactRow(impactBoth, linked, true), impactRow(impactStale, false, false)})
			} else {
				t.Error("original membership identity changed")
				w.WriteHeader(400)
			}
		case "/v1/acl/roles":
			json.NewEncoder(w).Encode([]any{map[string]any{"ID": impactRole, "Name": "owned-role", "Policies": []map[string]string{{"ID": impactPolicy}}, "ModifyIndex": 8, "Hash": "unchanged"}})
		default:
			t.Error("unexpected impact request")
			w.WriteHeader(400)
		}
	})
	return aclDocumentLease(t, s, 1), calls
}

func TestACLImpactTraversesActualPolicyLinksDeduplicatesAndKeepsWholePrivateReceipts(t *testing.T) {
	var changed atomic.Bool
	l, _ := impactPolicyFixture(t, &changed)
	input := impactInput("policy", impactPolicy)
	out, err := l.ObserveHTTPACLImpact(context.Background(), input)
	if err != nil || !out.Complete || len(out.Reads) != 4 || out.Rows != 8 || len(out.References) != 4 || out.Bytes != aclImpactCost(out) {
		t.Fatal("incomplete original impact", err)
	}
	for _, ref := range out.References {
		if ref.Identity == impactStale {
			t.Fatal("stale membership index became an actual link")
		}
		want := 1
		if ref.Identity == impactBoth {
			want = 2
		}
		if len(ref.Locations) != want {
			t.Fatal("original direct/inherited token membership lost")
		}
		for _, loc := range ref.Locations {
			if out.Reads[loc.Read].Result.Projection.Rows[loc.Row].Identity != ref.Identity {
				t.Fatal("reference points to another original row")
			}
		}
	}
	for _, read := range out.Reads {
		if read.Result.LocalRecordID == "" || read.BodySHA256 != aclImpactBodyHash(read.Result.Receipt.HTTP.Body) || !reflect.DeepEqual(read.Result.Scope, l.Scope()) {
			t.Fatal("complete original receipt or scope lost")
		}
	}
	if l.LastResult().HTTP != nil || l.LastHTTPACLMutation().Receipt.HTTP != nil || l.s.reserved != 0 || l.planReserved != 0 {
		t.Fatal("impact escaped private reads or reservation leaked")
	}
	if err := CompareACLImpact(out, out.Clone()); err != nil {
		t.Fatal("original impact could not revalidate", err)
	}
	copy := out.Clone()
	copy.Reads[0].Result.Receipt.HTTP.Body[0] = 'x'
	copy.References[0].Locations[0].Row = 100
	if err := CompareACLImpact(out, copy); err == nil || out.References[0].Locations[0].Row == 100 {
		t.Fatal("impact aliases returned evidence")
	}
	for _, edit := range []func(*contract.KVHTTPACLImpact){
		func(o *contract.KVHTTPACLImpact) {
			o.Reads = o.Reads[:1]
			o.Rows = 1
			o.References = nil
			o.Bytes = aclImpactCost(*o)
		},
		func(o *contract.KVHTTPACLImpact) { o.References = nil; o.Bytes = aclImpactCost(*o) },
		func(o *contract.KVHTTPACLImpact) { o.Reads[1].Result.Request.Policy = new(impactRole) },
		func(o *contract.KVHTTPACLImpact) { o.Reads[1].Result.Scope.Generation++ },
		func(o *contract.KVHTTPACLImpact) { o.Reads[1].BodySHA256 = strings.Repeat("0", 64) },
		func(o *contract.KVHTTPACLImpact) { o.Reads[1].Result.Projection.Rows = nil },
	} {
		invalid := out.Clone()
		edit(&invalid)
		if err := CompareACLImpact(invalid, invalid.Clone()); err == nil {
			t.Fatal("forged incomplete observation validated against itself")
		}
	}
	changed.Store(true)
	next, err := l.ObserveHTTPACLImpact(context.Background(), input)
	if err != nil || !next.Complete || len(next.References) != 3 {
		t.Fatal("changed returned links not observed", err)
	}
	if !errors.Is(CompareACLImpact(out, next), ErrConflict) {
		t.Fatal("unchanged native index/hash bypassed raw-body comparison")
	}
	if *out.Reads[1].Result.Receipt.HTTP.Metadata.Index != *next.Reads[1].Result.Receipt.HTTP.Metadata.Index {
		t.Fatal("fixture unexpectedly changed query index")
	}
}

func TestACLImpactAllTargetsAuthMethodReferencesAndMissingTargets(t *testing.T) {
	for _, resource := range []string{"token", "role", "auth-method", "binding-rule"} {
		t.Run(resource, func(t *testing.T) {
			id := impactRole
			if resource == "auth-method" {
				id = "owned-auth"
			}
			s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/acl/" + resource + "/" + id:
					key := "ID"
					if resource == "token" {
						key = "AccessorID"
					}
					if resource == "auth-method" {
						key = "Name"
					}
					json.NewEncoder(w).Encode(map[string]any{key: id, "ModifyIndex": 8})
				case "/v1/acl/binding-rules":
					json.NewEncoder(w).Encode([]any{map[string]string{"ID": impactDirect, "AuthMethod": "owned-auth"}, map[string]string{"ID": impactStale, "AuthMethod": "unrelated"}})
				case "/v1/acl/tokens":
					if resource == "auth-method" {
						if r.URL.Query().Get("authmethod") != id {
							t.Error("auth method identity changed")
						}
						json.NewEncoder(w).Encode([]any{map[string]any{"AccessorID": impactDirect, "AuthMethod": "owned-auth", "Local": true}})
					} else {
						json.NewEncoder(w).Encode([]any{impactRow(impactInherited, false, true)})
					}
				default:
					t.Error("unexpected original target route")
					w.WriteHeader(404)
				}
			})
			l := aclDocumentLease(t, s, 1)
			o, err := l.ObserveHTTPACLImpact(context.Background(), impactInput(resource, id))
			want := 1
			refs := 0
			if resource == "role" {
				want, refs = 2, 1
			}
			if resource == "auth-method" {
				want, refs = 3, 2
			}
			if err != nil || !o.Complete || len(o.Reads) != want || len(o.References) != refs {
				t.Fatal("native target reference traversal failed", err)
			}
		})
	}
	for _, status := range []int{401, 403, 404, 500} {
		var attempts atomic.Int32
		s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.WriteHeader(status)
			io.WriteString(w, "original target unavailable")
		})
		l := aclDocumentLease(t, s, 1)
		o, err := l.ObserveHTTPACLImpact(context.Background(), impactInput("policy", impactPolicy))
		// Only the original target may be attempted; no fallback membership read.
		if err == nil || o.Complete || len(o.Reads) != 1 || o.Reads[0].Result.Receipt.HTTP.Status != status || attempts.Load() != 1 {
			t.Fatal("absent or denied target became an empty reference set")
		}
	}
}

func TestACLImpactBoundsPartialLinksAndReservationRefuseFurtherReads(t *testing.T) {
	for _, mode := range []string{"rows", "reads", "bytes", "filtered", "malformed", "null-list", "duplicate-link"} {
		t.Run(mode, func(t *testing.T) {
			var reads atomic.Int32
			s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				if r.URL.Path == "/v1/acl/policy/"+impactPolicy {
					io.WriteString(w, `{"ID":"`+impactPolicy+`"}`)
					return
				}
				if r.URL.Path != "/v1/acl/tokens" {
					t.Error("continued after incomplete original read")
				}
				switch mode {
				case "filtered":
					w.Header().Set("X-Consul-Results-Filtered-By-ACLs", "true")
					io.WriteString(w, "[]")
				case "malformed":
					io.WriteString(w, `[{"AccessorID":"`+impactDirect+`","Policies":[{"Name":"unresolved"}]}]`)
				case "duplicate-link":
					io.WriteString(w, `[{"AccessorID":"`+impactDirect+`","Policies":[{"ID":"`+impactPolicy+`"},{"ID":"`+impactPolicy+`"}]}]`)
				case "null-list":
					io.WriteString(w, "null")
				default:
					json.NewEncoder(w).Encode([]any{impactRow(impactDirect, true, false)})
				}
			})
			l := aclDocumentLease(t, s, 1)
			input := impactInput("policy", impactPolicy)
			if mode == "rows" {
				input.MaxRows = 1
			}
			if mode == "reads" {
				input.MaxReads = 1
			}
			if mode == "bytes" {
				input.MaxBytes = 1
			}
			o, err := l.ObserveHTTPACLImpact(context.Background(), input)
			want := int32(2)
			if mode == "reads" {
				want = 1
			}
			if mode == "bytes" {
				want = 0
			}
			if err == nil || o.Complete || reads.Load() != want || l.s.reserved != 0 || l.planReserved != 0 {
				t.Fatal("incomplete observation continued or leaked reservation", err)
			}
			if want > 0 && len(o.Reads) != int(want) {
				t.Fatal("original incomplete receipt discarded")
			}
		})
	}
	var change atomic.Bool
	l, calls := impactPolicyFixture(t, &change)
	before := calls.Load()
	input := impactInput("policy", impactPolicy)
	input.MaxReads = 129
	if _, err := l.ObserveHTTPACLImpact(context.Background(), input); !errors.Is(err, ErrInvalid) || calls.Load() != before {
		t.Fatal("invalid limits performed I/O")
	}
	l.s.budget = l.s.retained + pendingReceiptBudget
	if _, err := l.ObserveHTTPACLImpact(context.Background(), impactInput("policy", impactPolicy)); !errors.Is(err, ErrBounds) || calls.Load() != before {
		t.Fatal("shared budget refusal performed I/O")
	}
}

func TestACLImpactCancellationAndCloseWaitForOriginalRead(t *testing.T) {
	for _, closeLease := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "close"}[closeLease], func(t *testing.T) {
			started := make(chan struct{})
			returned := make(chan struct{})
			s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				io.WriteString(w, `{"ID":"`+impactPolicy+`",`)
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(returned)
			})
			l := aclDocumentLease(t, s, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				o contract.KVHTTPACLImpact
				e error
			}
			done := make(chan completion, 1)
			go func() {
				o, e := l.ObserveHTTPACLImpact(ctx, impactInput("policy", impactPolicy))
				done <- completion{o, e}
			}()
			wait(t, started)
			if _, err := l.ObserveHTTPACLImpact(context.Background(), impactInput("policy", impactPolicy)); !errors.Is(err, contract.ErrKVBusy) {
				t.Fatal("overlapped original read")
			}
			closed := make(chan struct{})
			if closeLease {
				go func() { l.Close(); close(closed) }()
			} else {
				cancel()
			}
			out := <-done
			wait(t, returned)
			if closeLease {
				wait(t, closed)
			}
			if out.e == nil || out.o.Complete || len(out.o.Reads) != 1 || out.o.Reads[0].Result.Receipt.HTTP.EOF || out.o.Reads[0].Result.Receipt.HTTP.Captured || l.s.reserved != 0 {
				t.Fatal("cancelled original response promoted or reservation leaked")
			}
		})
	}
}
