package kvworkspace

import (
	"context"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type authRecordPlan struct {
	*fakePlan
	record contract.KVRequestRecord
}

func (p *authRecordPlan) RecordedRequest() contract.KVRequestRecord { return p.record.Clone() }

type authRecordLease struct {
	*fakeLease
	plan *authRecordPlan
}

func (l *authRecordLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
	l.plan = &authRecordPlan{fakePlan: &fakePlan{request: r, write: true}, record: contract.KVRequestRecord{Request: contract.KVRequest{Method: r.Method, JSON: `{"name":" original user "}`}, OmittedFields: []string{"password"}}}
	return l.plan, nil
}

func TestAuthOriginalRedactedRecordsSurviveParkingAndUnknownReceiptSelection(t *testing.T) {
	s, d, base := testState(t)
	l := &authRecordLease{fakeLease: base}
	d.lease = l
	active := true
	s.deps.Active = func(*Document) bool { return active }
	entered, release := make(chan struct{}), make(chan struct{})
	base.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		if !strings.Contains(p.Snapshot().JSON, "original transient password") {
			t.Error("execution did not capture complete original password")
		}
		close(entered)
		<-release
		record := p.(contract.KVPreparationRecord).RecordedRequest()
		return contract.KVResult{Request: record.Request, RequestOmittedFields: record.OmittedFields, Attempted: true, WriteOutcomeUnknown: true, Code: "Unavailable", Native: []byte{1, 2, 3}}, contract.ErrKVLeaseLost
	}
	s.SetMethod(d, "/etcdserverpb.Auth/UserChangePassword")
	d.Editor.Buffer.Set(`{"name":" original user ","password":"original transient password"}`)
	s.Run(d)
	if d.confirmation == nil || base.executes.Load() != 0 {
		t.Fatal("password preparation did not require separate confirmation")
	}
	s.Confirm(d, true)
	await(t, entered)
	if strings.Contains(d.job.authored.JSON, "password") {
		t.Fatal("worker retained an authored secret record")
	}
	active = false
	close(release)
	await(t, d.job.finished)
	s.Consume()
	if d.last != nil || !d.Busy() {
		t.Fatal("parked Auth completion was admitted early")
	}
	active = true
	consume(t, s, d)
	r, ok := d.UnknownReceipt()
	if !ok || strings.Contains(r.Authored.JSON, "password") || strings.Contains(r.Result.Request.JSON, "password") || len(r.Result.RequestOmittedFields) != 1 || !r.Result.WriteOutcomeUnknown {
		t.Fatal("original unknown Auth record lost explicit redaction")
	}
	r.Result.RequestOmittedFields[0] = "mutated"
	if d.unknown.Result.RequestOmittedFields[0] != "password" {
		t.Fatal("original omission metadata aliases caller")
	}
	d.selectedUnknown = true
	raw, err := s.OperationRecord(d)
	if err != nil || strings.Contains(string(raw), "original transient password") || !strings.Contains(string(raw), `"request_omitted_fields"`) || !strings.Contains(string(raw), `"password"`) || !strings.Contains(string(raw), `"AQID"`) {
		t.Fatal("export lost complete original reply or leaked password", err)
	}
	if d.Request().JSON != `{"name":" original user ","password":"original transient password"}` {
		t.Fatal("redaction changed retained editable input")
	}
	base.executes.Store(0)
	s.Run(d)
	if d.confirmation != nil || base.executes.Load() != 0 {
		t.Fatal("unknown Auth outcome admitted another write")
	}
}

func TestAuthReceiptOmissionMetadataIsNotInferredFromReplyOrName(t *testing.T) {
	r := Receipt{Scope: testScope(), Authored: contract.KVRequest{Method: "/etcdserverpb.Auth/UserAdd", JSON: `{"name":"password-user","options":{"no_password":true}}`}, Result: contract.KVResult{Request: contract.KVRequest{Method: "/etcdserverpb.Auth/UserAdd", JSON: `{"name":"password-user","options":{"no_password":true}}`}, Attempted: true, Completed: true, Native: []byte{1}}}
	raw, err := encodeOperationReceipt(r)
	if err != nil || strings.Contains(string(raw), "request_omitted_fields") || !strings.Contains(string(raw), "password-user") {
		t.Fatal("non-secret Auth request was rewritten or falsely marked incomplete", err)
	}
}
