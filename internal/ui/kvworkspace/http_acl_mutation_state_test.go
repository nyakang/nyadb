package kvworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
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

type aclMutationStatePlan struct {
	request contract.KVHTTPACLMutationRequest
	id      string
	impact  contract.KVHTTPACLImpact
	closed  atomic.Bool
}

func (p *aclMutationStatePlan) Request() contract.KVHTTPACLMutationRequest { return p.request.Clone() }
func (p *aclMutationStatePlan) LocalRecordID() string                      { return p.id }

type aclMutationStateLease struct {
	*aclStateLease
	mutations, preparations atomic.Int32
	mutate                  func(context.Context, contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error)
}

func (l *aclMutationStateLease) PrepareHTTPACLMutation(r contract.KVHTTPACLMutationRequest) (contract.KVHTTPACLMutationPreparation, error) {
	return &aclMutationStatePlan{request: r.Clone(), id: fmt.Sprintf("mutation-%d", l.preparations.Add(1))}, nil
}
func (p *aclMutationStatePlan) Impact() contract.KVHTTPACLImpact { return p.impact.Clone() }
func (p *aclMutationStatePlan) Close() error                     { p.closed.Store(true); return nil }
func (l *aclMutationStateLease) PrepareHTTPACLMutationPlan(ctx context.Context, r contract.KVHTTPACLMutationRequest, limits contract.KVHTTPACLImpactLimits) (contract.KVHTTPACLMutationPlan, contract.KVHTTPACLImpact, error) {
	p, _ := l.PrepareHTTPACLMutation(r)
	plan := p.(*aclMutationStatePlan)
	read := aclStateResult(l.Scope(), &aclStatePlan{id: "preview"})
	read.Receipt.HTTP.Kind = "acl-read"
	read.Receipt.HTTP.Request.Method = "GET"
	o := contract.KVHTTPACLImpact{Scope: l.Scope(), Request: contract.KVHTTPACLImpactRequest{Resource: r.Resource, Identity: r.Identity, MaxRows: limits.MaxRows, MaxReads: limits.MaxReads, MaxBytes: limits.MaxBytes}, Bytes: 4096, Rows: 1, Complete: true, Reads: []contract.KVHTTPACLImpactRead{{Result: read, BodySHA256: fmt.Sprintf("%x", sha256.Sum256(read.Receipt.HTTP.Body))}}}
	if r.Operation == "create" {
		copy := r.Clone()
		o.Creation = &copy
	}
	plan.impact = o.Clone()
	return plan, o, nil
}
func (l *aclMutationStateLease) ExecuteHTTPACLMutation(ctx context.Context, p contract.KVHTTPACLMutationPreparation) (out contract.KVHTTPACLMutationResult, err error) {
	defer func() {
		if plan, ok := p.(contract.KVHTTPACLMutationPlan); ok {
			out.Impact = &contract.KVHTTPACLMutationImpactEvidence{Preview: plan.Impact(), Recheck: plan.Impact(), Revalidated: true}
		}
	}()
	l.mutations.Add(1)
	if l.mutate != nil {
		return l.mutate(ctx, p)
	}
	return aclMutationStateResult(l.Scope(), p), nil
}
func (l *aclMutationStateLease) LastHTTPACLMutation() contract.KVHTTPACLMutationResult {
	return contract.KVHTTPACLMutationResult{}
}
func aclMutationStateResult(scope contract.KVScope, p contract.KVHTTPACLMutationPreparation) contract.KVHTTPACLMutationResult {
	read := aclStateResult(scope, &aclStatePlan{id: p.LocalRecordID()})
	return contract.KVHTTPACLMutationResult{LocalRecordID: p.LocalRecordID(), Scope: scope.Clone(), Request: p.Request(), Receipt: read.Receipt, Projection: read.Projection}
}
func aclMutationWorkspace(t *testing.T) (*State, *Document, *aclMutationStateLease, *httpTestProvider) {
	t.Helper()
	s, d, read, p := aclWorkspace(t)
	l := &aclMutationStateLease{aclStateLease: read}
	d.lease = l
	p.reserve = func(uint64) (contract.KVRequestLease, error) { return l, nil }
	return s, d, l, p
}
func mutationStateInput() contract.KVHTTPACLMutationRequest {
	return contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "create", Definition: []byte(" {\n\"Description\":\"original\"\n} ")}
}
func runACLMutation(t *testing.T, s *State, d *Document, input contract.KVHTTPACLMutationRequest) {
	s.SetHTTPACLMutationInput(d, input)
	s.PrepareHTTPACLMutation(d)
	consume(t, s, d)
	s.ConfirmHTTPACLMutation(d, true)
}

func TestHTTPACLMutationWorkspaceRequiresExactImmutableConfirmation(t *testing.T) {
	for _, change := range []string{"none", "unchecked", "input", "editor", "scope", "lease", "unknown", "park", "disconnect", "cancel"} {
		t.Run(change, func(t *testing.T) {
			s, d, l, _ := aclMutationWorkspace(t)
			input := mutationStateInput()
			original := input.Clone()
			s.SetHTTPACLMutationInput(d, input)
			input.Definition[0] = 'x'
			s.PrepareHTTPACLMutation(d)
			consume(t, s, d)
			if l.mutations.Load() != 0 || d.httpACLMutation.confirmation == nil || !reflect.DeepEqual(d.httpACLMutation.confirmation.request, original) || !d.Dirty() {
				t.Fatal("preparation dispatched or lost private draft")
			}
			inspection := d.httpACLMutation.confirmation.plan.Request()
			inspection.Definition[0] = 'x'
			switch change {
			case "input":
				s.SetHTTPACLMutationInput(d, contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "delete", Identity: "later"})
			case "editor":
				d.Editor.Buffer.Set("changed")
			case "scope":
				d.scope.Generation++
			case "lease":
				d.lease = &aclMutationStateLease{aclStateLease: &aclStateLease{fakeLease: &fakeLease{scope: d.scope.Clone()}}}
			case "unknown":
				d.unknown = &Receipt{}
			case "park":
				s.deps.Active = func(*Document) bool { return false }
				s.Consume()
				s.deps.Active = func(*Document) bool { return true }
			case "disconnect":
				s.DisconnectProfile(d.profileID)
			case "cancel":
				s.CancelConfirmation(d)
			}
			s.ConfirmHTTPACLMutation(d, change != "unchecked")
			if change == "none" {
				s.ConfirmHTTPACLMutation(d, true)
				consume(t, s, d)
				if l.mutations.Load() != 1 || !reflect.DeepEqual(d.httpACLMutation.last.Request, original) {
					t.Fatal("original confirmed mutation lost or repeated")
				}
			} else if d.Busy() || l.mutations.Load() != 0 {
				t.Fatal("stale or unchecked confirmation executed", change)
			}
		})
	}
}

func TestHTTPACLMutationWorkspaceParksSecretsAndKeepsUnknownAcrossLaterReads(t *testing.T) {
	s, d, l, _ := aclMutationWorkspace(t)
	d.last = httpTestReceipt(d)
	last := d.last
	before, err := s.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	active := true
	s.deps.Active = func(*Document) bool { return active }
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	l.mutate = func(ctx context.Context, p contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
		close(entered)
		<-release
		r := aclMutationStateResult(l.Scope(), p)
		r.Receipt.Completed = false
		r.Receipt.WriteOutcomeUnknown = true
		r.Projection = nil
		return r, errors.New("synthetic-sensitive-provider-field")
	}
	input := mutationStateInput()
	original := input.Clone()
	runACLMutation(t, s, d, input)
	input.Definition[0] = 'x'
	await(t, entered)
	active = false
	once.Do(func() { close(release) })
	await(t, d.job.finished)
	s.Consume()
	if !d.Busy() || d.httpACLMutationContent() || d.last != last {
		t.Fatal("parked write entered another document")
	}
	active = true
	consume(t, s, d)
	if !d.httpACLMutationContent() || !d.hasUnknownWrites() || !reflect.DeepEqual(d.httpACLMutation.last.Request, original) || d.last != last || d.unknown != nil || strings.Contains(d.Error(), "synthetic-sensitive") {
		t.Fatal("private original write changed, leaked or lost barrier")
	}
	after, err := s.OperationRecord(d)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("private write entered generic export")
	}
	receipt := d.httpACLMutation.last.Clone()
	if _, ok := s.RevealedHTTPACLMutationReceipt(d); ok {
		t.Fatal("write secrets automatically revealed")
	}
	if s.RevealHTTPACLMutationReceipt(d, "foreign") || !s.RevealHTTPACLMutationReceipt(d, receipt.LocalRecordID) {
		t.Fatal("private reveal not receipt-bound")
	}
	revealed, ok := s.RevealedHTTPACLMutationReceipt(d)
	if !ok || !bytes.Contains(revealed.Receipt.HTTP.Body, []byte("synthetic-sensitive")) {
		t.Fatal("explicit reveal lost exact private bytes")
	}
	revealed.Receipt.HTTP.Body[0] = 'x'
	s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
	consume(t, s, d)
	if !reflect.DeepEqual(*d.httpACLMutation.last, receipt) || !d.hasUnknownWrites() || d.unknown != nil {
		t.Fatal("later ACL read replaced unknown mutation")
	}
	if _, ok := s.RevealedHTTPACLMutationReceipt(d); ok {
		t.Fatal("later read kept old private reveal")
	}
	preparations := l.preparations.Load()
	s.PrepareHTTPACLMutation(d)
	consume(t, s, d)
	s.ConfirmHTTPACLMutation(d, true)
	if l.preparations.Load() != preparations || l.mutations.Load() != 1 || d.Busy() {
		t.Fatal("later read removed private write barrier")
	}
}

func TestHTTPACLMutationWorkspaceRecoveryRejectsChangedOriginalReceipt(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			s, d, l, p := aclMutationWorkspace(t)
			l.mutate = func(ctx context.Context, p contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
				r := aclMutationStateResult(l.Scope(), p)
				r.Receipt.Completed = false
				r.Receipt.WriteOutcomeUnknown = true
				return r, errors.New("original unknown")
			}
			runACLMutation(t, s, d, mutationStateInput())
			consume(t, s, d)
			original := d.httpACLMutation.last.Clone()
			s.RevealHTTPACLMutationReceipt(d, original.LocalRecordID)
			fresh := &fakeLease{scope: l.Scope()}
			fresh.scope.Generation++
			p.lease = fresh
			p.recover = func(contract.KVRequestLease) (contract.KVRecovery, error) {
				r := original.Clone()
				if changed {
					r.Scope.Generation = fresh.scope.Generation
				}
				return contract.KVRecovery{Lease: &aclMutationStateLease{aclStateLease: &aclStateLease{fakeLease: fresh}}, ACLMutationResult: &r}, nil
			}
			s.DisconnectProfile(d.profileID)
			s.Recover(d)
			consume(t, s, d)
			if !reflect.DeepEqual(*d.httpACLMutation.last, original) || l.mutations.Load() != 1 || !d.hasUnknownWrites() || d.last != nil || d.unknown != nil {
				t.Fatal("recovery relabeled, replayed or exposed private mutation")
			}
			if _, ok := s.RevealedHTTPACLMutationReceipt(d); ok {
				t.Fatal("recovery automatically revealed secrets")
			}
			if changed {
				if !d.needsRecovery || !fresh.closed.Load() {
					t.Fatal("changed receipt recovery accepted")
				}
			} else if d.needsRecovery || d.scope.Generation != fresh.scope.Generation || d.httpACLMutation.last.Scope.Generation == d.scope.Generation {
				t.Fatal("original generation lost")
			}
			s.PrepareHTTPACLMutation(d)
			consume(t, s, d)
			s.ConfirmHTTPACLMutation(d, true)
			if d.Busy() {
				t.Fatal("pure recovery cleared unknown barrier")
			}
		})
	}
}

func TestHTTPACLMutationWorkspaceCancelAndCloseWaitForOriginalExecutor(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(fmt.Sprint(closing), func(t *testing.T) {
			s, d, l, _ := aclMutationWorkspace(t)
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			l.mutate = func(ctx context.Context, p contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-release
				r := aclMutationStateResult(l.Scope(), p)
				r.Receipt.Completed = false
				r.Receipt.WriteOutcomeUnknown = true
				r.Receipt.HTTP.Canceled = true
				r.Projection = nil
				return r, ctx.Err()
			}
			runACLMutation(t, s, d, mutationStateInput())
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
					t.Fatal("close returned before original write executor")
				case <-time.After(20 * time.Millisecond):
				}
			} else {
				s.Consume()
				if !d.Busy() || !d.CancelRequested() || d.httpACLMutationContent() {
					t.Fatal("cancel invented completion")
				}
			}
			once.Do(func() { close(release) })
			if closing {
				await(t, closed)
				if d.httpACLMutation != nil || d.job != nil || d.lease != nil {
					t.Fatal("closed document retained private writes")
				}
			} else {
				consume(t, s, d)
				if !d.httpACLMutationContent() || !d.hasUnknownWrites() || !d.httpACLMutation.last.Receipt.HTTP.Canceled || d.last != nil {
					t.Fatal("canceled write lost private unknown receipt")
				}
			}
		})
	}
}

func TestHTTPACLMutationWorkspaceLeaveTracksPrivateDraftAndReceipt(t *testing.T) {
	s, d, _, _ := aclMutationWorkspace(t)
	runACLMutation(t, s, d, mutationStateInput())
	consume(t, s, d)
	if !s.GuardLeave(d, nil) {
		t.Fatal("leave ignored private mutation")
	}
	s.DecideLeave(ui.Services{}, false)
	if !s.GuardLeave(d, nil) {
		t.Fatal("canceled leave permitted secret disposal")
	}
	s.DecideLeave(ui.Services{}, true)
	if s.GuardLeave(d, nil) {
		t.Fatal("unchanged private state ignored leave decision")
	}
	s.SetHTTPACLMutationInput(d, contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "delete", Identity: "later"})
	if !s.GuardLeave(d, nil) {
		t.Fatal("new draft reused old discard permission")
	}
	s.Close(d)
	if d.httpACLMutation != nil {
		t.Fatal("close retained secret draft or receipt")
	}
}

func TestHTTPACLMutationWorkspaceRejectsForeignOutcomeAndBoundsWithoutSecretExposure(t *testing.T) {
	for _, kind := range []string{"scope", "record", "request"} {
		t.Run(kind, func(t *testing.T) {
			s, d, l, _ := aclMutationWorkspace(t)
			l.mutate = func(ctx context.Context, p contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
				r := aclMutationStateResult(l.Scope(), p)
				switch kind {
				case "scope":
					r.Scope.Generation++
				case "record":
					r.LocalRecordID = "foreign"
				case "request":
					r.Request.Definition = []byte(`{"Description":"foreign"}`)
				}
				return r, nil
			}
			runACLMutation(t, s, d, mutationStateInput())
			consume(t, s, d)
			if !d.httpACLMutationContent() || d.httpACLMutation.valid || !d.hasUnknownWrites() || d.httpACLMutation.last.Projection != nil || d.Error() != contract.ErrRevoked.Error() || d.last != nil || d.unknown != nil {
				t.Fatal("foreign write result gained authority or lost original private evidence")
			}
			input := d.httpACLMutation.input.Clone()
			huge := mutationStateInput()
			huge.Definition = make([]byte, inputLimit+1)
			s.SetHTTPACLMutationInput(d, huge)
			if !reflect.DeepEqual(d.httpACLMutation.input, input) {
				t.Fatal("oversized draft replaced original private input")
			}
			s.SetMethod(d, "Put")
			s.Run(d)
			s.Confirm(d, true)
			if d.Busy() || l.executes.Load() != 0 {
				t.Fatal("foreign mutation result failed to block generic writes")
			}
		})
	}
}
