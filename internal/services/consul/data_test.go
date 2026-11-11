package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataFixture struct {
	mu        sync.Mutex
	entries   []map[string]any
	reads     atomic.Int32
	httpIndex atomic.Uint64
	intercept func(http.ResponseWriter, *http.Request) bool
}

func (f *dataFixture) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Consul-Index", "18446744073709551615")
	if index := f.httpIndex.Load(); index != 0 {
		w.Header().Set("X-Consul-Index", strconv.FormatUint(index, 10))
	}
	w.Header().Set("X-Consul-Results-Filtered-By-Acls", "true")
	switch r.URL.Path {
	case "/v1/agent/self":
		w.Write([]byte(`{"Config":{"Version":"2.0.2","NodeID":"` + testAgentID + `","NodeName":"original","Datacenter":"dc1"}}`))
		return
	case "/v1/status/leader":
		w.Write([]byte(`"127.0.0.1:8300"`))
		return
	}
	if r.Method == "GET" && r.URL.Query().Has("keys") {
		w.Write([]byte("[]"))
		return
	}
	f.reads.Add(1)
	if f.intercept != nil && f.intercept(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	entries := []map[string]any{}
	if r.URL.Path == "/v1/txn" {
		var operations []TxnOperation
		if json.NewDecoder(r.Body).Decode(&operations) != nil || len(operations) != 1 {
			w.WriteHeader(400)
			return
		}
		for _, entry := range f.entries {
			key := entry["Key"].(string)
			if operations[0].KV.Verb == "get-tree" && strings.HasPrefix(key, operations[0].KV.Key) || operations[0].KV.Verb == "get" && key == operations[0].KV.Key {
				entries = append(entries, map[string]any{"KV": entry})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"Results": entries, "Errors": []any{}})
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/v1/kv/")
	for _, entry := range f.entries {
		if r.URL.Query().Has("recurse") && strings.HasPrefix(entry["Key"].(string), key) || entry["Key"] == key {
			entries = append(entries, entry)
		}
	}
	json.NewEncoder(w).Encode(entries)
}

func dataTestSession(t *testing.T, prefix string, f *dataFixture) *Session {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(server.Close)
	s, err := OpenNativeSession(context.Background(), 11, Config{Endpoint: server.URL, Prefix: prefix, Datacenter: "dc1", Consistency: "consistent"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func dataTestLease(t *testing.T, s *Session) *dataLease {
	t.Helper()
	raw, err := s.reserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys"))
	if err != nil {
		t.Fatal(err)
	}
	l := raw.(*dataLease)
	t.Cleanup(func() { l.Close() })
	return l
}

func dataTestEntry(key string, value []byte) map[string]any {
	return map[string]any{"Key": key, "Value": value, "Flags": uint64(math.MaxUint64), "CreateIndex": uint64(9007199254740993), "ModifyIndex": uint64(math.MaxUint64), "LockIndex": uint64(0), "Session": nil}
}

func dataTestPage(t *testing.T, l *dataLease, options contract.DataOptions) *contract.DataPage {
	t.Helper()
	p, err := l.page(context.Background(), l.object.ProfileID, l.object.Catalog, l.object.Name, options)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDataReservationCombinedLimitAndOriginalCloseWaitsWithoutIO(t *testing.T) {
	f := &dataFixture{}
	s := dataTestSession(t, "scope", f)
	before := f.reads.Load()
	for i := 0; i < 16; i++ {
		if _, err := s.ReserveKV(uint64(i + 1)); err != nil {
			t.Fatal(err)
		}
		dataTestLease(t, s)
	}
	if _, err := s.ReserveKV(99); !errors.Is(err, contract.ErrKVLeaseLimit) {
		t.Fatal("KV exceeded combined logical limit", err)
	}
	if _, err := s.reserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys")); !errors.Is(err, contract.ErrKVLeaseLimit) {
		t.Fatal("data exceeded combined logical limit", err)
	}
	if f.reads.Load() != before {
		t.Fatal("reservation made HTTP requests")
	}
	var l *dataLease
	for original := range s.dataLeases {
		l = original
		break
	}
	ctx, done, err := l.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel original data worker")
	}
	select {
	case <-closed:
		t.Fatal("close did not wait for original worker acknowledgment")
	default:
	}
	done()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not finish")
	}
	if len(s.dataLeases) != 0 || len(s.leases) != 0 || l.key != ([32]byte{}) {
		t.Fatal("close retained logical resources or signing key")
	}
}

func TestDataLivePagesOwnBoundariesAndPreserveOriginalBytesUnsignedNullAndReceipts(t *testing.T) {
	value := bytes.Repeat([]byte{0, 255}, 1024)
	f := &dataFixture{entries: []map[string]any{dataTestEntry("scope/d", []byte{4}), dataTestEntry("scope/b", value), dataTestEntry("scope/a", []byte{1}), dataTestEntry("foreign", []byte{9})}}
	s := dataTestSession(t, "scope", f)
	l := dataTestLease(t, s)
	p := dataTestPage(t, l, contract.DataOptions{Limit: 2})
	if !p.LiveRead || p.PagingMode != "cursor" || !p.StableOrder || !p.HasMore || p.Editable == nil || !*p.Editable || p.Rows[0][0].Value.Text != "scope/a" || p.Rows[1][0].Value.Text != "scope/b" || p.Rows[1][2].Value.Text != "18446744073709551615" || p.Rows[1][3].Value.Text != "9007199254740993" || p.Rows[1][6].Value.Kind != contract.Null {
		t.Fatal("live original rows or uint64/session null changed")
	}
	if !p.Rows[1][1].Truncated || len(p.Rows[1][1].Value.Bytes) != 1024 || len(p.RowSnapshots[1].Original) != 0 {
		t.Fatal("page values or snapshots exceeded bounded preview")
	}
	var observed dataObservation
	if err := l.decode([]byte(p.RowSnapshots[1].Metadata), &observed); err != nil || observed.ModifyIndex != "18446744073709551615" || observed.HTTPIndex == nil || *observed.HTTPIndex != "18446744073709551615" || !observed.SessionNull || observed.ACLFiltered == nil || !*observed.ACLFiltered {
		t.Fatal("original observation metadata changed", err)
	}
	original := l.scopes[dataPageScope].LastResult()
	if len(original.HTTP.Entries) != 3 || original.HTTP.Entries[0].Key != "scope/d" {
		t.Fatal("grid sorting changed original receipt order")
	}
	f.mu.Lock()
	f.entries = []map[string]any{dataTestEntry("scope/aa", []byte{2}), dataTestEntry("scope/c", []byte{3}), dataTestEntry("scope/d", []byte{4})}
	f.mu.Unlock()
	next := dataTestPage(t, l, contract.DataOptions{Offset: 2, Limit: 2, After: p.NextCursor})
	if !next.LiveRead || next.Rows[0][0].Value.Text != "scope/c" || next.Rows[1][0].Value.Text != "scope/d" || next.HasMore {
		t.Fatal("live changes were treated as a fixed snapshot or ordinal offset")
	}
	back := dataTestPage(t, l, contract.DataOptions{Limit: 2, After: p.CurrentCursor})
	if back.Rows[0][0].Value.Text != "scope/aa" {
		t.Fatal("repeat did not observe current live data")
	}
	second := dataTestLease(t, s)
	before := f.reads.Load()
	if _, err := second.page(context.Background(), 11, "kv", "keys", contract.DataOptions{Offset: 2, Limit: 2, After: p.NextCursor}); err == nil || f.reads.Load() != before {
		t.Fatal("cursor crossed original logical document or made IO")
	}
	dataTestPage(t, l, contract.DataOptions{Limit: 2})
	if _, err := l.page(context.Background(), 11, "kv", "keys", contract.DataOptions{Offset: 2, Limit: 2, After: p.NextCursor}); err == nil {
		t.Fatal("refresh reused old boundary epoch")
	}
}

func TestDataExactNoncanonicalPrefixesFieldLimitsAndSingleObservationExports(t *testing.T) {
	f := &dataFixture{entries: []map[string]any{dataTestEntry("exact//中文/one", bytes.Repeat([]byte{128}, 4096)), dataTestEntry("exact//中文/two", []byte{0, 255})}}
	s := dataTestSession(t, "exact//中文/", f)
	l := dataTestLease(t, s)
	p := dataTestPage(t, l, contract.DataOptions{Limit: 1})
	if p.Rows[0][0].Value.Text != "exact//中文/one" || l.scopes[dataPageScope].LastResult().HTTP.Request.Path != "/v1/txn" {
		t.Fatal("noncanonical original prefix was cleaned")
	}
	ref := *p.RowRefs[0]
	row, err := l.row(context.Background(), 11, "kv", "keys", ref)
	if err != nil || len(row.Values["value"].Bytes) != 4096 || row.Values["flags"].Text != "18446744073709551615" {
		t.Fatal("complete original field changed", err)
	}
	chunk, err := l.chunk(context.Background(), 11, "kv", "keys", "value", ref, 100, 1024)
	if err != nil || !chunk.Binary || chunk.NextOffset != 1124 || chunk.TotalUnits != 4096 || len(chunk.Text) != 2050 {
		t.Fatal("field chunk boundaries changed", err)
	}
	before := f.reads.Load()
	if _, err := l.chunk(context.Background(), 11, "kv", "keys", "value", ref, 0, 1025); err == nil || f.reads.Load() != before {
		t.Fatal("field limit dispatched")
	}
	ref.TypedValues[0].Text = "changed"
	if _, err := l.row(context.Background(), 11, "kv", "keys", ref); err == nil || f.reads.Load() != before {
		t.Fatal("foreign typed/raw key made IO")
	}
	count, err := l.count(context.Background(), 11, "kv", "keys")
	if err != nil || count != 2 || l.scopes[dataCountScope].client == l.scopes[dataPageScope].client {
		t.Fatal("count reused page channel or invented total", err)
	}
	before = f.reads.Load()
	var exported [][]contract.DataValue
	err = l.export(context.Background(), 11, "kv", "keys", contract.DataOptions{}, []string{"key", "value", "flags", "session"}, func(values []contract.DataValue) error {
		exported = append(exported, values)
		if len(exported) == 1 {
			f.mu.Lock()
			f.entries = nil
			f.mu.Unlock()
		}
		return nil
	})
	if err != nil || f.reads.Load() != before+2 || len(exported) != 2 || len(exported[0][1].Bytes) != 4096 || !bytes.Equal(exported[1][1].Bytes, []byte{0, 255}) || exported[0][2].Text != "18446744073709551615" || !exported[0][3].Null {
		// The additional request is this executor's initial original identity
		// observation via get-tree; export emission itself performs one read.
		t.Fatal("export joined live reads or changed bytes/null metadata", err, f.reads.Load()-before)
	}
}

func TestDataRejectsUnknownRulesForeignRowsAndIncompleteObservationsWithoutPartialSuccess(t *testing.T) {
	f := &dataFixture{}
	s := dataTestSession(t, "scope", f)
	l := dataTestLease(t, s)
	before := f.reads.Load()
	for _, options := range []contract.DataOptions{{Limit: 1, Offset: 10}, {Limit: 1, FilterColumn: "key", FilterValue: "guess"}, {Limit: 1, SortColumn: "flags"}, {Limit: 0}, {Limit: 1001}, {Limit: 1, After: &contract.DataCursor{Database: "kv", Table: "keys", Columns: []string{"consul_live"}, Values: [][]byte{[]byte(`{"body":{},"mac":"foreign"}`)}}}} {
		if _, err := l.page(context.Background(), 11, "kv", "keys", options); err == nil || f.reads.Load() != before {
			t.Fatal("unsupported rule or cursor dispatched")
		}
	}
	if _, err := s.reserveDataDocument(contract.ObjectRef{ProfileID: 11, Catalog: "kv", Schema: "scope", Kind: "keyspace", Name: "keys"}); err == nil {
		t.Fatal("prefix became namespace schema")
	}
	h := &contract.KVHTTPResult{Ended: true, Captured: true, EOF: true, Acknowledged: true, Succeeded: true}
	result := contract.KVResult{Completed: true, HTTP: h}
	for _, kind := range []string{"incomplete", "duplicate", "foreign", "missing-value", "projection", "too-many", "too-large"} {
		t.Run(kind, func(t *testing.T) {
			r := result.Clone()
			e := contract.KVHTTPEntry{Key: "scope/a", ValuePresent: true}
			r.HTTP.Entries = []contract.KVHTTPEntry{e}
			switch kind {
			case "incomplete":
				r.HTTP.EOF = false
			case "duplicate":
				r.HTTP.Entries = append(r.HTTP.Entries, e)
			case "foreign":
				r.HTTP.Entries[0].Key = "foreign"
			case "missing-value":
				r.HTTP.Entries[0].ValuePresent = false
			case "projection":
				r.HTTP.ProjectionError = "original projection exceeds bound"
			case "too-many":
				r.HTTP.Entries = make([]contract.KVHTTPEntry, entryLimit+1)
			case "too-large":
				r.HTTP.Entries[0].Value = make([]byte, valueLimit+1)
			}
			if _, err := l.entries(r); err == nil {
				t.Fatal("incomplete or incompatible native observation admitted")
			}
		})
	}
	oversize := result.Clone()
	for i := 0; i < 65; i++ {
		oversize.HTTP.Entries = append(oversize.HTTP.Entries, contract.KVHTTPEntry{Key: "scope/" + strings.Repeat("x", i+1), ValuePresent: true, Value: make([]byte, valueLimit)})
	}
	if _, err := l.entries(oversize); !errors.Is(err, ErrBounds) {
		t.Fatal("32 MiB complete value budget ignored", err)
	}
	if s.Capabilities() != dataCapabilities() {
		t.Fatal("original data product capabilities changed")
	}
	if _, ok := any(s).(contract.DataDocumentsProvider); !ok {
		t.Fatal("original data document routing unavailable")
	}
}

func TestDataCancellationRetiresOnlyOriginalAuxiliaryChannelAndPageFailureRequiresReopen(t *testing.T) {
	for _, scope := range []int{dataCountScope, dataPageScope} {
		t.Run([]string{"page", "count"}[scope], func(t *testing.T) {
			started := make(chan struct{})
			var blocked atomic.Bool
			f := &dataFixture{entries: []map[string]any{dataTestEntry("scope/key", []byte{0, 255})}}
			f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Query().Has("recurse") && blocked.CompareAndSwap(false, true) {
					close(started)
					<-r.Context().Done()
					return true
				}
				return false
			}
			s := dataTestSession(t, "scope", f)
			l := dataTestLease(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			finished := make(chan error, 1)
			go func() {
				if scope == dataPageScope {
					_, err := l.page(ctx, 11, "kv", "keys", contract.DataOptions{Limit: 1})
					finished <- err
				} else {
					_, err := l.count(ctx, 11, "kv", "keys")
					finished <- err
				}
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("original request did not start")
			}
			cancel()
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("canceled original request became complete")
				}
			case <-time.After(time.Second):
				t.Fatal("cancel did not wait for executor acknowledgment")
			}
			before := f.reads.Load()
			if scope == dataPageScope {
				if _, err := l.page(context.Background(), 11, "kv", "keys", contract.DataOptions{Limit: 1}); !errors.Is(err, errDataReadLost) || f.reads.Load() != before {
					t.Fatal("failed page automatically retried original range", err)
				}
				fresh := dataTestLease(t, s)
				if p := dataTestPage(t, fresh, contract.DataOptions{Limit: 1}); len(p.Rows) != 1 {
					t.Fatal("explicit original reopen unavailable")
				}
				fresh.Close()
			} else {
				if _, err := l.count(context.Background(), 11, "kv", "keys"); err == nil || f.reads.Load() != before {
					t.Fatal("lost count channel automatically replaced")
				}
				p := dataTestPage(t, l, contract.DataOptions{Limit: 1})
				if _, err := l.row(context.Background(), 11, "kv", "keys", *p.RowRefs[0]); err != nil {
					t.Fatal("count cancel changed original field channel", err)
				}
			}
			l.Close()
			if l.key != ([32]byte{}) || len(s.dataLeases) != 0 {
				t.Fatal("close retained original data resources")
			}
		})
	}
}

func TestDataSessionMissingNullEmptyAndUUIDRemainOriginalAcrossNeutralProjection(t *testing.T) {
	for _, session := range []string{"missing", "null", `""`, `"11111111-2222-4333-8444-555555555555"`} {
		raw := `{"Key":"key","Value":"AP8=","Flags":18446744073709551615}`
		if session != "missing" {
			raw = strings.TrimSuffix(raw, "}") + `,"Session":` + session + `}`
		}
		entry, err := decodeEntry([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if entry.HasField("Session") != (session != "missing") || entry.SessionNull != (session == "null") {
			t.Fatal("original Session field state changed")
		}
		neutral := neutralEntry(entry)
		cell := dataCells(neutral, true)[6].Value
		if (cell.Kind == contract.Null) != (session == "missing" || session == "null") || cell.Kind != contract.Null && cell.Text != entry.Session {
			t.Fatal("Session null and empty were conflated")
		}
		if session != "missing" && session != "null" && neutral.SessionNull {
			t.Fatal("valid UUID or empty Session became null")
		}
	}
}

func TestDataKeyAndValuePreviewsBoundCopiesWithoutChangingCompleteIdentity(t *testing.T) {
	key := "scope/" + strings.Repeat("中", 1000)
	entry := contract.KVHTTPEntry{Key: key, ValuePresent: true, Value: bytes.Repeat([]byte{0, 255}, valueLimit/2)}
	preview := dataCells(entry, true)
	if !preview[0].Truncated || len(preview[0].Value.Text) > 1024 || !preview[1].Truncated || len(preview[1].Value.Bytes) != 1024 || preview[0].Value.Text == key {
		t.Fatal("preview copied the complete field")
	}
	complete := dataCells(entry, false)
	if complete[0].Value.Text != key || len(complete[1].Value.Bytes) != valueLimit {
		t.Fatal("complete native fields were truncated")
	}
	preview[1].Value.Bytes[0] = 9
	if entry.Value[0] != 0 || complete[1].Value.Bytes[0] != 0 {
		t.Fatal("preview aliased complete bytes")
	}
}

func TestDataPageRetirementPreservesIndependentOriginalCountFieldAndExport(t *testing.T) {
	started := make(chan struct{})
	var armed atomic.Bool
	f := &dataFixture{entries: []map[string]any{dataTestEntry("scope/key", []byte{0, 255})}}
	f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Query().Has("recurse") && armed.CompareAndSwap(true, false) {
			close(started)
			<-r.Context().Done()
			return true
		}
		return false
	}
	s := dataTestSession(t, "scope", f)
	l := dataTestLease(t, s)
	p := dataTestPage(t, l, contract.DataOptions{Limit: 1})
	if _, err := l.count(context.Background(), 11, "kv", "keys"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.row(context.Background(), 11, "kv", "keys", *p.RowRefs[0]); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	armed.Store(true)
	finished := make(chan error, 1)
	go func() { _, err := l.page(ctx, 11, "kv", "keys", contract.DataOptions{Limit: 1}); finished <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("original page request did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("canceled page completed")
		}
	case <-time.After(time.Second):
		t.Fatal("original page cancellation did not acknowledge")
	}
	if n, err := l.count(context.Background(), 11, "kv", "keys"); err != nil || n != 1 {
		t.Fatal("page retirement affected original count", err)
	}
	if row, err := l.row(context.Background(), 11, "kv", "keys", *p.RowRefs[0]); err != nil || !bytes.Equal(row.Values["value"].Bytes, []byte{0, 255}) {
		t.Fatal("page retirement affected original fields", err)
	}
	emitted := 0
	if err := l.export(context.Background(), 11, "kv", "keys", contract.DataOptions{}, []string{"key", "value"}, func([]contract.DataValue) error { emitted++; return nil }); err != nil || emitted != 1 {
		t.Fatal("page retirement affected independent export", err)
	}
	before := f.reads.Load()
	if _, err := l.page(context.Background(), 11, "kv", "keys", contract.DataOptions{Limit: 1}); !errors.Is(err, errDataReadLost) || f.reads.Load() != before {
		t.Fatal("retired page replayed", err)
	}
}

func TestDataOriginalRowVerificationIgnoresLiveHTTPIndexButRejectsChangedRowsAndForeignProofs(t *testing.T) {
	f := &dataFixture{entries: []map[string]any{dataTestEntry("scope/key", []byte{0, 255})}}
	s := dataTestSession(t, "scope", f)
	l := dataTestLease(t, s)
	p := dataTestPage(t, l, contract.DataOptions{Limit: 1})
	f.httpIndex.Store(7)
	row, err := l.row(context.Background(), 11, "kv", "keys", *p.RowRefs[0])
	if err != nil || !reflect.DeepEqual(row.Snapshot, p.RowSnapshots[0]) {
		t.Fatal("unrelated HTTP index invalidated original row verification", err)
	}
	actual := l.scopes[dataFieldScope].LastResult()
	if actual.HTTP.Metadata.Index == nil || *actual.HTTP.Metadata.Index != 7 {
		t.Fatal("field receipt lost its actual independent HTTP index")
	}
	f.mu.Lock()
	f.entries[0]["Value"] = []byte{1, 255}
	f.mu.Unlock()
	row, err = l.row(context.Background(), 11, "kv", "keys", *p.RowRefs[0])
	if err != nil || reflect.DeepEqual(row.Snapshot, p.RowSnapshots[0]) {
		t.Fatal("actual changed row was accepted as the original observation", err)
	}
	peer := dataTestLease(t, s)
	before := f.reads.Load()
	if _, err := peer.row(context.Background(), 11, "kv", "keys", *p.RowRefs[0]); err == nil || f.reads.Load() != before {
		t.Fatal("original row proof crossed document lease")
	}
	ref := cloneDataRef(*p.RowRefs[0])
	ref.Values[0][len(ref.Values[0])-5] ^= 1
	if _, err := l.row(context.Background(), 11, "kv", "keys", ref); err == nil || f.reads.Load() != before {
		t.Fatal("tampered original row proof dispatched")
	}
}
