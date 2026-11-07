package solr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func profileConfig(t *testing.T, cfg connections.SolrConfig) connections.Profile {
	t.Helper()
	cfg, err := connections.NormalizeSolrConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	p, err := connections.Defaults(connections.Solr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Host, p.Port = 71, u.Hostname(), uint16(port)
	p.Name = "owned native session"
	p.Username = cfg.Username
	if u.Scheme == "http" {
		p.TLSMode = "disable"
	}
	p.SolrOptions = connections.SolrOptions{Version: 1, Auth: cfg.Auth, ContextPath: u.Path, CAFile: cfg.CAFile, ServerName: cfg.ServerName}
	return p
}
func testSession(t *testing.T, cfg connections.SolrConfig, secret string) *Session {
	t.Helper()
	raw, err := OpenSession(context.Background(), profileConfig(t, cfg), secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func nodeJSON(boot int64, cloud bool) string {
	var i Identity
	_ = decodeJSON([]byte(testIdentity), &i)
	if boot > 0 {
		i.JVM.JMX.StartTime = time.UnixMilli(boot).UTC().Format(time.RFC3339Nano)
	}
	if cloud {
		i.Mode = "solrcloud"
		i.ZKHost = "owned-zookeeper:2181"
	}
	raw, _ := json.Marshal(i)
	return string(raw)
}

const testSchema = `{"name":"owned","version":1.7,"uniqueKey":"id","fields":[{"name":"id","type":"string","required":true,"stored":true},{"name":"exact_l","type":"plong","stored":true}],"dynamicFields":[{"name":"*_s","type":"string"}],"fieldTypes":[{"name":"string","class":"solr.StrField"}],"copyFields":[{"source":"id","dest":"copy_s"}]}`
const testConfig = `{"requestHandler":{"/select":{"class":"solr.SearchHandler"}},"updateRequestProcessorChain":{"owned":[{"class":"solr.LogUpdateProcessorFactory"}]}}`

func mockCore(name, instance, started string) string {
	return fmt.Sprintf(`{"name":%q,"instanceDir":%q,"dataDir":%q,"config":"solrconfig.xml","schema":"managed-schema.xml","startTime":%q}`, name, instance, instance+"/data", started)
}
func TestSolrSessionNamespaceBindingAndReadOnlyCompleteSchema(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/solr/admin/info/system":
			fmt.Fprint(w, nodeJSON(100, false))
		case "/solr/admin/cores":
			fmt.Fprint(w, `{"responseHeader":{"status":0},"status":{"owned":`+mockCore("owned", "/owned", "2026-11-08T00:00:00Z")+`}}`)
		case "/solr/owned/schema":
			fmt.Fprint(w, `{"responseHeader":{"status":0,"QTime":`+strconv.Itoa(int(calls.Load()))+`},"schema":`+testSchema+`}`)
		case "/solr/owned/config":
			fmt.Fprint(w, `{"responseHeader":{"status":0},"config":`+testConfig+`}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	s := testSession(t, connections.SolrConfig{Endpoint: server.URL}, "")
	ns, err := s.Namespaces(context.Background())
	if err != nil || len(ns) != 1 || !strings.HasPrefix(ns[0].Ref.Catalog, "solr-observation:") {
		t.Fatal(ns, err)
	}
	before := calls.Load()
	for _, n := range []contract.NamespaceRef{{ProfileID: 72, Catalog: s.catalog}, {ProfileID: 71, Catalog: "wrong"}, {ProfileID: 71, Catalog: s.catalog, Schema: "invented"}} {
		if _, err = s.Browse(context.Background(), n); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("unqualified namespace accepted", err)
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid namespace dispatched")
	}
	objects, err := s.Browse(context.Background(), ns[0].Ref)
	if err != nil || len(objects) != 1 || objects[0].Ref.Kind != "core" {
		t.Fatal(objects, err)
	}
	m, err := s.TableMetadata(context.Background(), objects[0].Ref)
	if err != nil || m.Editable || len(m.Columns) != 3 || len(m.Definitions) != 4 || m.Columns[0].Key != "" || !strings.Contains(m.Definitions[1].SQL, "copyFields") || !strings.Contains(m.Definitions[2].SQL, "updateRequestProcessorChain") {
		t.Fatal("incomplete or editable schema", m, err)
	}
	c := s.Capabilities()
	if !c.Query || !c.Metadata || !c.Objects || c.Documents || c.EditableTables {
		t.Fatal("unverified capabilities", c)
	}
}
func TestSolrSessionRequiresBootAndRevokesPreconnectedOriginalLeases(t *testing.T) {
	var boot atomic.Int64
	boot.Store(100)
	var dispatches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/solr/admin/info/system" {
			fmt.Fprint(w, nodeJSON(boot.Load(), false))
			return
		}
		dispatches.Add(1)
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	cfg := connections.SolrConfig{Endpoint: server.URL}
	boot.Store(0)
	if s, err := OpenSession(context.Background(), profileConfig(t, cfg), ""); err == nil {
		_ = s.Close()
		t.Fatal("missing epoch accepted")
	}
	boot.Store(100)
	s := testSession(t, cfg, "")
	first, _ := s.ReserveQuery(1)
	second, _ := s.ReserveQuery(2)
	if err := first.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	boot.Store(101)
	if _, err := first.Execute(context.Background(), "", "POST /owned/update\n[]"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("changed node dispatched", err)
	}
	if _, err := second.Execute(context.Background(), "", "GET /owned/select"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("other stale lease remained alive", err)
	}
	if _, err := first.Replace(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old session rebound", err)
	}
	if dispatches.Load() != 0 {
		t.Fatal("request replayed on new epoch")
	}
	fresh := testSession(t, cfg, "")
	if fresh.catalog == s.catalog {
		t.Fatal("changed observation reused namespace")
	}
}
func TestSolrMetadataRejectsChangedCoreSchemaAndConfigWithoutRetry(t *testing.T) {
	for _, change := range []string{"reload", "swap", "replace", "schema", "config", "malformed"} {
		t.Run(change, func(t *testing.T) {
			var status, schema, config atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/solr/admin/info/system":
					fmt.Fprint(w, nodeJSON(100, false))
				case "/solr/admin/cores":
					count := status.Add(1)
					instance, started := "/owned", "2026-11-08T00:00:00Z"
					if count > 1 && (change == "reload" || change == "replace") {
						started = "2026-11-08T00:00:01Z"
					}
					if count > 1 && change == "swap" {
						instance = "/other"
					}
					fmt.Fprint(w, `{"responseHeader":{"status":0},"status":{"owned":`+mockCore("owned", instance, started)+`}}`)
				case "/solr/owned/schema":
					value := testSchema
					if schema.Add(1) > 1 && change == "schema" {
						value = strings.Replace(value, `"plong"`, `"pdouble"`, 1)
					}
					if change == "malformed" {
						value = `{"name":"owned","fields":null,"fieldTypes":[]}`
					}
					fmt.Fprint(w, `{"responseHeader":{"status":0},"schema":`+value+`}`)
				case "/solr/owned/config":
					value := testConfig
					if config.Add(1) > 1 && change == "config" {
						value = strings.Replace(value, "LogUpdateProcessorFactory", "RunUpdateProcessorFactory", 1)
					}
					fmt.Fprint(w, `{"responseHeader":{"status":0},"config":`+value+`}`)
				}
			}))
			defer server.Close()
			s := testSession(t, connections.SolrConfig{Endpoint: server.URL}, "")
			_, err := s.TableMetadata(context.Background(), s.Namespace("").Object("core", "owned"))
			expected := error(ErrChanged)
			if change == "malformed" {
				expected = ErrProtocol
			}
			if !errors.Is(err, expected) {
				t.Fatal("stale metadata published", err)
			}
			if schema.Load() > 2 || config.Load() > 2 || status.Load() > 3 {
				t.Fatal("changed observation automatically retried")
			}
		})
	}
}
func TestSolrQueryUnknownReceiptPreservesExactBodyAndNeverReplays(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/solr/admin/info/system" {
			fmt.Fprint(w, nodeJSON(100, false))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			writes.Add(1)
			w.WriteHeader(409)
			fmt.Fprint(w, `{"error":{"message":"applied prefix may remain","exact":9007199254740993}}`)
			return
		}
		fmt.Fprint(w, `{"exact":9007199254740993,"decimal":1.123456789012345678901}`)
	}))
	defer server.Close()
	s := testSession(t, connections.SolrConfig{Endpoint: server.URL}, "")
	l, _ := s.ReserveQuery(1)
	result, err := l.Execute(context.Background(), "", "POST /owned/update\n[]")
	if !errors.Is(err, contract.ErrSQLSessionLost) || len(result.Steps) != 1 || result.Steps[0].State != "uncertain" || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") {
		t.Fatal("lost receipt changed", result, err)
	}
	if _, err = l.Execute(context.Background(), "", "POST /owned/update\n[]"); !errors.Is(err, contract.ErrSQLSessionLost) || writes.Load() != 1 {
		t.Fatal("unknown write replayed", err)
	}
	replacement, err := l.Replace()
	if err != nil || writes.Load() != 1 {
		t.Fatal("recovery replayed", err)
	}
	if result, err = replacement.Execute(context.Background(), s.catalog, "GET /owned/get"); err != nil || !strings.Contains(result.Sets[0].Rows[0][1].Text, "1.123456789012345678901") {
		t.Fatal("recovery lost exact response", err)
	}
	if _, err = replacement.Execute(context.Background(), "invented", "POST /owned/update\n[]"); err == nil || writes.Load() != 1 {
		t.Fatal("invalid namespace dispatched")
	}
}
func TestSolrQueryReleaseAndSessionCloseSettleOriginalExecutors(t *testing.T) {
	for _, closeSession := range []bool{false, true} {
		t.Run(strconv.FormatBool(closeSession), func(t *testing.T) {
			entered, acked := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/solr/admin/info/system" {
					fmt.Fprint(w, nodeJSON(100, false))
					return
				}
				if r.Method == "POST" {
					_, _ = io.Copy(io.Discard, r.Body)
					close(entered)
					<-r.Context().Done()
					close(acked)
					return
				}
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			cfg := connections.SolrConfig{Endpoint: server.URL}
			s := testSession(t, cfg, "")
			first, _ := s.ReserveQuery(1)
			other, _ := s.ReserveQuery(2)
			independent := testSession(t, cfg, "")
			fresh, _ := independent.ReserveQuery(1)
			done := make(chan error, 1)
			go func() { _, err := first.Execute(context.Background(), "", "POST /owned/update\n[]"); done <- err }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("executor never entered")
			}
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if closeSession {
						_ = s.Close()
					} else {
						first.Release()
					}
				}()
			}
			wg.Wait()
			select {
			case err := <-done:
				if !errors.Is(err, contract.ErrSQLSessionLost) || !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("executor not settled")
			}
			select {
			case <-acked:
			case <-time.After(5 * time.Second):
				t.Fatal("server cancellation not acknowledged")
			}
			if !closeSession {
				if _, err := other.Execute(context.Background(), "", "GET /owned/get"); err != nil {
					t.Fatal("independent lease closed", err)
				}
			}
			if _, err := fresh.Execute(context.Background(), "", "GET /owned/get"); err != nil {
				t.Fatal("other session closed", err)
			}
		})
	}
}
