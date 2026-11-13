package consul

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func destroyImpactFixture(t *testing.T) contract.KVHTTPSessionDestroyImpact {
	t.Helper()
	keys := sessionKeysProjectionFixture(t, `[{"Key":"outside/z","Value":"AP8=","ModifyIndex":18446744073709551615,"Session":"`+sessionToolID+`"},{"Key":"outside/a","Value":"","ModifyIndex":9007199254740993,"Session":"`+sessionToolID+`"},{"Key":"unmatched","Value":"","Session":null}]`)
	if err := projectSessionKeys(&keys); err != nil {
		t.Fatal(err)
	}
	native := sessionProjectionResponse(`[{"ID":"` + sessionToolID + `","Behavior":"delete","future":{"unsigned":18446744073709551615}}]`)
	native.Protocol, native.Kind, native.Request.Path = "HTTP/1.1", "read", "/v1/session/info/"+sessionToolID
	document, err := DocumentRequest(native.Request)
	if err != nil {
		t.Fatal(err)
	}
	info := documentResult(document, native)
	return contract.KVHTTPSessionDestroyImpact{Scope: keys.Scope.Clone(), Request: contract.KVHTTPSessionDestroyImpactRequest{ID: keys.Request.ID, MaxRows: keys.Request.MaxRows, MaxBytes: keys.Request.MaxBytes}, Session: info, Keys: keys, Bytes: keys.Bytes + resultCost(&info) - (1 << 20), Complete: true}
}

func TestHTTPSessionDestroyImpactExpectationKeepsOriginalEvidenceAndRejectsChangedIdentity(t *testing.T) {
	o := destroyImpactFixture(t)
	original := o.Clone()
	e, err := SessionDestroyExpectation(o)
	if err != nil || e.ID != sessionToolID || e.Behavior != "delete" || len(e.HeldKeys) != 2 || e.HeldKeys[0].Key != "outside/a" || e.HeldKeys[1].ModifyIndex != math.MaxUint64 || !reflect.DeepEqual(o, original) {
		t.Fatal("immutable original impact changed", err)
	}
	copy := e.Clone()
	copy.HeldKeys[0].ModifyIndex = 0
	copy.Scope.Prefix[0] = 'X'
	if err := CompareSessionDestroyExpectation(e, o); err != nil {
		t.Fatal(err)
	}
	if err := CompareSessionDestroyExpectation(copy, o); !errors.Is(err, ErrConflict) {
		t.Fatal("mutated expectation became original", err)
	}
	for _, change := range []func(*contract.KVHTTPSessionDestroyImpact){
		func(v *contract.KVHTTPSessionDestroyImpact) { v.Scope.Generation++ },
		func(v *contract.KVHTTPSessionDestroyImpact) { v.Scope.HTTP.Endpoint = "https://foreign" },
		func(v *contract.KVHTTPSessionDestroyImpact) { v.Session.HTTP.Sessions.Rows[0].Behavior = "release" },
		func(v *contract.KVHTTPSessionDestroyImpact) { v.Keys.Root.HTTP.Entries[0].ModifyIndex-- },
	} {
		changed := o.Clone()
		change(&changed)
		if err := CompareSessionDestroyExpectation(e, changed); err == nil {
			t.Fatal("changed native identity/behavior/index accepted")
		}
	}
	if !bytes.Equal(o.Session.HTTP.Body, original.Session.HTTP.Body) || !bytes.Equal(o.Keys.Root.HTTP.Body, original.Keys.Root.HTTP.Body) {
		t.Fatal("comparison rewrote original JSON")
	}
}

func TestHTTPSessionDestroyImpactRejectsACLPartialAndUnknownBehavior(t *testing.T) {
	for _, change := range []func(*contract.KVHTTPSessionDestroyImpact){
		func(v *contract.KVHTTPSessionDestroyImpact) { v.Complete = false },
		func(v *contract.KVHTTPSessionDestroyImpact) {
			v.Keys.Complete = false
			v.Keys.Root.HTTP.Metadata.ACLFiltered = new(true)
		},
		func(v *contract.KVHTTPSessionDestroyImpact) { v.Session.HTTP.Metadata.ACLFiltered = new(true) },
		func(v *contract.KVHTTPSessionDestroyImpact) { v.Keys.Matches = v.Keys.Matches[:1] },
		func(v *contract.KVHTTPSessionDestroyImpact) { v.Session.HTTP.Sessions.Rows[0].Behavior = "" },
		func(v *contract.KVHTTPSessionDestroyImpact) {
			v.Session.HTTP.Sessions.Rows[0].NullFields = []string{"Behavior"}
		},
		func(v *contract.KVHTTPSessionDestroyImpact) {
			v.Keys.Root.HTTP.Entries[0].PresentFields = []string{"Key", "Session"}
		},
		func(v *contract.KVHTTPSessionDestroyImpact) { v.Bytes = v.Request.MaxBytes + 1 },
		func(v *contract.KVHTTPSessionDestroyImpact) { v.Session.HTTP.EOF = false },
	} {
		o := destroyImpactFixture(t)
		change(&o)
		if _, err := SessionDestroyExpectation(o); err == nil {
			t.Fatal("incomplete/foreign impact produced confirmation expectation")
		}
	}
}

func TestHTTPSessionDestroyImpactReadsOriginalInfoThenWholeRootWithoutWrites(t *testing.T) {
	var reads, writes atomic.Int32
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Consul-Index", "18446744073709551615")
		if r.Method != "GET" {
			writes.Add(1)
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/v1/agent/self":
			w.Write([]byte(`{"Config":{"Version":"2.0.2","NodeID":"` + testAgentID + `","NodeName":"original","Datacenter":"dc1"}}`))
		case "/v1/status/leader":
			w.Write([]byte(`"127.0.0.1:8300"`))
		case "/v1/session/info/" + sessionToolID:
			paths = append(paths, r.URL.Path)
			reads.Add(1)
			w.Write([]byte(`[{"ID":"` + sessionToolID + `","Behavior":"release"}]`))
		case "/v1/kv/configured":
			w.Write([]byte(`[]`))
		case "/v1/kv/":
			if r.URL.Query().Has("recurse") {
				paths = append(paths, r.URL.Path)
				reads.Add(1)
				w.Write([]byte(`[{"Key":"outside/original","Value":"AP8=","ModifyIndex":18446744073709551615,"Session":"` + sessionToolID + `"}]`))
			} else {
				w.Write([]byte(`[]`))
			}
		default:
			t.Errorf("unexpected original path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	s, err := OpenNativeSession(context.Background(), 1, Config{Endpoint: server.URL, Prefix: "configured"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	facet, ok := l.(contract.KVHTTPSessionDestroyImpactBrowsing)
	if !ok {
		t.Fatal("original optional facet unavailable")
	}
	request := contract.KVHTTPSessionDestroyImpactRequest{ID: sessionToolID, MaxRows: 10000, MaxBytes: 32 << 20}
	before := reads.Load()
	o, err := facet.ObserveHTTPSessionDestroyImpact(context.Background(), request)
	if err != nil || !o.Complete || string(o.Scope.Prefix) != "configured" || reads.Load()-before != 2 || writes.Load() != 0 {
		t.Fatal("whole-root original read contract changed", err)
	}
	if !slices.Equal(paths, []string{"/v1/session/info/" + sessionToolID, "/v1/kv/"}) {
		t.Fatal("read sequence changed", paths)
	}
	e, err := SessionDestroyExpectation(o)
	if err != nil || e.Behavior != "release" || len(e.HeldKeys) != 1 || e.HeldKeys[0].ModifyIndex != math.MaxUint64 {
		t.Fatal("native impact lost original unsigned key", err)
	}
	// Invalid UUIDs cannot perform even the first read, and an insufficient
	// combined receipt budget cannot yield a partial confirmation expectation.
	request.ID = "42"
	before = reads.Load()
	if _, err := facet.ObserveHTTPSessionDestroyImpact(context.Background(), request); !errors.Is(err, ErrInvalid) || reads.Load() != before {
		t.Fatal("invalid target performed I/O", err)
	}
	request.ID = sessionToolID
	request.MaxBytes = 1
	bounded, err := facet.ObserveHTTPSessionDestroyImpact(context.Background(), request)
	if !errors.Is(err, ErrBounds) || bounded.Complete || bounded.Session.HTTP == nil || bounded.Keys.Root.HTTP != nil {
		t.Fatal("combined bound erased original info or returned partial impact", err)
	}
}
