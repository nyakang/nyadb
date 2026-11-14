package consul

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func aclMutationPlan(t *testing.T, l *documentLease, r contract.KVHTTPACLMutationRequest) contract.KVHTTPACLMutationPreparation {
	t.Helper()
	p, err := l.PrepareHTTPACLMutation(r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func aclTokenCreate() contract.KVHTTPACLMutationRequest {
	return contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "create", Definition: []byte(" {\n\"Description\":\"owned 雪\"\n} ")}
}

const aclMutationTokenACK = `{"AccessorID":"` + aclMutationTestID + `","SecretID":"synthetic-private-value","CreateIndex":7,"ModifyIndex":8}`

func TestACLMutationDocumentPureOriginalPlanPrivateReceiptAndSingleUse(t *testing.T) {
	for _, operation := range []string{"create", "update", "delete", "clone"} {
		t.Run(operation, func(t *testing.T) {
			request := aclTokenCreate()
			request.Operation = operation
			if operation != "create" {
				request.Identity = aclMutationTestID
			}
			if operation == "delete" || operation == "clone" {
				request.Definition = nil
			}
			if operation == "clone" {
				request.Description = new("original description")
			}
			native, err := aclNativeMutationRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			var writes atomic.Int32
			s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.Method != native.Method || r.URL.EscapedPath() != native.Path || !bytes.Equal(body, native.Body) {
					t.Error("original mutation changed before dispatch")
				}
				if operation == "delete" {
					io.WriteString(w, "true")
				} else {
					ack := aclMutationTokenACK
					if operation == "clone" {
						ack = strings.ReplaceAll(ack, aclMutationTestID, aclMutationOtherID)
					}
					io.WriteString(w, ack)
				}
			})
			l, foreign := aclDocumentLease(t, s, 1), aclDocumentLease(t, s, 2)
			before := calls.Load()
			expected := request.Clone()
			p := aclMutationPlan(t, l, request)
			if len(request.Definition) > 0 {
				request.Definition[0] = 'x'
			}
			if request.Description != nil {
				*request.Description = "changed"
			}
			inspect := p.Request()
			if len(inspect.Definition) > 0 {
				inspect.Definition[0] = 'x'
			}
			if inspect.Description != nil {
				*inspect.Description = "changed"
			}
			if !reflect.DeepEqual(p.Request(), expected) || p.LocalRecordID() == "" || calls.Load() != before {
				t.Fatal("prepare performed I/O or retained caller input")
			}
			if _, ok := p.(contract.KVPreparation); ok {
				t.Fatal("private plan implements generic execution contract")
			}
			if _, err := foreign.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("foreign plan accepted")
			}
			raw := p.(*httpACLMutationPreparation).plan
			if _, err := l.Execute(context.Background(), raw); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("generic executor accepted private write")
			}
			if _, err := l.executeLockedPreparation(context.Background(), raw); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("composite executor accepted private write")
			}
			if _, err := l.Prepare(raw.request); err == nil || calls.Load() != before {
				t.Fatal("generic document bypassed private route")
			}
			result, err := l.ExecuteHTTPACLMutation(context.Background(), p)
			if err != nil || !result.Receipt.Completed || result.Receipt.WriteOutcomeUnknown || !reflect.DeepEqual(result.Request, expected) || writes.Load() != 1 {
				t.Fatal("original mutation receipt missing", err)
			}
			if (result.Projection == nil) != (operation == "delete") || result.ProjectionError != "" {
				t.Fatal("original ACK projection invalid")
			}
			if result.Projection != nil && !result.Projection.ContainsSensitiveValues {
				t.Fatal("generated secret classification lost")
			}
			owned := result.Clone()
			result.Receipt.HTTP.Body[0] = 'x'
			if result.Projection != nil {
				result.Projection.Rows[0].Raw[0] = 'x'
			}
			if !reflect.DeepEqual(l.LastHTTPACLMutation(), owned) || l.LastResult().HTTP != nil || l.LastHTTPACLRead().Receipt.HTTP != nil || l.recoveryOwned().Unknown != nil {
				t.Fatal("secret receipt aliased or entered generic/read cache")
			}
			if _, err := l.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || writes.Load() != 1 {
				t.Fatal("original write replayed")
			}
		})
	}
}

func TestACLMutationDocumentRejectsMixedInputsAndForeignAgentScopeWithoutIO(t *testing.T) {
	s, calls := documentSession(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid request dispatched") })
	l := aclDocumentLease(t, s, 1)
	before := calls.Load()
	for _, r := range []contract.KVHTTPACLMutationRequest{
		{Resource: "token", Operation: "create", Identity: aclMutationTestID, Definition: []byte(`{}`)},
		{Resource: "token", Operation: "create", Definition: []byte(`{}`), Description: new("")},
		{Resource: "token", Operation: "delete", Identity: aclMutationTestID, Definition: []byte(`{}`)},
		{Resource: "token", Operation: "clone", Identity: aclMutationTestID, Definition: []byte(`{}`)},
		{Resource: "policy", Operation: "clone", Identity: aclMutationTestID},
		{Resource: "templated-policy", Operation: "delete", Identity: "builtin/service"},
		{Resource: "token", Operation: "update", Identity: "self", Definition: []byte(`{}`)},
		{Resource: "token", Operation: "execute", Definition: []byte(`{}`)},
	} {
		if _, err := l.PrepareHTTPACLMutation(r); err == nil {
			t.Fatal("mixed or unsupported private request accepted")
		}
	}
	s.scope.HTTP.Datacenter = "foreign"
	if _, err := l.PrepareHTTPACLMutation(aclTokenCreate()); !errors.Is(err, ErrUnsupported) {
		t.Fatal("foreign agent DC accepted")
	}
	s.scope.HTTP.Datacenter = "dc1"
	if _, err := l.PrepareHTTPACLMutation(aclTokenCreate()); err != nil {
		t.Fatal("original agent proof rejected", err)
	}
	s.scope.HTTP.AgentAvailable = false
	if _, err := l.PrepareHTTPACLMutation(aclTokenCreate()); !errors.Is(err, ErrUnsupported) {
		t.Fatal("missing original agent proof invented")
	}
	if calls.Load() != before {
		t.Fatal("private preparation performed I/O")
	}
}

func TestACLMutationDocumentUnknownBarrierSurvivesReadsAndPureRecovery(t *testing.T) {
	for _, origin := range []string{"acl", "generic"} {
		t.Run(origin, func(t *testing.T) {
			s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PUT" {
					w.WriteHeader(500)
					io.WriteString(w, "original unknown mutation")
					return
				}
				if r.URL.Path == "/v1/acl/token/self" {
					io.WriteString(w, aclMutationTokenACK)
					return
				}
				io.WriteString(w, `[{"Key":"key","Value":null}]`)
			})
			l := aclDocumentLease(t, s, 1)
			earlyACL := aclMutationPlan(t, l, aclTokenCreate())
			earlyKV := documentPlan(t, l, putRequest("key", nil))
			var mutation contract.KVHTTPACLMutationResult
			var unknown contract.KVResult
			if origin == "acl" {
				p := aclMutationPlan(t, l, aclTokenCreate())
				var err error
				mutation, err = l.ExecuteHTTPACLMutation(context.Background(), p)
				if err == nil || !mutation.Receipt.WriteOutcomeUnknown {
					t.Fatal("unknown ACL response promoted")
				}
			} else {
				var err error
				unknown, err = l.Execute(context.Background(), documentPlan(t, l, putRequest("key", nil)))
				if err == nil || !unknown.WriteOutcomeUnknown {
					t.Fatal("unknown KV response promoted")
				}
			}
			aclRead := aclDocumentRead(t, l, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
			read := executeDocument(t, l, readRequest("key"))
			before := calls.Load()
			if _, err := l.ExecuteHTTPACLMutation(context.Background(), earlyACL); !errors.Is(err, contract.ErrKVWriteUnknown) {
				t.Fatal("earlier ACL plan bypassed unknown barrier")
			}
			if _, err := l.Execute(context.Background(), earlyKV); !errors.Is(err, contract.ErrKVWriteUnknown) {
				t.Fatal("earlier KV plan bypassed unknown barrier")
			}
			if _, err := l.PrepareHTTPACLMutation(aclTokenCreate()); !errors.Is(err, contract.ErrKVWriteUnknown) {
				t.Fatal("new ACL plan bypassed unknown barrier")
			}
			if !reflect.DeepEqual(l.LastHTTPACLMutation(), mutation) || !reflect.DeepEqual(l.LastHTTPACLRead(), aclRead) || !reflect.DeepEqual(l.LastResult(), read) || calls.Load() != before {
				t.Fatal("later reads changed original receipt or barrier dispatched")
			}
			cost := resultCost(&read) + aclReadResultCost(&aclRead)
			if origin == "acl" {
				cost += aclMutationResultCost(&mutation)
			} else {
				cost += resultCost(&unknown)
			}
			if l.cacheBytes != cost || s.retained != s.rootBytes+cost || s.reserved != 0 {
				t.Fatal("private evidence escaped shared budget")
			}
			fresh, err := OpenNativeSession(context.Background(), 1, s.cfg, "")
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			s.Close()
			before = calls.Load()
			recovery, err := fresh.RecoverKV(l)
			if err != nil {
				t.Fatal(err)
			}
			next := recovery.Lease.(*documentLease)
			defer next.Close()
			if calls.Load() != before || !reflect.DeepEqual(next.LastHTTPACLMutation(), mutation) || !reflect.DeepEqual(next.LastHTTPACLRead(), aclRead) || !reflect.DeepEqual(recovery.Last, read) {
				t.Fatal("recovery replayed or mixed original evidence")
			}
			if origin == "acl" {
				if recovery.ACLMutationResult == nil || !reflect.DeepEqual(*recovery.ACLMutationResult, mutation) || recovery.Unknown != nil {
					t.Fatal("private unknown entered generic recovery")
				}
				recovery.ACLMutationResult.Receipt.HTTP.Body[0] = 'x'
			} else {
				if recovery.ACLMutationResult != nil || recovery.Unknown == nil || !reflect.DeepEqual(*recovery.Unknown, unknown) {
					t.Fatal("generic unknown entered private recovery")
				}
			}
			if l.cacheBytes != 0 || l.LastHTTPACLMutation().Receipt.HTTP != nil || l.LastHTTPACLRead().Receipt.HTTP != nil || s.retained != 0 || next.cacheBytes != cost || fresh.retained != fresh.rootBytes+cost {
				t.Fatal("recovery did not transfer sole accounted ownership")
			}
			if _, err := next.PrepareHTTPACLMutation(aclTokenCreate()); !errors.Is(err, contract.ErrKVWriteUnknown) || calls.Load() != before {
				t.Fatal("pure recovery cleared write barrier")
			}
			kv, _ := DocumentRequest(putRequest("key", nil))
			if _, err := next.Prepare(kv); !errors.Is(err, contract.ErrKVWriteUnknown) {
				t.Fatal("recovery cleared generic write barrier")
			}
			l.Close()
			if !reflect.DeepEqual(next.LastHTTPACLMutation(), mutation) {
				t.Fatal("late close or recovery clone erased original receipt")
			}
			next.Close()
			if next.LastHTTPACLMutation().Receipt.HTTP != nil || next.cacheBytes != 0 || fresh.retained != fresh.rootBytes {
				t.Fatal("close retained private cache")
			}
		})
	}
}

func TestACLMutationDocumentBudgetAndReservedDestroyNeverConsumeWrite(t *testing.T) {
	s, l, f := destroyPlanFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected write") })
	p := aclMutationPlan(t, l, aclTokenCreate())
	destroy := prepareDestroyPlan(t, l)
	before := f.reads.Load()
	if r, err := l.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, contract.ErrKVBusy) || r.Receipt.Attempted || p.(*httpACLMutationPreparation).plan.used.Load() || f.reads.Load() != before || f.writes.Load() != 0 {
		t.Fatal("ACL write consumed authority during reserved destroy")
	}
	destroy.Close()
	receiptBudgetMu.Lock()
	s.budget = s.retained + pendingReceiptBudget - 1
	receiptBudgetMu.Unlock()
	if r, err := l.ExecuteHTTPACLMutation(context.Background(), p); !errors.Is(err, ErrBounds) || r.Receipt.Attempted || p.(*httpACLMutationPreparation).plan.used.Load() {
		t.Fatal("budget rejection consumed ACL plan")
	}
}
func TestACLMutationDocumentCancelCloseRetireAndRecoveryWaitForOriginalExecutor(t *testing.T) {
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
			p, err := l.PrepareHTTPACLMutation(aclTokenCreate())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan contract.KVHTTPACLMutationResult, 1)
			go func() { r, _ := l.ExecuteHTTPACLMutation(ctx, p); done <- r }()
			wait(t, entered)
			inspection := make(chan contract.KVHTTPACLMutationResult, 1)
			go func() { inspection <- l.LastHTTPACLMutation() }()
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
			if _, err := l.ExecuteHTTPACLMutation(ctx, p); !errors.Is(err, contract.ErrKVBusy) {
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
			if actionErr != nil || r.Receipt.HTTP == nil || !r.Receipt.Attempted || !r.Receipt.HTTP.Ended || !r.Receipt.HTTP.Canceled || r.Receipt.Completed || !r.Receipt.WriteOutcomeUnknown || r.Projection != nil || calls.Load() != before || l.LastResult().HTTP != nil {
				t.Fatal("canceled ACL write invented success, replayed or entered generic cache", actionErr)
			}
			if action == "recover" {
				defer recovery.Lease.Close()
				if recovery.ACLMutationResult == nil || !reflect.DeepEqual(*recovery.ACLMutationResult, r) || l.LastHTTPACLMutation().Receipt.HTTP != nil {
					t.Fatal("recovery lost parked original canceled receipt")
				}
			} else if action == "close" {
				if l.LastHTTPACLMutation().Receipt.HTTP != nil || l.cacheBytes != 0 {
					t.Fatal("close retained private ACL cache")
				}
			} else if !reflect.DeepEqual(l.LastHTTPACLMutation(), r) {
				t.Fatal("cancel/retirement discarded parked original ACL evidence")
			}
		})
	}
}

func TestACLMutationDocumentFailedRecoveryKeepsOriginalAccountedReceipt(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, aclMutationTokenACK) })
	l := aclDocumentLease(t, s, 1)
	p := aclMutationPlan(t, l, aclTokenCreate())
	original, err := l.ExecuteHTTPACLMutation(context.Background(), p)
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
	before := calls.Load()
	failed, err := fresh.RecoverKV(l)
	if !errors.Is(err, ErrBounds) || failed.Lease != nil || failed.ACLMutationResult == nil || !reflect.DeepEqual(*failed.ACLMutationResult, original) || !reflect.DeepEqual(l.LastHTTPACLMutation(), original) || l.cacheBytes == 0 || calls.Load() != before || failed.Last.HTTP != nil || failed.Unknown != nil {
		t.Fatal("failed recovery lost private ownership or replayed")
	}
	receiptBudgetMu.Lock()
	fresh.budget = sessionReceiptBudget
	receiptBudgetMu.Unlock()
	recovery, err := fresh.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if recovery.ACLMutationResult == nil || !reflect.DeepEqual(*recovery.ACLMutationResult, original) || calls.Load() != before || l.cacheBytes != 0 {
		t.Fatal("pure recovery retry changed original private receipt")
	}
}
