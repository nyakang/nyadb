package kvworkspace

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type httpTestProvider struct {
	*fakeProvider
	presentation contract.KVRequestPresentation
}

func (p *httpTestProvider) KVRequestPresentation() contract.KVRequestPresentation {
	return p.presentation
}
func httpTestState(t *testing.T) (*State, *Document, *httpTestProvider) {
	t.Helper()
	scope := testScope()
	scope.Header = contract.KVHeader{}
	scope.Prefix = []byte("original//")
	scope.HTTP = contract.KVHTTPIdentity{Protocol: "consul.http/1", Endpoint: "http://original:8500", Datacenter: "dc1", Consistency: "consistent", Configuration: "original-config"}
	request := contract.KVRequest{Method: contract.KVConsulHTTPRequestMethod, JSON: `{"method":"GET","path":"/v1/kv/original//","query":{"keys":[""]}}`}
	p := &httpTestProvider{fakeProvider: &fakeProvider{lease: &fakeLease{scope: scope}}, presentation: contract.KVRequestPresentation{Product: "Consul", Default: request, Methods: []contract.KVRequestMethod{{Name: "HTTP", Request: request}}}}
	s := New(Dependencies{ProviderFor: func(int64) (contract.KVRequestsProvider, error) { return p, nil }})
	d := s.Open(1, 7, contract.KVRequest{})
	t.Cleanup(s.CloseAll)
	return s, d, p
}
func httpTestReceipt(d *Document) *Receipt {
	index := uint64(math.MaxUint64)
	zero := uint64(0)
	leader := false
	h := &contract.KVHTTPResult{Protocol: d.scope.HTTP.Protocol, URL: d.scope.HTTP.Endpoint + "/v1/kv/original//", Request: contract.KVHTTPRequest{Method: "GET", Path: "/v1/kv/original//", Query: map[string][]string{"keys": {""}}, Body: []byte{0, 255}}, Kind: "kv-list", Attempted: true, Ended: true, Captured: true, EOF: true, Acknowledged: true, Succeeded: true, Status: 200, Header: map[string][]string{"X-Consul-Index": {"18446744073709551615"}, "X-Original": {"one", "two"}}, Trailer: map[string][]string{"X-Finish": {"yes"}}, Body: []byte{0, 255, 128}, Metadata: contract.KVHTTPMetadata{Index: &index, LastContact: &zero, KnownLeader: &leader}, Entries: []contract.KVHTTPEntry{{Key: "original//", Value: []byte{0, 255}, ValuePresent: true, Flags: math.MaxUint64, CreateIndex: 9007199254740993, ModifyIndex: math.MaxUint64, PresentFields: []string{"Key", "Value", "Flags"}}}, Txn: &contract.KVHTTPTransaction{Committed: false, Errors: []contract.KVHTTPTransactionError{{OpIndex: 1, What: "native rejection"}}}, SessionID: "11111111-2222-4333-8444-555555555555"}
	return &Receipt{Scope: d.scope.Clone(), Authored: d.Request(), Result: contract.KVResult{Request: d.Request(), Attempted: true, Completed: true, HTTP: h, Code: "200"}}
}
func TestHTTPPresentationIsCapturedAndDoesNotExposeEtcdTools(t *testing.T) {
	s, d, p := httpTestState(t)
	original := d.Request()
	p.presentation.Methods[0].Name = "foreign"
	p.presentation.Default.JSON = "foreign"
	methods := s.MethodsFor(d)
	if len(methods) != 1 || methods[0].Name != "HTTP" || d.Request() != original || s.defaultRequestFor(d) != original {
		t.Fatal("provider presentation was aliased or etcd tools leaked")
	}
	methods[0].Name = "mutated inspection"
	if s.MethodsFor(d)[0].Name != "HTTP" {
		t.Fatal("method inspection changed original presentation")
	}
	s.ChooseMethod(d, SnapshotMethod)
	if d.Request() != original {
		t.Fatal("foreign method replaced original document")
	}
}
func TestHTTPOperationRecordPreservesOriginalBytesPresenceAndUnsignedPrecision(t *testing.T) {
	s, d, _ := httpTestState(t)
	d.last = httpTestReceipt(d)
	raw, err := s.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record["native_protobuf"] != nil || record["protobuf_json"] != nil || record["native_header"] != nil {
		t.Fatal("HTTP record fabricated protobuf metadata")
	}
	var http struct {
		Body            []byte
		Request         contract.KVHTTPRequest
		Header, Trailer map[string][]string
		Metadata        struct {
			Index, LastContact *string
			KnownLeader        *bool
		}
		Entries []struct {
			Flags, CreateIndex, ModifyIndex string
			Value                           []byte
			ValuePresent, ValueNull         bool
			PresentFields                   []string
		}
		Txn       *contract.KVHTTPTransaction
		SessionID string
	}
	if err = json.Unmarshal(record["http"], &http); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(http.Body, d.last.Result.HTTP.Body) || !bytes.Equal(http.Request.Body, []byte{0, 255}) || http.Request.Path != "/v1/kv/original//" || len(http.Header["X-Original"]) != 2 || http.Trailer["X-Finish"][0] != "yes" || *http.Metadata.Index != "18446744073709551615" || *http.Metadata.LastContact != "0" || *http.Metadata.KnownLeader || http.Entries[0].CreateIndex != "9007199254740993" || http.Entries[0].Flags != "18446744073709551615" || !http.Entries[0].ValuePresent || http.Entries[0].ValueNull || http.Txn.Errors[0].OpIndex != 1 || http.SessionID == "" {
		t.Fatal("complete native HTTP record changed")
	}
	clone, _ := d.LastReceipt()
	clone.Result.HTTP.Body[0] = 9
	clone.Result.HTTP.Header["X-Original"][0] = "changed"
	*clone.Result.HTTP.Metadata.Index = 1
	again, _ := s.OperationRecord(d)
	if !bytes.Equal(raw, again) {
		t.Fatal("inspection mutated original receipt")
	}
	text, ok := s.ReceiptJSON(d)
	if !ok || text != string(raw) {
		t.Fatal("clipboard record differs from complete export")
	}
}
func TestHTTPBodyPreviewBoundsUTF8AndBinaryWithoutFullBodyAllocation(t *testing.T) {
	body := append(bytes.Repeat([]byte("x"), previewLimit-1), []byte("中suffix")...)
	got := httpReceiptPreview(&contract.KVHTTPResult{Body: body})
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "\n…") || len(got) > previewLimit+4 || strings.Contains(got, "Base64") {
		t.Fatal("UTF-8 boundary was corrupted")
	}
	binary := []byte{0xff, 0, 128}
	if got = httpReceiptPreview(&contract.KVHTTPResult{Body: binary}); got != "Base64:\n"+base64.StdEncoding.EncodeToString(binary) {
		t.Fatal("binary preview changed original prefix")
	}
	small := bytes.Repeat([]byte("x"), previewLimit+100)
	large := bytes.Repeat([]byte("x"), 48<<20)
	allocSmall := testing.AllocsPerRun(3, func() { _ = httpReceiptPreview(&contract.KVHTTPResult{Body: small}) })
	allocLarge := testing.AllocsPerRun(3, func() { _ = httpReceiptPreview(&contract.KVHTTPResult{Body: large}) })
	if allocLarge > allocSmall+1 || httpReceiptPreview(&contract.KVHTTPResult{Body: small}) != httpReceiptPreview(&contract.KVHTTPResult{Body: large}) {
		t.Fatal("preview depended on entire body")
	}
}
func TestHTTPExportCapturesOriginalBodyIncludingCompleteEmptyAndPartial(t *testing.T) {
	for _, kind := range []string{"complete", "empty", "partial", "record"} {
		t.Run(kind, func(t *testing.T) {
			s, d, _ := httpTestState(t)
			d.last = httpTestReceipt(d)
			if kind == "empty" {
				d.last.Result.HTTP.Body = []byte{}
			}
			if kind == "partial" {
				d.last.Result.HTTP.EOF = false
				d.last.Result.HTTP.Acknowledged = false
				d.last.Result.HTTP.WriteOutcomeUnknown = true
				d.last.Result.WriteOutcomeUnknown = true
			}
			original := bytes.Clone(d.last.Result.HTTP.Body)
			var name string
			var exported []byte
			s.SetFiles(Files{SavePath: func(suggested string) (string, error) { name = suggested; return "original", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
				var b bytes.Buffer
				if err := write(&b); err != nil {
					return err
				}
				exported = b.Bytes()
				return nil
			}})
			s.ExportReceipt(d, kind != "record")
			d.last.Result.HTTP.Body = []byte("new receipt")
			consume(t, s, d)
			if kind == "record" {
				var record map[string]json.RawMessage
				if json.Unmarshal(exported, &record) != nil || record["http"] == nil || record["protobuf_json"] != nil || name != "operation.nyakv-receipt.json" {
					t.Fatal("export did not capture complete HTTP record")
				}
				return
			}
			if !bytes.Equal(exported, original) || kind == "partial" && name != "response.http-body.partial.bin" || kind != "partial" && name != "response.http-body.bin" {
				t.Fatal("original body export changed", name)
			}
		})
	}
}
func TestHTTPRequestFilesRejectForeignProductBeforeReplacingOriginalDraft(t *testing.T) {
	for _, target := range []string{"consul", "etcd"} {
		t.Run(target, func(t *testing.T) {
			s, d, _ := httpTestState(t)
			foreign := DefaultRequest()
			if target == "etcd" {
				s, d, _ = testState(t)
				foreign = contract.KVRequest{Method: contract.KVConsulHTTPRequestMethod, JSON: "unfinished {"}
			}
			original := d.Request()
			raw, err := EncodeRequest(foreign)
			if err != nil {
				t.Fatal(err)
			}
			s.SetFiles(Files{OpenPath: func() (string, error) { return "foreign.nyakv", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }})
			s.OpenRequest(d)
			consume(t, s, d)
			if d.Request() != original || d.path != "" || d.Error() == "" {
				t.Fatal("foreign file replaced original draft")
			}
		})
	}
	original := contract.KVRequest{Method: contract.KVConsulHTTPRequestMethod, JSON: "unfinished {"}
	raw, err := EncodeRequest(original)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeRequest(raw)
	if err != nil || restored != original {
		t.Fatal("HTTP unfinished draft cannot round-trip")
	}
}
func TestHTTPRecoveryRejectsChangedIdentityAndParksOriginalUnknown(t *testing.T) {
	s, d, p := httpTestState(t)
	d.needsRecovery = true
	original := httpTestReceipt(d)
	original.Result.Completed = false
	original.Result.WriteOutcomeUnknown = true
	original.Result.HTTP.WriteOutcomeUnknown = true
	original.Result.HTTP.Acknowledged = false
	fresh := &fakeLease{scope: d.scope.Clone()}
	fresh.scope.Generation++
	p.recover = func(contract.KVRequestLease) (contract.KVRecovery, error) {
		result := original.Result.Clone()
		return contract.KVRecovery{Lease: fresh, Unknown: &result}, nil
	}
	p.lease.scope.HTTP.Consistency = "stale"
	s.Recover(d)
	if d.job != nil || !d.needsRecovery {
		t.Fatal("changed identity reached original recovery")
	}
	p.lease.scope = d.scope.Clone()
	active := true
	s.deps.Active = func(*Document) bool { return active }
	s.Recover(d)
	job := d.job
	await(t, job.finished)
	active = false
	s.Consume()
	if d.job != job || d.unknown != nil {
		t.Fatal("parked recovery consumed original unknown")
	}
	active = true
	s.Consume()
	if d.needsRecovery || d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || fresh.executes.Load() != 0 || fresh.connects.Load() != 0 {
		t.Fatal("pure recovery lost original unknown or dispatched")
	}
	d.last = httpTestReceipt(d)
	if d.unknown.Result.HTTP.Acknowledged || !d.unknown.Result.WriteOutcomeUnknown {
		t.Fatal("later read promoted original unknown")
	}
	d.needsRecovery = true
	foreign := &fakeLease{scope: d.scope.Clone()}
	foreign.scope.HTTP.Endpoint = "http://foreign:8500"
	p.recover = func(contract.KVRequestLease) (contract.KVRecovery, error) {
		return contract.KVRecovery{Lease: foreign}, nil
	}
	s.Recover(d)
	consume(t, s, d)
	if !d.needsRecovery || !foreign.closed.Load() || d.lease != fresh {
		t.Fatal("returned foreign scope entered original document")
	}
}

func TestHTTPReceiptSmallWindowRetainsReadableOriginalBody(t *testing.T) {
	s, d, _ := httpTestState(t)
	d.last = httpTestReceipt(d)
	copy := d.last.Clone()
	d.unknown = &copy
	d.unknown.Result.WriteOutcomeUnknown = true
	d.unknown.Result.HTTP.Acknowledged = false
	d.unknown.Result.HTTP.CaptureError = "original body incomplete"
	d.selectedUnknown = true
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d) }, 720, 520)
	body, ok := tt.Find(locales.Text("KV 原生回执"))
	if !ok || body.H < 24 || body.Y+body.H > 520 {
		t.Fatal("small window lost original HTTP body", body)
	}
}

func TestHTTPConfirmationSummaryUsesPreparedNativeMethodAndPath(t *testing.T) {
	request := contract.KVRequest{Method: contract.KVConsulHTTPRequestMethod, JSON: `{"method":"PUT","path":"/v1/kv/original%2Fkey","body_base64":"AP8="}`}
	if requestConfirmationSummary(request) != "PUT /v1/kv/original%2Fkey" {
		t.Fatal("confirmation exposes an internal dispatch name")
	}
	if requestConfirmationSummary(DefaultRequest()) != DefaultRequest().Method {
		t.Fatal("original protobuf confirmation changed")
	}
}
