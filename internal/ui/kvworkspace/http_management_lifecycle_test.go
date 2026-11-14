package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

type managementTestPlan struct {
	scope  contract.KVScope
	input  contract.KVHTTPManagementRequest
	write  bool
	closed atomic.Bool
}

func (p *managementTestPlan) Scope() contract.KVScope                   { return p.scope.Clone() }
func (p *managementTestPlan) Request() contract.KVHTTPManagementRequest { return p.input.Clone() }
func (p *managementTestPlan) LocalRecordID() string                     { return "original-management-record" }
func (p *managementTestPlan) HTTP() contract.KVHTTPRequest {
	return contract.KVHTTPRequest{Method: "PUT", Path: "/v1/query/original", Body: bytes.Clone(p.input.Body)}
}
func (p *managementTestPlan) Writes() bool { return p.write }
func (p *managementTestPlan) Close() error { p.closed.Store(true); return nil }

type managementTestLease struct {
	*fakeLease
	run  func(context.Context, contract.KVHTTPManagementPreparation) (contract.KVHTTPManagementResult, error)
	last contract.KVHTTPManagementResult
}

func (l *managementTestLease) HTTPManagementOperations() []contract.KVHTTPManagementOperation {
	return []contract.KVHTTPManagementOperation{{Name: "agent-members", Title: "Agent members", Method: "GET", Path: "/v1/agent/members"}, {Name: "query-create", Title: "Create Prepared Query", Method: "POST", Path: "/v1/query", BodyKind: "json", Write: true}}
}
func (l *managementTestLease) PrepareHTTPManagement(in contract.KVHTTPManagementRequest) (contract.KVHTTPManagementPreparation, error) {
	return &managementTestPlan{scope: l.Scope(), input: in.Clone(), write: in.Operation == "query-create"}, nil
}
func (l *managementTestLease) ExecuteHTTPManagement(ctx context.Context, p contract.KVHTTPManagementPreparation) (result contract.KVHTTPManagementResult, err error) {
	l.executes.Add(1)
	defer func() { l.last = result.Clone() }()
	if l.run != nil {
		return l.run(ctx, p)
	}
	return contract.KVHTTPManagementResult{Scope: p.Scope(), Request: p.Request(), LocalRecordID: p.LocalRecordID(), Receipt: contract.KVResult{Attempted: true, Completed: true, HTTP: &contract.KVHTTPResult{Captured: true, EOF: true, Status: 200, Body: []byte(`[]`)}}}, nil
}
func (l *managementTestLease) LastHTTPManagement() contract.KVHTTPManagementResult {
	return l.last.Clone()
}

func TestManagementRecoveryAfterCapabilityProbeKeepsDisplayedOriginalReceipt(t *testing.T) {
	s, d, p := httpTestState(t)
	l := &managementTestLease{fakeLease: p.lease}
	d.lease = l
	s.OpenHTTPManagement(d)
	s.PrepareHTTPManagement(d)
	consume(t, s, d)
	original := d.httpManagement.last.Clone()
	s.RunHTTPCapabilities(d)
	consume(t, s, d)
	expected, _ := managementRecoveryExpected(d)
	if expected == nil || expected.Request.Operation != "audit-hash" || !reflect.DeepEqual(original, *d.httpManagement.last) {
		t.Fatal("probes replaced the user receipt or retained a stale recovery expectation")
	}
	out := outcome{recoveryManagement: expected, lease: l}
	s.consumeHTTPManagementRecovery(d, &operation{managementRecovery: expected}, &out)
	if out.err != nil || !reflect.DeepEqual(original, *d.httpManagement.last) {
		t.Fatal("original probe prevented recovery or replaced the displayed receipt", out.err)
	}
}

func TestCapabilityBudgetRetainsTheLastDispatchedOriginalResponse(t *testing.T) {
	s, d, p := httpTestState(t)
	l := &managementTestLease{fakeLease: p.lease}
	d.lease = l
	s.OpenHTTPManagement(d)
	l.run = func(_ context.Context, p contract.KVHTTPManagementPreparation) (contract.KVHTTPManagementResult, error) {
		return contract.KVHTTPManagementResult{Scope: p.Scope(), Request: p.Request(), LocalRecordID: p.LocalRecordID(), Receipt: contract.KVResult{Attempted: true, Completed: true, HTTP: &contract.KVHTTPResult{Captured: true, EOF: true, Status: 200, Body: make([]byte, 32<<20)}}}, nil
	}
	s.RunHTTPCapabilities(d)
	consume(t, s, d)
	if l.executes.Load() != 1 || len(d.httpManagement.capabilities) != 1 || len(d.httpManagement.capabilities[0].Receipt.HTTP.Body) != 32<<20 {
		t.Fatal("budget dropped dispatched evidence or started another probe")
	}
}

func TestManagementOriginalConfirmationAbandonedPlanAndParkingUnknownBarrier(t *testing.T) {
	s, d, p := httpTestState(t)
	l := &managementTestLease{fakeLease: p.lease}
	d.lease = l
	s.OpenHTTPManagement(d)
	x := d.httpManagement
	x.draft = httpManagementDraft{Operation: "query-create", Options: "{}", Body: `{"Token":"original-private","Service":{"Service":"original"}}`}
	s.PrepareHTTPManagement(d)
	plan := x.plan.(*managementTestPlan)
	s.ConfirmHTTPManagement(d, false)
	if l.executes.Load() != 0 || x.plan != plan {
		t.Fatal("unchecked write executed")
	}
	x.draft.Body = `{"Service":{"Service":"changed"}}`
	s.ConfirmHTTPManagement(d, true)
	if !plan.closed.Load() || l.executes.Load() != 0 {
		t.Fatal("changed definition retained authority")
	}
	s.PrepareHTTPManagement(d)
	pending := x.plan.(*managementTestPlan)
	s.DisconnectProfile(d.profileID)
	if !pending.closed.Load() || x.plan != nil {
		t.Fatal("disconnect retained a reserved plan")
	}
	d.needsRecovery = false
	entered, release := make(chan struct{}), make(chan struct{})
	l.run = func(ctx context.Context, p contract.KVHTTPManagementPreparation) (contract.KVHTTPManagementResult, error) {
		close(entered)
		<-release
		return contract.KVHTTPManagementResult{Scope: p.Scope(), Request: p.Request(), LocalRecordID: p.LocalRecordID(), Receipt: contract.KVResult{Attempted: true, WriteOutcomeUnknown: true, HTTP: &contract.KVHTTPResult{Status: 500, Captured: true, EOF: true, Body: []byte("original unknown")}}}, contract.ErrKVWriteUnknown
	}
	s.PrepareHTTPManagement(d)
	s.ConfirmHTTPManagement(d, true)
	<-entered
	finished := d.job.finished
	s.deps.Active = func(*Document) bool { return false }
	close(release)
	await(t, finished)
	s.Consume()
	if x.last != nil || !d.Busy() {
		t.Fatal("parked completion published to inactive document")
	}
	s.deps.Active = nil
	consume(t, s, d)
	if x.unknown == nil || !d.hasUnknownWrites() || d.last != nil {
		t.Fatal("private unknown evidence lost or leaked to generic results")
	}
	unknown := x.unknown.Clone()
	x.draft = httpManagementDraft{Operation: "agent-members", Options: "{}"}
	l.run = nil
	s.PrepareHTTPManagement(d)
	consume(t, s, d)
	if !reflect.DeepEqual(unknown, *x.unknown) || !d.hasUnknownWrites() {
		t.Fatal("later read confirmed unknown write")
	}
}

func TestPrivateManagementBrowserFullUnsignedPaginationClipboardAndExportConsent(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		t.Run(language, func(t *testing.T) {
			i18n.SetLocaleFunc(func() string { return language })
			s, d, p := httpTestState(t)
			d.lease = &managementTestLease{fakeLease: p.lease}
			s.OpenHTTPManagement(d)
			x := d.httpManagement
			var rows []json.RawMessage
			for i := range 53 {
				raw, _ := json.Marshal(struct {
					Index  int
					Value  json.Number
					Secret string
				}{i, json.Number("18446744073709551615"), "private-value"})
				rows = append(rows, raw)
			}
			body, _ := json.Marshal(rows)
			x.last = &contract.KVHTTPManagementResult{Scope: d.scope.Clone(), LocalRecordID: "original", Receipt: contract.KVResult{HTTP: &contract.KVHTTPResult{Status: 200, Body: body}}}
			x.revealed = true
			x.browser.set("original", body)
			x.browser.page = 2
			x.browser.selected = 52
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(); s.privateJSONView(c, d, &x.browser, "management") }, 720, 520)
			tt.Frame()
			if !strings.Contains(string(x.browser.selectedRaw()), "18446744073709551615") {
				t.Fatal("private detail rounded uint64")
			}
			if err := tt.Click(locales.Text("复制完整原响应详情")); err != nil {
				t.Fatal(err)
			}
			consume(t, s, d)
			if tt.Clipboard() != string(rows[52]) {
				t.Fatal("clipboard changed original token")
			}
			var exported []byte
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "private.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
				var out bytes.Buffer
				err := write(&out)
				exported = bytes.Clone(out.Bytes())
				return err
			}})
			s.managementFile(d, "management-export")
			if d.FileBusy() {
				t.Fatal("export bypassed explicit private consent")
			}
			x.exportConsent = true
			s.managementFile(d, "management-detail-export")
			consume(t, s, d)
			if !bytes.Equal(exported, rows[52]) || x.exportConsent {
				t.Fatal("full private export changed or consent remained reusable")
			}
			if p.lease.executes.Load() != 0 || !bytes.Equal(body, x.last.Receipt.HTTP.Body) {
				t.Fatal("local browser dispatched or changed original response")
			}
		})
	}
}

func TestCapabilityStatusRequiresOriginalCompleteHTTPAndNeverInfersFromDenial(t *testing.T) {
	for _, tc := range []struct {
		status   int
		body     string
		complete bool
		want     string
	}{{200, "[]", true, "可用"}, {200, "[]", false, "未知"}, {403, "denied", true, "权限不足"}, {401, "ACL support disabled\n", true, "已禁用"}, {404, "missing", true, "不支持"}, {500, "failure", true, "未知"}} {
		r := contract.KVHTTPManagementResult{Receipt: contract.KVResult{Completed: true, HTTP: &contract.KVHTTPResult{Status: tc.status, Body: []byte(tc.body), Captured: true, EOF: tc.complete}}}
		if got := capabilityCaption(r); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}
