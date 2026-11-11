package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeDocumentSession(t *testing.T, id int64) (*Session, *Client, Config, string) {
	t.Helper()
	base, cfg, token := nativeClient(t)
	s, err := OpenNativeSession(context.Background(), id, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, base, cfg, token
}

func TestConsulNativeDocumentOriginalScopesBytesAndCompleteReceipts(t *testing.T) {
	base, cfg, token := nativeClient(t)
	cfg.Datacenter = "dc1"
	cfg.Consistency = "consistent"
	s, err := OpenNativeSession(context.Background(), 801, cfg, token)
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
	key := keyFor(t, "original")
	value := []byte{0, 255, 128, 13, 10}
	native, _ := KeyPutRequest(key, value, u64(math.MaxUint64), u64(0))
	p := documentPlan(t, l, native)
	if opens.Load() != 0 || l.Scope().Header != (contract.KVHeader{}) || l.Scope().HTTP.AgentID != os.Getenv("NYADB_CONSUL_NODE_ID") || l.Scope().Namespace.ProfileID != 801 || l.Scope().Generation != 1 {
		t.Fatal("pure reservation invented identity or opened")
	}
	if token != "" && strings.Contains(p.Snapshot().JSON, token) {
		t.Fatal("temporary token entered request snapshot")
	}
	value[0] = 9
	native.Body[1] = 9
	r, err := l.Execute(context.Background(), p)
	if err != nil || !r.Completed || r.HTTP == nil || !r.HTTP.Acknowledged || !r.HTTP.Succeeded || !r.HTTP.Ended || r.NativeCaptured || r.Native != nil || r.Reply != nil {
		t.Fatal("native receipt was reinterpreted", err)
	}
	if _, err = l.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original authorization replayed")
	}
	read := executeDocument(t, l, mustKeyRead(t, key))
	h := read.HTTP
	if len(h.Entries) != 1 || h.Entries[0].Flags != math.MaxUint64 || !bytes.Equal(h.Entries[0].Value, []byte{0, 255, 128, 13, 10}) || h.Metadata.Index == nil || !h.Captured || !h.EOF || read.Header != (contract.KVHeader{}) {
		t.Fatal("HTTP native fields lost precision or bytes")
	}
	u, _ := url.Parse(h.URL)
	if u.Query().Get("dc") != "dc1" || !u.Query().Has("consistent") || u.Query().Has("token") {
		t.Fatal("actual original scoped URL was not retained")
	}
	if !bytes.Equal(nativeRead(t, base, key).Value, h.Entries[0].Value) {
		t.Fatal("original physical request did not write original bytes")
	}
	h.Body[0] = 'x'
	h.Entries[0].Value[0] = 8
	*h.Metadata.Index = 1
	if l.LastResult().HTTP.Body[0] != '[' || l.LastResult().HTTP.Entries[0].Value[0] != 0 {
		t.Fatal("inspection mutated retained original")
	}
}
func mustKeyRead(t *testing.T, key string) Request {
	t.Helper()
	r, err := KeyReadRequest(key)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConsulNativeDocumentTransactionsAndGeneratedSessionIDsRemainNative(t *testing.T) {
	s, base, _, _ := nativeDocumentSession(t, 802)
	l, _ := s.ReserveKV(1)
	defer l.Close()
	key := keyFor(t, "exact//key/")
	value := []byte{0, 255}
	put, _ := KeyPutRequest(key, value, u64(math.MaxUint64), u64(0))
	r := executeDocument(t, l, put)
	if r.HTTP.Txn == nil || !r.HTTP.Txn.Committed || !r.Completed {
		t.Fatal("native transaction ACK missing")
	}
	read := executeDocument(t, l, mustKeyRead(t, key))
	if len(read.HTTP.Txn.Results) != 1 || read.HTTP.Txn.Results[0].Key != key || read.HTTP.Txn.Results[0].Flags != math.MaxUint64 || !bytes.Equal(read.HTTP.Txn.Results[0].Value, value) {
		t.Fatal("transaction results remapped to signed revisions")
	}
	stale, _ := KeyPutRequest(key, []byte("foreign"), nil, u64(math.MaxUint64))
	p := documentPlan(t, l, stale)
	rejected, err := l.Execute(context.Background(), p)
	if !errors.Is(err, ErrConflict) || !rejected.Completed || rejected.WriteOutcomeUnknown || rejected.HTTP.Txn == nil || len(rejected.HTTP.Txn.Errors) == 0 || rejected.HTTP.Txn.Committed || !bytes.Equal(nativeRead(t, base, key).Value, value) {
		t.Fatal("native conflict became unknown or partially wrote", err)
	}
	raw, _ := json.Marshal(map[string]any{"Name": t.Name(), "TTL": "10m", "LockDelay": "0s", "NodeChecks": []string{}})
	created := executeDocument(t, l, Request{Method: "PUT", Path: "/v1/session/create", Body: raw})
	id := created.HTTP.SessionID
	if !validID(id) || created.Reply != nil || created.Header != (contract.KVHeader{}) {
		t.Fatal("generated UUID was mapped to a numeric lease")
	}
	renewed := executeDocument(t, l, Request{Method: "PUT", Path: "/v1/session/renew/" + id})
	if renewed.HTTP.SessionID != id {
		t.Fatal("renewal changed original UUID")
	}
	executeDocument(t, l, Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
}

func TestConsulNativeDocumentOriginalProcessedWritesNeverReplayAfterLostReply(t *testing.T) {
	base, cfg, token := nativeClient(t)
	for _, kind := range []string{"put", "delete", "txn", "session-create", "session-renew", "session-destroy"} {
		for _, mode := range []string{"lost", "partial", "http500", "http408", "cancel", "close"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				key := keyFor(t, "original")
				value := []byte("actual native\x00" + mode)
				id := ""
				var request Request
				switch kind {
				case "put":
					request = putRequest(key, value)
				case "delete":
					nativeDo(t, base, putRequest(key, value))
					request, _ = KeyDeleteRequest(key, nil, false)
				case "txn":
					request, _ = TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: "check-not-exists", Key: key}}, {KV: &TxnKV{Verb: "set", Key: key, Value: &value}}})
				case "session-create":
					raw, _ := json.Marshal(map[string]any{"Name": t.Name(), "TTL": "10m", "NodeChecks": []string{}})
					request = Request{Method: "PUT", Path: "/v1/session/create", Body: raw}
				default:
					id = createSession(t, base, "release")
					path := "/v1/session/renew/"
					if kind == "session-destroy" {
						path = "/v1/session/destroy/"
					}
					request = Request{Method: "PUT", Path: path + id}
				}
				relay, calls, committed := nativeFault(t, cfg, token, mode)
				s, err := OpenNativeSession(context.Background(), 803, relay, token)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				l, _ := s.ReserveKV(1)
				defer l.Close()
				if err = l.Preconnect(context.Background()); err != nil {
					t.Fatal(err)
				}
				p := documentPlan(t, l, request)
				earlier := documentPlan(t, l, putRequest(key+"-later", nil))
				before := calls.Load()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				var original contract.KVResult
				go func() { original, err = l.Execute(ctx, p); close(done) }()
				wait(t, committed)
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					s.Close()
				}
				wait(t, done)
				if err == nil || !original.WriteOutcomeUnknown || original.HTTP == nil || original.HTTP.Acknowledged || !original.HTTP.Ended || calls.Load() != before+1 {
					t.Fatal("actual processed original write was confirmed or replayed", err)
				}
				if mode == "partial" && (!bytes.Equal(original.HTTP.Body, l.LastResult().HTTP.Body) || original.HTTP.EOF || original.HTTP.Captured) {
					t.Fatal("body prefix became a complete ACK")
				}
				if mode == "http500" || mode == "http408" {
					if _, err = l.Execute(context.Background(), earlier); !errors.Is(err, contract.ErrKVWriteUnknown) {
						t.Fatal("earlier plan bypassed unknown")
					}
					executeDocument(t, l, Request{Method: "GET", Path: "/v1/session/list"})
				}
				target := s
				if mode == "close" {
					target, err = OpenNativeSession(context.Background(), 803, relay, token)
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
				if calls.Load() != before || recovery.Unknown == nil || !recovery.Unknown.WriteOutcomeUnknown || !bytes.Equal(recovery.Unknown.HTTP.Body, original.HTTP.Body) || recovery.Unknown.Request != original.Request {
					t.Fatal("pure recovery rewrote original receipt")
				}
				newWrite, _ := DocumentRequest(putRequest(key+"-again", nil))
				if _, err = recovery.Lease.Prepare(newWrite); !errors.Is(err, contract.ErrKVWriteUnknown) {
					t.Fatal("recovery removed write barrier")
				}
				if _, err = recovery.Lease.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
					t.Fatal("recovery granted old authority")
				}
				if err = recovery.Lease.Preconnect(context.Background()); err != nil {
					t.Fatal(err)
				}
				executeDocument(t, recovery.Lease, Request{Method: "GET", Path: "/v1/session/list"})
				if !bytes.Equal(recovery.Lease.(*documentLease).recoveryOwned().Unknown.HTTP.Body, original.HTTP.Body) {
					t.Fatal("later explicit read overwrote original unknown")
				}
				switch kind {
				case "put", "txn":
					if !bytes.Equal(nativeRead(t, base, key).Value, value) {
						t.Fatal("actual write did not occur")
					}
				case "delete":
					if nativeDo(t, base, readRequest(key)).Status != 404 {
						t.Fatal("actual deletion did not occur")
					}
				case "session-create":
					list := nativeDo(t, base, Request{Method: "GET", Path: "/v1/session/list"})
					var sessions []struct{ ID, Name string }
					json.Unmarshal(list.Body, &sessions)
					count := 0
					for _, v := range sessions {
						if v.Name == t.Name() {
							id = v.ID
							count++
						}
					}
					if count != 1 || !validID(id) {
						t.Fatal("original UUID creation was missing or replayed")
					}
				case "session-renew", "session-destroy":
					list := nativeDo(t, base, Request{Method: "GET", Path: "/v1/session/info/" + id})
					var sessions []struct{ ID string }
					json.Unmarshal(list.Body, &sessions)
					if kind == "session-renew" && (len(sessions) != 1 || sessions[0].ID != id) || kind == "session-destroy" && len(sessions) != 0 {
						t.Fatal("original UUID operation changed")
					}
				}
				if kind == "session-create" || kind == "session-renew" {
					nativeDo(t, base, Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
				}
			})
		}
	}
}

func TestConsulNativeDocumentReceiptBudgetAndCrossSessionTransfer(t *testing.T) {
	s, _, cfg, token := nativeDocumentSession(t, 804)
	l, _ := s.ReserveKV(1)
	defer l.Close()
	var opens atomic.Int32
	s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) { opens.Add(1); return s.openCaptured(ctx) }
	key := keyFor(t, "bounded")
	value := bytes.Repeat([]byte{255}, valueLimit)
	p := documentPlan(t, l, putRequest(key, value))
	receiptBudgetMu.Lock()
	s.budget = s.retained + pendingReceiptBudget - 1
	receiptBudgetMu.Unlock()
	r, err := l.Execute(context.Background(), p)
	if !errors.Is(err, ErrBounds) || r.Attempted || r.WriteOutcomeUnknown || opens.Load() != 0 {
		t.Fatal("budget limit dispatched original request")
	}
	receiptBudgetMu.Lock()
	s.budget = sessionReceiptBudget
	receiptBudgetMu.Unlock()
	if _, err = l.Execute(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	read := executeDocument(t, l, readRequest(key))
	if len(read.HTTP.Entries[0].Value) != valueLimit {
		t.Fatal("complete native value was truncated")
	}
	fresh, err := OpenNativeSession(context.Background(), 804, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	old := l.(*documentLease)
	size := old.cacheBytes
	receiptBudgetMu.Lock()
	fresh.budget = fresh.retained + size - 1
	receiptBudgetMu.Unlock()
	recovery, err := fresh.RecoverKV(l)
	if !errors.Is(err, ErrBounds) || recovery.Lease != nil || old.cacheBytes != size || len(recovery.Last.HTTP.Entries[0].Value) != valueLimit {
		t.Fatal("failed transfer lost original receipt budget")
	}
	receiptBudgetMu.Lock()
	fresh.budget = sessionReceiptBudget
	receiptBudgetMu.Unlock()
	recovery, err = fresh.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if old.cacheBytes != 0 || recovery.Lease.(*documentLease).cacheBytes != size || len(recovery.Last.HTTP.Entries[0].Value) != valueLimit {
		t.Fatal("recovery did not transfer complete original cache")
	}
}

func TestConsulNativeDocumentCapturedTLSAndUnavailableRestrictedIdentity(t *testing.T) {
	base, cfg, token := nativeClient(t)
	if cfg.CAFile != "" {
		folder := t.TempDir()
		for _, pair := range []struct {
			source string
			target *string
			name   string
		}{{cfg.CAFile, &cfg.CAFile, "ca.crt"}, {cfg.ClientCertFile, &cfg.ClientCertFile, "client.crt"}, {cfg.ClientKeyFile, &cfg.ClientKeyFile, "client.key"}} {
			if pair.source != "" {
				raw, err := os.ReadFile(pair.source)
				if err != nil {
					t.Fatal(err)
				}
				*pair.target = filepath.Join(folder, pair.name)
				if err = os.WriteFile(*pair.target, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		s, err := OpenNativeSession(context.Background(), 805, cfg, token)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		for _, path := range []string{cfg.CAFile, cfg.ClientCertFile, cfg.ClientKeyFile} {
			if path != "" {
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		}
		l, _ := s.ReserveKV(1)
		defer l.Close()
		if err = l.Preconnect(context.Background()); err != nil {
			t.Fatal("original captured TLS was reopened", err)
		}
		executeDocument(t, l, Request{Method: "GET", Path: "/v1/session/list"})
		cfg, _ = nativeConfig(t)
	}
	if cfg.Auth == "none" {
		return
	}
	nativeDo(t, base, putRequest("allowed/native-session", []byte{0, 255}))
	s, err := OpenNativeSession(context.Background(), 806, cfg, os.Getenv("NYADB_CONSUL_READ_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.KVScope().HTTP.AgentAvailable || s.KVScope().HTTP.AgentID != "" || s.KVScope().Version != "" || s.KVScope().Header != (contract.KVHeader{}) {
		t.Fatal("unavailable original agent identity was invented")
	}
	l, _ := s.ReserveKV(1)
	defer l.Close()
	read := executeDocument(t, l, readRequest("allowed/native-session"))
	if !bytes.Equal(read.HTTP.Entries[0].Value, []byte{0, 255}) {
		t.Fatal("restricted original read unavailable")
	}
	p := documentPlan(t, l, putRequest("allowed/native-session", []byte("foreign")))
	denied, err := l.Execute(context.Background(), p)
	if err == nil || denied.HTTP.Status != 403 || !denied.Completed || denied.WriteOutcomeUnknown || !bytes.Equal(nativeRead(t, base, "allowed/native-session").Value, []byte{0, 255}) {
		t.Fatal("original permission denial changed evidence", err)
	}
}

func TestConsulNativeDocumentOriginalBlockingReadRetirementAndExplicitRecovery(t *testing.T) {
	s, base, cfg, token := nativeDocumentSession(t, 807)
	key := keyFor(t, "blocking")
	nativeDo(t, base, putRequest(key, []byte("original")))
	l, _ := s.ReserveKV(1)
	defer l.Close()
	observed := executeDocument(t, l, readRequest(key))
	index := observed.HTTP.Metadata.Index
	request := readRequest(key)
	request.Query = url.Values{"index": {jsonNumber(*index)}, "wait": {"5m"}}
	p := documentPlan(t, l, request)
	done := make(chan contract.KVResult, 1)
	go func() { r, _ := l.Execute(context.Background(), p); done <- r }()
	select {
	case <-done:
		t.Fatal("original blocking query did not wait")
	case <-time.After(150 * time.Millisecond):
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	original := <-done
	if original.WriteOutcomeUnknown || original.HTTP == nil || !original.HTTP.Ended || !original.HTTP.Canceled || !l.LastResult().HTTP.Canceled {
		t.Fatal("retirement dropped original blocking read")
	}
	fresh, err := OpenNativeSession(context.Background(), 807, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	recovery, err := fresh.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if recovery.Unknown != nil || !recovery.Last.HTTP.Canceled || recovery.Last.Request != original.Request {
		t.Fatal("pure recovery changed original read receipt")
	}
	executeDocument(t, recovery.Lease, readRequest(key))
}
func jsonNumber(n uint64) string { raw, _ := json.Marshal(n); return string(raw) }
