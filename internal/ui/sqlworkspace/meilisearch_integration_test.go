package sqlworkspace

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
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
)

func TestMeilisearchNativeRESTLostWriteCancelAndExplicitRecovery(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	for _, fault := range []string{"lost", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
			admin, err := meilisearch.Open(ctx, cfg, parsed.Password)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			var writes atomic.Int32
			committed, acked := make(chan struct{}), make(chan struct{})
			id := "owned_sql_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			uid := id
			seed, _, err := admin.Submit(ctx, meilisearch.Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"` + uid + `","primaryKey":"id"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = admin.WaitTask(ctx, seed, 30*time.Second); err != nil {
				t.Fatal(err)
			}
			accepted := make(chan meilisearch.Receipt, 1)
			relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
				if err != nil {
					w.WriteHeader(502)
					return
				}
				response, err := admin.Do(r.Context(), meilisearch.Request{Method: r.Method, Path: strings.TrimPrefix(r.URL.RequestURI(), "/gateway/meilisearch"), Body: body})
				if err != nil && response.Status == 0 {
					w.WriteHeader(502)
					return
				}
				if r.Method == "POST" {
					if response.Status != 202 {
						t.Error("native original write was not admitted")
					}
					writes.Add(1)
					var receipt struct {
						TaskUID uint64 `json:"taskUid"`
					}
					if json.Unmarshal(response.Body, &receipt) != nil {
						t.Error("native receipt malformed")
					}
					// Read the original task through the native Submit codec's receipt fields.
					var fields struct {
						IndexUID   *string `json:"indexUid"`
						Status     string  `json:"status"`
						Type       string  `json:"type"`
						EnqueuedAt string  `json:"enqueuedAt"`
					}
					if json.Unmarshal(response.Body, &fields) != nil {
						t.Error("native receipt malformed")
					}
					accepted <- meilisearch.Receipt{UID: uint32(receipt.TaskUID), IndexUID: fields.IndexUID, Type: fields.Type, EnqueuedAt: fields.EnqueuedAt, Raw: response.Body}
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
						_ = conn.Close()
					}
					close(acked)
					return
				}
				w.Header().Set("Content-Type", response.ContentType)
				w.Header().Set("ETag", response.ETag)
				w.WriteHeader(response.Status)
				_, _ = w.Write(response.Body)
			}))
			defer relay.Close()
			ca := filepath.Join(t.TempDir(), "relay-ca.pem")
			if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			endpoint, _ := url.Parse(relay.URL)
			port, _ := strconv.Atoi(endpoint.Port())
			p := parsed.Profile
			p.Host, p.Port = endpoint.Hostname(), uint16(port)
			p.TLSMode = "verify-full"
			p.MeilisearchOptions.ContextPath, p.MeilisearchOptions.CAFile, p.MeilisearchOptions.ServerName = "/gateway/meilisearch", ca, "example.com"
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			if _, err = m.ConnectSession(ctx, p, parsed.Password); err != nil {
				t.Fatal(err)
			}
			runtime := EngineRuntime(m.Session)
			var status string
			s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: m.IsConnected, QueryDialect: func(int64) string { return "meilisearch" }, Namespace: m.Namespace, Status: func(v string) { status = v }})
			defer s.CloseAll()
			request := "POST /indexes/" + uid + "/documents\n[{\"id\":\"one\",\"n\":9007199254740993,\"text\":\"原始;请求🐱\"}]"
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
				t.Fatal("native server never committed")
			}
			if fault == "canceled" {
				s.RequestCancel(d)
				if !d.Loading() || !d.CancelRequested() {
					t.Fatal("busy cleared before original acknowledgement")
				}
			}
			wait()
			if !d.NeedsRecovery() || d.Editor.Buffer.String() != request || writes.Load() != 1 || strings.Contains(status, "SQL") || strings.Contains(d.Error(), "事务") {
				t.Fatal("native recovery lost request/scope", d.Error(), status)
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
			receipt := <-accepted
			if _, err = admin.WaitTask(ctx, receipt, 30*time.Second); err != nil {
				t.Fatal(err)
			}
			d.Editor.Buffer.Set("GET /indexes/" + uid + "/documents/one")
			s.Run(nil, d)
			wait()
			if d.Error() != "" || d.Result() == nil || !strings.Contains(d.Result().Sets[0].Rows[0][1].Text, "9007199254740993") || writes.Load() != 1 {
				t.Fatal("fresh original read failed", d.Error())
			}
			taskResponse, err := admin.Do(ctx, meilisearch.Request{Method: "GET", Path: "/tasks?indexUids=" + uid + "&types=documentAdditionOrUpdate"})
			var history struct {
				Results []json.RawMessage `json:"results"`
			}
			if err != nil || json.Unmarshal(taskResponse.Body, &history) != nil || len(history.Results) != 1 {
				t.Fatal("recovery resubmitted native task", err)
			}

		})
	}
}
