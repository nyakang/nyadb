package trino

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrinoNativeCommittedWriteLostReplyNeverReplays(t *testing.T) {
	cfg, password := nativeConfig(t)
	original, err := Open(context.Background(), cfg, password)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	name := "nyadb_loss_" + fmt.Sprint(time.Now().UnixNano())
	nativeRun(t, original, "CREATE TABLE memory.default."+name+"(n BIGINT)")
	defer nativeRun(t, original, "DROP TABLE memory.default."+name)
	upstream, _ := url.Parse(cfg.Endpoint)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Director = nil
	proxy.Rewrite = func(r *httputil.ProxyRequest) {
		r.SetURL(upstream)
		r.Out.Host = upstream.Host
		r.Out.Header.Del("Accept-Encoding") // Decode pages before fixture rewriting.
	}
	proxy.Transport = original.http.Transport
	var proxyURL string
	var armed, dropped atomic.Bool
	proxy.ModifyResponse = func(r *http.Response) error {
		raw, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			return err
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil {
			var next, update string
			_ = json.Unmarshal(obj["nextUri"], &next)
			_ = json.Unmarshal(obj["updateType"], &update)
			if next != "" {
				u, err := url.Parse(next)
				if err != nil {
					return err
				}
				obj["nextUri"], _ = json.Marshal(proxyURL + u.RequestURI())
				raw, _ = json.Marshal(obj)
			}
			if next == "" && update == "INSERT" && armed.Load() && dropped.CompareAndSwap(false, true) {
				r.StatusCode = http.StatusBadGateway
				r.Status = "502 Bad Gateway"
				raw = nil
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		r.Header.Del("Content-Length")
		return nil
	}
	server := httptest.NewUnstartedServer(proxy)
	server.StartTLS()
	defer server.Close()
	proxyURL = server.URL
	ca := filepath.Join(t.TempDir(), "proxy-ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	through := cfg
	through.Endpoint = server.URL
	through.CAFile = ca
	through.ServerName = ""
	client, err := Open(context.Background(), through, password)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	armed.Store(true)
	_, err = client.Run(context.Background(), "INSERT INTO memory.default."+name+" VALUES(9007199254740993)", nil)
	if !dropped.Load() || !errors.Is(err, ErrLost) {
		t.Fatal("real committed reply was not made uncertain", err)
	}
	if _, err = client.Run(context.Background(), "INSERT INTO memory.default."+name+" VALUES(9007199254740993)", nil); !errors.Is(err, ErrLost) {
		t.Fatal("uncertain write resumed implicitly", err)
	}
	rows := nativeRun(t, original, "SELECT n FROM memory.default."+name)
	if len(rows) != 1 || string(rows[0][0]) != "9007199254740993" {
		t.Fatal("native committed write was replayed or lost", rows)
	}
}

func TestTrinoNativeCloseReleasesOriginalTransaction(t *testing.T) {
	cfg, password := nativeConfig(t)
	c, err := Open(context.Background(), cfg, password)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	nativeRun(t, c, "START TRANSACTION READ ONLY")
	token := c.headers.Get("X-Trino-Transaction-Id")
	if token == "" || token == "NONE" {
		t.Fatal("original transaction not captured")
	}
	observer, err := Open(context.Background(), cfg, password)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	if rows := nativeRun(t, observer, "SELECT transaction_id FROM system.runtime.transactions WHERE transaction_id='"+token+"'"); len(rows) != 1 {
		t.Fatal("native transaction not present", rows)
	}
	if err = c.Close(); err != nil {
		t.Fatal("original transaction close failed", err)
	}
	if rows := nativeRun(t, observer, "SELECT transaction_id FROM system.runtime.transactions WHERE transaction_id='"+token+"'"); len(rows) != 0 {
		t.Fatal("close leaked original transaction", rows)
	}
}
