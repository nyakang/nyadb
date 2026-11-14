package kvworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func consulWorkspaceToolInput(t *testing.T, s *State, d *Document, kind contract.KVRequestToolKind, input contract.KVRequestToolInput) {
	t.Helper()
	last, unknown := d.last, d.unknown
	s.OpenRequestTool(d, kind)
	s.SetRequestToolInput(d, kind, input)
	if d.requestTools == nil || !d.requestTools.open {
		t.Fatal("original structured tool unavailable")
	}
	s.GenerateRequestTool(d)
	if d.requestTools.open || d.error != "" || d.Busy() || d.confirmation != nil || d.last != last || d.unknown != unknown {
		t.Fatal("generation dispatched or replaced original evidence", d.error)
	}
}

func consulWorkspaceRunTool(t *testing.T, s *State, d *Document, write bool) {
	t.Helper()
	s.Run(d)
	if write {
		if d.confirmation == nil || d.Busy() {
			t.Fatal("structured write bypassed separate original confirmation", d.error)
		}
		s.Confirm(d, false)
		if d.Busy() {
			t.Fatal("unchecked structured request dispatched")
		}
		s.Confirm(d, true)
		s.Confirm(d, true)
	}
	consume(t, s, d)
	if d.last == nil || d.last.Result.HTTP == nil || !d.last.Result.HTTP.Acknowledged || !d.last.Result.HTTP.Succeeded {
		t.Fatal("original tool HTTP request failed", d.error)
	}
}

func TestConsulNativePublicWorkspaceStructuredToolsOriginalRequestsBytesScopesAndLeave(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	s, d, m := consulWorkspaceState(t, p, secret)
	if len(d.RequestTools()) != 30 {
		t.Fatal("structured public tool set missing")
	}
	root := "native/workspace/tools/" + strings.ReplaceAll(t.Name(), "/", "-")
	key := root + "/first"
	value := []byte{0, 255, 128}
	index := "0"
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolPut, contract.KVToolRead, contract.KVToolList, contract.KVToolRaw, contract.KVToolDelete, contract.KVToolTransaction} {
		t.Run(string(kind), func(t *testing.T) {
			input := contract.KVRequestToolInput{Key: key}
			switch kind {
			case contract.KVToolPut:
				input.ValueBase64, input.Flags, input.CompareIndex = base64.StdEncoding.EncodeToString(value), "18446744073709551615", "0"
			case contract.KVToolList:
				input.Key, input.KeysOnly = root+"/", true
			case contract.KVToolDelete:
				input.CompareIndex = index
			case contract.KVToolTransaction:
				input = contract.KVRequestToolInput{TransactionJSON: ` [ { "KV": {"Verb":"cas","Key":` + strconv.Quote(root+"/txn") + `,"Value":"AP8=","Flags":0,"Index":0} }, {"KV":{"Verb":"get","Key":` + strconv.Quote(root+"/txn") + `}} ] `}
			}
			s.OpenRequestTool(d, kind)
			s.SetRequestToolInput(d, kind, input)
			consulWorkspaceScreenshot(t, s, d, "tool-form-"+string(kind))
			toolUI := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 720, 520)
			toolUI.Frame()
			if err := toolUI.Click(locales.Text("生成请求文本")); err != nil {
				t.Fatal(err)
			}
			if d.requestTools.open || d.Busy() || d.confirmation != nil || d.error != "" {
				t.Fatal("tool form generation dispatched", d.error)
			}
			consulWorkspaceScreenshot(t, s, d, "tool-generated-"+string(kind))
			write := kind == contract.KVToolPut || kind == contract.KVToolDelete || kind == contract.KVToolTransaction
			if kind == contract.KVToolPut {
				zero, max := uint64(0), uint64(math.MaxUint64)
				native, _ := consul.KeyPutRequest(key, value, &max, &zero)
				want, _ := consul.DocumentRequest(native)
				if d.Request() != want {
					t.Fatal("structured form differs from canonical original request")
				}
			}
			if kind == contract.KVToolTransaction {
				var envelope struct {
					Body []byte `json:"body_base64"`
				}
				if json.Unmarshal([]byte(d.Request().JSON), &envelope) != nil || string(envelope.Body) != input.TransactionJSON {
					t.Fatal("tool replaced original transaction bytes")
				}
			}
			consulWorkspaceRunTool(t, s, d, write)
			h := d.last.Result.HTTP
			switch kind {
			case contract.KVToolRead:
				if len(h.Entries) != 1 || h.Entries[0].Key != key || h.Entries[0].Flags != math.MaxUint64 || !bytes.Equal(h.Entries[0].Value, value) {
					t.Fatal("structured read changed original bytes/flags")
				}
				index = strconv.FormatUint(h.Entries[0].ModifyIndex, 10)
			case contract.KVToolRaw:
				if _, present := h.Request.Query["raw"]; !bytes.Equal(h.Body, value) || !present {
					t.Fatal("raw tool did not retain original HTTP body")
				}
			case contract.KVToolList:
				if h.Txn == nil || len(h.Txn.Results) != 1 || h.Txn.Results[0].Key != key || !bytes.Equal(h.Txn.Results[0].Value, value) || h.Request.Path != "/v1/txn" {
					t.Fatal("exact prefix tool cleaned identity or discarded native entries")
				}
			case contract.KVToolTransaction:
				if h.Txn == nil || !h.Txn.Committed || len(h.Txn.Results) < 1 {
					t.Fatal("native transaction observation missing")
				}
			}
			consulWorkspaceScreenshot(t, s, d, "tool-receipt-"+string(kind))
		})
	}
	// Empty bytes and explicit zero remain distinct from omitted flags/CAS.
	// A native false CAS does not become a successful or unknown write.
	emptyKey := root + "/empty"
	zeroInput := contract.KVRequestToolInput{Key: emptyKey, ValueBase64: "", Flags: "0", CompareIndex: "0"}
	consulWorkspaceToolInput(t, s, d, contract.KVToolPut, zeroInput)
	consulWorkspaceRunTool(t, s, d, true)
	consulWorkspaceToolInput(t, s, d, contract.KVToolRead, contract.KVRequestToolInput{Key: emptyKey})
	consulWorkspaceRunTool(t, s, d, false)
	if entries := d.last.Result.HTTP.Entries; len(entries) != 1 || !entries[0].ValuePresent || len(entries[0].Value) != 0 || entries[0].Flags != 0 {
		t.Fatal("empty bytes or zero flags changed in actual structured read")
	}
	consulWorkspaceToolInput(t, s, d, contract.KVToolPut, zeroInput)
	s.Run(d)
	s.Confirm(d, true)
	consume(t, s, d)
	if h := d.last.Result.HTTP; !h.Acknowledged || h.Succeeded || d.unknown != nil || string(h.Body) != "false" {
		t.Fatal("explicit zero CAS was omitted or native false was promoted")
	}
	// Conflicting transaction writes remain atomic and retain native errors.
	txnInput := contract.KVRequestToolInput{TransactionJSON: `[{"KV":{"Verb":"cas","Key":` + strconv.Quote(emptyKey) + `,"Value":"AQ==","Index":0}},{"KV":{"Verb":"set","Key":` + strconv.Quote(root+"/atomic-absent") + `,"Value":"Ag=="}}]`}
	consulWorkspaceToolInput(t, s, d, contract.KVToolTransaction, txnInput)
	s.Run(d)
	s.Confirm(d, true)
	consume(t, s, d)
	if h := d.last.Result.HTTP; h.Status != 409 || !h.Acknowledged || h.Succeeded || h.Txn == nil || len(h.Txn.Errors) == 0 || d.unknown != nil {
		t.Fatal("atomic tool transaction conflict lost native original errors")
	}
	consulWorkspaceToolInput(t, s, d, contract.KVToolTransaction, contract.KVRequestToolInput{TransactionJSON: `[{"KV":{"Verb":"check-not-exists","Key":` + strconv.Quote(root+"/atomic-absent") + `}}]`})
	consulWorkspaceRunTool(t, s, d, false)
	// Incompatible recursive/CAS inputs remain a draft and cannot authorize a
	// cleaned path or partial write. Explicit recursive deletion uses the same
	// ordinary confirmation after the user removes CAS.
	s.OpenRequestTool(d, contract.KVToolDelete)
	input := contract.KVRequestToolInput{Key: root + "/", Recursive: true, CompareIndex: "0"}
	s.SetRequestToolInput(d, contract.KVToolDelete, input)
	original := d.Request()
	s.GenerateRequestTool(d)
	if d.Request() != original || d.error == "" || !d.requestTools.open {
		t.Fatal("CAS plus recursive deletion was silently changed")
	}
	input.CompareIndex = ""
	s.SetRequestToolInput(d, contract.KVToolDelete, input)
	s.GenerateRequestTool(d)
	consulWorkspaceRunTool(t, s, d, true)
	// Read-only transactions use the ordinary read path despite HTTP PUT.
	consulWorkspaceToolInput(t, s, d, contract.KVToolTransaction, contract.KVRequestToolInput{TransactionJSON: `[{"KV":{"Verb":"check-not-exists","Key":` + strconv.Quote(key) + `}}]`})
	consulWorkspaceRunTool(t, s, d, false)
	// Park a completion and consume it only on original document activation.
	consulWorkspaceToolInput(t, s, d, contract.KVToolList, contract.KVRequestToolInput{Key: root, KeysOnly: true})
	previous := d.last
	s.Run(d)
	job := d.job
	active := false
	s.deps.Active = func(doc *Document) bool { return active && doc == d }
	await(t, job.finished)
	s.Consume()
	if d.last != previous || d.job != job {
		t.Fatal("parked tool result consumed by another activation")
	}
	active = true
	consume(t, s, d)
	// A pending form survives disconnect. Offline generation and explicit
	// recovery preserve receipts and never execute the generated request.
	s.OpenRequestTool(d, contract.KVToolPut)
	input = contract.KVRequestToolInput{Key: root + "/offline", ValueBase64: "", Flags: "0", CompareIndex: "0"}
	s.SetRequestToolInput(d, contract.KVToolPut, input)
	if !s.GuardLeave(d, nil) {
		t.Fatal("original ungenerated input bypassed leave decision")
	}
	s.DecideLeave(ui.Services{}, false)
	m.Disconnect(p.ID)
	s.DisconnectProfile(p.ID)
	previous = d.last
	s.GenerateRequestTool(d)
	if d.last != previous || d.Busy() || !d.NeedsRecovery() || d.requestTools.open {
		t.Fatal("offline tool generation executed or discarded evidence")
	}
	generated := d.Request()
	encoded, err := EncodeRequest(generated)
	decoded, decodeErr := DecodeRequest(encoded)
	if err != nil || decodeErr != nil || decoded != generated {
		t.Fatal("structured request file changed original text")
	}
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.Recover(d)
	consume(t, s, d)
	if d.Request() != generated || d.last == nil || !bytes.Equal(d.last.Result.HTTP.Body, previous.Result.HTTP.Body) || d.confirmation != nil || d.NeedsRecovery() {
		t.Fatal("tool recovery executed or changed original draft")
	}
	s.Close(d)
	if d.requestTools != nil || d.requestBuilder != nil || d.Editor != nil {
		t.Fatal("original tool close retained inputs")
	}
	if token := os.Getenv("NYADB_CONSUL_READ_TOKEN"); token != "" {
		p.ID++
		p.ConsulOptions.Prefix = "allowed/"
		limited, doc, _ := consulWorkspaceState(t, p, token)
		consulWorkspaceToolInput(t, limited, doc, contract.KVToolPut, contract.KVRequestToolInput{Key: "allowed/tools-denied", ValueBase64: "AP8="})
		limited.Run(doc)
		limited.Confirm(doc, true)
		consume(t, limited, doc)
		if doc.last == nil || doc.last.Result.HTTP.Status != 403 || !doc.last.Result.HTTP.Acknowledged || doc.unknown != nil {
			t.Fatal("structured tool bypassed original restricted authorization")
		}
	}
}
