package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const sessionKeysTestID = "11111111-2222-4333-8444-555555555555"

type sessionKeysTestLease struct {
	*fakeLease
	reads   atomic.Int32
	observe func(context.Context, contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error)
}

func (l *sessionKeysTestLease) ObserveHTTPSessionKeys(ctx context.Context, r contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
	l.reads.Add(1)
	return l.observe(ctx, r)
}

func sessionKeysTestObservation(scope contract.KVScope, request contract.KVHTTPSessionKeysRequest, count int) contract.KVHTTPSessionKeysObservation {
	h := &contract.KVHTTPResult{Protocol: "HTTP/1.1", Kind: "kv-read", Status: 200, Attempted: true, Ended: true, Captured: true, EOF: true, Acknowledged: true, Succeeded: true, Request: contract.KVHTTPRequest{Method: "GET", Path: "/v1/kv/", Query: map[string][]string{"recurse": {""}}}}
	r := contract.KVResult{Request: contract.KVRequest{Method: contract.KVConsulHTTPRequestMethod, JSON: `{"method":"GET","path":"/v1/kv/","query":{"recurse":[""]}}`}, Attempted: true, Completed: true, HTTP: h}
	rows := make([]map[string]any, count)
	matches := make([]int, count)
	for i := range rows {
		key := fmt.Sprintf("outside/中文 %03d", i)
		h.Entries = append(h.Entries, contract.KVHTTPEntry{Key: key, Value: []byte{0, 255}, ValuePresent: true, Flags: math.MaxUint64, ModifyIndex: 9007199254740993, LockIndex: 1, Session: request.ID, PresentFields: []string{"Key", "Value", "Flags", "Session"}})
		rows[i] = map[string]any{"Key": key, "Value": "AP8=", "Flags": uint64(math.MaxUint64), "Session": request.ID, "future": map[string]any{"unsigned": uint64(math.MaxUint64), "null": nil}}
		matches[i] = i
	}
	h.Body, _ = json.Marshal(rows)
	return contract.KVHTTPSessionKeysObservation{Scope: scope.Clone(), Request: request, Root: r, Matches: matches, Bytes: 4096, ProjectionComplete: true, Complete: true}
}

func sessionKeysTestState(t *testing.T) (*State, *Document, *sessionKeysTestLease) {
	t.Helper()
	s, d, p := httpTestState(t)
	d.scope.HTTP.Protocol = "consul-http"
	p.lease.scope = d.scope.Clone()
	l := &sessionKeysTestLease{fakeLease: p.lease}
	d.lease = l
	scope := d.scope.Clone()
	l.observe = func(_ context.Context, r contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
		return sessionKeysTestObservation(scope, r, 53), nil
	}
	d.last = sessionBrowserReceipt([]contract.KVHTTPSession{{ID: sessionKeysTestID, PresentFields: []string{"ID"}}, {ID: "22222222-2222-4333-8444-555555555555", PresentFields: []string{"ID"}}})
	d.last.Scope = d.scope.Clone()
	d.httpSessionBrowser = &httpSessionBrowserState{}
	d.httpSessionBrowser.bind(d.last)
	d.httpSessionBrowser.selectRow(d.last, 0, 0)
	return s, d, l
}

func TestHTTPSessionHeldKeysCapturesOriginalSelectionAndParksCompleteUnknownIndependentEvidence(t *testing.T) {
	s, d, l := sessionKeysTestState(t)
	started, release := make(chan struct{}), make(chan struct{})
	scope := d.scope.Clone()
	l.observe = func(_ context.Context, r contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
		close(started)
		<-release
		return sessionKeysTestObservation(scope, r, 53), nil
	}
	unknown := httpTestReceipt(d)
	unknown.Result.WriteOutcomeUnknown = true
	unknown.Result.HTTP.WriteOutcomeUnknown = true
	d.unknown = unknown
	originalUnknown := unknown.Clone()
	originalRequest, originalReceipt := d.Request(), d.last
	s.ObserveSelectedHTTPSessionKeys(d)
	finished := d.job.finished
	await(t, started)
	d.httpSessionBrowser.selectRow(d.last, 0, 1)
	active := false
	s.deps.Active = func(*Document) bool { return active }
	close(release)
	await(t, finished)
	s.Consume()
	if d.job == nil || d.last != originalReceipt || l.reads.Load() != 1 {
		t.Fatal("parked completion consumed outside original active document")
	}
	active = true
	s.Consume()
	if d.Busy() || !d.httpSessionKeys || d.last.HTTPSessionKeys.Request.ID != sessionKeysTestID || len(d.last.HTTPSessionKeys.Matches) != 53 || d.Request() != originalRequest || !reflect.DeepEqual(*d.unknown, originalUnknown) || !d.hasUnknownWrites() {
		t.Fatal("late original target changed with selection, rewrote editor or promoted unknown destroy")
	}
	copy := d.last.Clone()
	copy.HTTPSessionKeys.Matches[0] = -1
	copy.Result.HTTP.Entries[0].Value[0] = 9
	if d.last.HTTPSessionKeys.Matches[0] != 0 || d.last.Result.HTTP.Entries[0].Value[0] != 0 {
		t.Fatal("receipt clone aliases held-key indices or bytes")
	}
	raw, err := s.OperationRecord(d)
	if err != nil || !bytes.Contains(raw, []byte(`"session_held_keys"`)) || !bytes.Contains(raw, []byte(sessionKeysTestID)) || !bytes.Contains(raw, []byte(`"Flags": "18446744073709551615"`)) {
		t.Fatal("original complete receipt lost target or unsigned held-key values", err)
	}
	var record struct {
		HTTP struct{ Body []byte }
		Keys *HTTPSessionKeysReceipt `json:"session_held_keys"`
	}
	if json.Unmarshal(raw, &record) != nil || record.Keys == nil || !bytes.Equal(record.HTTP.Body, d.last.Result.HTTP.Body) || len(record.Keys.Matches) != 53 {
		t.Fatal("complete root body or original projection metadata changed in export")
	}
	b := &httpSessionKeysBrowserState{}
	b.bind(d.last)
	if b.pageCount() != 2 || len(b.pageIndices()) != 50 || !b.movePage(d.last, 1) || len(b.pageIndices()) != 3 || !b.selectRow(d.last, 1, 2) {
		t.Fatal("local original-order held-key paging failed")
	}
	selected, ok := b.selected(d.last)
	if !ok || selected.Key != "outside/中文 052" || selected.Flags != math.MaxUint64 {
		t.Fatal("original qualified key selection changed")
	}
	preview := originalHTTPSessionKeyPreview(d.last, selected.Key)
	if !bytes.Contains([]byte(preview), []byte(`"future"`)) || !bytes.Contains([]byte(preview), []byte("18446744073709551615")) {
		t.Fatal("raw key details lost unknown fields or unsigned precision")
	}
	b.query = "中文 000"
	if _, ok := b.selected(d.last); ok {
		t.Fatal("filter edit retained stale key selection")
	}
	b.bind(d.last)
	if b.page != 0 || len(b.indices) != 1 || b.selectedKey != "" || l.reads.Load() != 1 {
		t.Fatal("local filtering performed I/O or retained stale selection")
	}
	if !reflect.DeepEqual(*d.unknown, originalUnknown) {
		t.Fatal("browsing changed original unknown evidence")
	}
}

func TestHTTPSessionHeldKeysRejectsForeignPartialAndACLCompleteClaims(t *testing.T) {
	for _, kind := range []string{"foreign-scope", "foreign-generation", "foreign-target", "partial-matches", "bounds", "filtered-complete", "filtered-visible", "projection-unavailable"} {
		t.Run(kind, func(t *testing.T) {
			s, d, l := sessionKeysTestState(t)
			original := d.last
			scope := d.scope.Clone()
			l.observe = func(_ context.Context, r contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
				o := sessionKeysTestObservation(scope, r, 2)
				switch kind {
				case "foreign-scope":
					o.Scope.HTTP.Endpoint = "http://foreign"
				case "foreign-generation":
					o.Scope.Generation++
				case "foreign-target":
					o.Request.ID = "22222222-2222-4333-8444-555555555555"
				case "partial-matches":
					o.Matches = []int{0}
				case "bounds":
					o.Bytes = o.Request.MaxBytes + 1
				case "filtered-complete", "filtered-visible":
					o.Root.HTTP.Metadata.ACLFiltered = new(true)
					o.Complete = kind == "filtered-complete"
				case "projection-unavailable":
					o.ProjectionComplete = false
					o.Complete = false
					o.Matches = nil
				}
				return o, nil
			}
			s.ObserveSelectedHTTPSessionKeys(d)
			consume(t, s, d)
			foreign := kind == "foreign-scope" || kind == "foreign-generation" || kind == "foreign-target"
			if foreign {
				if d.last != original || d.httpSessionKeys || d.error == "" {
					t.Fatal("foreign observation replaced original cache or became current keys")
				}
			} else if kind == "filtered-visible" {
				if !d.httpSessionKeys || !d.last.HTTPSessionKeys.ProjectionComplete || d.last.HTTPSessionKeys.Complete {
					t.Fatal("visible filtered observation lost its incomplete boundary")
				}
			} else if d.last == original || d.last.HTTPSessionKeys.ProjectionComplete || d.last.HTTPSessionKeys.Matches != nil || d.httpSessionKeys || !d.last.Result.HTTP.Acknowledged || len(d.last.Result.HTTP.Body) == 0 {
				t.Fatal("partial optional projection lost raw evidence or acquired complete browser state")
			}
		})
	}
}

func TestHTTPSessionHeldKeysCancellationWaitsForOriginalExecutorAndCloseReleasesReceipts(t *testing.T) {
	s, d, l := sessionKeysTestState(t)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	scope := d.scope.Clone()
	l.observe = func(ctx context.Context, r contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		o := sessionKeysTestObservation(scope, r, 0)
		o.Root.HTTP.Captured = false
		o.Root.HTTP.EOF = false
		o.ProjectionComplete = false
		o.Complete = false
		o.Matches = nil
		return o, ctx.Err()
	}
	s.ObserveSelectedHTTPSessionKeys(d)
	await(t, started)
	s.RequestCancel(d)
	await(t, canceled)
	s.Consume()
	if !d.Busy() || l.reads.Load() != 1 {
		t.Fatal("cancel guessed executor acknowledgement or replayed root read")
	}
	close(release)
	consume(t, s, d)
	if d.Busy() || d.last.HTTPSessionKeys.ProjectionComplete || d.last.Error == "" {
		t.Fatal("canceled original executor did not settle")
	}
	s.Close(d)
	if d.last != nil || d.unknown != nil || d.lease != nil || d.httpSessionKeysBrowser != nil || d.httpSessionKeys || !l.closed.Load() {
		t.Fatal("close retained original held-key caches or lease")
	}
	// A close while reading waits for the same executor and disposes its private
	// buffered completion rather than routing it to a different document.
	s2, d2, l2 := sessionKeysTestState(t)
	started2, ack := make(chan struct{}), make(chan struct{})
	l2.observe = func(ctx context.Context, r contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
		close(started2)
		<-ctx.Done()
		<-ack
		return contract.KVHTTPSessionKeysObservation{Scope: scope, Request: r}, errors.New("closed original executor")
	}
	s2.ObserveSelectedHTTPSessionKeys(d2)
	await(t, started2)
	go func() { time.Sleep(20 * time.Millisecond); close(ack) }()
	s2.Close(d2)
	if d2.job != nil || d2.last != nil || d2.lease != nil || l2.reads.Load() != 1 {
		t.Fatal("close retained private completion or replayed original root read")
	}
}

func TestHTTPSessionHeldKeysEmptyArrayPreservesWhitespaceAndRejectsNullOrMalformedEvidence(t *testing.T) {
	scope := contract.KVScope{HTTP: contract.KVHTTPIdentity{Protocol: "consul-http"}}
	request := contract.KVHTTPSessionKeysRequest{ID: sessionKeysTestID, MaxRows: 100, MaxBytes: 1 << 20}
	for _, body := range []string{`[]`, `[ ]`, " \n[\t\r\n ] \n", `null`, `{}`, `[null]`, `[] []`, `[`} {
		o := sessionKeysTestObservation(scope, request, 0)
		o.Root.HTTP.Body = []byte(body)
		valid := body == `[]` || body == `[ ]` || body == " \n[\t\r\n ] \n"
		if validHTTPSessionKeysProjection(o) != valid {
			t.Fatalf("empty array boundary changed for %q", body)
		}
	}
}
