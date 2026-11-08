package sqlworkspace

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/qdrant"
)

func TestQdrantNativeRESTLostWriteCancelAndExplicitRecovery(t *testing.T) {
	parsed := qdrantPublicFixture(t, "NYADB_QDRANT_CONFIG")
	for _, fault := range []string{"lost", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			cfg, _ := connections.QdrantProfileConfig(parsed.Profile)
			admin, err := qdrant.Open(ctx, cfg, parsed.Password)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			uid := "owned_sql_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			if _, err := admin.Do(ctx, qdrant.Request{Method: "PUT", Path: "/collections/" + uid, Body: []byte(`{"vectors":{"size":2,"distance":"Dot"}}`)}); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := admin.Do(ctx, qdrant.Request{Method: "DELETE", Path: "/collections/" + uid}); err != nil {
					t.Error("owned collection cleanup", err)
				}
			}()
			var writes atomic.Int32
			committed, acked := make(chan struct{}), make(chan struct{})
			relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
				if err != nil {
					w.WriteHeader(502)
					return
				}
				response, err := admin.Do(r.Context(), qdrant.Request{Method: r.Method, Path: strings.TrimPrefix(r.URL.RequestURI(), "/gateway/qdrant"), Body: body})
				if err != nil && response.Status == 0 {
					w.WriteHeader(502)
					return
				}
				if r.Method == "PUT" && r.URL.Path == "/gateway/qdrant/collections/"+uid+"/points" {
					if response.Status != 200 || !strings.Contains(string(response.Body), `"completed"`) {
						t.Error("original native write did not complete")
					}
					writes.Add(1)
					close(committed)
					if fault == "canceled" {
						<-r.Context().Done()
						close(acked)
						return
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
					} else {
						conn.Close()
					}
					close(acked)
					return
				}
				w.Header().Set("Content-Type", response.ContentType)
				w.WriteHeader(response.Status)
				w.Write(response.Body)
			}))
			defer relay.Close()
			ca := filepath.Join(t.TempDir(), "relay-ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			endpoint, _ := url.Parse(relay.URL)
			port, _ := strconv.Atoi(endpoint.Port())
			p := parsed.Profile
			p.Host, p.Port, p.TLSMode = endpoint.Hostname(), uint16(port), "verify-full"
			p.QdrantOptions.ContextPath, p.QdrantOptions.CAFile, p.QdrantOptions.ServerName = "/gateway/qdrant", ca, "example.com"
			p.QdrantOptions.ClientCertFile, p.QdrantOptions.ClientKeyFile = "", ""
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			if _, err := m.ConnectSession(ctx, p, parsed.Password); err != nil {
				t.Fatal(err)
			}
			runtime := EngineRuntime(m.Session)
			var status string
			s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: m.IsConnected, QueryDialect: func(int64) string { return "qdrant" }, Namespace: m.Namespace, Status: func(v string) { status = v }})
			defer s.CloseAll()
			request := "PUT /collections/" + uid + "/points?wait=true\n{\"points\":[{\"id\":18446744073709551615,\"vector\":[1,0],\"payload\":{\"n\":9007199254740993,\"text\":\"原始;请求🐱\"}}]}"
			d := s.Open(1, request, p.ID, uid, false)
			wait := func() {
				t.Helper()
				end := time.Now().Add(20 * time.Second)
				for (d.Loading() || d.prepareResults != nil) && time.Now().Before(end) {
					s.Consume()
					time.Sleep(time.Millisecond)
				}
				if d.Loading() || d.prepareResults != nil {
					t.Fatal("original REST executor not settled")
				}
			}
			s.Prepare(d)
			wait()
			if d.Error() != "" || !d.restRequests() || d.Namespace().Catalog != uid || d.Namespace().Schema != "" {
				t.Fatal("native original preconnect/scope", d.Error())
			}
			s.Run(nil, d)
			select {
			case <-committed:
			case <-time.After(15 * time.Second):
				t.Fatal("native server never completed original request")
			}
			if fault == "canceled" {
				s.RequestCancel(d)
				if !d.Loading() || !d.CancelRequested() {
					t.Fatal("busy cleared before original acknowledgement")
				}
			}
			wait()
			if !d.NeedsRecovery() || d.Editor.Buffer.String() != request || writes.Load() != 1 || strings.Contains(status, "SQL") || strings.Contains(d.Error(), "事务") {
				t.Fatal("native recovery lost original request", d.Error(), status)
			}
			s.Run(nil, d)
			if d.Loading() || writes.Load() != 1 {
				t.Fatal("lost write replayed")
			}
			s.Recover(d)
			wait()
			if d.NeedsRecovery() || d.Error() != "" || d.Editor.Buffer.String() != request || writes.Load() != 1 {
				t.Fatal("explicit recovery replayed", d.Error())
			}
			select {
			case <-acked:
			case <-time.After(5 * time.Second):
				t.Fatal("original relay not settled")
			}
			d.Editor.Buffer.Set("GET /collections/" + uid + "/points/18446744073709551615?with_payload=true&with_vector=true")
			s.Run(nil, d)
			wait()
			if d.Error() != "" || d.Result() == nil || len(d.Result().Sets) != 2 || d.Result().Sets[0].Rows[0][0].Text != "18446744073709551615" || !strings.Contains(d.Result().Sets[1].Rows[0][1].Text, "9007199254740993") || writes.Load() != 1 {
				t.Fatal("fresh original typed read failed", d.Error())
			}
			count, _, err := admin.Count(ctx, uid, nil)
			if err != nil || count != 1 || writes.Load() != 1 {
				t.Fatal("recovery resubmitted original mutation", err)
			}
		})
	}
}
