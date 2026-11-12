package kvworkspace

import (
	"bytes"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConsulNativePublicSessionToolsRenewalUnknownBarrierAndOriginalReceiptSelection(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	id := consulSessionToolCreate(t, writer, t.Name())
	proxy, calls, processed := consulHTTPSessionRenewalFault(t, p, "partial")
	s, d, m := consulWorkspaceState(t, proxy, secret)
	s.deps.HTTPSessionRenewalProviderFor = m.KVHTTPSessionRenewal
	d.httpSessionRenewalAvailable = true
	s.deps.HTTPWatchProviderFor = m.KVHTTPWatch
	d.httpWatchAvailable = true
	consulHTTPSessionRenewalInput(s, d, id, "100ms", 2, 16<<20)
	s.PrepareHTTPSessionRenewal(d)
	s.ConfirmHTTPSessionRenewal(d, true)
	await(t, processed)
	consume(t, s, d)
	if !d.hasUnknownWrites() || calls.Load() != 1 {
		t.Fatal("original unknown renewal missing")
	}
	original, err := s.HTTPSessionRenewalRecord(d)
	if err != nil {
		t.Fatal(err, d.Error())
	}
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionInfo, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}})
	s.Run(d)
	consume(t, s, d)
	if d.last == nil || !d.last.Result.HTTP.Acknowledged || !d.hasUnknownWrites() {
		t.Fatal("Session read erased original renewal barrier", d.Error())
	}
	// Select the original renewal archive without changing the current HTTP
	// editor method; generating a second info request must show its own result.
	tt := workspaceTester(s, d, 720, 520)
	tt.Frame()
	workspaceClick(t, tt, "HTTP Session 续租记录")
	if !d.selectedHTTPSessionRenewal || d.method != contract.KVConsulHTTPRequestMethod {
		t.Fatal("receipt selection unexpectedly changed editor method")
	}
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionInfo, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}})
	if d.selectedHTTPSessionRenewal {
		t.Fatal("same-method generated request kept old renewal view")
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
		input := contract.KVRequestToolInput{}
		if kind != contract.KVToolHTTPSessionCreate {
			input.HTTPSession.ID = id
		}
		if kind == contract.KVToolHTTPAcquire || kind == contract.KVToolHTTPRelease {
			input.Key = "native/workspace/renewal-tool-barrier"
		}
		consulWorkspaceToolInput(t, s, d, kind, input)
		s.Run(d)
		if d.confirmation != nil || d.Busy() || calls.Load() != 1 || !d.hasUnknownWrites() {
			t.Fatal("new generated Session tool bypassed renewal unknown barrier", kind)
		}
	}
	current, err := s.HTTPSessionRenewalRecord(d)
	if err != nil || !bytes.Equal(original, current) {
		t.Fatal("tool generation changed original renewal archive")
	}
	// The same selection transition must retain an independent Watch archive.
	consulHTTPWatchInput(t, s, d, "native/workspace/renewal-tool-barrier", "native/workspace", 1, 16<<20)
	s.StartHTTPWatch(d)
	consume(t, s, d)
	watch, err := s.HTTPWatchRecord(d)
	if err != nil {
		t.Fatal(err, d.Error())
	}
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionInfo, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}})
	s.Run(d)
	consume(t, s, d)
	tt.Frame()
	workspaceClick(t, tt, "HTTP Watch 观察")
	if !d.selectedHTTPWatch || d.method != contract.KVConsulHTTPRequestMethod {
		t.Fatal("Watch selection unexpectedly changed editor method")
	}
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionInfo, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}})
	if d.selectedHTTPWatch {
		t.Fatal("same-method generated request kept old Watch view")
	}
	got, err := s.HTTPWatchRecord(d)
	if err != nil || !bytes.Equal(watch, got) || !d.hasUnknownWrites() {
		t.Fatal("tool generation changed original Watch or unknown evidence")
	}
	consulSessionToolScreenshot(t, s, d, "retained-records")
}
