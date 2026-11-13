package consul

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type destroyPlanServer struct {
	version atomic.Uint64
	reads   atomic.Int32
	writes  atomic.Int32
	mode    atomic.Int32
	action  http.HandlerFunc
}

func destroyPlanFixture(t *testing.T, action http.HandlerFunc) (*Session, *documentLease, *destroyPlanServer) {
	t.Helper()
	f := &destroyPlanServer{action: action}
	f.version.Store(18446744073709551615)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Consul-Index", "18446744073709551615")
		switch r.URL.Path {
		case "/v1/agent/self":
			fmt.Fprint(w, `{"Config":{"Version":"2.0.2","NodeID":"`+testAgentID+`","NodeName":"original","Datacenter":"dc1"}}`)
		case "/v1/status/leader":
			fmt.Fprint(w, `"127.0.0.1:8300"`)
		case "/v1/kv/configured":
			fmt.Fprint(w, `[]`)
		case "/v1/session/info/" + sessionToolID:
			f.reads.Add(1)
			behavior := "delete"
			if f.mode.Load() == 1 {
				behavior = "release"
			}
			fmt.Fprint(w, `[{"ID":"`+sessionToolID+`","Behavior":"`+behavior+`","future":{"unsigned":18446744073709551615}}]`)
		case "/v1/kv/":
			if !r.URL.Query().Has("recurse") {
				fmt.Fprint(w, `[]`)
				return
			}
			f.reads.Add(1)
			switch f.mode.Load() {
			case 2:
				w.Header().Set("X-Consul-Results-Filtered-By-ACLs", "true")
			case 3:
				fmt.Fprint(w, `null`)
				return
			case 4:
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprintf(w, `[{"Key":"outside/original","Value":"AP8=","ModifyIndex":%d,"Session":"%s"}]`, f.version.Load(), sessionToolID)
		case "/v1/session/destroy/" + sessionToolID:
			if r.Method != "PUT" || r.Body != http.NoBody && r.ContentLength != 0 {
				t.Error("original bodyless PUT changed")
			}
			f.writes.Add(1)
			f.action(w, r)
		default:
			t.Errorf("unexpected original path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	s, err := OpenNativeSession(context.Background(), 2304, Config{Endpoint: server.URL, Prefix: "configured"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return s, l.(*documentLease), f
}

func prepareDestroyPlan(t *testing.T, l *documentLease) contract.KVHTTPSessionDestroyPreparation {
	t.Helper()
	p, err := l.PrepareHTTPSessionDestroy(context.Background(), nativeDestroyImpactRequest(sessionToolID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestHTTPSessionDestroyPlanOriginalSinglePUTAndCompleteEvidence(t *testing.T) {
	for _, response := range []string{"true", "false", "denied", "lost"} {
		t.Run(response, func(t *testing.T) {
			s, l, f := destroyPlanFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch response {
				case "denied":
					w.WriteHeader(403)
					fmt.Fprint(w, "Permission denied")
				case "lost":
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					connection.Close()
				default:
					fmt.Fprint(w, response)
				}
			})
			p := prepareDestroyPlan(t, l)
			preview := p.Impact()
			mutated := p.Impact()
			mutated.Keys.Root.HTTP.Body[0] = 'x'
			mutated.Keys.Root.HTTP.Entries[0].ModifyIndex = 0
			if !reflect.DeepEqual(preview, p.Impact()) || f.reads.Load() != 2 || f.writes.Load() != 0 || !p.Writes() {
				t.Fatal("mutable preview or premature native write")
			}
			foreign, _ := s.ReserveKV(2)
			defer foreign.Close()
			if _, err := foreign.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("foreign plan dispatched", err)
			}
			result, err := l.Execute(context.Background(), p)
			if (err != nil) != (response != "true") || f.writes.Load() != 1 || f.reads.Load() != 4 || result.HTTP == nil || result.Request != p.Snapshot() || result.HTTPSessionDestroy == nil || !result.HTTPSessionDestroy.Revalidated || !reflect.DeepEqual(preview, result.HTTPSessionDestroy.Preview) || !result.HTTPSessionDestroy.Recheck.Complete {
				t.Fatal("original single PUT or evidence changed", err)
			}
			if result.WriteOutcomeUnknown != (response == "lost") || result.Completed != (response != "lost") || !reflect.DeepEqual(result, l.LastResult()) {
				t.Fatal("native outcome promoted or evidence cache incomplete")
			}
			if response == "true" || response == "false" {
				if string(result.HTTP.Body) != response || result.HTTP.Succeeded != (response == "true") {
					t.Fatal("native boolean rewritten")
				}
			}
			if _, err := l.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || f.writes.Load() != 1 {
				t.Fatal("consumed destroy replayed", err)
			}
			before := f.reads.Load()
			recovery, err := s.RecoverKV(l)
			if err != nil || f.reads.Load() != before || f.writes.Load() != 1 || !reflect.DeepEqual(result, recovery.Last) || !reflect.DeepEqual(result, recovery.Lease.LastResult()) || l.planReserved != 0 {
				t.Fatal("recovery lost evidence or performed I/O", err)
			}
			defer recovery.Lease.Close()
			clone := recovery.Last.Clone()
			clone.HTTPSessionDestroy.Preview.Session.HTTP.Body[0] = 'x'
			if bytes.Equal(clone.HTTPSessionDestroy.Preview.Session.HTTP.Body, recovery.Lease.LastResult().HTTPSessionDestroy.Preview.Session.HTTP.Body) {
				t.Fatal("recovery evidence alias")
			}
			if response == "lost" {
				if recovery.Unknown == nil || !reflect.DeepEqual(result, *recovery.Unknown) {
					t.Fatal("independent unknown destroy evidence missing")
				}
				read := executeDocument(t, recovery.Lease, Request{Method: "GET", Path: "/v1/session/info/" + sessionToolID})
				if read.WriteOutcomeUnknown || !reflect.DeepEqual(*recovery.Lease.(*documentLease).recoveryOwned().Unknown, result) {
					t.Fatal("later read replaced unknown write")
				}
				if _, err := recovery.Lease.(*documentLease).PrepareHTTPSessionDestroy(context.Background(), nativeDestroyImpactRequest(sessionToolID)); !errors.Is(err, contract.ErrKVWriteUnknown) {
					t.Fatal("later read authorized unknown replay", err)
				}
			}
		})
	}
}

func TestHTTPSessionDestroyPlanRejectsChangedIncompleteAndCancelledRechecksWithoutPUT(t *testing.T) {
	for _, mode := range []int32{0, 1, 2, 3, 4, 5} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			_, l, f := destroyPlanFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("rejected plan dispatched PUT") })
			p := prepareDestroyPlan(t, l)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == 0 {
				f.version.Store(9007199254740993)
			} else if mode == 5 {
				cancel()
			} else {
				f.mode.Store(mode)
			}
			result, err := l.Execute(ctx, p)
			if err == nil || result.Attempted || result.Completed || result.HTTP != nil || result.WriteOutcomeUnknown || f.writes.Load() != 0 || result.HTTPSessionDestroy == nil || result.HTTPSessionDestroy.Revalidated || !result.HTTPSessionDestroy.Preview.Complete || l.planReserved != 0 {
				t.Fatal("rejected preflight changed write evidence", err)
			}
			if _, err := l.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("rejected plan was reusable", err)
			}
		})
	}
}

func TestHTTPSessionDestroyPlanCloseWaitsForOriginalWriteAndPreservesUnknown(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	_, l, f := destroyPlanFixture(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		fmt.Fprint(w, "true")
	})
	p := prepareDestroyPlan(t, l)
	type completion struct {
		result contract.KVResult
		err    error
	}
	done := make(chan completion, 1)
	go func() { result, err := l.Execute(context.Background(), p); done <- completion{result, err} }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original executor did not enter PUT")
	}
	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("plan close did not settle cancelled original executor")
	}
	close(release)
	result := <-done
	if result.err == nil || !result.result.WriteOutcomeUnknown || f.writes.Load() != 1 || l.planReserved != 0 || !reflect.DeepEqual(result.result, l.LastResult()) {
		t.Fatal("cancelled original write became known or lost complete evidence", result.err)
	}
	if _, err := l.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("closed plan replayed")
	}
}

func TestHTTPSessionDestroyPlanBudgetOwnershipAndRecoveryRevokeAuthority(t *testing.T) {
	s, l, f := destroyPlanFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("revoked authority dispatched PUT") })
	request := nativeDestroyImpactRequest(sessionToolID)
	receiptBudgetMu.Lock()
	budget := s.budget
	s.budget = s.retained + pendingReceiptBudget
	receiptBudgetMu.Unlock()
	if _, err := l.PrepareHTTPSessionDestroy(context.Background(), request); !errors.Is(err, ErrBounds) || f.reads.Load() != 0 || l.planReserved != 0 {
		t.Fatal("insufficient preview budget performed I/O", err)
	}
	receiptBudgetMu.Lock()
	s.budget = budget
	receiptBudgetMu.Unlock()
	p := prepareDestroyPlan(t, l)
	if _, err := l.PrepareHTTPSessionDestroy(context.Background(), request); !errors.Is(err, contract.ErrKVBusy) || f.reads.Load() != 2 {
		t.Fatal("second plan read or replaced captured authority", err)
	}
	original := p.(*sessionDestroyPreparation)
	copied := reflect.New(reflect.TypeOf(original).Elem())
	copied.Elem().Set(reflect.ValueOf(original).Elem())
	if _, err := l.Execute(context.Background(), copied.Interface().(contract.KVPreparation)); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("copied plan became original authority", err)
	}
	if err := copied.Interface().(contract.KVHTTPSessionDestroyPreparation).Close(); !errors.Is(err, contract.ErrKVPlanUsed) || !p.Impact().Complete {
		t.Fatal("copied close revoked original plan", err)
	}
	before := f.reads.Load()
	recovery, err := s.RecoverKV(l)
	if err != nil || f.reads.Load() != before || l.planReserved != 0 || p.Impact().Complete {
		t.Fatal("recovery read or retained authority", err)
	}
	defer recovery.Lease.Close()
	if _, err := recovery.Lease.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || f.writes.Load() != 0 {
		t.Fatal("recovery transferred destroy authority", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal("idempotent close failed", err)
	}
}
