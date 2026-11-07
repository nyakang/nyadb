package elasticsearch

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// The fault is applied to the real original server operation, not a synthetic
// write receipt. Late mode intentionally keeps that one captured request parked.
func nativeLostDocumentSession(t *testing.T, ctx context.Context, origin *Client, lostID string, late bool, before ...func()) (*Session, *atomic.Int32, func(), <-chan struct{}) {
	t.Helper()
	base, _ := url.Parse(origin.config.Endpoint)
	transport := origin.http.Transport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	upstream := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	allow, completed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(allow) }) }
	count := &atomic.Int32{}
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		lost := r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/_create/"+lostID)
		var hijacked bool
		if lost {
			if count.Add(1) != 1 {
				t.Error("captured native write was replayed")
			}
			if late {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				hijacked = true
				<-allow
			}
			if len(before) != 0 {
				before[0]()
			}
		}
		u := *r.URL
		u.Scheme, u.Host = base.Scheme, base.Host
		requestCtx := r.Context()
		if late && lost {
			requestCtx = ctx
		}
		request, err := http.NewRequestWithContext(requestCtx, r.Method, u.String(), strings.NewReader(string(body)))
		if err != nil {
			t.Error(err)
			return
		}
		request.Header = r.Header.Clone()
		response, err := upstream.Do(request)
		if err != nil {
			if lost {
				close(completed)
			}
			if !hijacked {
				w.WriteHeader(502)
			}
			return
		}
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if lost {
			if err != nil || response.StatusCode != 201 {
				t.Error("real lost-reply write was not committed")
			}
			if !hijacked && len(before) == 0 {
				conn, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					_ = conn.Close()
				}
			}
			close(completed)
			if len(before) == 0 {
				return
			}
		}
		for k, v := range response.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(func() { release(); proxy.Close() })
	ca := filepath.Join(t.TempDir(), "proxy-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := origin.config
	cfg.Endpoint, cfg.CAFile, cfg.ServerName = proxy.URL, ca, ""
	raw, err := OpenSession(ctx, sessionProfile(t, cfg), origin.secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	return s, count, release, completed
}
func TestElasticsearchNativeDataDocumentLostCommittedReplyVerifiesWithoutReplay(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	origin := nativeOpen(t, ctx)
	index := nativeIndex(t, origin, ctx)
	s, calls, _, completed := nativeLostDocumentSession(t, ctx, origin, "lost", false)
	l := nativeDataLease(t, s, "index", index)
	db := s.identity.ClusterUUID
	values := editDocument(`{"n":9007199254740993}`)
	values["_id"] = contract.DataValue{Text: "lost"}
	edit := []contract.DataEdit{{Kind: "insert", Values: values}}
	result, err := l.save(ctx, s.ProfileID(), db, index, edit)
	if !errors.Is(err, contract.ErrDataCommitUncertain) || result == nil || !result.CommitUncertain || result.Incomplete || result.Applied != 0 || len(result.Outcomes) != 1 || calls.Load() != 1 {
		t.Fatal("committed lost reply was replayed or falsely confirmed", err)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("real server did not finish original write")
	}
	if _, err = l.save(ctx, s.ProfileID(), db, index, edit); !errors.Is(err, contract.ErrDataCommitUncertain) || calls.Load() != 1 {
		t.Fatal("uncertain original request replayed", err)
	}
	tampered := cloneDocumentResult(result)
	tampered.Applied = 1
	if _, err = l.verify(ctx, s.ProfileID(), db, index, tampered); err == nil || calls.Load() != 1 {
		t.Fatal("altered receipt was accepted")
	}
	verification, err := l.verify(ctx, s.ProfileID(), db, index, cloneDocumentResult(result))
	if err != nil || verification.Status != "applied" || verification.Recovered == nil || !verification.Recovered.Verified || !verification.Recovered.CommitConfirmed || verification.Recovered.Applied != 1 || verification.Recovered.Outcomes[0].Saved.Mode != "fingerprints" || calls.Load() != 1 {
		t.Fatal("independent native verification failed", err)
	}
	actual, err := origin.GetDocument(ctx, DocumentID{Index: index, ID: "lost"})
	if err != nil || string(actual.Source) != `{"n":9007199254740993}` {
		t.Fatal("original native committed source missing", err)
	}
	if _, err = l.save(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "delete", Original: verification.Recovered.Outcomes[0].Saved}}); err != nil || calls.Load() != 1 {
		t.Fatal("verified native identity could not be used explicitly", err)
	}
}
func TestElasticsearchNativeDataDocumentCurrentAbsenceDoesNotAuthorizeLateWriteReplay(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	origin := nativeOpen(t, ctx)
	index := nativeIndex(t, origin, ctx)
	s, calls, release, completed := nativeLostDocumentSession(t, ctx, origin, "late", true)
	l := nativeDataLease(t, s, "index", index)
	db := s.identity.ClusterName
	values := editDocument(`{"n":9007199254740993}`)
	values["_id"] = contract.DataValue{Text: "late"}
	edit := []contract.DataEdit{{Kind: "insert", Values: values}}
	result, err := l.save(ctx, s.ProfileID(), db, index, edit)
	if !errors.Is(err, contract.ErrDataCommitUncertain) || result == nil || !result.CommitUncertain || calls.Load() != 1 {
		t.Fatal("late captured write was not held uncertain", err)
	}
	verification, err := l.verify(ctx, s.ProfileID(), db, index, result)
	if err != nil || verification.Status != "indeterminate" || len(verification.States) != 1 || verification.States[0] != "not_applied" || l.pending == nil {
		t.Fatal("current absence was treated as remote cancellation", err)
	}
	if _, err = l.save(ctx, s.ProfileID(), db, index, edit); !errors.Is(err, contract.ErrDataCommitUncertain) || calls.Load() != 1 {
		t.Fatal("absence observation authorized replay", err)
	}
	release()
	select {
	case <-completed:
	case <-time.After(10 * time.Second):
		t.Fatal("original captured write did not settle")
	}
	verification, err = l.verify(ctx, s.ProfileID(), db, index, result)
	if err != nil || verification.Status != "applied" || verification.Recovered == nil || calls.Load() != 1 {
		t.Fatal("late original write could not be independently recovered", err)
	}
}
func TestElasticsearchNativeDataDocumentLostMiddleReceiptRetainsWholeBatchDraft(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	origin := nativeOpen(t, ctx)
	index := nativeIndex(t, origin, ctx)
	s, calls, _, _ := nativeLostDocumentSession(t, ctx, origin, "lost_batch", false)
	l := nativeDataLease(t, s, "index", index)
	db := s.identity.ClusterUUID
	var edits []contract.DataEdit
	for _, id := range []string{"first", "lost_batch", "never"} {
		values := editDocument(`{"n":9007199254740993}`)
		values["_id"] = contract.DataValue{Text: id}
		edits = append(edits, contract.DataEdit{Kind: "insert", Values: values})
	}
	result, err := l.save(ctx, s.ProfileID(), db, index, edits)
	if !errors.Is(err, contract.ErrDataCommitUncertain) || result.Applied != 1 || !result.Incomplete || !result.CommitUncertain || len(result.Outcomes) != 2 || calls.Load() != 1 {
		t.Fatal("native partial result was reported complete", err)
	}
	verification, err := l.verify(ctx, s.ProfileID(), db, index, result)
	if err != nil || verification.Status != "indeterminate" || strings.Join(verification.States, ",") != "applied,applied,not_processed" || verification.Recovered != nil || l.pending == nil {
		t.Fatal("known attempted prefix became a recovered full batch", err)
	}
	if _, err = l.save(ctx, s.ProfileID(), db, index, edits); !errors.Is(err, contract.ErrDataCommitUncertain) || calls.Load() != 1 {
		t.Fatal("partial batch replayed", err)
	}
	if _, err = origin.GetDocument(ctx, DocumentID{Index: index, ID: "never"}); !isNativeStatus(err, 404) {
		t.Fatal("unprocessed edit was submitted", err)
	}
}

func TestElasticsearchNativeDataDocumentIndexReplacementDuringWriteCannotBecomeOriginalSuccess(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	origin := nativeOpen(t, ctx)
	index := nativeIndex(t, origin, ctx)
	originalUUID, err := indexUUID(ctx, origin, index)
	if err != nil {
		t.Fatal(err)
	}
	// Elasticsearch has no index UUID parameter in its document write condition.
	// Force replacement after the guard, before the real server resolves the PUT.
	hook := func() {
		if _, err := origin.Do(ctx, Request{Method: "DELETE", Path: "/" + index}); err != nil {
			t.Error(err)
			return
		}
		if _, err := origin.Do(ctx, Request{Method: "PUT", Path: "/" + index, Body: []byte(`{"settings":{"number_of_replicas":0}}`)}); err != nil {
			t.Error(err)
		}
	}
	s, calls, _, _ := nativeLostDocumentSession(t, ctx, origin, "race", false, hook)
	l := nativeDataLease(t, s, "index", index)
	db := s.identity.ClusterUUID
	values := editDocument(`{"n":1}`)
	values["_id"] = contract.DataValue{Text: "race"}
	result, err := l.save(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "insert", Values: values}})
	if !errors.Is(err, contract.ErrDataCommitUncertain) || result == nil || !result.CommitUncertain || result.CommitConfirmed || result.Applied != 1 || calls.Load() != 1 || l.pending == nil || !l.pending.unsafeIdentity {
		t.Fatal("replacement write was certified as original success", err)
	}
	newUUID, err := indexUUID(ctx, origin, index)
	if err != nil || newUUID == originalUUID {
		t.Fatal("real race did not replace the original index", err)
	}
	if _, err := origin.GetDocument(ctx, DocumentID{Index: index, ID: "race"}); err != nil {
		t.Fatal("test did not exercise a real replacement-index commit", err)
	}
	verification, err := l.verify(ctx, s.ProfileID(), db, index, result)
	if err != nil || verification.Status != "indeterminate" || verification.Recovered != nil || l.pending == nil {
		t.Fatal("replacement identity was adopted during verification", err)
	}
}
