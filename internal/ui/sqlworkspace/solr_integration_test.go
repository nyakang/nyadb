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
	"github.com/nyakang/nyadb/internal/services/solr"
)

// A real server commits first; the loopback relay loses the receipt or waits
// for cancellation. Recovery must retain the request and never submit it.
func TestSolrNativeRESTLostWriteCancelAndExplicitRecovery(t *testing.T) {
	if os.Getenv("NYADB_SOLR_WORKBENCH_IT") != "1" {
		t.Skip("requires owned disposable Solr")
	}
	for _, fault := range []string{"lost", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
			defer cancel()
			raw, err := os.ReadFile(os.Getenv("NYADB_SOLR_CONFIG"))
			if err != nil {
				t.Fatal("private probe unavailable")
			}
			var c struct {
				Endpoint, Username, Auth string
				CAFile                   string `json:"ca_file"`
				ServerName               string `json:"server_name"`
				PasswordFile             string `json:"password_file"`
			}
			if json.Unmarshal(raw, &c) != nil {
				t.Fatal("invalid private probe")
			}
			secret, err := os.ReadFile(c.PasswordFile)
			if err != nil {
				t.Fatal("private secret unavailable")
			}
			admin, err := solr.Open(ctx, connections.SolrConfig{Endpoint: c.Endpoint, Auth: c.Auth, Username: c.Username, CAFile: c.CAFile, ServerName: c.ServerName}, string(secret))
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			index := "nyadb_grid_fault_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			run := func(method, path, body string) {
				t.Helper()
				if _, e := admin.Do(ctx, solr.Request{Method: method, Path: path, Body: []byte(body)}); e != nil {
					t.Fatal(e)
				}
			}
			create, remove := "/admin/cores?action=CREATE&configSet=nyadb_edit&name=", "/admin/cores?action=UNLOAD&deleteInstanceDir=true&core="
			if admin.Identity().Mode == "solrcloud" {
				create, remove = "/admin/collections?action=CREATE&numShards=2&replicationFactor=1&collection.configName=nyadb_edit&name=", "/admin/collections?action=DELETE&name="
			}
			run("GET", create+index+"&wt=json", "")
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				if _, e := admin.Do(cleanup, solr.Request{Method: "GET", Path: remove + index + "&wt=json"}); e != nil {
					t.Error("owned fault target cleanup", e)
				}
			}()
			run("POST", "/"+index+"/update?commit=true&wt=json", `[{"id":"original","text_s":"before","exact_l":9007199254740993}]`)
			nativeEndpoint, _ := url.Parse(c.Endpoint)

			var writes atomic.Int32
			committed, acknowledged := make(chan struct{}), make(chan struct{})
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, e := io.ReadAll(io.LimitReader(r.Body, 2<<20))
				if e != nil {
					w.WriteHeader(502)
					return
				}
				response, e := admin.Do(r.Context(), solr.Request{Method: r.Method, Path: strings.TrimPrefix(r.URL.RequestURI(), nativeEndpoint.Path), ContentType: r.Header.Get("Content-Type"), Body: body})
				if e != nil {
					w.WriteHeader(502)
					return
				}
				if r.Method == "POST" && r.URL.Path == nativeEndpoint.Path+"/"+index+"/update" {
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
			p, _ := connections.Defaults(connections.Solr)
			p.ID = 75
			p.Name = "Owned native request recovery test"
			p.Host = endpoint.Hostname()
			p.Port = uint16(port)
			p.TLSMode = "disable"
			p.SolrOptions.Auth = "none"
			p.SolrOptions.ContextPath = nativeEndpoint.Path
			p.Username = ""
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			if _, err = m.ConnectSession(ctx, p, ""); err != nil {
				t.Fatal(err)
			}
			runtime := EngineRuntime(m.Session)
			var status string
			s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: m.IsConnected, QueryDialect: func(int64) string { return "solr" }, Namespace: m.Namespace, Status: func(v string) { status = v }})
			defer s.CloseAll()
			request := "POST /" + index + "/update?wt=json\n[{\"id\":\"original\",\"text_s\":\"committed native request🐱\",\"exact_l\":9007199254740995}]"
			d := s.Open(1, request, p.ID, "Solr · "+admin.Identity().Mode, false)
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
			d.Editor.Buffer.Set("GET /" + index + "/get?id=original&wt=json")
			s.Run(nil, d)
			wait()
			if d.Error() != "" || d.Result() == nil || len(d.Result().Sets) != 1 || len(d.Result().Sets[0].Rows) != 1 || !strings.Contains(d.Result().Sets[0].Rows[0][1].Text, "9007199254740995") || writes.Load() != 1 {
				t.Fatal("explicit fresh read failed or replayed native write", d.Error())
			}
		})
	}
}
