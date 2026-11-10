package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type fakeManagementLease struct {
	*fakeBrowserLease
	prepare func(contract.KVRequest) (contract.KVPreparation, error)
}

func (l *fakeManagementLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
	if l.prepare != nil {
		return l.prepare(r)
	}
	return &fakePlan{request: r, write: r.Method == leaseGrantMethod || r.Method == leaseRevokeMethod}, nil
}
func managementTestState(t *testing.T) (*State, *Document, *fakeManagementLease) {
	t.Helper()
	l := &fakeManagementLease{fakeBrowserLease: &fakeBrowserLease{fakeLease: &fakeLease{scope: testScope()}}}
	p := &fakeProvider{lease: l.fakeLease, reserve: func(uint64) (contract.KVRequestLease, error) { return l, nil }}
	s := New(Dependencies{ProviderFor: func(int64) (contract.KVRequestsProvider, error) { return p, nil }})
	d := s.Open(1, 7, DefaultLeaseBrowserRequest(LeaseBrowseMethod))
	t.Cleanup(s.CloseAll)
	return s, d, l
}

func TestKVLeaseManagementExactInputsAndGeneratedIDAcknowledgement(t *testing.T) {
	s, d, l := managementTestState(t)
	original := d.Request()
	l.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		return contract.KVResult{Request: p.Snapshot(), Attempted: true, Completed: true, Native: []byte{1, 2}, Reply: &contract.KVReply{Kind: "lease_grant", Lease: &contract.KVLeaseReceipt{ID: 9007199254740993, TTL: 61}}}, nil
	}
	s.OpenLeaseGrant(d)
	for _, ttl := range []string{"0", "-1", "1.0", "1e3", "9223372036854775808", "+1", ""} {
		d.leaseManagement.ttl = ttl
		s.PrepareLeaseManagement(d)
		if d.confirmation != nil || d.Busy() {
			t.Fatal("invalid TTL prepared", ttl)
		}
	}
	d.leaseManagement.ttl = "9223372036854775807"
	d.leaseManagement.id = "-9223372036854775808"
	s.PrepareLeaseManagement(d)
	if d.confirmation == nil || d.confirmation.management.RequestedID != math.MinInt64 || d.confirmation.management.RequestedTTL != math.MaxInt64 {
		t.Fatal("form narrowed int64", d.Error())
	}
	s.CancelConfirmation(d)
	s.OpenLeaseGrant(d)
	s.PrepareLeaseManagement(d)
	s.Confirm(d, false)
	if d.Request() != original || l.executes.Load() != 0 || d.confirmation == nil {
		t.Fatal("preview executed or changed editor")
	}
	s.Confirm(d, true)
	consume(t, s, d)
	if d.Request() != original || d.last.LeaseManagement.RequestedID != 0 || d.last.Result.Reply.Lease.ID != 9007199254740993 || l.executes.Load() != 1 {
		t.Fatal("grant identity guessed or receipt changed")
	}
	s.ReadGrantedLease(d)
	consume(t, s, d)
	if d.browserDetail == nil || d.browserDetail.Detail.Request.ID != 9007199254740993 {
		t.Fatal("acknowledged generated ID lost")
	}
}

func TestKVLeaseManagementFreshCompleteRevokeObservationAndImmutablePlan(t *testing.T) {
	s, d, l := managementTestState(t)
	id := int64(9007199254740993)
	original := d.Request()
	s.OpenLeaseRevoke(d)
	d.leaseManagement.id = strings.TrimSpace(signedText(id))
	s.PrepareLeaseManagement(d)
	consume(t, s, d)
	p := d.confirmation
	if p == nil || p.management.Observation == nil || l.details.Load() != 1 || l.executes.Load() != 0 || !p.management.Observation.Request.IncludeKeys || len(p.management.Observation.Result.Reply.Lease.Keys[1]) != 4096 {
		t.Fatal("revoke omitted fresh complete observation", d.Error())
	}
	originalObserved := bytes.Clone(p.management.Observation.Result.Reply.Lease.Keys[0])
	d.browserDetail.Detail.Result.Reply.Lease.Keys[0][0] = 9
	l.execute = func(_ context.Context, plan contract.KVPreparation) (contract.KVResult, error) {
		if !managementRequestEqual(plan.Snapshot(), leaseManagementRequest(leaseRevokeMethod, id, 0)) {
			t.Error("revoke changed original ID")
		}
		return contract.KVResult{Request: plan.Snapshot(), Attempted: true, Completed: true, Native: []byte{4, 5}, Reply: &contract.KVReply{Kind: "lease_revoke"}}, nil
	}
	s.Confirm(d, true)
	consume(t, s, d)
	r, ok := d.LastReceipt()
	if !ok || r.LeaseManagement.DocumentID != 1 || !bytes.Equal(r.LeaseManagement.Observation.Result.Reply.Lease.Keys[0], originalObserved) || d.Request() != original {
		t.Fatal("immutable original observation/editor lost")
	}
	r.LeaseManagement.Observation.Result.Reply.Lease.Keys[0][0] = 8
	if !bytes.Equal(d.last.LeaseManagement.Observation.Result.Reply.Lease.Keys[0], originalObserved) {
		t.Fatal("receipt clone aliases observation")
	}
	raw, err := s.OperationRecord(d)
	var record map[string]any
	if err != nil || json.Unmarshal(raw, &record) != nil || !bytes.Contains(raw, []byte(`"requested_id": "9007199254740993"`)) || !bytes.Contains(raw, []byte(`"revoke_semantics"`)) || !bytes.Contains(raw, []byte(`"native_protobuf": "AP8="`)) {
		t.Fatal("complete original observation absent from receipt", err)
	}
}

func TestKVLeaseManagementRejectsChangedEditorForeignPlanAndUnknownAuthorization(t *testing.T) {
	for _, mode := range []string{"editor", "lease", "scope", "plan", "mutated-plan", "unknown", "renewal-unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, d, l := managementTestState(t)
			s.OpenLeaseGrant(d)
			if mode == "plan" {
				l.prepare = func(r contract.KVRequest) (contract.KVPreparation, error) {
					return &fakePlan{request: leaseManagementRequest(leaseGrantMethod, 42, 60), write: true}, nil
				}
			}
			s.PrepareLeaseManagement(d)
			if mode == "mutated-plan" {
				d.confirmation.plan.(*fakePlan).request = leaseManagementRequest(leaseGrantMethod, 42, 60)
			}
			if mode == "editor" {
				d.Editor.Buffer.Set(`{"limit":2}`)
			}
			if mode == "lease" {
				l.lost.Store(true)
			}
			if mode == "scope" {
				d.scope.Generation++
			}
			if mode == "unknown" {
				d.unknown = &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true}}
			}
			if mode == "renewal-unknown" {
				d.renewalUnknown = &RenewalReceipt{Batch: contract.KVRenewalBatch{Terminal: contract.KVRenewalTerminal{WriteOutcomeUnknown: true}}}
			}
			s.Confirm(d, true)
			if d.Busy() || l.executes.Load() != 0 {
				t.Fatal("stale/unknown/foreign authorization executed", mode)
			}
		})
	}
}

func TestKVLeaseManagementObservationCancellationParkingAndDiscardNeverAuthorizes(t *testing.T) {
	for _, mode := range []string{"cancel", "parked", "disconnect", "close", "editor", "generation", "foreign", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			s, d, l := managementTestState(t)
			entered, release := make(chan struct{}), make(chan struct{})
			l.detail = func(ctx context.Context, r contract.KVLeaseDetailRequest) (contract.KVLeaseDetail, error) {
				close(entered)
				<-release
				o := contract.KVLeaseDetail{Scope: l.Scope(), Request: r, Result: browserTestTTL(r.ID)}
				if mode == "foreign" {
					o.Scope.Generation++
				}
				if mode == "invalid" {
					o.Result.Reply.Lease.ID++
				}
				return o, ctx.Err()
			}
			s.OpenLeaseRevoke(d)
			d.leaseManagement.id = "-9223372036854775808"
			s.PrepareLeaseManagement(d)
			await(t, entered)
			job := d.job
			active := true
			s.deps.Active = func(*Document) bool { return active }
			switch mode {
			case "cancel":
				s.RequestCancel(d)
				s.CancelConfirmation(d)
			case "parked":
				active = false
			case "disconnect":
				s.DisconnectProfile(d.profileID)
			case "editor":
				d.Editor.Buffer.Set(`{"limit":4}`)
			case "generation":
				d.scope.Generation++
			}
			s.Consume()
			if !d.Busy() {
				t.Fatal("observation stopped before executor acknowledgement")
			}
			close(release)
			await(t, job.finished)
			if mode == "close" {
				s.Close(d)
				if d.confirmation != nil || d.leaseManagement != nil || d.browserLast != nil {
					t.Fatal("close retained modal/caches")
				}
				return
			}
			s.Consume()
			if mode == "parked" {
				if d.job != job || d.confirmation != nil {
					t.Fatal("parked preview admitted")
				}
				active = true
				s.Consume()
				if d.confirmation == nil {
					t.Fatal("original parked preview lost")
				}
				s.CancelConfirmation(d)
			}
			if d.confirmation != nil || l.executes.Load() != 0 {
				t.Fatal("invalid/canceled/retired preview authorized", mode)
			}
		})
	}
}

func TestKVLeaseManagementUnknownGrantNeverGuessesGeneratedIDOrReplays(t *testing.T) {
	s, d, l := managementTestState(t)
	l.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		return contract.KVResult{Request: p.Snapshot(), Attempted: true, WriteOutcomeUnknown: true, Native: []byte{9}}, errors.New("lost reply")
	}
	s.OpenLeaseGrant(d)
	s.PrepareLeaseManagement(d)
	s.Confirm(d, true)
	consume(t, s, d)
	unknown := d.unknown
	s.ReadGrantedLease(d)
	s.RunLeaseBrowser(d)
	consume(t, s, d)
	s.OpenLeaseGrant(d)
	s.OpenLeaseRevoke(d)
	if d.leaseManagement != nil || d.confirmation != nil || d.unknown != unknown || l.executes.Load() != 1 || l.details.Load() != 0 {
		t.Fatal("unknown Grant guessed ID or cleared write barrier")
	}
}

func TestKVLeaseManagementLeaveGuardIncludesUnsubmittedForm(t *testing.T) {
	s, d, _ := managementTestState(t)
	s.OpenLeaseGrant(d)
	if !s.GuardLeave(d, nil) {
		t.Fatal("form draft silently discarded")
	}
	s.DecideLeave(ui.Services{}, true)
	if d.leaseManagement != nil || s.GuardLeave(d, nil) {
		t.Fatal("discard did not dispose unsubmitted form")
	}
}

func TestKVLeaseManagementListSelectionDoesNotUseAnotherCachedDetail(t *testing.T) {
	s, d, _ := managementTestState(t)
	s.Run(d)
	consume(t, s, d)
	s.SelectLease(d, math.MinInt64)
	s.ReadSelectedLease(d)
	consume(t, s, d)
	d.browserReceiptChoice = 1
	s.SelectLease(d, math.MaxInt64)
	s.OpenLeaseRevoke(d)
	if d.leaseManagement == nil || d.leaseManagement.id != signedText(math.MaxInt64) {
		t.Fatal("revoke preferred stale detail over visible list selection")
	}
	s.CancelConfirmation(d)
	d.browserReceiptChoice = 2
	s.OpenLeaseRevoke(d)
	if d.leaseManagement == nil || d.leaseManagement.id != signedText(math.MinInt64) {
		t.Fatal("detail revoke did not use visible original ID")
	}
}

func TestKVLeaseManagementPreviewFileCapturesOriginalNativeRequest(t *testing.T) {
	s, d, _ := managementTestState(t)
	original, saved, path := d.Request(), d.saved, d.path
	s.OpenLeaseGrant(d)
	d.leaseManagement.id = "9007199254740993"
	s.PrepareLeaseManagement(d)
	want := d.confirmation.authored
	entered, release := make(chan struct{}), make(chan struct{})
	var exported []byte
	s.SetFiles(Files{SavePath: func(string) (string, error) { close(entered); <-release; return "original.nyakv", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = bytes.Clone(b.Bytes())
		return err
	}})
	s.ExportLeaseManagementRequest(d)
	await(t, entered)
	s.CancelConfirmation(d)
	d.Editor.Buffer.Set(`{"limit":2}`)
	close(release)
	consume(t, s, d)
	r, err := DecodeRequest(exported)
	if err != nil || r != want || d.saved != saved || d.path != path {
		t.Fatal("preview file changed captured native request/browser baseline", err)
	}
	d.Editor.Buffer.Set(original.JSON)
}
