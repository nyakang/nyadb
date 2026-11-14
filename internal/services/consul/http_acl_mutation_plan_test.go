package consul

import (
	"bytes"
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

func TestACLMutationPlanAllExistingResourcesKeepOriginalDefinitionsAndClone(t *testing.T) {
	for _, resource := range []string{"token", "policy", "role", "auth-method", "binding-rule"} {
		operations := []string{"update", "delete"}
		if resource == "token" {
			operations = append(operations, "clone")
		}
		for _, operation := range operations {
			t.Run(resource+"/"+operation, func(t *testing.T) {
				id, key := aclMutationTestID, "ID"
				if resource == "token" {
					key = "AccessorID"
				}
				if resource == "auth-method" {
					id, key = "owned-method", "Name"
				}
				input := contract.KVHTTPACLMutationRequest{Resource: resource, Operation: operation, Identity: id}
				if operation == "update" {
					input.Definition = []byte(" {\n\"Description\":\"original exact definition\"\n} ")
				}
				if operation == "clone" {
					input.Description = new("original clone")
				}
				expected := input.Clone()
				native, err := aclNativeMutationRequest(expected)
				if err != nil {
					t.Fatal(err)
				}
				var writes atomic.Int32
				s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" {
						writes.Add(1)
						body, _ := io.ReadAll(r.Body)
						if r.Method != native.Method || r.URL.EscapedPath() != native.Path || !bytes.Equal(body, native.Body) {
							t.Error("original planned definition or route changed")
						}
						if operation == "delete" {
							io.WriteString(w, "true")
							return
						}
						identity := id
						if operation == "clone" {
							identity = aclMutationOtherID
						}
						ack := map[string]any{key: identity, "CreateIndex": 7, "ModifyIndex": 8}
						if resource == "token" {
							ack["SecretID"] = "synthetic-original"
						}
						json.NewEncoder(w).Encode(ack)
						return
					}
					if r.URL.Path == "/v1/acl/"+resource+"/"+id {
						json.NewEncoder(w).Encode(map[string]any{key: id, "ModifyIndex": 8})
						return
					}
					io.WriteString(w, "[]")
				})
				l := aclDocumentLease(t, s, 1)
				p, preview, err := l.PrepareHTTPACLMutationPlan(context.Background(), input, aclPlanLimits())
				if err != nil {
					t.Fatal(err)
				}
				if len(input.Definition) > 0 {
					input.Definition[0] = 'x'
				}
				if input.Description != nil {
					*input.Description = "changed"
				}
				inspection := p.Request()
				if len(inspection.Definition) > 0 {
					inspection.Definition[0] = 'x'
				}
				if inspection.Description != nil {
					*inspection.Description = "changed again"
				}
				if !reflect.DeepEqual(p.Request(), expected) {
					t.Fatal("original plan input aliases inspection")
				}
				out, err := l.ExecuteHTTPACLMutation(context.Background(), p)
				if err != nil || !out.Receipt.Completed || out.Impact == nil || !out.Impact.Revalidated || !reflect.DeepEqual(out.Impact.Preview, preview) || !reflect.DeepEqual(out.Request, expected) || writes.Load() != 1 {
					t.Fatal("planned native operation failed", err)
				}
			})
		}
	}
}

func TestACLMutationPlanCancellationAndCloseWaitAtOriginalRecheckAndWrite(t *testing.T) {
	for _, stage := range []string{"recheck", "write"} {
		for _, mode := range []string{"cancel", "close-plan", "close-lease"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				started := make(chan struct{})
				returned := make(chan struct{})
				var reads, writes atomic.Int32
				s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
					block := false
					if r.Method == "GET" {
						block = reads.Add(1) == 4 && stage == "recheck"
					} else {
						writes.Add(1)
						block = stage == "write"
					}
					if block {
						w.WriteHeader(200)
						io.WriteString(w, `{"original":`)
						w.(http.Flusher).Flush()
						close(started)
						<-r.Context().Done()
						close(returned)
						return
					}
					if strings.HasPrefix(r.URL.Path, "/v1/acl/policy/") {
						io.WriteString(w, `{"ID":"`+impactPolicy+`"}`)
					} else {
						io.WriteString(w, "[]")
					}
				})
				l := aclDocumentLease(t, s, 1)
				p, preview, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits())
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type completion struct {
					r contract.KVHTTPACLMutationResult
					e error
				}
				done := make(chan completion, 1)
				go func() { r, e := l.ExecuteHTTPACLMutation(ctx, p); done <- completion{r, e} }()
				wait(t, started)
				if _, err := l.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVBusy) {
					t.Fatal("original executor overlapped")
				}
				closed := make(chan struct{})
				switch mode {
				case "cancel":
					cancel()
					close(closed)
				case "close-plan":
					go func() { p.Close(); close(closed) }()
				case "close-lease":
					go func() { l.Close(); close(closed) }()
				}
				out := <-done
				wait(t, returned)
				wait(t, closed)
				if out.e == nil || out.r.Impact == nil || !reflect.DeepEqual(out.r.Impact.Preview, preview) || out.r.Impact.Revalidated != (stage == "write") || out.r.Receipt.WriteOutcomeUnknown != (stage == "write") || l.s.reserved != 0 || l.planReserved != 0 {
					t.Fatal("original cancellation lost impact, outcome or reservation")
				}
				if stage == "recheck" && (out.r.Receipt.Attempted || writes.Load() != 0 || len(out.r.Impact.Recheck.Reads) != 1 || out.r.Impact.Recheck.Reads[0].Result.Receipt.HTTP.EOF) {
					t.Fatal("cancelled recheck executed mutation or lost partial receipt")
				}
				if stage == "write" && (writes.Load() != 1 || !out.r.Receipt.Attempted || !out.r.Receipt.HTTP.Ended || out.r.Receipt.HTTP.EOF) {
					t.Fatal("cancelled original write receipt lost")
				}
			})
		}
	}
}

func aclPlanLimits() contract.KVHTTPACLImpactLimits {
	return contract.KVHTTPACLImpactLimits{MaxRows: 10000, MaxReads: 128, MaxBytes: 32 << 20}
}

type aclPlanFixture struct {
	l                        *documentLease
	reads, writes            atomic.Int32
	changed, unknown, denied atomic.Bool
}

func newACLPlanFixture(t *testing.T) *aclPlanFixture {
	t.Helper()
	f := &aclPlanFixture{}
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			f.writes.Add(1)
			if f.unknown.Load() {
				w.WriteHeader(500)
				io.WriteString(w, "original processed response unavailable")
				return
			}
			if r.Method == "DELETE" {
				io.WriteString(w, "true")
				return
			}
			if r.URL.Path == "/v1/acl/token" {
				io.WriteString(w, aclMutationTokenACK)
				return
			}
			t.Error("unexpected native mutation request")
			w.WriteHeader(400)
			return
		}
		f.reads.Add(1)
		if f.denied.Load() {
			w.WriteHeader(403)
			io.WriteString(w, "denied original read")
			return
		}
		switch r.URL.Path {
		case "/v1/acl/policy/" + impactPolicy:
			io.WriteString(w, `{"ID":"`+impactPolicy+`","Name":"original","ModifyIndex":8,"Hash":"unchanged"}`)
		case "/v1/acl/tokens":
			json.NewEncoder(w).Encode([]any{impactRow(impactDirect, !f.changed.Load(), false)})
		case "/v1/acl/roles":
			io.WriteString(w, "[]")
		default:
			t.Error("unexpected native impact read")
			w.WriteHeader(400)
		}
	})
	f.l = aclDocumentLease(t, s, 1)
	return f
}

func aclDeleteInput() contract.KVHTTPACLMutationRequest {
	return contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "delete", Identity: impactPolicy}
}

func TestACLMutationPlanOriginalPreviewRechecksThenSendsOnce(t *testing.T) {
	f := newACLPlanFixture(t)
	l := f.l
	p, preview, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits())
	if err != nil || p == nil || !preview.Complete || f.reads.Load() != 3 || f.writes.Load() != 0 || l.planReserved == 0 || !reflect.DeepEqual(p.Impact(), preview) {
		t.Fatal("original read preview failed", err)
	}
	foreign := aclDocumentLease(t, l.s, 2)
	if _, err := foreign.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("foreign lease accepted original impact plan")
	}
	if _, ok := p.(contract.KVPreparation); ok {
		t.Fatal("private plan exposed generic executor")
	}
	if _, err := l.ExecuteHTTPACLMutation(context.Background(), p.(*httpACLMutationPlan).prepared); !errors.Is(err, contract.ErrKVBusy) {
		t.Fatal("pure private route bypassed active impact authority")
	}
	earlyKV := documentPlan(t, l, putRequest("owned", nil))
	if _, err := l.Execute(context.Background(), earlyKV); !errors.Is(err, contract.ErrKVBusy) {
		t.Fatal("generic write bypassed reserved impact plan")
	}
	if _, err := l.PrepareHTTPSessionDestroy(context.Background(), nativeDestroyImpactRequest(sessionToolID)); !errors.Is(err, contract.ErrKVBusy) {
		t.Fatal("destroy planning overlapped original ACL plan")
	}
	if _, err := l.ObserveHTTPACLImpact(context.Background(), preview.Request); !errors.Is(err, contract.ErrKVBusy) {
		t.Fatal("observer reservation replaced original plan")
	}
	if _, _, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits()); !errors.Is(err, contract.ErrKVBusy) {
		t.Fatal("second original ACL plan replaced first")
	}
	copy := p.Impact()
	copy.Reads[0].Result.Receipt.HTTP.Body[0] = 'x'
	copy.References = nil
	if !reflect.DeepEqual(p.Impact(), preview) {
		t.Fatal("caller modified immutable preview")
	}
	out, err := l.ExecuteHTTPACLMutation(context.Background(), p)
	if err != nil || !out.Receipt.Completed || out.Receipt.WriteOutcomeUnknown || out.Impact == nil || !out.Impact.Revalidated || !reflect.DeepEqual(out.Impact.Preview, preview) || CompareACLImpact(out.Impact.Preview, out.Impact.Recheck) != nil || f.writes.Load() != 1 || f.reads.Load() != 6 {
		t.Fatal("original revalidated write failed", err)
	}
	if !reflect.DeepEqual(out, l.LastHTTPACLMutation()) || l.LastResult().HTTP != nil || l.planReserved != 0 || l.s.reserved != 0 {
		t.Fatal("original impact evidence lost or reservation leaked")
	}
	want := aclMutationResultCost(&out) + aclReadResultCost(l.aclLast)
	if l.cacheBytes != want || l.s.retained != l.s.rootBytes+want {
		t.Fatal("private impact evidence escaped shared receipt budget")
	}
	if p.Impact().Complete || p.Request().Resource != "" {
		t.Fatal("consumed plan retained private caches")
	}
	if _, err := l.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || f.writes.Load() != 1 {
		t.Fatal("impact plan replayed")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestACLMutationPlanChangedOrDeniedRecheckNeverWritesAndRetainsBothObservations(t *testing.T) {
	for _, mode := range []string{"changed", "denied", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newACLPlanFixture(t)
			l := f.l
			old, err := l.ExecuteHTTPACLMutation(context.Background(), aclMutationPlan(t, l, aclTokenCreate()))
			if err != nil {
				t.Fatal(err)
			}
			p, preview, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "changed":
				f.changed.Store(true)
			case "denied":
				f.denied.Store(true)
			case "cancelled":
				cancel()
			}
			out, err := l.ExecuteHTTPACLMutation(ctx, p)
			if err == nil || out.Receipt.Attempted || out.Receipt.WriteOutcomeUnknown || out.Impact == nil || out.Impact.Revalidated || !reflect.DeepEqual(out.Impact.Preview, preview) || f.writes.Load() != 1 || !reflect.DeepEqual(l.LastHTTPACLMutation(), old) || l.s.reserved != 0 {
				t.Fatal("failed recheck wrote, erased old write, or discarded preview")
			}
			if mode == "changed" && (!errors.Is(err, ErrConflict) || !out.Impact.Recheck.Complete) {
				t.Fatal("content change with stable index was accepted")
			}
			if mode == "denied" && (len(out.Impact.Recheck.Reads) != 1 || out.Impact.Recheck.Reads[0].Result.Receipt.HTTP.Status != 403) {
				t.Fatal("original denied recheck lost")
			}
			if _, err := l.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("failed original plan became replayable")
			}
		})
	}
}

func TestACLMutationPlanUnknownRetainsOriginalImpactAcrossReadsAndPureRecovery(t *testing.T) {
	f := newACLPlanFixture(t)
	l := f.l
	p, preview, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits())
	if err != nil {
		t.Fatal(err)
	}
	f.unknown.Store(true)
	out, err := l.ExecuteHTTPACLMutation(context.Background(), p)
	if err == nil || !out.Receipt.WriteOutcomeUnknown || out.Impact == nil || !out.Impact.Revalidated || !reflect.DeepEqual(out.Impact.Preview, preview) {
		t.Fatal("unknown original write lost impact")
	}
	if _, err := l.ObserveHTTPACLImpact(context.Background(), preview.Request); err != nil {
		t.Fatal(err)
	}
	before := f.reads.Load()
	writes := f.writes.Load()
	if _, _, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits()); !errors.Is(err, contract.ErrKVWriteUnknown) || f.reads.Load() != before {
		t.Fatal("unknown barrier allowed new plan reads")
	}
	recovered, err := l.s.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Lease.Close()
	next := recovered.Lease.(*documentLease)
	if recovered.ACLMutationResult == nil || !reflect.DeepEqual(*recovered.ACLMutationResult, out) || !reflect.DeepEqual(next.LastHTTPACLMutation(), out) || recovered.Last.HTTP != nil || recovered.Unknown != nil || f.reads.Load() != before || f.writes.Load() != writes {
		t.Fatal("pure recovery lost original impact or replayed I/O")
	}
	recovered.ACLMutationResult.Impact.Preview.Reads[0].Result.Receipt.HTTP.Body[0] = 'x'
	if !reflect.DeepEqual(next.LastHTTPACLMutation(), out) {
		t.Fatal("recovered impact aliases caller")
	}
	if _, _, err := next.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits()); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("recovery cleared original unknown barrier")
	}
}

func TestACLMutationPlanCloseRecoveryAndFailedPreparationReleaseOriginalEvidence(t *testing.T) {
	for _, mode := range []string{"close-plan", "close-lease", "recover", "retire"} {
		t.Run(mode, func(t *testing.T) {
			f := newACLPlanFixture(t)
			l := f.l
			p, _, err := l.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits())
			if err != nil {
				t.Fatal(err)
			}
			before := f.reads.Load()
			switch mode {
			case "close-plan":
				p.Close()
				p.Close()
			case "close-lease":
				l.Close()
			case "retire":
				l.retire()
			case "recover":
				r, err := l.s.RecoverKV(l)
				if err != nil {
					t.Fatal(err)
				}
				defer r.Lease.Close()
			}
			if p.Impact().Complete || p.Request().Resource != "" || l.planReserved != 0 || l.s.reserved != 0 || f.reads.Load() != before || f.writes.Load() != 0 {
				t.Fatal("plan close/recovery retained caches or executed")
			}
			if _, err := l.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("disposed impact plan retained authority")
			}
		})
	}
	f := newACLPlanFixture(t)
	f.denied.Store(true)
	p, o, err := f.l.PrepareHTTPACLMutationPlan(context.Background(), aclDeleteInput(), aclPlanLimits())
	if err == nil || p != nil || o.Complete || len(o.Reads) != 1 || o.Reads[0].Result.Receipt.HTTP.Status != 403 || f.l.planReserved != 0 || f.l.s.reserved != 0 {
		t.Fatal("failed prepare granted authority or discarded original observation")
	}
	before := f.reads.Load()
	unsupported := aclTokenCreate()
	unsupported.Operation = "unsupported"
	if _, _, err := f.l.PrepareHTTPACLMutationPlan(context.Background(), unsupported, aclPlanLimits()); !errors.Is(err, ErrUnsupported) || f.reads.Load() != before {
		t.Fatal("planner silently accepted an unsupported operation")
	}
}
