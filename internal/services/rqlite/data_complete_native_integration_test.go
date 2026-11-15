package rqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type heldCompleteRead struct {
	base         http.RoundTripper
	marker       string
	started, ack chan struct{}
	release      sync.Once
	requests     atomic.Int32
	original     chan [2]string
}

func (h *heldCompleteRead) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	res, err := h.base.RoundTrip(req)
	if err != nil {
		return res, err
	}
	if req.URL.Path == "/db/query" && strings.Contains(string(body), h.marker) {
		raw, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			return nil, err
		}
		res.Body = io.NopCloser(bytes.NewReader(raw))
		if h.requests.Add(1) == 1 {
			h.original <- [2]string{string(body), string(raw)}
			close(h.started)
		}
		<-h.ack
	}
	return res, nil
}

func TestNativeRQLiteCompleteReadsWaitOriginalTransactionAcknowledgementOnCancelAndClose(t *testing.T) {
	p := nativeFixture(t, "")
	admin := openNative(t, p, "original-fixture-password")
	l := lease(t, admin, 1)
	nativeExecute(t, l, "CREATE TABLE held_complete(id INTEGER PRIMARY KEY,v INTEGER);INSERT INTO held_complete VALUES(1,-9223372036854775808),(2,9223372036854775807)")
	for _, kind := range []string{"analysis", "export", "page"} {
		t.Run(kind, func(t *testing.T) {
			s := openNative(t, p, "original-fixture-password")
			marker := map[string]string{"analysis": "_nyadb_numeric", "export": "_nyadb_export_rows", "page": "_nyadb_cursor_values"}[kind]
			h := &heldCompleteRead{base: s.transport, marker: marker, started: make(chan struct{}), ack: make(chan struct{}), original: make(chan [2]string, 1)}
			defer h.release.Do(func() { close(h.ack) })
			s.client.Transport = h
			d, runtime := nativeDataDocument(t, s, "held_complete")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var rows atomic.Int32
			result := make(chan error, 1)
			go func() {
				var err error
				switch kind {
				case "analysis":
					_, err = runtime.Analyzer(ctx, p.ID, "main", "held_complete", contract.DataOptions{}, []string{"v"})
				case "export":
					err = runtime.Exporter(ctx, p.ID, "main", "held_complete", contract.DataOptions{}, []string{"v"}, func([]contract.DataValue) error { rows.Add(1); return nil })
				case "page":
					_, err = runtime.AdvancedLoader(ctx, p.ID, "main", "held_complete", contract.DataOptions{Limit: 1, UseKeyset: true})
				}
				result <- err
			}()
			select {
			case <-h.started:
			case <-time.After(10 * time.Second):
				t.Fatal("original native read never reached its captured acknowledgement")
			}
			original := <-h.original
			var statements []any
			if err := json.Unmarshal([]byte(original[0]), &statements); err != nil || len(statements) < 2 || !strings.Contains(statements[0].(string), "schema_version") || !json.Valid([]byte(original[1])) {
				t.Fatal("read did not retain its complete original guarded transaction")
			}
			if kind == "analysis" && len(statements) != 6 {
				t.Fatal("analysis split original moments into different native transactions")
			}
			cancel()
			closed := make(chan struct{})
			go func() { _ = d.Close(); close(closed) }()
			select {
			case <-result:
				t.Fatal("caller cancellation discarded original native acknowledgement")
			case <-closed:
				t.Fatal("document close discarded original native acknowledgement")
			case <-time.After(50 * time.Millisecond):
			}
			if rows.Load() != 0 {
				t.Fatal("export delivered rows before original acknowledgement")
			}
			h.release.Do(func() { close(h.ack) })
			select {
			case err := <-result:
				if !errors.Is(err, contract.ErrRevoked) {
					t.Fatal("closed original read published its revoked result", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("original read never settled after acknowledgement")
			}
			select {
			case <-closed:
			case <-time.After(10 * time.Second):
				t.Fatal("original document never released after acknowledgement")
			}
			if rows.Load() != 0 || h.requests.Load() != 1 || !admin.Alive() {
				t.Fatal("original native read replayed, published after close, or closed another handle")
			}
		})
	}
}
