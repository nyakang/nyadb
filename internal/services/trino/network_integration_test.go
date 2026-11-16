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
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
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
	// Exercise the original-document INSERT path through the same real-product
	// lost-reply relay. A later successful read cannot acknowledge this write.
	p, _ := connections.Defaults(connections.Trino)
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	p.ID, p.Name, p.Host, p.Port, p.Username, p.Database = 397, "original lost INSERT", u.Hostname(), uint16(port), cfg.Username, "memory"
	p.TrinoOptions.DefaultSchema, p.TrinoOptions.CAFile = "default", ca
	rawSession, err := OpenSession(context.Background(), p, password)
	if err != nil {
		t.Fatal(err)
	}
	session := rawSession.(*Session)
	defer session.Close()
	if _, err = session.Namespaces(context.Background()); err != nil {
		t.Fatal(err)
	}
	o := contract.NamespaceRef{ProfileID: p.ID, Catalog: "memory", Schema: "default"}.Object("TABLE", name)
	document, err := session.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	runtime := document.Runtime()
	label := namespaceLabel(o.Catalog, o.Schema)
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"n": {Kind: contract.Integer, Text: "9007199254740995"}}}, {Kind: "insert", Values: map[string]contract.DataValue{"n": {Kind: contract.Integer, Text: "9007199254740996"}}}}
	plan, err := runtime.PreviewEdits(context.Background(), p.ID, label, name, edits)
	if err != nil {
		t.Fatal(err)
	}
	dropped.Store(false)
	receipt, err := runtime.SubmitEdits(context.Background(), p.ID, label, name, plan)
	if !dropped.Load() || !errors.Is(err, contract.ErrDataCommitUncertain) || receipt == nil || receipt.Applied != 0 || !receipt.CommitUncertain || receipt.Sequential.Statements[0].State != contract.DataWriteUnknown || receipt.Sequential.Statements[1].State != contract.DataWriteNotSent {
		t.Fatal("document lost original unknown write or sent suffix", err)
	}
	progress, valid := contract.SequentialDataWriteProgress(receipt, 2)
	if !valid || !progress.Unknown {
		t.Fatal("unknown receipt does not preserve original statement identity")
	}
	page, err := runtime.AdvancedLoader(context.Background(), p.ID, label, name, contract.DataOptions{Object: o, Limit: 10})
	if err != nil || len(page.Rows) != 2 {
		t.Fatal("original committed values or unsent suffix changed", err)
	}
	if _, err = runtime.PreviewEdits(context.Background(), p.ID, label, name, edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("read acknowledged the original unknown write", err)
	}
	if err = document.Close(); err != nil {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenSession(context.Background(), p, password)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	rows = nativeRun(t, original, "SELECT n FROM memory.default."+name)
	if len(rows) != 2 {
		t.Fatal("explicit recovery replayed native INSERT", rows)
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
