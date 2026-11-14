package kvworkspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestHTTPACLReadInputPreservesNativePresenceAndRejectsUnsupportedCombinations(t *testing.T) {
	for _, resource := range []string{"token", "policy", "role", "auth-method", "binding-rule", "templated-policy"} {
		if _, err := (HTTPACLReadInput{Resource: resource, Lookup: "list"}).request(); err != nil {
			t.Fatal("native resource list rejected", resource)
		}
	}
	input := HTTPACLReadInput{Resource: "token", Lookup: "list", IncludePolicy: true, Policy: "", IncludeRole: true, Role: "original-role", IncludeAuthMethod: true, AuthMethod: "  exact %+ / 原值 ", IncludeServiceName: true, IncludeIndex: true, Index: "18446744073709551615", IncludeWait: true, Wait: "1ms"}
	r, err := input.request()
	if err != nil || r.Policy == nil || *r.Policy != "" || r.Role == nil || *r.Role != input.Role || r.AuthMethod == nil || *r.AuthMethod != input.AuthMethod || r.ServiceName == nil || *r.ServiceName != "" || r.Index == nil || *r.Index != math.MaxUint64 || r.Wait == nil || *r.Wait != "1ms" {
		t.Fatal("native membership/number presence was lost")
	}
	input.AuthMethod = "later"
	if *r.AuthMethod == input.AuthMethod {
		t.Fatal("request aliases input")
	}
	for _, bad := range []HTTPACLReadInput{
		{Resource: "policy", Lookup: "self"}, {Resource: "token", Lookup: "name", Identity: "token"}, {Resource: "auth-method", Lookup: "id", Identity: "method"},
		{Resource: "role", Lookup: "list", Identity: "unexpected"}, {Resource: "token", Lookup: "list", Expanded: true},
		{Resource: "policy", Lookup: "list", IncludePolicy: true}, {Resource: "role", Lookup: "list", IncludeRole: true},
		{Resource: "templated-policy", Lookup: "list", IncludeIndex: true, Index: "0"}, {Resource: "token", Lookup: "list", IncludeWait: true, Wait: "1ms"},
		{Resource: "token", Lookup: "list", IncludeIndex: true, Index: "18446744073709551616"}, {Resource: "token", Lookup: "list", IncludeIndex: true, Index: "+1"},
	} {
		if _, err := bad.request(); err == nil {
			t.Fatal("unsupported combination accepted")
		}
	}
}

func TestHTTPACLReadInputFileRetainsIncompleteDraftAndRejectsForeignFormats(t *testing.T) {
	input := HTTPACLReadInput{Resource: "token", Lookup: "id", Identity: "partial identity %", IncludeIndex: true, Index: "unfinished", IncludePolicy: true, Policy: "", Wait: "dormant"}
	raw, err := encodeHTTPACLReadInput(input)
	got, err2 := decodeHTTPACLReadInput(raw)
	if err != nil || err2 != nil || got != input {
		t.Fatal("input file normalized an incomplete draft")
	}
	for _, bad := range [][]byte{
		[]byte(`{"version":1,"kind":"consul-acl-read","input":null}`),
		bytes.Replace(raw, []byte(`"kind": "consul-acl-read"`), []byte(`"kind": "consul-original-acl-receipt"`), 1),
		bytes.Replace(raw, []byte(`"Identity": "partial identity %"`), []byte(`"Identity": null`), 1),
		bytes.Replace(raw, []byte(`"Identity": "partial identity %"`), []byte(`"Identity": "a", "Identity": "b"`), 1),
		bytes.Replace(raw, []byte(`"Resource": "token"`), []byte(`"resource": "token"`), 1),
		bytes.Replace(raw, []byte(`"Resource": "token"`), []byte(`"Resource": "token", "SecretID": "never-a-credential-file"`), 1),
	} {
		if _, err := decodeHTTPACLReadInput(bad); err == nil {
			t.Fatal("foreign, duplicate, null or unknown input fields accepted")
		}
	}
}

func TestHTTPACLReadFormExplicitRunPreservesGenericDraftAndUnknownBarrier(t *testing.T) {
	s, d, l, _ := aclWorkspace(t)
	before := d.Request()
	d.unknown = &Receipt{Scope: d.scope.Clone(), Result: contract.KVResult{WriteOutcomeUnknown: true}}
	unknown := d.unknown
	s.OpenHTTPACLReadForm(d)
	s.SetHTTPACLReadInput(d, HTTPACLReadInput{Resource: "token", Lookup: "name", Identity: "invalid"})
	s.RunHTTPACLReadForm(d)
	if d.Busy() || l.plans.Load() != 0 || !d.Dirty() {
		t.Fatal("invalid form reached original executor or lost edits")
	}
	s.SetHTTPACLReadInput(d, defaultHTTPACLReadInput())
	s.RunHTTPACLReadForm(d)
	if !d.Busy() || d.httpACL.form.open {
		t.Fatal("explicit original read did not start")
	}
	active := false
	s.deps.Active = func(*Document) bool { return active }
	await(t, d.job.finished)
	s.Consume()
	if d.httpACLContent() || !d.Busy() {
		t.Fatal("parked form read was consumed")
	}
	active = true
	consume(t, s, d)
	if !d.httpACLContent() || d.httpACL.browser == nil || !d.httpACL.browser.open || d.httpACL.revealed != "" || d.Request() != before || d.unknown != unknown || !d.hasUnknownWrites() || l.reads.Load() != 1 || l.executes.Load() != 0 {
		t.Fatal("explicit form read changed original evidence or write barrier")
	}
	s.OpenHTTPACLReadForm(d)
	input := HTTPACLReadInput{Resource: "role", Lookup: "name", Identity: "unfinished name"}
	s.SetHTTPACLReadInput(d, input)
	s.DisconnectProfile(d.profileID)
	if d.httpACL.form.input != input || !d.Dirty() {
		t.Fatal("disconnect lost original form draft")
	}
	s.RunHTTPACLReadForm(d)
	if d.Busy() || l.reads.Load() != 1 {
		t.Fatal("disconnected form replayed read")
	}
}

func TestHTTPACLReadFormFilesCaptureOriginalInputAndParkBeforePublication(t *testing.T) {
	for _, action := range []string{"save", "open", "cancel-open", "stale-open"} {
		t.Run(action, func(t *testing.T) {
			s, d, l, _ := aclWorkspace(t)
			s.OpenHTTPACLReadForm(d)
			before := HTTPACLReadInput{Resource: "policy", Lookup: "name", Identity: "  original %+ / 原值  "}
			s.SetHTTPACLReadInput(d, before)
			loaded := HTTPACLReadInput{Resource: "role", Lookup: "id", Identity: "incomplete"}
			raw, _ := encodeHTTPACLReadInput(loaded)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var written bytes.Buffer
			reads := 0
			picker := func() (string, error) { close(entered); <-release; return "owned-input.json", nil }
			s.SetFiles(Files{SavePath: func(string) (string, error) { return picker() }, OpenPath: picker, OpenReader: func(string) (io.ReadCloser, error) { reads++; return io.NopCloser(bytes.NewReader(raw)), nil }, WriteStream: func(string, func(io.Writer) error) error { return nil }})
			s.deps.Files.WriteStream = func(_ string, write func(io.Writer) error) error { return write(&written) }
			kind := "open-acl-input"
			if action == "save" {
				kind = "save-acl-input"
			}
			s.httpACLFormFile(d, kind, ui.Services{})
			await(t, entered)
			if action == "cancel-open" {
				s.CancelFile(d)
			}
			if action == "save" || action == "stale-open" {
				d.httpACL.form.input.Identity = "later edit"
				d.httpACL.form.serial++
			}
			once.Do(func() { close(release) })
			active := false
			s.deps.Active = func(*Document) bool { return active }
			await(t, d.io.finished)
			s.Consume()
			if !d.FileBusy() {
				t.Fatal("parked input file consumed before original activation")
			}
			active = true
			consume(t, s, d)
			switch action {
			case "save":
				got, err := decodeHTTPACLReadInput(written.Bytes())
				if err != nil || got != before || d.httpACL.form.saved != before || !d.Dirty() {
					t.Fatal("saved input used later form or erased unsaved edits")
				}
			case "open":
				if d.httpACL.form.input != loaded || d.Dirty() {
					t.Fatal("loaded input was not retained independently")
				}
			case "cancel-open":
				if reads != 0 || d.httpACL.form.input != before {
					t.Fatal("canceled picker read or replaced draft")
				}
			case "stale-open":
				if d.httpACL.form.input.Identity != "later edit" || d.Error() == "" {
					t.Fatal("late input load overwrote later edits")
				}
			}
			if l.plans.Load() != 0 || l.reads.Load() != 0 || l.executes.Load() != 0 {
				t.Fatal("input file operation dispatched")
			}
		})
	}
}

func TestHTTPACLReadFormCodecHasNoConnectionOrReceiptFields(t *testing.T) {
	raw, err := encodeHTTPACLReadInput(defaultHTTPACLReadInput())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 {
		t.Fatal("invalid input envelope")
	}
	for _, forbidden := range []string{"SecretID", "ProfileID", "Generation", "Receipt", "Token"} {
		if strings.Contains(string(raw), fmt.Sprintf("%q", forbidden)) {
			t.Fatal("input file contains non-draft state")
		}
	}
}
