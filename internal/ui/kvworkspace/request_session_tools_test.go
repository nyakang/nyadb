package kvworkspace

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func sessionRequestToolsTestState(t *testing.T) (*State, *Document, *toolTestLease) {
	t.Helper()
	s, d, _, l := requestToolTestState(t)
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionInfo, contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
		d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: kind})
	}
	return s, d, l
}

func TestSessionToolInputCopyAndExportPreservePartialDraftAndOriginalOwner(t *testing.T) {
	s, d, l := sessionRequestToolsTestState(t)
	s.OpenRequestTool(d, contract.KVToolHTTPSessionCreate)
	input := contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{IncludeName: true, Name: "", TTL: "partial-", LockDelay: "0s", NodeChecksJSON: "["}}
	s.SetRequestToolInput(d, contract.KVToolHTTPSessionCreate, input)
	original := d.Request()
	d.unknown = &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true}}
	unknown := d.unknown
	tt := workspaceTester(s, d, 720, 520)
	tt.Frame()
	workspaceClick(t, tt, "复制工具输入")
	consume(t, s, d)
	want, err := encodeRequestToolInput(contract.KVToolHTTPSessionCreate, input)
	if err != nil || tt.Clipboard() != string(want) || d.Request() != original || !d.Dirty() || l.executes.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("copy rewrote incomplete input or executed", err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	exported := make(chan []byte, 1)
	s.SetFiles(Files{SavePath: func(string) (string, error) { close(entered); <-release; return "captured.nyakv-tool.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var out bytes.Buffer
		err := write(&out)
		exported <- out.Bytes()
		return err
	}})
	s.ExportRequestToolInput(d)
	<-entered
	d.Editor.Buffer.Set("later editor text")
	active := false
	s.deps.Active = func(*Document) bool { return active }
	close(release)
	got := <-exported
	eventEventually(t, func() bool { return len(d.io.out) == 1 })
	s.Consume()
	if !d.FileBusy() || !bytes.Equal(got, want) {
		t.Fatal("export switched snapshot or consumed parked completion")
	}
	active = true
	consume(t, s, d)
	if d.Request().JSON != "later editor text" || d.saved != original || d.unknown != unknown || !d.hasUnknownWrites() || l.executes.Load() != 0 {
		t.Fatal("export changed request/save/unknown authority")
	}
	var record struct {
		Version int
		Tool    contract.KVRequestToolKind
		Input   contract.KVRequestToolInput
	}
	if json.Unmarshal(got, &record) != nil || record.Version != 1 || record.Tool != contract.KVToolHTTPSessionCreate || record.Input != input {
		t.Fatal("tool file lost optional presence or partial text")
	}
}

func TestSessionToolInputBoundsAndUnicodeDoNotDiscardOriginalDraft(t *testing.T) {
	s, d, _ := sessionRequestToolsTestState(t)
	s.OpenRequestTool(d, contract.KVToolHTTPSessionCreate)
	original, _ := d.RequestToolInput(contract.KVToolHTTPSessionCreate)
	oversized := original
	oversized.HTTPSession.Name = strings.Repeat("x", inputLimit+1)
	oversized.HTTPSession.IncludeName = true
	s.SetRequestToolInput(d, contract.KVToolHTTPSessionCreate, oversized)
	got, _ := d.RequestToolInput(contract.KVToolHTTPSessionCreate)
	if got != original || d.Error() == "" {
		t.Fatal("new Session fields bypassed complete draft size bound")
	}
	invalid := original
	invalid.HTTPSession.Node = string([]byte{255})
	if _, err := encodeRequestToolInput(contract.KVToolHTTPSessionCreate, invalid); err == nil {
		t.Fatal("invalid Unicode was silently replaced in exported input")
	}
}

func TestSessionToolGenerationRevokesPriorReceiptSelectionAndWriteConfirmation(t *testing.T) {
	s, d, l := sessionRequestToolsTestState(t)
	d.httpSessionRenewalAvailable = true
	s.SetMethod(d, HTTPSessionRenewalMethod)
	s.OpenRequestTool(d, contract.KVToolHTTPAcquire)
	input := contract.KVRequestToolInput{Key: "original/key", Flags: "18446744073709551615", HTTPSession: contract.KVHTTPSessionToolInput{ID: renewalUUID}}
	s.SetRequestToolInput(d, contract.KVToolHTTPAcquire, input)
	s.GenerateRequestTool(d)
	if d.selectedHTTPSessionRenewal || d.method != contract.KVConsulHTTPRequestMethod || d.Busy() || l.executes.Load() != 0 {
		t.Fatal("generated HTTP request retained incompatible receipt selection or dispatched")
	}
	s.Run(d)
	if d.confirmation == nil {
		t.Fatal("lock request skipped original confirmation")
	}
	s.OpenRequestTool(d, contract.KVToolHTTPAcquire)
	input.HTTPSession.ID = "different-id"
	s.SetRequestToolInput(d, contract.KVToolHTTPAcquire, input)
	s.Confirm(d, true)
	if d.confirmation != nil || l.executes.Load() != 0 {
		t.Fatal("changed UUID retained old write authority")
	}
}

func TestSessionToolGenerationResetsSameMethodReceiptSelection(t *testing.T) {
	for _, selection := range []string{"watch", "renewal"} {
		t.Run(selection, func(t *testing.T) {
			s, d, l := sessionRequestToolsTestState(t)
			s.SetMethod(d, contract.KVConsulHTTPRequestMethod)
			d.selectedHTTPWatch = selection == "watch"
			d.selectedHTTPSessionRenewal = selection == "renewal"
			s.OpenRequestTool(d, contract.KVToolHTTPSessionInfo)
			s.SetRequestToolInput(d, contract.KVToolHTTPSessionInfo, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: renewalUUID}})
			s.GenerateRequestTool(d)
			if d.selectedHTTPWatch || d.selectedHTTPSessionRenewal || d.requestTools.open || l.executes.Load() != 0 {
				t.Fatal("same-method generation retained foreign receipt selection or dispatched")
			}
		})
	}
}
