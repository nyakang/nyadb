package consul

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeDataSession(t *testing.T, cfg Config, token string) (*Session, *dataLease) {
	t.Helper()
	s, err := OpenNativeSession(context.Background(), 91, cfg, token)
	if err != nil {
		t.Fatal("original native data session unavailable", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dataTestLease(t, s)
}

func nativeDataPrefix(t *testing.T) string {
	return "native/data/" + strings.ReplaceAll(t.Name(), "/", "-")
}

func TestConsulNativeDataOriginalLiveBoundariesBytesUnsignedScopeAndReceipts(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, suffix := range []string{"canonical", "exact//"} {
		t.Run(suffix, func(t *testing.T) {
			prefix := nativeDataPrefix(t) + "/" + suffix
			cfg.Prefix, cfg.Datacenter, cfg.Consistency = prefix, "dc1", "consistent"
			max := uint64(math.MaxUint64)
			for _, name := range []string{"a", "b", "d"} {
				request, err := KeyPutRequest(prefix+"/"+name, bytes.Repeat([]byte{0, 255}, 1024), &max, nil)
				if err != nil {
					t.Fatal(err)
				}
				nativeDo(t, writer, request)
			}
			s, l := nativeDataSession(t, cfg, token)
			p := dataTestPage(t, l, contract.DataOptions{Limit: 2})
			if !p.LiveRead || !p.HasMore || p.Rows[0][0].Value.Text != prefix+"/a" || p.Rows[1][2].Value.Text != "18446744073709551615" || len(p.Rows[1][1].Value.Bytes) != 1024 || !p.Rows[1][1].Truncated || !*p.Editable {
				t.Fatal("actual native live data changed original bytes/flags or invented edit authority")
			}
			observed := l.scopes[dataPageScope].LastResult()
			if observed.HTTP == nil {
				t.Fatal("original native page receipt missing")
			}
			observedURL, err := url.Parse(observed.HTTP.URL)
			if observed.HTTP == nil || !observed.HTTP.Captured || !observed.HTTP.EOF || !observed.HTTP.Acknowledged || !observed.HTTP.Succeeded || err != nil || !observedURL.Query().Has("consistent") || observedURL.Query().Get("dc") != "dc1" {
				t.Fatal("original native page receipt or consistency missing")
			}
			if !canonicalHTTPKey(prefix) && observed.HTTP.Request.Path != "/v1/txn" {
				t.Fatal("noncanonical prefix was cleaned")
			}
			remove, _ := KeyDeleteRequest(prefix+"/b", nil, false)
			nativeDo(t, writer, remove)
			put, _ := KeyPutRequest(prefix+"/c", []byte{3}, nil, nil)
			nativeDo(t, writer, put)
			next := dataTestPage(t, l, contract.DataOptions{Offset: 2, Limit: 2, After: p.NextCursor})
			if len(next.Rows) != 2 || next.Rows[0][0].Value.Text != prefix+"/c" || next.Rows[1][0].Value.Text != prefix+"/d" || !next.LiveRead {
				t.Fatal("live changes became a fixed snapshot")
			}
			fresh := dataTestLease(t, s)
			if _, err := fresh.page(context.Background(), 91, "kv", "keys", contract.DataOptions{Offset: 2, Limit: 2, After: p.NextCursor}); err == nil {
				t.Fatal("native cursor crossed logical document")
			}
			fresh.Close()
			dataTestPage(t, l, contract.DataOptions{Limit: 2})
			if _, err := l.page(context.Background(), 91, "kv", "keys", contract.DataOptions{Offset: 2, Limit: 2, After: p.NextCursor}); err == nil {
				t.Fatal("native refresh accepted old epoch")
			}
		})
	}
}

func TestConsulNativeDataIndependentCountFieldExportAndOriginalRestrictedScope(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	prefix := nativeDataPrefix(t)
	value := bytes.Repeat([]byte{0, 255, 128}, 4096)
	max := uint64(math.MaxUint64)
	for i, raw := range [][]byte{value, {}, {0, 255}} {
		r, _ := KeyPutRequest(prefix+"/"+strconv.Itoa(i), raw, &max, nil)
		nativeDo(t, writer, r)
	}
	cfg.Prefix = prefix
	_, l := nativeDataSession(t, cfg, token)
	page := dataTestPage(t, l, contract.DataOptions{Limit: 1})
	count, err := l.count(context.Background(), 91, "kv", "keys")
	if err != nil || count != 3 {
		t.Fatal("actual original bounded count failed", err)
	}
	row, err := l.row(context.Background(), 91, "kv", "keys", *page.RowRefs[0])
	if err != nil || !bytes.Equal(row.Values["value"].Bytes, value) || row.Values["flags"].Text != "18446744073709551615" {
		t.Fatal("actual original complete field failed", err)
	}
	chunk, err := l.chunk(context.Background(), 91, "kv", "keys", "value", *page.RowRefs[0], 0, 1024)
	if err != nil || !chunk.Binary || chunk.NextOffset != 1024 || chunk.TotalUnits != int64(len(value)) {
		t.Fatal("actual original field limit changed", err)
	}
	var exported [][]contract.DataValue
	err = l.export(context.Background(), 91, "kv", "keys", contract.DataOptions{}, []string{"key", "value", "flags", "modify_index", "session"}, func(values []contract.DataValue) error {
		exported = append(exported, values)
		if len(exported) == 1 {
			r, _ := KeyPutRequest(prefix+"/2", []byte("changed after capture"), nil, nil)
			nativeDo(t, writer, r)
		}
		return nil
	})
	if err != nil || len(exported) != 3 || !bytes.Equal(exported[0][1].Bytes, value) || !bytes.Equal(exported[2][1].Bytes, []byte{0, 255}) || exported[0][2].Text != "18446744073709551615" {
		t.Fatal("actual export joined live observations", err)
	}
	for i, scope := range l.scopes {
		if scope.client == nil {
			t.Fatal("independent original scope not exercised", i)
		}
		for j := i + 1; j < len(l.scopes); j++ {
			if scope.client == l.scopes[j].client {
				t.Fatal("readers shared a physical executor")
			}
		}
	}
	if readToken := os.Getenv("NYADB_CONSUL_READ_TOKEN"); readToken != "" {
		allowed := "allowed/data/" + strings.ReplaceAll(t.Name(), "/", "-")
		r, _ := KeyPutRequest(allowed+"/key", []byte{0, 255}, nil, nil)
		nativeDo(t, writer, r)
		limited := cfg
		limited.Auth, limited.Prefix = "token", allowed
		s, lease := nativeDataSession(t, limited, readToken)
		p := dataTestPage(t, lease, contract.DataOptions{Limit: 10})
		if len(p.Rows) != 1 || p.Rows[0][0].Value.Text != allowed+"/key" || s.KVScope().HTTP.AgentAvailable {
			t.Fatal("original restricted scope or unavailable identity was invented")
		}
		if n, err := lease.count(context.Background(), 91, "kv", "keys"); err != nil || n != 1 {
			t.Fatal("restricted count failed", err)
		}
	}
}

func nativeDataRelay(t *testing.T, cfg Config, token, mode string) (Config, *atomic.Int32, <-chan struct{}) {
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
		request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, cfg.Endpoint+incoming.URL.RequestURI(), incoming.Body)
		if err != nil {
			t.Error("relay request invalid")
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
			t.Error("actual original response incomplete")
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if incoming.URL.Query().Has("recurse") && notified.CompareAndSwap(false, true) {
			calls.Add(1)
			close(processed)
			switch mode {
			case "partial":
				w.Header().Set("Content-Length", strconv.Itoa(len(body)+50))
				w.WriteHeader(response.StatusCode)
				w.Write(body[:min(len(body), 10)])
				return
			case "lost":
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			case "cancel", "close", "session-close":
				<-incoming.Context().Done()
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

func TestConsulNativeDataActualProcessedReadReplyLossCancellationOriginalCloseAndReopen(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, mode := range []string{"lost", "partial", "cancel", "close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			cfg.Prefix = nativeDataPrefix(t)
			put, _ := KeyPutRequest(cfg.Prefix+"/key", []byte{0, 255}, nil, nil)
			nativeDo(t, writer, put)
			proxy, calls, processed := nativeDataRelay(t, cfg, token, mode)
			s, l := nativeDataSession(t, proxy, token)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() { _, err := l.page(ctx, 91, "kv", "keys", contract.DataOptions{Limit: 1}); finished <- err }()
			select {
			case <-processed:
			case <-time.After(10 * time.Second):
				t.Fatal("actual native read not processed")
			}
			switch mode {
			case "cancel":
				cancel()
			case "close":
				l.Close()
			case "session-close":
				s.Close()
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("lost original observation became a complete page")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("original executor did not acknowledge read termination")
			}
			if calls.Load() != 1 {
				t.Fatal("original read replayed")
			}
			if _, err := l.page(context.Background(), 91, "kv", "keys", contract.DataOptions{Limit: 1}); err == nil || calls.Load() != 1 {
				t.Fatal("lost original range retried")
			}
			if mode != "close" && mode != "session-close" {
				receipt := l.scopes[dataPageScope].LastResult()
				if receipt.HTTP == nil || receipt.HTTP.Acknowledged || receipt.HTTP.WriteOutcomeUnknown || !receipt.HTTP.Ended {
					t.Fatal("original incomplete read receipt changed")
				}
				fresh := dataTestLease(t, s)
				if p := dataTestPage(t, fresh, contract.DataOptions{Limit: 1}); len(p.Rows) != 1 {
					t.Fatal("explicit original reopen failed")
				}
				fresh.Close()
			}
			l.Close()
			if len(s.dataLeases) != 0 || l.key != ([32]byte{}) {
				t.Fatal("original close retained data caches")
			}
		})
	}
}

func TestConsulNativeDataCompleteValueBudgetRejectsCountAndExportBeforeEmission(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	cfg.Prefix = nativeDataPrefix(t)
	value := bytes.Repeat([]byte{0, 255}, valueLimit/2)
	for i := 0; i < 65; i++ {
		put, err := KeyPutRequest(cfg.Prefix+"/"+strconv.Itoa(i), value, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		nativeDo(t, writer, put)
	}
	_, l := nativeDataSession(t, cfg, token)
	if _, err := l.count(context.Background(), 91, "kv", "keys"); err == nil {
		t.Fatal("oversize observation became a complete row count")
	}
	emitted := 0
	err := l.export(context.Background(), 91, "kv", "keys", contract.DataOptions{}, []string{"key", "value"}, func([]contract.DataValue) error { emitted++; return nil })
	if err == nil || emitted != 0 {
		t.Fatal("oversize export emitted a partial complete document")
	}
	if _, err := l.page(context.Background(), 91, "kv", "keys", contract.DataOptions{Limit: 1}); err == nil {
		t.Fatal("oversize observation became successful partial paging")
	}
	if _, err := l.page(context.Background(), 91, "kv", "keys", contract.DataOptions{Limit: 1}); !errors.Is(err, errDataReadLost) {
		t.Fatal("failed original observation was silently retried", err)
	}
	l.Close()
	if l.key != ([32]byte{}) {
		t.Fatal("oversize response cache retained after original close")
	}
}
