package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeMutationLockRequest(key string, value []byte, flags *uint64, id string, release bool) (Request, error) {
	verb := "lock"
	if release {
		verb = "unlock"
	}
	return TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: verb, Key: key, Value: &value, Flags: flags, Session: &id}}})
}

func nativeMutationAbsent(t *testing.T, c *Client, key string) bool {
	t.Helper()
	r, err := TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: "check-not-exists", Key: key}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Do(context.Background(), r)
	if errors.Is(err, ErrConflict) && result.Acknowledged {
		return false
	}
	if err != nil || !result.Captured || !result.EOF || !result.Acknowledged || !result.Succeeded {
		t.Fatal("original native absence receipt", err)
	}
	return true
}

func nativeMutationPreview(t *testing.T, l *dataLease, edits ...contract.DataEdit) contract.DataMutationPlan {
	t.Helper()
	p, err := l.preview(context.Background(), l.object.ProfileID, l.object.Catalog, l.object.Name, edits)
	if err != nil {
		t.Fatal("original native mutation preview", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}
func nativeMutationSubmit(t *testing.T, l *dataLease, p contract.DataMutationPlan) *contract.DataEditResult {
	t.Helper()
	out, err := l.submit(context.Background(), l.object.ProfileID, l.object.Catalog, l.object.Name, p)
	if out == nil {
		t.Fatal("original mutation did not return a receipt", err)
	}
	if err != nil || !out.CommitConfirmed || out.CommitUncertain || out.Incomplete || !out.RefreshRequired || out.Applied != len(out.Outcomes) {
		t.Fatalf("original mutation receipt: confirmed=%v uncertain=%v incomplete=%v applied=%d outcomes=%d; %v", out.CommitConfirmed, out.CommitUncertain, out.Incomplete, out.Applied, len(out.Outcomes), err)
	}
	return out
}
func nativeMutationOriginal(t *testing.T, l *dataLease, key, mode string) *contract.RowSnapshot {
	t.Helper()
	p := dataTestPage(t, l, contract.DataOptions{Limit: 100})
	for i, row := range p.Rows {
		if row[0].Value.Text != key {
			continue
		}
		s := p.RowSnapshots[i]
		if mode == "values" {
			r, err := l.row(context.Background(), l.object.ProfileID, l.object.Catalog, l.object.Name, *p.RowRefs[i])
			if err != nil {
				t.Fatal(err)
			}
			s = contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{{Original: r.Snapshot}}}).Edits[0].Original
			s.Mode = "values"
			s.Fingerprints = nil
			for _, c := range dataColumns() {
				v := r.Values[c.Name]
				kind := v.Kind
				if v.Null {
					kind = contract.Null
				}
				s.Original = append(s.Original, contract.Cell{Kind: kind, Text: v.Text, Bytes: bytes.Clone(v.Bytes)})
			}
		}
		return s
	}
	t.Fatal("original native row absent", key)
	return nil
}

func TestConsulNativeDataMutationOriginalAtomicInsertUpdateRenameDeleteCompleteEvidence(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, mode := range []string{"values", "fingerprints"} {
		t.Run(mode, func(t *testing.T) {
			prefix := nativeDataPrefix(t) + "//中文"
			cfg.Prefix = prefix
			_, l := nativeDataSession(t, cfg, token)
			value := bytes.Repeat([]byte{0, 255}, valueLimit/2)
			key, empty := prefix+"/full", prefix+"/empty"
			p := nativeMutationPreview(t, l,
				contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText(key), "value": dataBinary(value), "flags": {Kind: contract.Integer, Text: "18446744073709551615"}}},
				contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText(empty), "value": dataBinary(nil)}})
			copy := p.Snapshot()
			copy.Edits[0].Values["value"].Bytes[0] = 7
			out := nativeMutationSubmit(t, l, p)
			if len(out.Outcomes) != 2 || !out.Outcomes[0].Previews[1].Truncated || out.Outcomes[1].Previews[1].Value.Kind != contract.Null {
				t.Fatal("complete bytes or native empty semantics changed")
			}
			e := nativeRead(t, writer, key)
			if !bytes.Equal(e.Value, value) || e.Flags != math.MaxUint64 {
				t.Fatal("snapshot copy changed native original write")
			}
			if _, err := l.submit(context.Background(), 91, "kv", "keys", p); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("original plan replayed", err)
			}
			s := nativeMutationOriginal(t, l, key, mode)
			updated := []byte{0, 255, 1}
			out = nativeMutationSubmit(t, l, nativeMutationPreview(t, l, contract.DataEdit{Kind: "update", Original: s, Values: map[string]contract.DataValue{"value": dataBinary(updated)}}))
			if out.Outcomes[0].Saved == nil || nativeRead(t, writer, key).Flags != math.MaxUint64 {
				t.Fatal("update lost original flags or saved identity")
			}
			rename := prefix + "/renamed//"
			s = nativeMutationOriginal(t, l, key, mode)
			p = nativeMutationPreview(t, l, contract.DataEdit{Kind: "update", Original: s, Values: map[string]contract.DataValue{"key": dataText(rename), "flags": {Kind: contract.Integer, Text: "9007199254740993"}}})
			if !strings.Contains(p.Snapshot().NativeJSON, "BeforeImages") || !strings.Contains(p.Snapshot().NativeJSON, `"Verb":"cas"`) {
				t.Fatal("preview omitted original complete before evidence")
			}
			out = nativeMutationSubmit(t, l, p)
			target := nativeRead(t, writer, rename)
			if !bytes.Equal(target.Value, updated) || target.Flags != 9007199254740993 || target.CreateIndex != target.ModifyIndex || !nativeMutationAbsent(t, writer, key) {
				t.Fatal("native rename identity/bytes/source absence changed")
			}
			// A generated original reference supports a subsequent edit directly.
			nativeMutationSubmit(t, l, nativeMutationPreview(t, l, contract.DataEdit{Kind: "delete", Original: out.Outcomes[0].Saved}, contract.DataEdit{Kind: "delete", Original: nativeMutationOriginal(t, l, empty, mode)}))
			if !nativeMutationAbsent(t, writer, rename) || !nativeMutationAbsent(t, writer, empty) {
				t.Fatal("confirmed deletes retained keys")
			}
		})
	}
}

func TestConsulNativeDataMutationOriginalObservationDestinationAndSessionConflictsRollbackBatch(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, change := range []string{"value", "flags", "delete", "recreate", "destination", "acquire", "release", "destroy", "expire"} {
		t.Run(change, func(t *testing.T) {
			prefix := nativeDataPrefix(t)
			cfg.Prefix = prefix
			key, dst, earlier := prefix+"/source", prefix+"/destination", prefix+"/earlier"
			nativeDo(t, writer, putRequest(key, []byte("original")))
			_, l := nativeDataSession(t, cfg, token)
			var sessionID string
			if change == "release" || change == "destroy" {
				sessionID = createSession(t, writer, "release")
				r, _ := nativeMutationLockRequest(key, []byte("original"), nil, sessionID, false)
				nativeDo(t, writer, r)
				locked := nativeMutationOriginal(t, l, key, "fingerprints")
				if _, err := l.preview(context.Background(), 91, "kv", "keys", []contract.DataEdit{{Kind: "delete", Original: locked}}); !errors.Is(err, contract.ErrUnsupported) {
					t.Fatal("original session-locked row authorized", err)
				}
				r, _ = nativeMutationLockRequest(key, []byte("original"), nil, sessionID, true)
				nativeDo(t, writer, r)
			}
			s := nativeMutationOriginal(t, l, key, "fingerprints")
			p := nativeMutationPreview(t, l, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText(earlier), "value": dataBinary([]byte("first mutation must roll back"))}}, contract.DataEdit{Kind: "update", Original: s, Values: map[string]contract.DataValue{"key": dataText(dst)}})
			switch change {
			case "value":
				nativeDo(t, writer, putRequest(key, []byte("foreign")))
			case "flags":
				r, _ := KeyPutRequest(key, []byte("original"), u64(math.MaxUint64), nil)
				nativeDo(t, writer, r)
			case "delete", "recreate":
				nativeDo(t, writer, Request{Method: "DELETE", Path: "/v1/kv/" + strings.ReplaceAll(key, "/", "%2F")})
				if change == "recreate" {
					nativeDo(t, writer, putRequest(key, []byte("original")))
				}
			case "destination":
				nativeDo(t, writer, putRequest(dst, []byte("foreign target")))
			case "acquire", "release", "destroy", "expire":
				if sessionID == "" {
					sessionID = createSession(t, writer, "release")
				}
				r, _ := nativeMutationLockRequest(key, []byte("original"), nil, sessionID, false)
				nativeDo(t, writer, r)
				if change == "release" {
					r, _ = nativeMutationLockRequest(key, []byte("original"), nil, sessionID, true)
					nativeDo(t, writer, r)
				}
				if change == "destroy" {
					r = Request{Method: "PUT", Path: "/v1/session/destroy/" + sessionID}
					nativeDo(t, writer, r)
				}
				if change == "expire" {
					deadline := time.Now().Add(30 * time.Second)
					for nativeRead(t, writer, key).Session != "" {
						if time.Now().After(deadline) {
							t.Fatal("original session did not expire")
						}
						time.Sleep(100 * time.Millisecond)
					}
				}
			}
			before := nativeDo(t, writer, readRequest(key))
			out, err := l.submit(context.Background(), 91, "kv", "keys", p)
			if !errors.Is(err, contract.ErrDataConflict) || out == nil || out.CommitConfirmed || out.CommitUncertain || out.Incomplete || len(out.Outcomes) != 0 {
				t.Fatal("native 409 did not reject original batch", out, err)
			}
			after := nativeDo(t, writer, readRequest(key))
			if !reflect.DeepEqual(before.Entries, after.Entries) || !nativeMutationAbsent(t, writer, earlier) {
				t.Fatal("conflict left an earlier mutation or altered foreign observation")
			}
			if l.uncertain || l.write.recoveryOwned().Unknown != nil {
				t.Fatal("confirmed conflict invented unknown")
			}
			if change != "destination" && !nativeMutationAbsent(t, writer, dst) {
				t.Fatal("conflict created destination")
			}
			// A separate, explicit new original plan remains usable after 409.
			nativeMutationSubmit(t, l, nativeMutationPreview(t, l, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText(earlier), "value": dataBinary(nil)}}))
		})
	}
}

// Relay waits for the actual upstream transaction, then damages only that
// original write receipt. Handshake/read requests remain independent.
func nativeMutationRelay(t *testing.T, cfg Config, token, mode string) (Config, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	client, err := New(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	calls := &atomic.Int32{}
	processed := make(chan struct{})
	var notified atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		raw, err := io.ReadAll(incoming.Body)
		if err != nil {
			return
		}
		request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, cfg.Endpoint+incoming.URL.RequestURI(), bytes.NewReader(raw))
		if err != nil {
			t.Error(err)
			return
		}
		request.Header = incoming.Header.Clone()
		response, err := client.http.Do(request)
		if err != nil {
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Error(err)
			return
		}
		for k, values := range response.Header {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		write := false
		if incoming.URL.Path == "/v1/txn" {
			_, write, _ = validateTxn(raw)
		}
		if write {
			calls.Add(1)
		}
		if write && response.StatusCode == 200 && notified.CompareAndSwap(false, true) {
			close(processed)
			switch mode {
			case "lost":
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			case "partial":
				w.Header().Set("Content-Length", strconv.Itoa(len(body)+50))
				w.WriteHeader(200)
				w.Write(body[:min(20, len(body))])
				return
			case "cancel", "close", "session-close":
				<-incoming.Context().Done()
				return
			case "missing":
				body = []byte(`{"Results":null,"Errors":null}`)
			case "wrong-value", "wrong-key", "wrong-flags", "wrong-index", "extra-result", "missing-field", "wrong-check", "missing-check", "wrong-source-value", "wrong-source-index", "wrong-source-session", "missing-source":
				var reply map[string]json.RawMessage
				json.Unmarshal(body, &reply)
				var results []map[string]map[string]json.RawMessage
				json.Unmarshal(reply["Results"], &results)
				last := results[len(results)-1]["KV"]
				switch mode {
				case "wrong-source-value":
					results[len(results)-2]["KV"]["Value"] = json.RawMessage(`"Zm9yZWlnbg=="`)
				case "wrong-source-index":
					results[len(results)-2]["KV"]["ModifyIndex"] = json.RawMessage(`0`)
				case "wrong-source-session":
					results[len(results)-2]["KV"]["Session"] = json.RawMessage(`"00000000-0000-0000-0000-000000000001"`)
				case "missing-source":
					results = append(results[:len(results)-2], results[len(results)-1])
				case "wrong-value":
					last["Value"] = json.RawMessage(`"Zm9yZWlnbg=="`)
				case "wrong-key":
					last["Key"] = json.RawMessage(`"foreign"`)
				case "wrong-flags":
					last["Flags"] = json.RawMessage(`1`)
				case "wrong-index":
					last["ModifyIndex"] = json.RawMessage(`0`)
				case "extra-result":
					results = append(results, results[len(results)-1])
				case "missing-field":
					delete(last, "LockIndex")
				case "wrong-check":
					results[0]["KV"]["Flags"] = json.RawMessage(`1`)
				case "missing-check":
					results = results[1:]
				}
				reply["Results"], _ = json.Marshal(results)
				body, _ = json.Marshal(reply)
			case "http500", "http408":
				w.Header().Del("Content-Length")
				status := 500
				if mode == "http408" {
					status = 408
				}
				w.WriteHeader(status)
				w.Write([]byte("processed original transaction"))
				return
			}
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	proxy := cfg
	proxy.Endpoint, proxy.CAFile, proxy.ServerName, proxy.ClientCertFile, proxy.ClientKeyFile = server.URL, "", "", "", ""
	return proxy, calls, processed
}

func TestConsulNativeDataMutationProcessedReplyFaultsCancellationAndOriginalCloseNeverReplay(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, mode := range []string{"lost", "partial", "missing", "wrong-value", "wrong-key", "wrong-flags", "wrong-index", "extra-result", "missing-field", "wrong-check", "missing-check", "http500", "cancel", "close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			cfg.Prefix = nativeDataPrefix(t)
			proxy, calls, processed := nativeMutationRelay(t, cfg, token, mode)
			s, l := nativeDataSession(t, proxy, token)
			key := cfg.Prefix + "/processed"
			edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText(key), "value": dataBinary([]byte{0, 255})}}}
			if mode == "wrong-check" || mode == "missing-check" {
				nativeDo(t, writer, putRequest(key, []byte("original")))
				edits = []contract.DataEdit{{Kind: "update", Original: nativeMutationOriginal(t, l, key, "fingerprints"), Values: map[string]contract.DataValue{"value": dataBinary([]byte{0, 255})}}}
			}
			p := nativeMutationPreview(t, l, edits...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				out *contract.DataEditResult
				err error
			}
			done := make(chan completion, 1)
			go func() { out, err := l.submit(ctx, 91, "kv", "keys", p); done <- completion{out, err} }()
			select {
			case <-processed:
			case <-time.After(10 * time.Second):
				t.Fatal("original upstream write not processed")
			}
			switch mode {
			case "cancel":
				cancel()
			case "close":
				l.Close()
			case "session-close":
				s.Close()
			}
			var c completion
			select {
			case c = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original write acknowledgement did not settle")
			}
			if c.out == nil || c.out.CommitConfirmed || c.out.Applied != 0 || !(c.out.CommitUncertain || c.out.Incomplete) || !errors.Is(c.err, contract.ErrDataCommitUncertain) {
				t.Fatal("processed reply fault promoted success", c.out, c.err)
			}
			if !bytes.Equal(nativeRead(t, writer, key).Value, []byte{0, 255}) {
				t.Fatal("fault did not occur after actual native commit")
			}
			if mode != "close" && mode != "session-close" {
				unknown := l.write.recoveryOwned().Unknown
				if unknown == nil {
					t.Fatal("original complete/incomplete unknown receipt missing")
				}
				dataTestPage(t, l, contract.DataOptions{Limit: 10})
				if _, err := l.count(context.Background(), 91, "kv", "keys"); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(l.write.recoveryOwned().Unknown, unknown) {
					t.Fatal("independent read replaced original unknown receipt")
				}
				if _, err := l.preview(context.Background(), 91, "kv", "keys", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal("new write crossed barrier", err)
				}
				if _, err := l.submit(context.Background(), 91, "kv", "keys", p); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal("old write replayed", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("original transaction was retried", calls.Load())
			}
			l.Close()
			receiptBudgetMu.Lock()
			reserved := s.reserved
			receiptBudgetMu.Unlock()
			if reserved != 0 || l.write.last != nil || l.write.unknown != nil {
				t.Fatal("close retained write receipts or plan budget", reserved)
			}
		})
	}
}

func TestConsulNativeDataMutationOriginalLeaseForeignPlanRevocationAndBounds(t *testing.T) {
	_, cfg, token := nativeClient(t)
	cfg.Prefix = nativeDataPrefix(t)
	s, l := nativeDataSession(t, cfg, token)
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText(cfg.Prefix + "/key"), "value": dataBinary(nil)}}}
	p := nativeMutationPreview(t, l, edits...)
	other := dataTestLease(t, s)
	if _, err := other.submit(context.Background(), 91, "kv", "keys", p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("foreign document plan accepted", err)
	}
	p.Close()
	if _, err := l.submit(context.Background(), 91, "kv", "keys", p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("closed plan accepted", err)
	}
	p = nativeMutationPreview(t, l, edits...)
	newPlan := nativeMutationPreview(t, l, edits...)
	if _, err := l.submit(context.Background(), 91, "kv", "keys", p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("replaced plan accepted", err)
	}
	newPlan.Close()
	batch := []contract.DataEdit{}
	for i := 0; i < 22; i++ {
		batch = append(batch, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText(cfg.Prefix + "/" + strconv.Itoa(i)), "value": dataBinary(nil)}})
	}
	if _, err := l.preview(context.Background(), 91, "kv", "keys", batch); !errors.Is(err, ErrBounds) {
		t.Fatal("oversized atomic batch accepted", err)
	}
	if l.write.LastResult().HTTP == nil || l.write.LastResult().HTTP.Request.Path != "/v1/txn" {
		t.Fatal("original executor not retained")
	}
	if count, err := l.count(context.Background(), 91, "kv", "keys"); err != nil || count != 0 {
		t.Fatal("revoked/bounded requests left writes", count, err)
	}
	p = nativeMutationPreview(t, l, edits...)
	s.Close()
	if _, err := l.submit(context.Background(), 91, "kv", "keys", p); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original session plan accepted", err)
	}
}

func TestConsulNativeDataMutationRestrictedTokenOriginalWriteDenialAndExplicitNewPlan(t *testing.T) {
	writer, cfg, _ := nativeClient(t)
	readToken := os.Getenv("NYADB_CONSUL_READ_TOKEN")
	if readToken == "" {
		return
	}
	cfg.Auth, cfg.Prefix = "token", "allowed/"+nativeDataPrefix(t)
	_, l := nativeDataSession(t, cfg, readToken)
	key := cfg.Prefix + "/key"
	p, err := l.preview(context.Background(), 91, "kv", "keys", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText(key), "value": dataBinary(nil)}}})
	if err != nil {
		// Native check-not-exists itself requires write permission. A read-only
		// token receives an original 409 ACL error before a write plan exists.
		r := l.write.LastResult()
		if p != nil || l.uncertain || r.HTTP == nil || r.HTTP.Status != 409 || r.HTTP.Txn == nil ||
			!strings.Contains(strings.ToLower(err.Error()), "permission") || r.WriteOutcomeUnknown {
			t.Fatal("native preview ACL denial lost original rejection", err)
		}
	} else {
		defer p.Close()
		out, err := l.submit(context.Background(), 91, "kv", "keys", p)
		if err == nil || out == nil || out.CommitConfirmed || out.CommitUncertain || out.Incomplete || l.uncertain {
			t.Fatal("native ACL rejection invented success or unknown", err)
		}
	}
	if !nativeMutationAbsent(t, writer, key) {
		t.Fatal("read-only token wrote key")
	}
	writeToken := os.Getenv("NYADB_CONSUL_WRITE_TOKEN")
	_, allowed := nativeDataSession(t, cfg, writeToken)
	nativeMutationSubmit(t, allowed, nativeMutationPreview(t, allowed, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText(key), "value": dataBinary(nil)}}))
}

func TestConsulNativeDataMutationNativeRequestLimitRejectionNeverSplitsOrReplays(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	cfg.Prefix = nativeDataPrefix(t)
	_, l := nativeDataSession(t, cfg, token)
	key := cfg.Prefix + "/complete"
	value := bytes.Repeat([]byte{0, 255}, 200<<10)
	p := nativeMutationPreview(t, l, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText(key), "value": dataBinary(value)}}, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText(cfg.Prefix + "/other"), "value": dataBinary(nil)}})
	out, err := l.submit(context.Background(), 91, "kv", "keys", p)
	if os.Getenv("NYADB_CONSUL_TXN_MAX_REQUEST") == "8388608" {
		if err != nil || out == nil || !out.CommitConfirmed || !bytes.Equal(nativeRead(t, writer, key).Value, value) {
			t.Fatal("explicit native request limit did not permit complete write", err)
		}
	} else {
		if err == nil || out == nil || out.CommitConfirmed || out.CommitUncertain || out.Incomplete || l.uncertain || l.write.LastResult().HTTP.Status != 413 {
			t.Fatal("native default request limit did not retain known rejection", err)
		}
		if n, err := l.count(context.Background(), 91, "kv", "keys"); err != nil || n != 0 {
			t.Fatal("native limit split a batch or applied a partial write", n, err)
		}
	}
	if _, err := l.submit(context.Background(), 91, "kv", "keys", p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("native request limit allowed replay", err)
	}
}
