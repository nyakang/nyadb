package shell

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

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestMeilisearchNativePublicOriginalDocumentParkedReadAndClose(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	upstream, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	uid := "owned_park_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	submit := func(method, path, body string) {
		t.Helper()
		receipt, _, err := upstream.Submit(ctx, meilisearch.Request{Method: method, Path: path, Body: []byte(body)})
		if err == nil {
			_, err = upstream.WaitTask(ctx, receipt, 30*time.Second)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	submit("POST", "/indexes", `{"uid":"`+uid+`","primaryKey":"id"}`)
	defer submit("DELETE", "/indexes/"+uid, "")
	submit("POST", "/indexes/"+uid+"/documents", `[{"id":"original","integer":9007199254740993}]`)
	entered, release, delivered, closing, acked := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var attempts atomic.Int32
	var pageDelivered, verified atomic.Bool
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		response, err := upstream.Do(r.Context(), meilisearch.Request{Method: r.Method, Path: r.URL.RequestURI(), Body: body})
		if err != nil && response.Status == 0 {
			w.WriteHeader(502)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/documents") && r.URL.Query().Get("limit") == "100" {
			switch attempts.Add(1) {
			case 1:
				close(entered)
				select {
				case <-release:
					pageDelivered.Store(true)
				case <-r.Context().Done():
					return
				}
			case 2:
				close(closing)
				<-r.Context().Done()
				close(acked)
				return
			}
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
		if pageDelivered.Load() && r.URL.Path == "/version" && verified.CompareAndSwap(false, true) {
			close(delivered)
		}
	}))
	defer relay.Close()
	ca := filepath.Join(t.TempDir(), "relay-ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := url.Parse(relay.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	p := parsed.Profile
	p.ID, p.Name, p.Host, p.Port, p.TLSMode = 0, "Original native data", endpoint.Hostname(), uint16(port), "verify-full"
	p.MeilisearchOptions.CAFile, p.MeilisearchOptions.ServerName, p.MeilisearchOptions.ContextPath = ca, "example.com", ""
	p.MeilisearchOptions.ClientCertFile, p.MeilisearchOptions.ClientKeyFile = "", ""
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p, err = profiles.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	other := p
	other.ID, other.Name = 0, "Parked target"
	other, err = profiles.Create(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = parsed.Password
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 900, 700)
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("original native data work did not settle")
		}
		tt.Frame()
	}
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" {
		t.Fatal(w.Error)
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, uid))
	wait(func() bool { return w.Pending == "" && w.Cache[uid] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: uid, Category: "文档", Object: "documents"})
	doc := s.currentTab()
	if doc.data == nil {
		t.Fatal("qualified original document missing")
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("original page did not enter relay")
	}
	s.selectProfileByName(other.Name)
	close(release)
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("parked page did not finish its original observation")
	}
	for range 10 {
		tt.Frame()
	}
	if !doc.data.Busy() || doc.data.Page() != nil || s.sessions.Current().ProfileID == p.ID || s.selectedProfileID != other.ID {
		t.Fatal("late native result changed the active profile or parked cache")
	}
	s.selectProfileByName(p.Name)
	s.selectTab(doc.id)
	wait(func() bool { return !doc.data.Busy() })
	if doc.data.Error() != "" || doc.data.Page().Rows[0][0].Text != "original" || !strings.Contains(doc.data.Page().Rows[0][1].Text, "9007199254740993") || attempts.Load() != 1 {
		t.Fatal("activation rebound or replayed original native read", doc.data.Error())
	}
	if err = clickMySQLDataToolbar(tt, "刷新表数据：documents"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closing:
	case <-ctx.Done():
		t.Fatal("original refresh did not enter")
	}
	s.closeActive()
	select {
	case <-acked:
	case <-time.After(5 * time.Second):
		t.Fatal("closed original document did not acknowledge its executor")
	}
	if attempts.Load() != 2 || !s.runtime.IsConnected(p.ID) {
		t.Fatal("document close replayed a read or closed its healthy session")
	}
	t.Log("native data result parked under a different original profile; activation consumed without replay; document close canceled only its captured native executor")
}
