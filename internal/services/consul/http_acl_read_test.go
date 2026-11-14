package consul

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func aclDocumentLease(t *testing.T, s *Session, document uint64) *documentLease {
	t.Helper()
	l, err := s.ReserveKV(document)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l.(*documentLease)
}

func aclDocumentRead(t *testing.T, l *documentLease, request contract.KVHTTPACLReadRequest) contract.KVHTTPACLReadResult {
	t.Helper()
	p, err := l.PrepareHTTPACLRead(request)
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.ExecuteHTTPACLRead(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestACLDocumentPreparationIsPureImmutablePrivateAndOneUse(t *testing.T) {
	var reads atomic.Int32
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.URL.Path != "/v1/acl/tokens" || r.URL.Query().Get("policy") != aclProjectionID || r.URL.Query().Get("index") != "18446744073709551615" || !r.URL.Query().Has("authmethod") || r.URL.Query().Get("authmethod") != "" {
			t.Error("original ACL request changed before native dispatch")
		}
		w.Write([]byte(`[{"AccessorID":"` + aclProjectionID + `","SecretID":"<hidden>"}]`))
	})
	l, foreign := aclDocumentLease(t, s, 1), aclDocumentLease(t, s, 2)
	before := calls.Load()
	request := contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "list", Policy: new(aclProjectionID), AuthMethod: new(""), Index: new(^uint64(0)), Wait: new("1s")}
	expected := request.Clone()
	p, err := l.PrepareHTTPACLRead(request)
	if err != nil {
		t.Fatal(err)
	}
	*request.Policy, *request.Index, *request.AuthMethod = "changed", 1, "changed"
	copy := p.Request()
	*copy.Wait, *copy.Policy = "changed", "changed"
	if calls.Load() != before || !reflect.DeepEqual(p.Request(), expected) {
		t.Fatal("ACL preparation performed I/O or retained caller-owned pointers")
	}
	if _, ok := p.(contract.KVPreparation); ok {
		t.Fatal("private ACL preparation exposes generic execution contract")
	}
	if _, err := foreign.ExecuteHTTPACLRead(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("foreign lease accepted private ACL authority")
	}
	private := p.(*httpACLReadPreparation).plan
	if _, err := l.Execute(context.Background(), private); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("generic executor accepted private ACL plan")
	}
	if _, err := l.executeLockedPreparation(context.Background(), private); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("generic composite executor accepted private ACL plan")
	}
	if _, err := l.Prepare(private.request); err == nil || calls.Load() != before {
		t.Fatal("authored request bypassed private ACL route gate")
	}
	r, err := l.ExecuteHTTPACLRead(context.Background(), p)
	if err != nil || !r.Receipt.Completed || r.Projection == nil || !reflect.DeepEqual(r.Request, expected) || reads.Load() != 1 || l.LastResult().HTTP != nil {
		t.Fatal("original ACL execution lost evidence or entered generic cache", err)
	}
	r.Receipt.HTTP.Body[0] = 'x'
	r.Projection.Rows[0].Raw[0] = 'x'
	*r.Request.Policy = "changed"
	if cached := l.LastHTTPACLRead(); cached.Receipt.HTTP.Body[0] != '[' || cached.Projection.Rows[0].Raw[0] != '{' || *cached.Request.Policy != aclProjectionID {
		t.Fatal("ACL inspection mutated original cache")
	}
	if _, err := l.ExecuteHTTPACLRead(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || reads.Load() != 1 {
		t.Fatal("ACL plan replayed")
	}
}

func TestACLDocumentInvalidLookupAndOptionsNeverDispatch(t *testing.T) {
	s, calls := documentSession(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid ACL request dispatched") })
	l := aclDocumentLease(t, s, 1)
	before := calls.Load()
	for _, request := range []contract.KVHTTPACLReadRequest{
		{Resource: "policy", Lookup: "self"}, {Resource: "token", Lookup: "self", Identity: aclProjectionID},
		{Resource: "token", Lookup: "name", Identity: "self"}, {Resource: "policy", Lookup: "list", Identity: aclProjectionID},
		{Resource: "auth-method", Lookup: "id", Identity: "oidc"}, {Resource: "templated-policy", Lookup: "id", Identity: aclProjectionID},
		{Resource: "policy", Lookup: "list", Role: new(aclProjectionID)}, {Resource: "token", Lookup: "list", Expanded: true},
		{Resource: "templated-policy", Lookup: "list", Index: new(uint64(1))}, {Resource: "token", Lookup: "delete", Identity: aclProjectionID},
	} {
		if _, err := l.PrepareHTTPACLRead(request); err == nil {
			t.Fatal("unsupported ACL lookup or option accepted")
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid ACL preparation performed I/O")
	}
}

func TestACLDocumentCacheRemainsSeparateFromUnknownWriteAndLaterReadsAcrossRecovery(t *testing.T) {
	body := `{"AccessorID":"` + aclProjectionID + `","SecretID":"synthetic-test-value"}`
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT":
			w.WriteHeader(500)
			w.Write([]byte("original unknown write"))
		case r.URL.Path == "/v1/acl/token/self":
			w.Write([]byte(body))
		default:
			w.Write([]byte(`[{"Key":"key","Value":null}]`))
		}
	})
	l := aclDocumentLease(t, s, 1)
	p := documentPlan(t, l, putRequest("key", nil))
	unknown, err := l.Execute(context.Background(), p)
	if err == nil || !unknown.WriteOutcomeUnknown {
		t.Fatal("fixture did not create original unknown write")
	}
	acl := aclDocumentRead(t, l, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
	if !reflect.DeepEqual(l.LastResult(), unknown) || !acl.Projection.ContainsSensitiveValues {
		t.Fatal("ACL read replaced unknown write or lost secret classification")
	}
	read := executeDocument(t, l, readRequest("key"))
	if !reflect.DeepEqual(l.LastHTTPACLRead(), acl) || !reflect.DeepEqual(l.LastResult(), read) || bytes.Contains(read.HTTP.Body, []byte("synthetic-test-value")) {
		t.Fatal("generic read lost or disclosed ACL evidence")
	}
	expectedCost := resultCost(&unknown) + resultCost(&read) + aclReadResultCost(&acl)
	if l.cacheBytes != expectedCost || s.retained != s.rootBytes+expectedCost || s.reserved != 0 {
		t.Fatal("private ACL cache escaped shared document/session budget")
	}
	fresh, err := OpenNativeSession(context.Background(), 1, s.cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	recovery, err := fresh.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	next := recovery.Lease.(*documentLease)
	defer next.Close()
	if calls.Load() != before || recovery.ACLResult == nil || !reflect.DeepEqual(*recovery.ACLResult, acl) || !reflect.DeepEqual(recovery.Last, read) || recovery.Unknown == nil || !reflect.DeepEqual(*recovery.Unknown, unknown) || !reflect.DeepEqual(next.LastHTTPACLRead(), acl) {
		t.Fatal("pure recovery replayed or mixed original receipts")
	}
	if l.LastHTTPACLRead().Receipt.HTTP != nil || l.LastResult().HTTP != nil || l.cacheBytes != 0 || next.cacheBytes != expectedCost || s.retained != 0 || fresh.retained != fresh.rootBytes+expectedCost {
		t.Fatal("recovery failed to transfer sole accounted ownership")
	}
	recovery.ACLResult.Receipt.HTTP.Body[0] = 'x'
	if !reflect.DeepEqual(next.LastHTTPACLRead(), acl) {
		t.Fatal("recovery return aliases private cache")
	}
	request, _ := DocumentRequest(putRequest("key", nil))
	if _, err := next.Prepare(request); !errors.Is(err, contract.ErrKVWriteUnknown) || calls.Load() != before {
		t.Fatal("ACL read or recovery cleared independent write barrier")
	}
	l.Close()
	if !reflect.DeepEqual(next.LastHTTPACLRead(), acl) {
		t.Fatal("late original close erased transferred ACL evidence")
	}
	next.Close()
	if fresh.retained != fresh.rootBytes || next.LastHTTPACLRead().Receipt.HTTP != nil || next.cacheBytes != 0 {
		t.Fatal("explicit close did not release ACL cache")
	}
}

func TestACLDocumentBudgetFailurePreservesPlanAndOriginalRecoveryEvidence(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	l := aclDocumentLease(t, s, 1)
	p, err := l.PrepareHTTPACLRead(contract.KVHTTPACLReadRequest{Resource: "policy", Lookup: "list"})
	if err != nil {
		t.Fatal(err)
	}
	receiptBudgetMu.Lock()
	s.budget = s.retained + pendingReceiptBudget - 1
	receiptBudgetMu.Unlock()
	before := calls.Load()
	r, err := l.ExecuteHTTPACLRead(context.Background(), p)
	if !errors.Is(err, ErrBounds) || r.Receipt.Attempted || calls.Load() != before || p.(*httpACLReadPreparation).plan.used.Load() {
		t.Fatal("ACL budget failure dispatched or consumed original plan")
	}
	receiptBudgetMu.Lock()
	s.budget = sessionReceiptBudget
	receiptBudgetMu.Unlock()
	r, err = l.ExecuteHTTPACLRead(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenNativeSession(context.Background(), 1, s.cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	receiptBudgetMu.Lock()
	fresh.budget = fresh.retained + l.cacheBytes - 1
	receiptBudgetMu.Unlock()
	before = calls.Load()
	failed, err := fresh.RecoverKV(l)
	if !errors.Is(err, ErrBounds) || failed.Lease != nil || failed.ACLResult == nil || !reflect.DeepEqual(*failed.ACLResult, r) || !reflect.DeepEqual(l.LastHTTPACLRead(), r) || l.cacheBytes == 0 || calls.Load() != before {
		t.Fatal("failed recovery lost private evidence or replayed")
	}
	receiptBudgetMu.Lock()
	fresh.budget = sessionReceiptBudget
	receiptBudgetMu.Unlock()
	recovery, err := fresh.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if recovery.ACLResult == nil || !reflect.DeepEqual(*recovery.ACLResult, r) || calls.Load() != before || l.cacheBytes != 0 {
		t.Fatal("retry of pure recovery lost original ACL ownership")
	}
}

func TestACLDocumentCancelCloseRetireAndRecoveryWaitForOriginalExecutor(t *testing.T) {
	for _, action := range []string{"cancel", "close", "retire", "recover"} {
		t.Run(action, func(t *testing.T) {
			s, calls := documentSession(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected I/O") })
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			releaseExecutor := func() { once.Do(func() { close(release) }) }
			defer releaseExecutor()
			s.opener = func(context.Context) (*Client, OpenEvidence, error) {
				c := newCapturedClient(s.cfg, nil, "")
				c.cache = false
				c.http.Transport = watchRoundTripper(func(r *http.Request) (*http.Response, error) {
					close(entered)
					<-r.Context().Done()
					close(canceled)
					<-release
					return nil, r.Context().Err()
				})
				return c, s.evidence, nil
			}
			l := aclDocumentLease(t, s, 1)
			p, err := l.PrepareHTTPACLRead(contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan contract.KVHTTPACLReadResult, 1)
			go func() { r, _ := l.ExecuteHTTPACLRead(ctx, p); done <- r }()
			wait(t, entered)
			inspection := make(chan contract.KVHTTPACLReadResult, 1)
			go func() { inspection <- l.LastHTTPACLRead() }()
			select {
			case r := <-inspection:
				if r.Receipt.HTTP != nil {
					t.Fatal("unfinished ACL executor invented a receipt")
				}
			case <-time.After(time.Second):
				t.Fatal("cache inspection waited for ACL executor")
			}
			other := documentPlan(t, l, readRequest("key"))
			if _, err := l.Execute(ctx, other); !errors.Is(err, contract.ErrKVBusy) {
				t.Fatal("generic read bypassed ACL operation lock")
			}
			if _, err := l.ExecuteHTTPACLRead(ctx, p); !errors.Is(err, contract.ErrKVBusy) {
				t.Fatal("second ACL read bypassed original operation lock")
			}
			var recovery contract.KVRecovery
			var actionErr error
			settled := make(chan struct{})
			before := calls.Load()
			go func() {
				switch action {
				case "cancel":
					cancel()
				case "close":
					actionErr = l.Close()
				case "retire":
					actionErr = s.Close()
				case "recover":
					recovery, actionErr = s.RecoverKV(l)
				}
				close(settled)
			}()
			wait(t, canceled)
			select {
			case <-done:
				t.Fatal("ACL completion preceded original executor acknowledgement")
			case <-time.After(20 * time.Millisecond):
			}
			if action != "cancel" {
				select {
				case <-settled:
					t.Fatal("lifecycle action returned before original executor")
				default:
				}
			}
			releaseExecutor()
			wait(t, settled)
			r := <-done
			if actionErr != nil || r.Receipt.HTTP == nil || !r.Receipt.Attempted || !r.Receipt.HTTP.Ended || !r.Receipt.HTTP.Canceled || r.Receipt.Completed || r.Receipt.WriteOutcomeUnknown || r.Projection != nil || calls.Load() != before || l.LastResult().HTTP != nil {
				t.Fatal("canceled ACL read invented success, replayed or entered generic cache", actionErr)
			}
			if action == "recover" {
				defer recovery.Lease.Close()
				if recovery.ACLResult == nil || !reflect.DeepEqual(*recovery.ACLResult, r) || l.LastHTTPACLRead().Receipt.HTTP != nil {
					t.Fatal("recovery lost parked original canceled receipt")
				}
			} else if action == "close" {
				if l.LastHTTPACLRead().Receipt.HTTP != nil || l.cacheBytes != 0 {
					t.Fatal("close retained private ACL cache")
				}
			} else if !reflect.DeepEqual(l.LastHTTPACLRead(), r) {
				t.Fatal("cancel/retirement discarded parked original ACL evidence")
			}
		})
	}
}
