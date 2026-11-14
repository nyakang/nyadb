package kvworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type aclStatePlan struct {
	request contract.KVHTTPACLReadRequest
	id      string
}

func (p *aclStatePlan) Request() contract.KVHTTPACLReadRequest { return p.request.Clone() }
func (p *aclStatePlan) LocalRecordID() string                  { return p.id }

type aclStateLease struct {
	*fakeLease
	plans, reads atomic.Int32
	read         func(context.Context, contract.KVHTTPACLReadPreparation) (contract.KVHTTPACLReadResult, error)
}

func (l *aclStateLease) PrepareHTTPACLRead(request contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadPreparation, error) {
	return &aclStatePlan{request.Clone(), fmt.Sprintf("local-%d", l.plans.Add(1))}, nil
}
func (l *aclStateLease) ExecuteHTTPACLRead(ctx context.Context, p contract.KVHTTPACLReadPreparation) (contract.KVHTTPACLReadResult, error) {
	l.reads.Add(1)
	if l.read != nil {
		return l.read(ctx, p)
	}
	return aclStateResult(l.Scope(), p), nil
}
func (l *aclStateLease) LastHTTPACLRead() contract.KVHTTPACLReadResult {
	return contract.KVHTTPACLReadResult{}
}

func aclStateResult(scope contract.KVScope, p contract.KVHTTPACLReadPreparation) contract.KVHTTPACLReadResult {
	raw := []byte(`{"AccessorID":"original-id","SecretID":"synthetic-sensitive-value"}`)
	return contract.KVHTTPACLReadResult{LocalRecordID: p.LocalRecordID(), Scope: scope.Clone(), Request: p.Request(),
		Receipt:    contract.KVResult{Attempted: true, Completed: true, HTTP: &contract.KVHTTPResult{Body: raw, Attempted: true, Ended: true, Captured: true, EOF: true, Acknowledged: true, Succeeded: true, Status: 200}},
		Projection: &contract.KVHTTPACLProjection{Resource: "token", Lookup: "self", ProjectionComplete: true, Complete: true, ContainsSensitiveValues: true, Rows: []contract.KVHTTPACLRow{{Identity: "original-id", Raw: bytes.Clone(raw), SecretState: "value"}}}}
}

func aclWorkspace(t *testing.T) (*State, *Document, *aclStateLease, *httpTestProvider) {
	t.Helper()
	s, d, p := httpTestState(t)
	d.scope.HTTP.Protocol = "consul-http"
	p.lease.scope = d.scope.Clone()
	l := &aclStateLease{fakeLease: p.lease}
	d.lease = l
	p.reserve = func(uint64) (contract.KVRequestLease, error) { return l, nil }
	return s, d, l, p
}

func TestHTTPACLWorkspaceParksOriginalImmutableReadOutsideGenericReceipts(t *testing.T) {
	s, d, l, _ := aclWorkspace(t)
	d.last = httpTestReceipt(d)
	d.unknown = &Receipt{Scope: d.scope.Clone(), Result: contract.KVResult{WriteOutcomeUnknown: true}}
	last, unknown := d.last, d.unknown
	before, err := s.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	active := true
	s.deps.Active = func(*Document) bool { return active }
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	l.read = func(ctx context.Context, p contract.KVHTTPACLReadPreparation) (contract.KVHTTPACLReadResult, error) {
		close(entered)
		<-release
		return aclStateResult(l.Scope(), p), nil
	}
	request := contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "list", Policy: new("original-policy")}
	original := request.Clone()
	s.ReadHTTPACL(d, request)
	if !d.Busy() {
		t.Fatal("ACL read did not start")
	}
	*request.Policy = "later edit"
	await(t, entered)
	s.Run(d)
	if l.executes.Load() != 0 {
		t.Fatal("generic executor bypassed active ACL operation")
	}
	active = false
	once.Do(func() { close(release) })
	await(t, d.job.finished)
	s.Consume()
	if !d.Busy() || d.httpACLContent() || d.last != last || d.unknown != unknown {
		t.Fatal("parked ACL completion entered document or replaced generic evidence")
	}
	active = true
	consume(t, s, d)
	if !d.httpACLContent() || !d.httpACL.valid || !reflect.DeepEqual(d.httpACL.last.Request, original) || d.last != last || d.unknown != unknown || !d.hasUnknownWrites() {
		t.Fatal("original request or independent receipts changed")
	}
	after, err := s.OperationRecord(d)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("private ACL receipt entered generic operation record")
	}
	if _, ok := s.RevealedHTTPACLReceipt(d); ok {
		t.Fatal("ACL secrets revealed by default")
	}
	id := d.httpACL.last.LocalRecordID
	if s.RevealHTTPACLReceipt(d, "foreign") || !s.RevealHTTPACLReceipt(d, id) {
		t.Fatal("reveal not bound to selected original receipt")
	}
	r, ok := s.RevealedHTTPACLReceipt(d)
	if !ok || !bytes.Contains(r.Receipt.HTTP.Body, []byte("synthetic-sensitive-value")) {
		t.Fatal("explicit reveal lost original bytes")
	}
	r.Receipt.HTTP.Body[0] = 'x'
	if d.httpACL.last.Receipt.HTTP.Body[0] != '{' {
		t.Fatal("reveal aliases private cache")
	}
	active = false
	s.Consume()
	active = true
	if _, ok := s.RevealedHTTPACLReceipt(d); ok {
		t.Fatal("parking retained secret reveal")
	}
}

func TestHTTPACLWorkspaceSanitizesErrorsAndRejectsForeignResultIdentity(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprint(foreign), func(t *testing.T) {
			s, d, l, _ := aclWorkspace(t)
			l.read = func(ctx context.Context, p contract.KVHTTPACLReadPreparation) (contract.KVHTTPACLReadResult, error) {
				r := aclStateResult(l.Scope(), p)
				if foreign {
					r.Scope.Generation++
					return r, nil
				}
				return r, errors.New("synthetic-sensitive-server-field")
			}
			s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
			consume(t, s, d)
			if strings.Contains(d.Error(), "synthetic-sensitive") || strings.Contains(d.httpACL.error, "synthetic-sensitive") || !d.httpACLContent() {
				t.Fatal("ACL error disclosed parser/server text or dropped original receipt")
			}
			if foreign && (d.httpACL.valid || d.httpACL.last.Projection != nil || d.httpACL.last.Scope.Generation == d.scope.Generation) {
				t.Fatal("foreign result acquired current document identity")
			}
			if d.last != nil || d.unknown != nil {
				t.Fatal("ACL result entered generic receipts")
			}
		})
	}
}

func TestHTTPACLWorkspaceCancelWaitsAndCloseClearsOriginalPrivateEvidence(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(fmt.Sprint(closing), func(t *testing.T) {
			s, d, l, _ := aclWorkspace(t)
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			l.read = func(ctx context.Context, p contract.KVHTTPACLReadPreparation) (contract.KVHTTPACLReadResult, error) {
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-release
				r := aclStateResult(l.Scope(), p)
				r.Receipt.Completed = false
				r.Receipt.HTTP.Canceled = true
				r.Receipt.HTTP.Acknowledged = false
				r.Projection = nil
				return r, ctx.Err()
			}
			s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
			await(t, entered)
			closed := make(chan struct{})
			if closing {
				go func() { s.Close(d); close(closed) }()
			} else {
				s.RequestCancel(d)
			}
			await(t, canceled)
			if closing {
				select {
				case <-closed:
					t.Fatal("document close returned before original ACL executor")
				case <-time.After(20 * time.Millisecond):
				}
			} else {
				s.Consume()
				if !d.Busy() || !d.CancelRequested() || d.httpACLContent() {
					t.Fatal("cancel claimed completion before original executor")
				}
			}
			once.Do(func() { close(release) })
			if closing {
				await(t, closed)
				if d.httpACL != nil || d.job != nil || d.lease != nil {
					t.Fatal("closed original document retained ACL caches")
				}
			} else {
				consume(t, s, d)
				if !d.httpACLContent() || !d.httpACL.last.Receipt.HTTP.Canceled || d.httpACL.last.Receipt.Completed || d.last != nil {
					t.Fatal("canceled ACL evidence lost or entered generic cache")
				}
			}
		})
	}
}

func TestHTTPACLWorkspaceRecoveryPreservesOriginalGenerationAndRevealRequiresNewSelection(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			s, d, l, p := aclWorkspace(t)
			s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
			consume(t, s, d)
			original := d.httpACL.last.Clone()
			s.RevealHTTPACLReceipt(d, original.LocalRecordID)
			fresh := &fakeLease{scope: l.Scope()}
			fresh.scope.Generation++
			p.lease = fresh
			p.recover = func(previous contract.KVRequestLease) (contract.KVRecovery, error) {
				r := original.Clone()
				if changed {
					r.Scope.Generation = fresh.scope.Generation
				}
				return contract.KVRecovery{Lease: &aclStateLease{fakeLease: fresh}, ACLResult: &r}, nil
			}
			s.DisconnectProfile(d.profileID)
			s.Recover(d)
			consume(t, s, d)
			if !reflect.DeepEqual(*d.httpACL.last, original) || l.reads.Load() != 1 || d.last != nil || d.unknown != nil {
				t.Fatal("pure ACL recovery replayed, relabeled or entered generic receipts")
			}
			if _, ok := s.RevealedHTTPACLReceipt(d); ok {
				t.Fatal("disconnect/recovery automatically revealed private receipt")
			}
			if changed {
				if !d.needsRecovery || !fresh.closed.Load() {
					t.Fatal("changed recovery receipt accepted a fresh lease")
				}
			} else if d.needsRecovery || d.scope.Generation != fresh.scope.Generation || d.httpACL.last.Scope.Generation == d.scope.Generation {
				t.Fatal("old private receipt lost original generation")
			}
		})
	}
}

func TestHTTPACLWorkspaceLeavePermitTracksOriginalReceiptAndCloseDisposesCache(t *testing.T) {
	s, d, _, _ := aclWorkspace(t)
	s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
	consume(t, s, d)
	if !s.GuardLeave(d, nil) {
		t.Fatal("closing document skipped private receipt disposition")
	}
	s.DecideLeave(ui.Services{}, false)
	if !d.httpACLContent() {
		t.Fatal("canceling leave dropped private evidence")
	}
	if !s.GuardLeave(d, nil) {
		t.Fatal("canceled leave granted discard permission")
	}
	s.DecideLeave(ui.Services{}, true)
	if s.GuardLeave(d, nil) {
		t.Fatal("unchanged receipt did not honor explicit leave decision")
	}
	s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
	consume(t, s, d)
	if !s.GuardLeave(d, nil) {
		t.Fatal("new ACL receipt reused an old discard permission")
	}
	s.Close(d)
	if d.httpACL != nil || d.httpACLContent() {
		t.Fatal("document close retained private ACL receipt")
	}
}

func TestHTTPACLWorkspaceFailedRecoveryDoesNotExposeProviderSecrets(t *testing.T) {
	s, d, _, p := aclWorkspace(t)
	s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
	consume(t, s, d)
	original := d.httpACL.last.Clone()
	p.recover = func(contract.KVRequestLease) (contract.KVRecovery, error) {
		return contract.KVRecovery{}, errors.New("synthetic-sensitive-provider-secret")
	}
	s.DisconnectProfile(d.profileID)
	s.Recover(d)
	consume(t, s, d)
	if d.Error() != httpACLRecoveryError || !d.needsRecovery || !reflect.DeepEqual(*d.httpACL.last, original) {
		t.Fatal("failed recovery exposed provider text or changed private evidence")
	}
}
