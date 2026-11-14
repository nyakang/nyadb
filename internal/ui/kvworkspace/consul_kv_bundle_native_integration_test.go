package kvworkspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/consulspec"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
	"reflect"
	"strings"
	"testing"
)

func TestHTTPKVNativeBundleIndependentTransactionsSearchExportAndPureRecovery(t *testing.T) {
	profile, secret := consulWorkspaceProfile(t)
	s, d, m := consulWorkspaceState(t, profile, secret)
	s.OpenHTTPKVTools(d)
	x := d.httpKVTools
	root := "native/bundle-complete/" + rand.Text() + "/"
	b := consulspec.Bundle{Format: "nyadb-consul-kv-bundle", Version: 1, Prefix: root, ScopeKind: "prefix", Entries: []consulspec.BundleEntry{}}
	for i := range 65 {
		b.Entries = append(b.Entries, consulspec.BundleEntry{Key: fmt.Sprintf("%skey%02d", root, i), Value: base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("original value %d", i))), Flags: "18446744073709551615", CreateIndex: "0", ModifyIndex: "0"})
	}
	x.bundle = &b
	s.PreviewHTTPKVImport(d)
	consume(t, s, d)
	if x.preview == nil || !x.preview.CanApply || len(x.preview.Requests) != 2 || len(x.observations) != 65 {
		t.Fatal("native complete target preview lost", d.Error())
	}
	for batch := 0; batch < 2; batch++ {
		s.PrepareHTTPKVImportBatch(d)
		if x.plan == nil || d.Busy() {
			t.Fatal("batch confirmation missing", d.Error())
		}
		s.ConfirmHTTPKVImportBatch(d)
		if d.Busy() {
			t.Fatal("unchecked batch executed")
		}
		x.checked = true
		s.ConfirmHTTPKVImportBatch(d)
		consume(t, s, d)
		if x.batch != batch+1 || len(x.batches) != batch+1 || !x.batches[batch].Result.Completed {
			t.Fatal("native batch result lost", d.Error())
		}
	}
	x.prefix = root
	x.prefixMode = true
	x.query = "ORIGINAL"
	x.keys = false
	x.values = true
	x.limit = "100"
	s.RunHTTPKVSearch(d, false)
	consume(t, s, d)
	if x.search == nil || x.search.Scanned != 65 || x.search.Matched != 65 || len(x.search.Matches) != 65 || x.search.Matches[0].Entry.Flags != ^uint64(0) {
		t.Fatal("native search lost full response or unsigned flags", d.Error())
	}
	s.RunHTTPKVSearch(d, true)
	consume(t, s, d)
	if x.bundle == nil || len(x.bundle.Entries) != 65 || consulspec.ValidateBundle(*x.bundle) != nil {
		t.Fatal("native Bundle export incomplete", d.Error())
	}
	s.OpenHTTPManagement(d)
	s.PrepareHTTPManagement(d)
	consume(t, s, d)
	if d.httpManagement.last == nil || !d.httpManagement.last.Receipt.Completed {
		t.Fatal("original management read missing", d.Error())
	}
	managementOriginal := d.httpManagement.last.Clone()
	s.RunHTTPCapabilities(d)
	consume(t, s, d)
	if len(d.httpManagement.capabilities) != len(capabilityProbes) {
		t.Fatalf("capability probes incomplete: %d, %s", len(d.httpManagement.capabilities), d.Error())
	}
	original := x.search.Clone()
	batches := append([]Receipt(nil), x.batches...)
	scope := d.Scope()
	request := d.Request()
	if err := m.Disconnect(profile.ID); err != nil {
		t.Fatal(err)
	}
	s.DisconnectProfile(profile.ID)
	if !d.NeedsRecovery() || d.Request() != request || !reflect.DeepEqual(original, *x.search) {
		t.Fatal("disconnect changed original evidence")
	}
	if err := m.Connect(context.Background(), profile, secret); err != nil {
		t.Fatal(err)
	}
	s.Recover(d)
	consume(t, s, d)
	if d.NeedsRecovery() || d.Scope().Generation <= scope.Generation || !reflect.DeepEqual(original, *x.search) || !reflect.DeepEqual(batches, x.batches) || !reflect.DeepEqual(managementOriginal, *d.httpManagement.last) {
		t.Fatal("pure recovery changed archive", d.Error())
	}
	provider, _ := m.KVRequests(profile.ID)
	cleanup, _ := provider.ReserveKV(999)
	defer cleanup.Close()
	r, err := consul.TxnRequest([]consul.TxnOperation{{KV: &consul.TxnKV{Verb: "delete-tree", Key: root}}})
	if err != nil {
		t.Fatal(err)
	}
	authored, err := consul.DocumentRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	p, err := cleanup.Prepare(authored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cleanup.Execute(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}
func TestManagementInputRejectsNullFlagsAndPrivateFileEnvelope(t *testing.T) {
	ops := []contract.KVHTTPManagementOperation{{Name: "agent-members", Path: "/v1/agent/members", Options: []string{"wan"}}}
	for _, input := range []string{`null`, `{"wan":[null]}`, `{"wan":null}`, `{"wan":[false]}`, `{"wan":["","true"]}`} {
		if _, err := parseManagementInput(httpManagementDraft{Operation: "agent-members", Options: input}, ops); err == nil {
			t.Fatal("null option gained native authority", input)
		}
	}
	if _, err := parseManagementInput(httpManagementDraft{Operation: "agent-members", Options: `{"wan":[""]}`}, ops); err != nil {
		t.Fatal("explicit native flag rejected", err)
	}
	envelope := []byte(`{"Version":1,"Kind":"consul-management-input","Input":{"Operation":"agent-members","Kind":"","Identity":"","Options":"{}","Body":""}}`)
	if !validManagementEnvelope(envelope) {
		t.Fatal("valid private input rejected")
	}
	for _, bad := range [][]byte{bytes.Replace(envelope, []byte(`"Body":""`), []byte(`"Body":null`), 1), bytes.Replace(envelope, []byte(`"Version":1`), []byte(`"Version":1,"Unknown":true`), 1), []byte(strings.Replace(string(envelope), `"Input":{`, `"Input":{"Future":"x",`, 1))} {
		if validManagementEnvelope(bad) {
			t.Fatal("foreign private file accepted")
		}
	}
}
