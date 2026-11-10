package kvworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type authFormLease struct{ *fakeLease }

func (*authFormLease) AuthMethods() []string {
	var out []string
	for _, method := range authMethods {
		out = append(out, method.Path)
	}
	return out
}

func (*authFormLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
	write := r.Method != authPath("AuthStatus") && r.Method != authPath("UserList") && r.Method != authPath("RoleList") && r.Method != authPath("UserGet") && r.Method != authPath("RoleGet")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.JSON), &fields); err != nil {
		return nil, err
	}
	record := contract.KVRequestRecord{Request: r}
	if fields["password"] != nil {
		delete(fields, "password")
		record.OmittedFields = []string{"password"}
		raw, _ := json.Marshal(fields)
		record.Request.JSON = string(raw)
	}
	return &authRecordPlan{fakePlan: &fakePlan{request: r, write: write}, record: record}, nil
}

func authFormState(t *testing.T) (*State, *Document, *authFormLease) {
	s, d, base := testState(t)
	l := &authFormLease{base}
	d.lease = l
	return s, d, l
}

func TestAuthStructuredRequestsKeepExactNamesCompleteRangesAndSeparateInitialSteps(t *testing.T) {
	key := bytes.Repeat([]byte{255}, 4096)
	p := &authDraft{method: authPath("RoleAdd"), name: " exact role ", initialPermission: true, access: "READWRITE", rangeKind: "unbounded", encoding: "base64", key: base64.StdEncoding.EncodeToString(key)}
	requests, err := authDraftRequests(p)
	if err != nil || len(requests) != 2 || requests[0].Method != authPath("RoleAdd") || requests[1].Method != authPath("RoleGrantPermission") {
		t.Fatal("role create/permission combined or rejected", err)
	}
	var permission struct {
		Name string `json:"name"`
		Perm struct {
			Key    []byte `json:"key"`
			End    []byte `json:"range_end"`
			Access string `json:"permType"`
		} `json:"perm"`
	}
	if json.Unmarshal([]byte(requests[1].JSON), &permission) != nil || permission.Name != " exact role " || !bytes.Equal(permission.Perm.Key, key) || !bytes.Equal(permission.Perm.End, []byte{0}) || permission.Perm.Access != "READWRITE" {
		t.Fatal("maximum permission bytes/names changed")
	}
	p = &authDraft{method: authPath("UserAdd"), name: " exact user ", password: " temporary password ", initialRoles: " role one \n role two "}
	requests, err = authDraftRequests(p)
	if err != nil || len(requests) != 3 || !strings.Contains(requests[0].JSON, " temporary password ") || !strings.Contains(requests[1].JSON, " role one ") || !strings.Contains(requests[2].JSON, " role two ") {
		t.Fatal("separate initial grants changed originals", err)
	}
	for _, kind := range []string{"exact", "range", "prefix", "all", "unbounded"} {
		p = &authDraft{rangeKind: kind, encoding: "base64", key: "AP8=", end: "AQ==", access: "READ"}
		got, err := authDraftPermission(p)
		if err != nil {
			t.Fatal(err)
		}
		if kind == "range" && !bytes.Equal(got.End, []byte{1}) || kind == "prefix" && !bytes.Equal(got.End, []byte{1}) || kind == "exact" && len(got.End) != 0 || kind == "all" && (!bytes.Equal(got.Key, []byte{0}) || !bytes.Equal(got.End, []byte{0})) {
			t.Fatal("native permission geometry changed")
		}
	}
	for _, p := range []*authDraft{{method: authPath("UserAdd"), name: "u", password: "secret", noPassword: true}, {method: authPath("UserChangePassword"), name: "u", noPassword: true}, {method: authPath("UserAdd"), name: "u", password: "s", initialRoles: "same\nsame"}, {method: authPath("RoleGrantPermission"), name: "r", access: "READ", rangeKind: "range", encoding: "text", key: "a", end: "a"}} {
		if _, err := authDraftRequests(p); err == nil {
			t.Fatal("ambiguous/unsupported management form accepted")
		}
	}
}

func TestAuthOriginalMultiStepConfirmationAndPartialUnknownStopWithoutReplay(t *testing.T) {
	s, d, l := authFormState(t)
	s.OpenAuthAction(d, "UserAdd")
	p := d.authDraft
	p.name, p.password, p.initialRoles = " exact user ", "original transient secret", " first role \n second role "
	s.PrepareAuthAction(d)
	if d.confirmation == nil || d.authDraft != nil || p.password != "" || len(d.confirmation.auth.plans) != 3 || l.executes.Load() != 0 {
		t.Fatal("Auth form did IO or did not capture all immutable steps")
	}
	if !strings.Contains(d.confirmation.auth.plans[0].Snapshot().JSON, "original transient secret") || strings.Contains(d.confirmation.auth.receipt.Requests[0].Request.JSON, "original transient secret") || !slices.Equal(d.confirmation.auth.receipt.Requests[0].OmittedFields, []string{"password"}) {
		t.Fatal("original authorization/record redaction changed")
	}
	var calls int
	l.execute = func(_ context.Context, plan contract.KVPreparation) (contract.KVResult, error) {
		calls++
		record := plan.(contract.KVPreparationRecord).RecordedRequest()
		r := contract.KVResult{Request: record.Request, RequestOmittedFields: record.OmittedFields, Attempted: true, Completed: calls == 1, WriteOutcomeUnknown: calls == 2, Native: []byte{byte(calls)}}
		if calls == 2 {
			return r, contract.ErrKVLeaseLost
		}
		return r, nil
	}
	s.Confirm(d, false)
	if l.executes.Load() != 0 {
		t.Fatal("unchecked batch dispatched")
	}
	s.Confirm(d, true)
	consume(t, s, d)
	r, ok := d.UnknownReceipt()
	if !ok || calls != 2 || len(r.AuthManagement.Requests) != 3 || len(r.AuthManagement.Steps) != 2 || !r.AuthManagement.Steps[0].Result.Completed || !r.AuthManagement.Steps[1].Result.WriteOutcomeUnknown || strings.Contains(r.AuthManagement.Requests[0].Request.JSON, "original transient secret") {
		t.Fatal("original partial Auth receipts were erased/replayed or unknown step did not stop")
	}
	r.AuthManagement.Steps[0].Result.Native[0] = 99
	if d.unknown.AuthManagement.Steps[0].Result.Native[0] != 1 {
		t.Fatal("partial native receipt aliases caller")
	}
	d.selectedUnknown = true
	raw, err := s.OperationRecord(d)
	if err != nil || strings.Contains(string(raw), "original transient secret") || !strings.Contains(string(raw), `"auth_management"`) || !strings.Contains(string(raw), `"AQ=="`) || !strings.Contains(string(raw), `"Ag=="`) || !strings.Contains(string(raw), " second role ") {
		t.Fatal("partial Auth export omitted original/planned steps or exposed a password", err)
	}
	s.OpenAuthAction(d, "RoleAdd")
	if d.authDraft != nil {
		t.Fatal("unknown Auth step admitted another write")
	}
}

func TestAuthReadCachesRejectRetirementAndDoNotResolveOriginalUnknownBatch(t *testing.T) {
	s, d, l := authFormState(t)
	d.unknown = &Receipt{Scope: d.scope.Clone(), Result: contract.KVResult{WriteOutcomeUnknown: true, Native: []byte{9}}}
	l.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		return contract.KVResult{Request: p.Snapshot(), Completed: true, Attempted: true, Reply: &contract.KVReply{Auth: &contract.KVAuthReceipt{Users: []string{" original user "}}}}, nil
	}
	s.ReadAuth(d, "UserList", "")
	consume(t, s, d)
	if d.authState().users == nil || !slices.Equal(d.authState().users.Result.Reply.Auth.Users, []string{" original user "}) || d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown {
		t.Fatal("explicit original Auth read did not cache or erased unknown")
	}
	original := d.authState().users
	s.ReadAuth(d, "UserList", "")
	finished := d.job.finished
	await(t, finished)
	l.lost.Store(true)
	consume(t, s, d)
	if d.authState().users != original || !d.NeedsRecovery() {
		t.Fatal("retired original read replaced live directory")
	}
}

func TestAuthNativeMethodsNeedOriginalExplicitFacetAndChangedEditorRevokesConfirmation(t *testing.T) {
	s, d, _ := testState(t)
	s.OpenAuthAction(d, "UserAdd")
	if d.AuthManagementAvailable() || d.authDraft != nil {
		t.Fatal("Auth inferred from connection/name")
	}
	s, d, _ = authFormState(t)
	s.OpenAuthAction(d, "UserAdd")
	d.authDraft.name, d.authDraft.password = "u", "secret"
	s.PrepareAuthAction(d)
	d.Editor.Buffer.Set("changed browser input")
	s.Confirm(d, true)
	if d.confirmation != nil || d.Busy() {
		t.Fatal("changed original browser dispatched old Auth plan")
	}
	s.OpenAuthAction(d, "RoleAdd")
	d.authDraft.name = "r"
	s.PrepareAuthAction(d)
	d.scope.Generation++
	s.Confirm(d, true)
	if d.confirmation != nil || d.Busy() {
		t.Fatal("changed generation dispatched old Auth plan")
	}
	if _, err := authRoleNames("r\nr"); err == nil {
		t.Fatal("duplicate original role accepted")
	}
}

func TestAuthOriginalStepBudgetStopsBeforeAnotherRPCWithoutErasingConfirmedReceipts(t *testing.T) {
	s, d, l := authFormState(t)
	s.OpenAuthAction(d, "UserAdd")
	d.authDraft.name, d.authDraft.password, d.authDraft.initialRoles = "u", "s", "r1\nr2"
	s.PrepareAuthAction(d)
	var calls int
	l.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		calls++
		record := p.(contract.KVPreparationRecord).RecordedRequest()
		return contract.KVResult{Request: record.Request, RequestOmittedFields: record.OmittedFields, Attempted: true, Completed: true, Native: make([]byte, (8<<20)+1)}, nil
	}
	s.Confirm(d, true)
	consume(t, s, d)
	r, ok := d.LastReceipt()
	if !ok || calls != 2 || len(r.AuthManagement.Steps) != 2 || len(r.AuthManagement.Requests) != 3 || r.Error == "" || d.unknown != nil || !r.AuthManagement.Steps[0].Result.Completed || len(r.AuthManagement.Steps[0].Result.Native) != (8<<20)+1 {
		t.Fatal("bounded step capture replayed another RPC or erased original acknowledged evidence")
	}
}

func TestAuthDraftLeaveGuardAndTerminalSecretCleanup(t *testing.T) {
	for _, terminal := range []string{"cancel", "discard", "disconnect", "retire", "close"} {
		t.Run(terminal, func(t *testing.T) {
			s, d, l := authFormState(t)
			s.OpenAuthAction(d, "UserAdd")
			draft := d.authDraft
			draft.name, draft.password = "original user", "transient secret"
			d.saved = d.Request()
			if !s.GuardLeave(d, nil) {
				t.Fatal("Auth form bypassed leave decision")
			}
			s.leave = nil
			switch terminal {
			case "cancel":
				s.CancelConfirmation(d)
			case "discard":
				if !s.GuardLeave(d, nil) {
					t.Fatal("Auth draft leave decision missing")
				}
				s.DecideLeave(ui.Services{}, true)
			case "disconnect":
				s.DisconnectProfile(d.profileID)
			case "retire":
				l.lost.Store(true)
				s.Consume()
			case "close":
				s.Close(d)
			}
			if draft.password != "" || d.authDraft != nil {
				t.Fatal("terminal Auth state retained temporary secret")
			}
		})
	}
	s, d, _ := authFormState(t)
	s.OpenAuthAction(d, "RoleAdd")
	d.authDraft.name = "r"
	s.PrepareAuthAction(d)
	d.saved = d.Request()
	if !s.GuardLeave(d, nil) {
		t.Fatal("Auth confirmation bypassed leave decision")
	}
}
