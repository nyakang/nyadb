package consul

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEnterpriseDeletionRequiresCompleteOriginalScopedImpactAndRecheck(t *testing.T) {
	for _, mode := range []string{"complete", "filtered", "denied", "changed"} {
		t.Run(mode, func(t *testing.T) {
			var reads, writes, serviceReads atomic.Int32
			s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					writes.Add(1)
					return
				}
				reads.Add(1)
				if r.URL.Query().Get("ns") != "target" || r.URL.Query().Has("prefix") {
					t.Error("impact changed target scope")
				}
				if r.URL.Path == "/v1/acl/tokens" {
					if mode == "filtered" {
						w.Header().Set("X-Consul-Results-Filtered-By-ACLs", "true")
					}
					if mode == "denied" {
						w.WriteHeader(403)
						return
					}
				}
				if r.URL.Path == "/v1/catalog/services" {
					if mode == "changed" && serviceReads.Add(1) > 1 {
						w.Write([]byte(`{"new":[]}`))
					} else {
						w.Write([]byte(`{}`))
					}
					return
				}
				w.Write([]byte(`[]`))
			})
			l, _ := s.ReserveKV(1)
			defer l.Close()
			in := contract.KVHTTPManagementRequest{Operation: "namespace-delete", Identity: "target"}
			if _, err := l.(contract.KVHTTPManaging).PrepareHTTPManagement(in); !errors.Is(err, ErrUnsupported) {
				t.Fatal("unplanned delete accepted")
			}
			plan, impact, err := l.(contract.KVHTTPEnterpriseDeletionPlanning).PrepareHTTPEnterpriseDeletion(context.Background(), in)
			if mode == "filtered" || mode == "denied" {
				if err == nil || plan != nil || impact.Complete || writes.Load() != 0 {
					t.Fatal("incomplete scope allowed delete", err)
				}
				return
			}
			if err != nil || plan == nil || !impact.Complete || len(impact.Observations) != 24 || writes.Load() != 0 {
				t.Fatal("pure impact preview lost", err)
			}
			defer plan.Close()
			generic := documentPlan(t, l, putRequest("other", nil))
			if _, err = l.Execute(context.Background(), generic); !errors.Is(err, contract.ErrKVBusy) {
				t.Fatal("other write bypassed reserved plan", err)
			}
			result, err := l.(contract.KVHTTPManaging).ExecuteHTTPManagement(context.Background(), plan)
			if mode == "changed" {
				if err == nil || writes.Load() != 0 || result.EnterpriseVerification == nil {
					t.Fatal("stale scope deleted", err)
				}
				return
			}
			if err != nil || writes.Load() != 1 || !result.Receipt.Completed || result.EnterpriseImpact == nil || result.EnterpriseVerification == nil {
				t.Fatal("complete original delete failed", err)
			}
			if _, err = l.(contract.KVHTTPManaging).ExecuteHTTPManagement(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("Enterprise delete replayed", err)
			}
			if l.LastResult().HTTP != nil {
				t.Fatal("private inventory exposed generically")
			}
		})
	}
}
func TestEnterpriseDeletionRejectsDefaultAndReleasesPrivatePlans(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "services") {
			w.Write([]byte(`{}`))
		} else {
			w.Write([]byte(`[]`))
		}
	})
	l, _ := s.ReserveKV(1)
	defer l.Close()
	before := calls.Load()
	for _, name := range []string{"default", "", "bad/name"} {
		if p, _, err := l.(contract.KVHTTPEnterpriseDeletionPlanning).PrepareHTTPEnterpriseDeletion(context.Background(), contract.KVHTTPManagementRequest{Operation: "namespace-delete", Identity: name}); err == nil || p != nil {
			t.Fatal("invalid deletion target accepted")
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid prepare dispatched")
	}
	p, _, err := l.(contract.KVHTTPEnterpriseDeletionPlanning).PrepareHTTPEnterpriseDeletion(context.Background(), contract.KVHTTPManagementRequest{Operation: "namespace-delete", Identity: "target"})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	p.Close()
	raw := l.(*documentLease)
	if raw.enterprisePlan != nil || raw.planReserved != 0 {
		t.Fatal("closed plan retained quota")
	}
}
