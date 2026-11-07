package sqlworkspace

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

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/easysearch"
)

// A real server commits first; the loopback relay loses the receipt or waits
// for cancellation. Recovery must retain the request and never submit it.
func TestEasysearchNativeRESTLostWriteCancelAndExplicitRecovery(t *testing.T) {
	if os.Getenv("NYADB_EASYSEARCH_IT") != "1" {
		t.Skip("requires owned disposable Easysearch")
	}
	for _, fault := range []string{"lost", "canceled"} {
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
			committed, acknowledged := make(chan struct{}), make(chan struct{})
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
				if r.Method == "PUT" && r.URL.Path == "/"+index+"/_doc/original" {
					writes.Add(1)
					close(committed)
					if fault == "canceled" {
						<-r.Context().Done()
						close(acknowledged)
						return
					}
					conn, _, e := w.(http.Hijacker).Hijack()
					if e != nil {
						t.Error(e)
						return
					}
					_ = conn.Close()
					return
				}
				w.Header().Set("Content-Type", response.ContentType)
				w.WriteHeader(response.Status)
				_, _ = w.Write(response.Body)
			}))
			defer relay.Close()
			endpoint, _ := url.Parse(relay.URL)
			port, _ := strconv.Atoi(endpoint.Port())
			p, _ := connections.Defaults(connections.Easysearch)
			p.ID = 75
			p.Name = "Owned native request recovery test"
			p.Host = endpoint.Hostname()
			p.Port = uint16(port)
			p.TLSMode = "disable"
			p.EasysearchOptions.Auth = "none"
			p.Username = ""
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			if _, err = m.ConnectSession(ctx, p, ""); err != nil {
				t.Fatal(err)
			}
			runtime := EngineRuntime(m.Session)
			var status string
			s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: m.IsConnected, QueryDialect: func(int64) string { return "easysearch" }, Namespace: m.Namespace, Status: func(v string) { status = v }})
			defer s.CloseAll()
			request := "PUT /" + index + "/_doc/original\n{\"name\":\"committed native request🐱\",\"exact\":9007199254740995}"
			d := s.Open(1, request, p.ID, admin.Identity().ClusterName, false)
			wait := func() {
				t.Helper()
				end := time.Now().Add(20 * time.Second)
				for (d.Loading() || d.prepareResults != nil) && time.Now().Before(end) {
					s.Consume()
					time.Sleep(time.Millisecond)
				}
				if d.Loading() || d.prepareResults != nil {
					t.Fatal("request worker did not settle")
				}
			}
			s.Prepare(d)
			wait()
			if d.Error() != "" {
				t.Fatal("native prepare", d.Error())
			}
			s.Run(nil, d)
			select {
			case <-committed:
			case <-time.After(15 * time.Second):
				t.Fatal("real server did not acknowledge write")
			}
			if fault == "canceled" {
				s.RequestCancel(d)
				if !d.Loading() || !d.CancelRequested() {
					t.Fatal("cancel cleared busy before acknowledgement")
				}
				select {
				case <-acknowledged:
				case <-time.After(10 * time.Second):
					t.Fatal("original relay did not settle cancellation")
				}
			}
			wait()
			if !d.NeedsRecovery() || d.Editor.Buffer.String() != request || writes.Load() != 1 || !strings.Contains(d.Error(), "请求") || strings.Contains(d.Error(), "事务") || strings.Contains(status, "SQL") {
				t.Fatal("native session loss lost request or used relational warning", d.Error(), status)
			}
			s.Run(nil, d)
			if d.Loading() || writes.Load() != 1 {
				t.Fatal("lost request replayed before recovery")
			}
			s.Recover(d)
			wait()
			if d.NeedsRecovery() || d.Loading() || d.Editor.Buffer.String() != request || writes.Load() != 1 || d.Error() != "" {
				t.Fatal("explicit recovery replayed or discarded request", d.Error())
			}
			d.Editor.Buffer.Set("GET /" + index + "/_doc/original")
			s.Run(nil, d)
			wait()
			if d.Error() != "" || d.Result() == nil || len(d.Result().Sets) != 1 || len(d.Result().Sets[0].Rows) != 1 || !strings.Contains(d.Result().Sets[0].Rows[0][1].Text, "9007199254740995") || writes.Load() != 1 {
				t.Fatal("explicit fresh read failed or replayed native write", d.Error())
			}
		})
	}
}
