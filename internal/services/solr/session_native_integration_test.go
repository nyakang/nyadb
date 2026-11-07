package solr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeMetadataReady(t *testing.T, s *Session, name, kind string) *contract.TableStructure {
	t.Helper()
	end := time.Now().Add(60 * time.Second)
	for {
		m, err := s.TableMetadata(context.Background(), s.Namespace("").Object(kind, name))
		if err == nil {
			return m
		}
		if time.Now().After(end) {
			t.Fatal("native read endpoints not ready", err)
		}
		// Only an explicitly repeated metadata read is allowed here. No native
		// mutation is retried by readiness polling.
		time.Sleep(200 * time.Millisecond)
	}
}
func TestSolrNativeSessionMetadataRESTAndChangedBindings(t *testing.T) {
	if os.Getenv("NYADB_SOLR_SESSION_IT") != "1" {
		t.Skip("requires owned session acceptance")
	}
	cfg, secret := nativeFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 150*time.Second)
	defer stop()
	s := testSession(t, cfg, secret)
	ns, err := s.Namespaces(ctx)
	if err != nil || len(ns) != 1 {
		t.Fatal("native namespace", err)
	}
	objects, err := s.Browse(ctx, ns[0].Ref)
	if err != nil || len(objects) == 0 {
		t.Fatal("native core browser", err)
	}
	m := nativeMetadataReady(t, s, "nyadb_fixture", "core")
	if m.Editable || len(m.Columns) == 0 || len(m.Definitions) != 4 || !strings.Contains(m.Definitions[1].SQL, "dynamicFields") || !strings.Contains(m.Definitions[2].SQL, "requestHandler") {
		t.Fatal("native full structure", m)
	}
	lease, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	id := nativeID()
	result, err := lease.Execute(ctx, s.catalog, "POST /nyadb_fixture/update?commit=true\n"+fmt.Sprintf(`[{"id":%q,"exact_l":9007199254740993,"text_s":"原生 Solr 会话🐱"}]`, id))
	if err != nil || result.Steps[0].State != "completed" {
		t.Fatal("native REST update", err)
	}
	result, err = lease.Execute(ctx, s.catalog, "GET /nyadb_fixture/get?wt=json&id="+url.QueryEscape(id))
	if err != nil || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") || !strings.Contains(result.Sets[0].Rows[0][1].Text, "原生 Solr 会话") {
		t.Fatal("native exact query", result, err)
	}
	admin := nativeOpen(t, ctx)
	before := nativeRun(t, admin, ctx, Request{Method: "GET", Path: "/admin/cores?action=STATUS&indexInfo=false&wt=json"})
	nativeRun(t, admin, ctx, Request{Method: "GET", Path: "/admin/cores?action=RELOAD&core=nyadb_fixture&wt=json"})
	nativeMetadataReady(t, s, "nyadb_fixture", "core")
	after := nativeRun(t, admin, ctx, Request{Method: "GET", Path: "/admin/cores?action=STATUS&indexInfo=false&wt=json"})
	var a, b struct {
		Status map[string]struct {
			StartTime string `json:"startTime"`
		} `json:"status"`
	}
	if decodeJSON(before.Body, &a) != nil || decodeJSON(after.Body, &b) != nil || a.Status["nyadb_fixture"].StartTime == b.Status["nyadb_fixture"].StartTime {
		t.Fatal("native reload observation did not change")
	}
	// Execute a native reload after the first schema read through an owned TLS
	// relay. Metadata must reject the old core observation instead of retrying.
	var triggered atomic.Bool
	relay := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response *Response) bool {
		if strings.HasSuffix(r.URL.Path, "/nyadb_fixture/schema") && triggered.CompareAndSwap(false, true) {
			_, e := admin.Do(ctx, Request{Method: "GET", Path: "/admin/cores?action=RELOAD&core=nyadb_fixture&wt=json"})
			if e != nil {
				t.Error("native injected reload", e)
			}
		}
		return false
	})
	through := testSession(t, relay.config, "")
	if _, err = through.TableMetadata(ctx, through.Namespace("").Object("core", "nyadb_fixture")); !errors.Is(err, ErrChanged) || !triggered.Load() {
		t.Fatal("native changed core published", err)
	}
	// A preconnected lease rechecks the original native boot observation. A relay
	// fault changes only the observed boot, never submits the user's POST.
	var changed atomic.Bool
	var dispatched atomic.Int32
	epochRelay := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response *Response) bool {
		if strings.HasSuffix(r.URL.Path, "/admin/info/system") && changed.Load() {
			var i Identity
			if decodeJSON(response.Body, &i) != nil {
				t.Error("native epoch receipt")
			}
			started, e := time.Parse(time.RFC3339Nano, i.JVM.JMX.StartTime)
			if e != nil {
				t.Error("native boot timestamp", e)
			}
			i.JVM.JMX.StartTime = started.Add(time.Millisecond).UTC().Format(time.RFC3339Nano)
			response.Body, _ = json.Marshal(i)
		}
		if r.Method == "POST" {
			dispatched.Add(1)
		}
		return false
	})
	observed := testSession(t, epochRelay.config, "")
	original, _ := observed.ReserveQuery(1)
	if err = original.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	changed.Store(true)
	if _, err = original.Execute(ctx, "", "POST /nyadb_fixture/update\n[]"); !errors.Is(err, contract.ErrSQLSessionLost) || dispatched.Load() != 0 {
		t.Fatal("native changed epoch dispatched", err)
	}
}
func TestSolrNativeCloudSessionAliasesSchemasAndREST(t *testing.T) {
	if os.Getenv("NYADB_SOLR_CLOUD_IT") != "1" {
		t.Skip("requires owned native SolrCloud fixture")
	}
	cfg, secret := readNativeFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 180*time.Second)
	defer stop()
	s := testSession(t, cfg, secret)
	if s.identity.Mode != "solrcloud" || s.identity.ZKHost == "" {
		t.Fatal("native cloud identity")
	}
	ns, err := s.Namespaces(ctx)
	if err != nil || len(ns) != 1 {
		t.Fatal(err)
	}
	objects, err := s.Browse(ctx, ns[0].Ref)
	if err != nil || len(objects) != 4 {
		t.Fatal("native cloud collections/aliases", objects, err)
	}
	m := nativeMetadataReady(t, s, "CloudAlias", "alias")
	if m.Editable || len(m.Definitions) != 6 || !strings.Contains(m.Definitions[1].SQL, "uniqueKey") || !strings.Contains(m.Definitions[3].SQL, "owned_extra_s") {
		t.Fatal("native alias member structures lost", m)
	}
	lower := nativeMetadataReady(t, s, "cloudalias", "alias")
	if len(lower.Definitions) != 4 {
		t.Fatal("case-sensitive native aliases collapsed")
	}
	lease, _ := s.ReserveQuery(1)
	id := nativeID()
	result, err := lease.Execute(ctx, s.catalog, "POST /cloudalias/update?commit=true\n"+fmt.Sprintf(`[{"id":%q,"exact_l":9007199254740993,"text_s":"SolrCloud 原生🐱"}]`, id))
	if err != nil || result.Steps[0].State != "completed" {
		t.Fatal("native cloud REST update", err)
	}
	result, err = lease.Execute(ctx, s.catalog, "GET /cloudalias/get?wt=json&id="+url.QueryEscape(id))
	if err != nil || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") {
		t.Fatal("native cloud REST exact read", err)
	}
	admin, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var fired atomic.Bool
	relay := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response *Response) bool {
		if strings.HasSuffix(r.URL.Path, "/nyadb_cloud/schema") && fired.CompareAndSwap(false, true) {
			_, e := admin.Do(ctx, Request{Method: "GET", Path: "/admin/collections?action=CREATEALIAS&name=CloudAlias&collections=nyadb_second,nyadb_cloud&wt=json"})
			if e != nil {
				t.Error("native alias mutation", e)
			}
		}
		return false
	})
	original := testSession(t, relay.config, "")
	if _, err = original.TableMetadata(ctx, original.Namespace("").Object("alias", "CloudAlias")); !errors.Is(err, ErrChanged) || !fired.Load() {
		t.Fatal("native alias change published", err)
	}
	refreshed := nativeMetadataReady(t, s, "CloudAlias", "alias")
	if !strings.Contains(refreshed.Definition, `"Members":["nyadb_second","nyadb_cloud"]`) {
		t.Fatal("explicit alias refresh order lost")
	}
}
