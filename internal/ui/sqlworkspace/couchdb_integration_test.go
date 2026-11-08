package sqlworkspace

import (
	"context"
	"encoding/json"
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
	"github.com/nyakang/nyadb/internal/services/couchdb"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func couchdbPublicFixture(t *testing.T, configName string) connections.URIImport {
	t.Helper()
	if os.Getenv("NYADB_COUCHDB_WORKBENCH_IT") != "1" {
		t.Skip("requires owned native CouchDB workbench fixture")
	}
	raw, err := os.ReadFile(os.Getenv(configName))
	if err != nil {
		t.Fatal("private CouchDB fixture config unavailable")
	}
	var cfg struct {
		Endpoint, Auth, Username string
		CAFile                   string `json:"ca_file"`
		ServerName               string `json:"server_name"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("invalid private CouchDB config")
	}
	secret := ""
	if cfg.PasswordFile != "" {
		raw, err = os.ReadFile(cfg.PasswordFile)
		if err != nil {
			t.Fatal("private credential unavailable")
		}
		secret = string(raw)
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal("invalid private endpoint")
	}
	uri := url.URL{Scheme: "couchdb", Host: endpoint.Host, Path: endpoint.Path}
	options := url.Values{"auth": {cfg.Auth}, "sslmode": {"disable"}}
	if endpoint.Scheme == "https" {
		options.Set("sslmode", "verify-full")
	}
	if cfg.CAFile != "" {
		options.Set("ca_file", cfg.CAFile)
	}
	if cfg.ServerName != "" {
		options.Set("server_name", cfg.ServerName)
	}
	if cfg.Auth == "basic" {
		uri.User = url.UserPassword(cfg.Username, secret)
	}
	uri.RawQuery = options.Encode()
	parsed, err := connections.ParseURI(uri.String())
	if err != nil {
		t.Fatal("public CouchDB URI rejected", err)
	}
	parsed.Profile.ID, parsed.Profile.Name = 96, "Native CouchDB workbench"
	return parsed
}

func TestCouchDBNativeRESTLostWriteCancelAndExplicitRecovery(t *testing.T) {
	parsed := couchdbPublicFixture(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	for _, fault := range []string{"lost", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			cfg, _ := connections.CouchDBProfileConfig(parsed.Profile)
			admin, err := couchdb.Open(ctx, cfg, parsed.Password)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			var writes atomic.Int32
			committed, acked := make(chan struct{}), make(chan struct{})
			id := "owned_sql_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
				if err != nil {
					w.WriteHeader(502)
					return
				}
				response, err := admin.Do(r.Context(), couchdb.Request{Method: r.Method, Path: strings.TrimPrefix(r.URL.RequestURI(), "/gateway/couchdb"), Body: body})
				if err != nil && response.Status == 0 {
					w.WriteHeader(502)
					return
				}
				if r.Method == "PUT" {
					if response.Status != 201 {
						t.Error("native original write did not commit")
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
			p.CouchDBOptions.ContextPath, p.CouchDBOptions.CAFile, p.CouchDBOptions.ServerName = "/gateway/couchdb", ca, "example.com"
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			if _, err = m.ConnectSession(ctx, p, parsed.Password); err != nil {
				t.Fatal(err)
			}
			runtime := EngineRuntime(m.Session)
			var status string
			s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: m.IsConnected, QueryDialect: func(int64) string { return "couchdb" }, Namespace: m.Namespace, Status: func(v string) { status = v }})
			defer s.CloseAll()
			request := "PUT /nyadb_fixture/" + id + "\n{\"_id\":\"" + id + "\",\"n\":9007199254740993,\"text\":\"原始;请求🐱\"}"
			d := s.Open(1, request, p.ID, "nyadb_fixture", false)
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
			if d.Error() != "" || !d.restRequests() || d.Namespace().Catalog != "nyadb_fixture" || d.Namespace().Schema != "" {
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
			d.Editor.Buffer.Set("GET /nyadb_fixture/" + id)
			s.Run(nil, d)
			wait()
			if d.Error() != "" || d.Result() == nil || !strings.Contains(d.Result().Sets[0].Rows[0][1].Text, "9007199254740993") || writes.Load() != 1 {
				t.Fatal("fresh original read failed", d.Error())
			}
			var doc map[string]json.RawMessage
			if json.Unmarshal([]byte(d.Result().Sets[0].Rows[0][1].Text), &doc) != nil || !strings.HasPrefix(string(doc["_rev"]), `"1-`) {
				t.Fatal("recovery replayed committed PUT")
			}
		})
	}
}
