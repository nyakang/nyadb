package dataworkspace

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/easysearch"
)

// The relay changes only receipts after the real server has acknowledged a
// write. The upstream client still verifies the disposable server's CA.
func TestEasysearchNativeSharedGridLostMalformedWritesVerifyWithoutReplay(t *testing.T) {
	if os.Getenv("NYADB_EASYSEARCH_IT") != "1" {
		t.Skip("requires owned disposable Easysearch")
	}
	for _, fault := range []string{"lost", "malformed"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			raw, err := os.ReadFile(os.Getenv("NYADB_EASYSEARCH_CONFIG"))
			if err != nil {
				t.Fatal("private probe unavailable")
			}
			var c struct {
				Endpoint, Username, Auth string
				CAFile                   string `json:"ca_file"`
				PasswordFile             string `json:"password_file"`
			}
			if json.Unmarshal(raw, &c) != nil {
				t.Fatal("invalid private probe")
			}
			secret, err := os.ReadFile(c.PasswordFile)
			if err != nil {
				t.Fatal("private secret unavailable")
			}
			admin, err := easysearch.Open(ctx, connections.EasysearchConfig{Endpoint: c.Endpoint, Auth: c.Auth, Username: c.Username, CAFile: c.CAFile}, string(secret))
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			index := "nyadb_grid_fault_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			run := func(method, path, body string) {
				t.Helper()
				if _, e := admin.Do(ctx, easysearch.Request{Method: method, Path: path, Body: []byte(body)}); e != nil {
					t.Fatal(e)
				}
			}
			run("PUT", "/"+index, `{"settings":{"number_of_replicas":0},"mappings":{"dynamic":false}}`)
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
				defer stop()
				if _, e := admin.Do(cleanup, easysearch.Request{Method: "DELETE", Path: "/" + index}); e != nil {
					t.Error("owned fault index cleanup", e)
				}
			}()
			run("PUT", "/"+index+"/_create/original?refresh=wait_for", `{"name":"before","exact":9007199254740993}`)
			var writes atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, e := io.ReadAll(io.LimitReader(r.Body, 2<<20))
				if e != nil {
					w.WriteHeader(502)
					return
				}
				response, e := admin.Do(r.Context(), easysearch.Request{Method: r.Method, Path: r.URL.RequestURI(), ContentType: r.Header.Get("Content-Type"), Body: body})
				if e != nil {
					w.WriteHeader(502)
					return
				}
				if r.Method == "PUT" && strings.Contains(r.URL.Path, "/_doc/") {
					writes.Add(1)
					if fault == "lost" {
						conn, _, e := w.(http.Hijacker).Hijack()
						if e != nil {
							t.Error(e)
							return
						}
						_ = conn.Close()
						return
					}
					response.Body = []byte(`{"result":"updated"}`)
				}
				w.Header().Set("Content-Type", response.ContentType)
				w.WriteHeader(response.Status)
				_, _ = w.Write(response.Body)
			}))
			defer relay.Close()
			endpoint, _ := url.Parse(relay.URL)
			port, _ := strconv.Atoi(endpoint.Port())
			p, _ := connections.Defaults(connections.Easysearch)
			p.ID = 74
			p.Name = "Owned native grid receipt test"
			p.Host = endpoint.Hostname()
			p.Port = uint16(port)
			p.TLSMode = "disable"
			p.EasysearchOptions.Auth = "none"
			p.Username = ""
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			handle, err := m.ConnectSession(ctx, p, "")
			if err != nil {
				t.Fatal(err)
			}
			session := Session{ProfileID: p.ID, Generation: handle.Generation(), Connected: true}
			s := New(Dependencies{Session: func() Session { return session }, SessionFor: func(id int64) Session {
				return Session{ProfileID: id, Generation: m.Generation(id), Connected: m.IsConnected(id)}
			}, RuntimeFor: m.Data})
			defer s.CloseAll()
			d := s.OpenObject(1, m.Namespace(p.ID, admin.Identity().ClusterName).Object("index", index), handle.Capabilities())
			wait := func() {
				t.Helper()
				end := time.Now().Add(20 * time.Second)
				for d.Busy() && time.Now().Before(end) {
					s.Consume(ui.Services{})
					time.Sleep(time.Millisecond)
				}
				if d.Busy() {
					t.Fatal("native grid worker did not settle")
				}
			}
			s.Load(ui.Services{}, d, false)
			wait()
			if d.err != "" || d.page == nil || len(d.page.Rows) != 1 {
				t.Fatal("native initial page", d.err)
			}
			d.edit.Batch = true
			source := `{"name":"retained receipt🐱","exact":9007199254740995}`
			s.EditCell(ui.Services{}, d, 0, "document", contract.Cell{Kind: contract.JSON, Text: source})
			s.Save(ui.Services{}, d)
			wait()
			if writes.Load() != 1 || !d.HasDraft() || d.edit.Uncertain == nil || d.edit.Err == "" {
				t.Fatal("unknown server write cleared draft or receipt", d.edit.Err, writes.Load())
			}
			s.Save(ui.Services{}, d)
			s.Consume(ui.Services{})
			if writes.Load() != 1 || d.edit.Busy || !d.HasDraft() {
				t.Fatal("Save replayed an uncertain write")
			}
			observed, err := admin.Do(ctx, easysearch.Request{Method: "GET", Path: "/" + index + "/_doc/original"})
			if err != nil || !strings.Contains(string(observed.Body), "9007199254740995") {
				t.Fatal("server did not commit native write", err)
			}
			s.VerifySave(ui.Services{}, d)
			wait()
			if writes.Load() != 1 || d.HasDraft() || d.edit.Uncertain != nil || !strings.Contains(d.edit.Err, "已核验") || d.err != "" || !strings.Contains(d.page.Rows[0][3].Text, "9007199254740995") {
				t.Fatal("explicit read verification failed or replayed write", d.edit.Err, d.err, writes.Load())
			}
			s.VerifySave(ui.Services{}, d)
			if writes.Load() != 1 || d.edit.Busy {
				t.Fatal("settled verification replayed write")
			}
		})
	}
}
