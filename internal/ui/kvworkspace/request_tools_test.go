package kvworkspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type testRequestBuilder struct{ marker string }

func (b testRequestBuilder) BuildKVRequest(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	if input.Key == "invalid" {
		return contract.KVRequest{}, errors.New("invalid tool input")
	}
	method := "GET"
	if kind == contract.KVToolPut || kind == contract.KVToolDelete || httpSessionRequestTool(kind) && kind != contract.KVToolHTTPSessionInfo && kind != contract.KVToolHTTPSessionList && kind != contract.KVToolHTTPNodeSessions {
		method = "PUT"
	}
	raw, _ := json.Marshal(struct {
		Method, Path string
		Input        contract.KVRequestToolInput
	}{method, b.marker + input.Key, input})
	return contract.KVRequest{Method: contract.KVConsulHTTPRequestMethod, JSON: string(raw)}, nil
}

type toolTestProvider struct {
	*httpTestProvider
	builder contract.KVRequestBuilder
}

func (p *toolTestProvider) KVRequestBuilder() contract.KVRequestBuilder { return p.builder }

type toolTestLease struct{ *fakeLease }

func (l *toolTestLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	var request struct{ Method string }
	if err := json.Unmarshal([]byte(r.JSON), &request); err != nil {
		return nil, err
	}
	return &fakePlan{request: r, write: request.Method == "PUT"}, nil
}

func requestToolTestState(t *testing.T) (*State, *Document, *toolTestProvider, *toolTestLease) {
	t.Helper()
	base, old, hp := httpTestState(t)
	base.Close(old)
	hp.presentation.Tools = []contract.KVRequestTool{
		{Kind: contract.KVToolPut, Default: contract.KVRequestToolInput{Key: "original//"}},
		{Kind: contract.KVToolRead, Default: contract.KVRequestToolInput{Key: "original//"}},
	}
	lease := &toolTestLease{hp.lease}
	hp.reserve = func(uint64) (contract.KVRequestLease, error) { return lease, nil }
	hp.lease.closed.Store(false)
	p := &toolTestProvider{httpTestProvider: hp, builder: testRequestBuilder{marker: "original:"}}
	s := New(Dependencies{ProviderFor: func(id int64) (contract.KVRequestsProvider, error) {
		if id != 7 {
			t.Fatal("tool selected another profile")
		}
		return p, nil
	}})
	d := s.Open(2, 7, contract.KVRequest{})
	t.Cleanup(s.CloseAll)
	return s, d, p, lease
}

func TestRequestToolsCaptureBuilderPrefixAndOnlyGenerateText(t *testing.T) {
	s, d, p, l := requestToolTestState(t)
	original := d.Request()
	p.builder = testRequestBuilder{marker: "foreign:"}
	p.presentation.Tools[0].Default.Key = "foreign"
	tools := d.RequestTools()
	tools[0].Default.Key = "mutated inspection"
	s.OpenRequestTool(d, contract.KVToolPut)
	input, ok := d.RequestToolInput(contract.KVToolPut)
	if !ok || input.Key != "original//" || d.Dirty() {
		t.Fatal("tool did not capture original presentation")
	}
	input.Key, input.Flags, input.CompareIndex, input.ValueBase64 = "absolute/key", "18446744073709551615", "0", "AP+A"
	s.SetRequestToolInput(d, contract.KVToolPut, input)
	if !d.Dirty() || d.Request() != original || l.executes.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("form change dispatched or replaced text")
	}
	s.GenerateRequestTool(d)
	if d.Request() == original || !bytes.Contains([]byte(d.Request().JSON), []byte("original:absolute/key")) || d.confirmation != nil || d.Busy() || d.hasRequestToolDraft() || l.executes.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("generation dispatched or used foreign builder")
	}
	s.Run(d)
	if d.confirmation == nil || l.executes.Load() != 0 {
		t.Fatal("generated write bypassed confirmation")
	}
	s.Confirm(d, false)
	s.Confirm(d, true)
	s.Confirm(d, true)
	consume(t, s, d)
	if l.executes.Load() != 1 || d.last == nil {
		t.Fatal("generated write did not use original one-use lease")
	}
}

func TestRequestToolChangesRevokeAuthorizationAndPreserveFailedAndParkedDrafts(t *testing.T) {
	s, d, _, l := requestToolTestState(t)
	s.OpenRequestTool(d, contract.KVToolPut)
	input, _ := d.RequestToolInput(contract.KVToolPut)
	s.GenerateRequestTool(d)
	s.Run(d)
	if d.confirmation == nil {
		t.Fatal("missing original write confirmation")
	}
	s.OpenRequestTool(d, contract.KVToolPut)
	s.Confirm(d, true)
	if d.confirmation != nil || l.executes.Load() != 0 {
		t.Fatal("opening form retained old authority")
	}
	input.Key = "invalid"
	s.SetRequestToolInput(d, contract.KVToolPut, input)
	original := d.Request()
	s.GenerateRequestTool(d)
	if d.Request() != original || d.error == "" || !d.hasRequestToolDraft() || !d.requestTools.open {
		t.Fatal("invalid input discarded original or form")
	}
	s.OpenRequestTool(d, contract.KVToolRead)
	s.OpenRequestTool(d, contract.KVToolPut)
	retained, _ := d.RequestToolInput(contract.KVToolPut)
	if retained != input {
		t.Fatal("switching tools lost form input")
	}
	active := false
	s.deps.Active = func(*Document) bool { return active }
	s.GenerateRequestTool(d)
	input.Key = "parked mutation"
	s.SetRequestToolInput(d, contract.KVToolPut, input)
	retained, _ = d.RequestToolInput(contract.KVToolPut)
	if retained.Key != "invalid" || d.Request() != original {
		t.Fatal("parked input was consumed")
	}
	active = true
	s.DisconnectProfile(7)
	s.OpenRequestTool(d, contract.KVToolPut)
	input.Key = "offline draft"
	s.SetRequestToolInput(d, contract.KVToolPut, input)
	s.GenerateRequestTool(d)
	if d.Request() == original || !d.NeedsRecovery() || d.Busy() || l.executes.Load() != 0 {
		t.Fatal("offline generation executed or recovered")
	}
	s.Close(d)
	if d.requestBuilder != nil || d.requestTools != nil || len(d.RequestTools()) != 0 {
		t.Fatal("close retained tool inputs or builder")
	}
}

func TestRequestToolsGuardUnappliedInputsAndRejectEditorRevert(t *testing.T) {
	s, d, _, l := requestToolTestState(t)
	s.OpenRequestTool(d, contract.KVToolPut)
	input, _ := d.RequestToolInput(contract.KVToolPut)
	input.ValueBase64 = "AP8="
	s.SetRequestToolInput(d, contract.KVToolPut, input)
	d.requestTools.open = false
	if !s.GuardLeave(d, nil) {
		t.Fatal("unapplied form input bypassed leave guard")
	}
	s.DecideLeave(ui.Services{}, true)
	if s.GuardLeave(d, nil) {
		t.Fatal("unchanged discard decision was lost")
	}
	input.Flags = "0"
	s.SetRequestToolInput(d, contract.KVToolPut, input)
	if !s.GuardLeave(d, nil) {
		t.Fatal("changed form reused old leave permission")
	}
	s.DecideLeave(ui.Services{}, false)
	s.OpenRequestTool(d, contract.KVToolPut)
	original := d.Request()
	d.Editor.Buffer.Set("edited elsewhere")
	d.Editor.Buffer.Set(original.JSON)
	s.GenerateRequestTool(d)
	if d.Request() != original || d.error == "" {
		t.Fatal("edited/reverted text accepted stale generation")
	}
	s.OpenRequestTool(d, contract.KVToolPut)
	s.GenerateRequestTool(d)
	s.Run(d)
	original = d.Request()
	d.Editor.Buffer.Set("changed after confirmation")
	d.Editor.Buffer.Set(original.JSON)
	s.Confirm(d, true)
	if d.confirmation != nil || l.executes.Load() != 0 {
		t.Fatal("editor revert reused old write authorization")
	}
}

func TestRequestToolFilesKeepUnappliedInputsAndRevokeOriginalConfirmation(t *testing.T) {
	s, d, _, l := requestToolTestState(t)
	s.OpenRequestTool(d, contract.KVToolPut)
	s.GenerateRequestTool(d)
	s.Run(d)
	original := d.Request()
	encoded, _ := EncodeRequest(original)
	s.SetFiles(Files{OpenPath: func() (string, error) { return "saved", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(encoded)), nil }})
	s.OpenRequest(d)
	consume(t, s, d)
	s.Confirm(d, true)
	if d.confirmation != nil || l.executes.Load() != 0 {
		t.Fatal("file loading reused old authorization")
	}
	s.OpenRequestTool(d, contract.KVToolPut)
	input, _ := d.RequestToolInput(contract.KVToolPut)
	input.ValueBase64 = "AP8="
	s.SetRequestToolInput(d, contract.KVToolPut, input)
	d.requestTools.open = false
	var saved []byte
	s.SetFiles(Files{WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		saved = b.Bytes()
		return err
	}})
	s.SaveRequest(d, false)
	consume(t, s, d)
	roundtrip, err := DecodeRequest(saved)
	if err != nil || roundtrip != original || !d.Dirty() || !s.GuardLeave(d, nil) {
		t.Fatal("request file pretended to save unapplied inputs", err)
	}
}

func TestRequestToolsUIGenerationUsesExistingWriteConfirmation(t *testing.T) {
	s, d, _, l := requestToolTestState(t)
	s.OpenRequestTool(d, contract.KVToolPut)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.View(c, d)
		s.Modals(c)
	}, 720, 520)
	tt.Frame()
	if err := tt.Click(locales.Text("绝对键或前缀")); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("explicit/key")
	tt.Frame()
	if err := tt.Click(locales.Text("生成请求文本")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if l.executes.Load() != 0 || d.requestTools.open || d.confirmation != nil {
		t.Fatal("UI generation dispatched")
	}
	if err := tt.Click(locales.Text("运行 KV 请求")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, label := range []string{"我确认执行此原生写入请求", "确认执行 KV 写入"} {
		if err := tt.Click(locales.Text(label)); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	consume(t, s, d)
	if l.executes.Load() != 1 {
		t.Fatal("generated UI request bypassed original executor")
	}
}
