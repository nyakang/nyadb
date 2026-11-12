package consul

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeSessionToolCreate(t *testing.T, c *Client, name, ttl, behavior string) string {
	t.Helper()
	r, _ := sessionToolRequest(t, contract.KVToolHTTPSessionCreate, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{IncludeName: true, Name: name, TTL: ttl, Behavior: behavior, LockDelay: "0s", NodeChecksJSON: "[]"}})
	created := nativeDo(t, c, r)
	if !validID(created.SessionID) {
		t.Fatal("native create did not supply a UUID")
	}
	return created.SessionID
}

func nativeSessionToolID(t *testing.T, kind contract.KVRequestToolKind, id string) Request {
	t.Helper()
	r, _ := sessionToolRequest(t, kind, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}})
	return r
}

func TestConsulNativeSessionToolsCreateInfoDestroyOriginalPresenceAndUUID(t *testing.T) {
	base, cfg, token := nativeClient(t)
	cfg.Datacenter, cfg.Consistency = "dc1", "consistent"
	s, err := OpenNativeSession(context.Background(), 2001, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var opens atomic.Int32
	s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) { opens.Add(1); return s.openCaptured(ctx) }
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, input := range []contract.KVRequestToolInput{{}, {HTTPSession: contract.KVHTTPSessionToolInput{IncludeName: true, Name: "", IncludeNode: true, Node: s.KVScope().HTTP.AgentName, TTL: "10m", LockDelay: "0s", NodeChecksJSON: "[]", Behavior: "release"}}} {
		request, authored := sessionToolRequest(t, contract.KVToolHTTPSessionCreate, input)
		before := opens.Load()
		plan, err := l.Prepare(authored)
		if err != nil || !plan.Writes() || plan.Snapshot() != authored || opens.Load() != before {
			t.Fatal("text generation/preparation connected or changed original request", err)
		}
		created, err := l.Execute(context.Background(), plan)
		if err != nil || created.HTTP == nil || !created.Completed || !created.HTTP.Succeeded || !validID(created.HTTP.SessionID) || created.Native != nil || created.Reply != nil || !bytes.Equal(created.HTTP.Request.Body, request.Body) {
			t.Fatal("original generated Session request/receipt changed", err)
		}
		id := created.HTTP.SessionID
		u, _ := url.Parse(created.HTTP.URL)
		if u.Query().Get("dc") != "dc1" || u.Query().Has("token") {
			t.Fatal("original request escaped scoped datacenter")
		}
		if _, err := l.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
			t.Fatal("creation authorization replayed", err)
		}
		info := executeDocument(t, l, nativeSessionToolID(t, contract.KVToolHTTPSessionInfo, id))
		var sessions []struct {
			ID, Name, Node, TTL, Behavior string
			LockDelay                     uint64
			NodeChecks                    []string
		}
		if info.HTTP == nil || json.Unmarshal(info.HTTP.Body, &sessions) != nil || len(sessions) != 1 || sessions[0].ID != id {
			t.Fatal("original Session information missing")
		}
		if input.HTTPSession.IncludeName && (sessions[0].Name != "" || sessions[0].Node != input.HTTPSession.Node || sessions[0].TTL != "10m0s" && sessions[0].TTL != "10m" || sessions[0].LockDelay != 0 || len(sessions[0].NodeChecks) != 0) {
			t.Fatal("explicit native empty/zero fields changed", sessions[0])
		}
		if !bytes.Equal(nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionInfo, id)).Body, info.HTTP.Body) {
			t.Fatal("complete Session information differed from original read")
		}
		executeDocument(t, l, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
		gone := executeDocument(t, l, nativeSessionToolID(t, contract.KVToolHTTPSessionInfo, id))
		if json.Unmarshal(gone.HTTP.Body, &sessions) != nil || len(sessions) != 0 {
			t.Fatal("destroyed original Session still present")
		}
	}
	// The immutable builder remains usable after close, without granting I/O.
	builder := s.KVRequestBuilder()
	s.Close()
	if _, err := builder.BuildKVRequest(contract.KVToolHTTPSessionInfo, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: sessionToolID}}); err != nil {
		t.Fatal("captured pure builder depended on live session", err)
	}
}

func TestConsulNativeSessionToolsAcquireReleaseNativeBooleanBytesAndFlags(t *testing.T) {
	s, base, _, _ := nativeDocumentSession(t, 2002)
	l, _ := s.ReserveKV(1)
	defer l.Close()
	first := nativeSessionToolCreate(t, base, t.Name()+"-first", "10m", "release")
	second := nativeSessionToolCreate(t, base, t.Name()+"-second", "10m", "release")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, first))
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, second))
	key := keyFor(t, "中文 %?#")
	value := []byte{0, 255, 128, 13, 10}
	input := contract.KVRequestToolInput{Key: key, ValueBase64: base64.StdEncoding.EncodeToString(value), Flags: "18446744073709551615", HTTPSession: contract.KVHTTPSessionToolInput{ID: first}}
	acquire, _ := sessionToolRequest(t, contract.KVToolHTTPAcquire, input)
	r := executeDocument(t, l, acquire)
	if !bytes.Equal(bytes.TrimSpace(r.HTTP.Body), []byte("true")) || !r.HTTP.Succeeded || r.HTTP.Txn != nil || r.Reply != nil {
		t.Fatal("HTTP acquire replaced native boolean semantics")
	}
	entry := nativeRead(t, base, key)
	if entry.Session != first || entry.Flags != math.MaxUint64 || !bytes.Equal(entry.Value, value) || entry.LockIndex == 0 {
		t.Fatal("original acquire lost identity, bytes or uint64 flags")
	}
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
		other := input
		other.HTTPSession.ID, other.ValueBase64, other.Flags = second, "AA==", "0"
		request, _ := sessionToolRequest(t, kind, other)
		p := documentPlan(t, l, request)
		falseResult, err := l.Execute(context.Background(), p)
		if !errors.Is(err, ErrConflict) || falseResult.HTTP == nil || !falseResult.Completed || falseResult.WriteOutcomeUnknown || falseResult.HTTP.Succeeded || !bytes.Equal(bytes.TrimSpace(falseResult.HTTP.Body), []byte("false")) {
			t.Fatal("native false was promoted or treated as unknown", kind, err)
		}
		after := nativeRead(t, base, key)
		if after.Session != entry.Session || after.Flags != entry.Flags || after.ModifyIndex != entry.ModifyIndex || !bytes.Equal(after.Value, entry.Value) {
			t.Fatal("rejected original lock request changed the key")
		}
	}
	input.ValueBase64, input.Flags = "", "0"
	release, _ := sessionToolRequest(t, contract.KVToolHTTPRelease, input)
	executeDocument(t, l, release)
	entry = nativeRead(t, base, key)
	if entry.Session != "" || entry.Flags != 0 || len(entry.Value) != 0 {
		t.Fatal("native release did not preserve explicit empty bytes/zero flags")
	}
	input.HTTPSession.ID = second
	acquire, _ = sessionToolRequest(t, contract.KVToolHTTPAcquire, input)
	executeDocument(t, l, acquire)
	if nativeRead(t, base, key).Session != second {
		t.Fatal("second explicit acquisition did not use original UUID")
	}
}

func TestConsulNativeSessionToolsProcessedWritesUnknownAndOriginalRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
		for _, mode := range []string{"lost", "partial", "http500", "http408", "cancel", "close"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				key, id := keyFor(t, "original"), ""
				input := contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{IncludeName: true, Name: t.Name(), TTL: "10m", NodeChecksJSON: "[]", LockDelay: "0s"}}
				if kind != contract.KVToolHTTPSessionCreate {
					id = nativeSessionToolCreate(t, base, t.Name(), "10m", "release")
					defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
					input = contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}}
					if kind == contract.KVToolHTTPAcquire || kind == contract.KVToolHTTPRelease {
						input.Key, input.ValueBase64, input.Flags = key, "AP8=", "18446744073709551615"
						if kind == contract.KVToolHTTPRelease {
							lock, _ := sessionToolRequest(t, contract.KVToolHTTPAcquire, input)
							nativeDo(t, base, lock)
						}
					}
				}
				_, request := sessionToolRequest(t, kind, input)
				relay, calls, processed := nativeFault(t, cfg, token, mode)
				s, err := OpenNativeSession(context.Background(), 2003, relay, token)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				l, _ := s.ReserveKV(1)
				defer l.Close()
				if err := l.Preconnect(context.Background()); err != nil {
					t.Fatal(err)
				}
				plan, err := l.Prepare(request)
				if err != nil {
					t.Fatal(err)
				}
				before := calls.Load()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				var result contract.KVResult
				go func() { result, err = l.Execute(ctx, plan); close(done) }()
				wait(t, processed)
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					s.Close()
				}
				wait(t, done)
				if err == nil || result.HTTP == nil || !result.WriteOutcomeUnknown || result.HTTP.Acknowledged || result.HTTP.SessionID != "" || calls.Load() != before+1 || result.Request != request {
					t.Fatal("processed write guessed UUID/acknowledgement or replayed", err)
				}
				if mode == "partial" && (len(result.HTTP.Body) == 0 || result.HTTP.EOF || result.HTTP.Captured) {
					t.Fatal("partial original reply changed")
				}
				target := s
				if mode == "close" {
					target, err = OpenNativeSession(context.Background(), 2003, relay, token)
					if err != nil {
						t.Fatal(err)
					}
					defer target.Close()
				}
				before = calls.Load()
				recovery, err := target.RecoverKV(l)
				if err != nil {
					t.Fatal(err)
				}
				defer recovery.Lease.Close()
				if calls.Load() != before || recovery.Unknown == nil || recovery.Unknown.Request != request || !bytes.Equal(recovery.Unknown.HTTP.Body, result.HTTP.Body) {
					t.Fatal("evidence-only recovery changed original request or reply")
				}
				if _, err := recovery.Lease.Prepare(request); !errors.Is(err, contract.ErrKVWriteUnknown) {
					t.Fatal("recovery authorized replay", err)
				}
				executeDocument(t, recovery.Lease, Request{Method: "GET", Path: "/v1/session/list"})
				unknown := recovery.Lease.(*documentLease).recoveryOwned().Unknown
				if unknown == nil || !unknown.WriteOutcomeUnknown || unknown.HTTP.SessionID != "" || !bytes.Equal(unknown.HTTP.Body, result.HTTP.Body) {
					t.Fatal("later read confirmed original unknown write")
				}
				switch kind {
				case contract.KVToolHTTPSessionCreate:
					var list []struct{ ID, Name string }
					if json.Unmarshal(nativeDo(t, base, Request{Method: "GET", Path: "/v1/session/list"}).Body, &list) != nil {
						t.Fatal("original Session list invalid")
					}
					count := 0
					for _, item := range list {
						if item.Name == t.Name() {
							count++
							nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, item.ID))
						}
					}
					if count != 1 {
						t.Fatal("creation missing or duplicated", count)
					}
				case contract.KVToolHTTPSessionDestroy:
					var list []json.RawMessage
					if json.Unmarshal(nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionInfo, id)).Body, &list) != nil || len(list) != 0 {
						t.Fatal("original destroy did not occur")
					}
				default:
					entry := nativeRead(t, base, key)
					wantID := id
					if kind == contract.KVToolHTTPRelease {
						wantID = ""
					}
					if entry.Session != wantID || entry.Flags != math.MaxUint64 || !bytes.Equal(entry.Value, []byte{0, 255}) {
						t.Fatal("actual original lock operation did not occur")
					}
				}
			})
		}
	}
}

func TestConsulNativeSessionToolsOriginalACLAndNaturalExpiry(t *testing.T) {
	base, cfg, _ := nativeClient(t)
	id := nativeSessionToolCreate(t, base, t.Name(), "10s", "delete")
	key := keyFor(t, "expires")
	lock, _ := sessionToolRequest(t, contract.KVToolHTTPAcquire, contract.KVRequestToolInput{Key: key, HTTPSession: contract.KVHTTPSessionToolInput{ID: id}})
	nativeDo(t, base, lock)
	deadline := time.Now().Add(40 * time.Second)
	for nativeDo(t, base, mustKeyRead(t, key)).Status != 404 {
		if time.Now().After(deadline) {
			t.Fatal("original delete-behavior Session did not expire")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if cfg.Auth == "none" {
		return
	}
	id = nativeSessionToolCreate(t, base, t.Name()+"-acl", "10m", "release")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
	for _, token := range []string{os.Getenv("NYADB_CONSUL_READ_TOKEN"), os.Getenv("NYADB_CONSUL_SESSION_READ_TOKEN")} {
		if token == "" {
			t.Fatal("missing original restricted fixture token")
		}
		s, err := OpenNativeSession(context.Background(), 2004, cfg, token)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		l, _ := s.ReserveKV(1)
		defer l.Close()
		read := executeDocument(t, l, nativeSessionToolID(t, contract.KVToolHTTPSessionInfo, id))
		var sessions []struct{ ID string }
		if json.Unmarshal(read.HTTP.Body, &sessions) != nil || token == os.Getenv("NYADB_CONSUL_READ_TOKEN") && len(sessions) != 0 || token == os.Getenv("NYADB_CONSUL_SESSION_READ_TOKEN") && (len(sessions) != 1 || sessions[0].ID != id) {
			t.Fatal("original ACL-filtered Session read changed")
		}
		for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease} {
			input := contract.KVRequestToolInput{}
			if kind != contract.KVToolHTTPSessionCreate {
				input.HTTPSession.ID = id
			}
			if kind == contract.KVToolHTTPAcquire || kind == contract.KVToolHTTPRelease {
				input.Key = "allowed/" + key
			}
			request, _ := sessionToolRequest(t, kind, input)
			result, err := l.Execute(context.Background(), documentPlan(t, l, request))
			if err == nil || result.HTTP == nil || result.HTTP.Status != 403 || !result.HTTP.Acknowledged || result.WriteOutcomeUnknown || result.HTTP.Succeeded {
				t.Fatal("original write denial changed", kind, err)
			}
		}
	}
}
