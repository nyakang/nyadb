package shell

import (
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/qdrant"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/locales"
)

type qdrantManagementHarness struct {
	s        *State
	tt       *ui.Tester
	services ui.Services
	profile  connections.Profile
	password string
	admin    *qdrant.Client
}

func newQdrantManagementHarness(t *testing.T, parsed connections.URIImport) *qdrantManagementHarness {
	t.Helper()
	s, _, profiles, _ := profileTestState(t)
	p, err := profiles.Create(context.Background(), parsed.Profile)
	if err != nil {
		t.Fatal(err)
	}
	h := &qdrantManagementHarness{s: s, profile: p, password: parsed.Password}
	h.tt = ui.NewTester(func(c *ui.Context) { h.services = c.Services(); s.View(c) }, 1280, 800)
	s.loadProfiles()
	s.sessions.Activate(p.ID)
	s.selectedProfileID = p.ID
	s.sessions.Current().Password = parsed.Password
	s.sessions.Connect(p, h.services.Invalidate)
	h.wait(t, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if s.sessions.StatusFor(p.ID) != "已连接" {
		t.Fatal(s.sessions.Current().Error)
	}
	cfg, _ := connections.QdrantProfileConfig(p)
	h.admin, err = qdrant.Open(context.Background(), cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.admin.Close() })
	return h
}
func (h *qdrantManagementHarness) wait(t *testing.T, done func() bool) {
	t.Helper()
	end := time.Now().Add(30 * time.Second)
	for !done() && time.Now().Before(end) {
		h.tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if !done() {
		t.Fatal("native original management did not settle")
	}
	h.tt.Frame()
}
func (h *qdrantManagementHarness) click(t *testing.T, label string) {
	t.Helper()
	if err := h.tt.Click(locales.Text(label)); err != nil {
		t.Fatal(label, err)
	}
	h.tt.Frame()
}
func (h *qdrantManagementHarness) run(t *testing.T, method, path, body string) {
	t.Helper()
	if _, err := h.admin.Do(context.Background(), qdrant.Request{Method: method, Path: path, Body: []byte(body)}); err != nil {
		t.Fatal(err)
	}
}
func (h *qdrantManagementHarness) capture(t *testing.T, panel string) {
	t.Helper()
	output := os.Getenv("NYADB_QDRANT_MANAGEMENT_IMAGES")
	if output == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			if err := os.MkdirAll(filepath.Join(output, language), 0700); err != nil {
				t.Fatal(err)
			}
			capture := ui.NewTester(h.s.View, size[0], size[1])
			writeDomainCapture(t, filepath.Join(output, language), fmt.Sprintf("%s-%dx%d", panel, size[0], size[1]), capture)
			control := "读取原集合信息"
			if panel == "receipt" || panel == "unresolved" {
				control = "核验原集合操作"
			}
			if panel == "preview" {
				control = "确认提交集合操作"
			}
			r, ok := capture.Find(locales.Text(control))
			if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
				t.Fatal("management control outside viewport", language, panel, r)
			}
		}
	}
	i18n.SetLocaleFunc(nil)
	h.tt.Frame()
}
func (h *qdrantManagementHarness) prepare(t *testing.T, d *objectworkspace.Document, f objectworkspace.CollectionManagementForm) {
	t.Helper()
	if !h.s.objectWorkspace.SetCollectionManagementForm(d, f) {
		t.Fatal("original form not editable")
	}
	h.tt.Frame()
	h.click(t, "读取原集合信息")
	h.wait(t, func() bool { return !d.Loading() })
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
	h.click(t, "预览集合操作")
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
	if f.Action == "collection_delete" || f.Action == "alias_delete" || f.Action == "alias_rename" || f.Action == "alias_retarget" {
		confirmation := f.Alias
		if f.Action == "collection_delete" {
			confirmation = f.Collection
		}
		h.click(t, "输入原名称确认操作")
		h.tt.Type(confirmation)
		h.tt.Frame()
	}
	if !h.s.objectWorkspace.CanSubmitCollectionManagement(d) {
		t.Fatal("reviewed original plan cannot submit", d.Error())
	}
}
func (h *qdrantManagementHarness) verify(t *testing.T, d *objectworkspace.Document) {
	t.Helper()
	h.click(t, "核验原集合操作")
	h.wait(t, func() bool { return !d.Loading() })
	r := h.s.objectWorkspace.CollectionManagementResult(d)
	if d.Error() != "" || r.State != "applied" || !r.Matched || h.s.objectWorkspace.CollectionManagementPending(d) {
		t.Fatal("native operation did not verify", d.Error(), r.State)
	}
}
func TestQdrantNativePublicCollectionManagementGraphicalActionsGuardsAndParkedEvidence(t *testing.T) {
	parsed := qdrantPublicFixture(t, "NYADB_QDRANT_CONFIG")
	h := newQdrantManagementHarness(t, parsed)
	s := h.s
	p := h.profile
	name := "owned_graphical_" + fmt.Sprint(time.Now().UnixNano())
	other := name + "_destination"
	alias := name + "_alias"
	renamed := alias + "_renamed"
	for _, n := range []string{name, other} {
		n := n
		t.Cleanup(func() {
			_, _ = h.admin.Do(context.Background(), qdrant.Request{Method: "DELETE", Path: "/collections/" + n})
		})
	}
	h.run(t, "PUT", "/collections/"+other, `{"vectors":{"size":3,"distance":"Cosine"}}`)
	s.openCollectionManagement(h.services, p.ID)
	h.tt.Frame()
	tab := s.currentTab()
	d := tab.object
	if d == nil || !d.CollectionManagementMode() {
		t.Fatal("public graphical management unavailable")
	}
	s.openCollectionManagement(h.services, p.ID)
	if s.currentTab() != tab {
		t.Fatal("qualified management document duplicated")
	}
	form := objectworkspace.CollectionManagementForm{Action: "collection_create", Collection: name, Timeout: "30", Configuration: `{"vectors":{"size":3,"distance":"Cosine"},"metadata":{"n":9007199254740993,"text":"原始配置🐱"}}`}
	s.objectWorkspace.SetCollectionManagementForm(d, form)
	h.tt.Frame()
	h.capture(t, "form")
	h.prepare(t, d, form)
	h.capture(t, "preview")
	h.click(t, "返回编辑")
	h.capture(t, "read")
	h.click(t, "预览集合操作")
	h.click(t, "确认提交集合操作")
	h.wait(t, func() bool { return !d.Loading() })
	r := s.objectWorkspace.CollectionManagementResult(d)
	if !slices.Contains(s.tabs, tab) || h.tt.HasText("保留原集合草稿与证据？") {
		t.Fatal("namespace refresh pruned global management")
	}
	if r.State != "response" || !r.Receipt.HasResult || !r.Receipt.Result || !s.objectWorkspace.CollectionManagementPending(d) {
		t.Fatal("boolean receipt prematurely verified", r.State, d.Error())
	}
	h.capture(t, "receipt")
	h.verify(t, d)
	h.capture(t, "verified")
	h.wait(t, func() bool { return slices.Contains(s.sessions.Current().Databases, name) })
	// The original REST query can reference any collection. Its dirty text must be guarded.
	s.submitNavigation(h.services, navigation.DatabaseNode(p.ID, name))
	h.wait(t, func() bool { return s.sessions.Current().Cache[name] != nil && s.sessions.Current().Pending == "" })
	s.submitNavigation(h.services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: name, Category: "向量点", Object: "points"})
	data := s.currentTab()
	h.wait(t, func() bool { return data.data != nil && !data.data.Busy() })
	s.submitNavigation(h.services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: name, Category: "集合信息", Object: name})
	definition := s.currentTab()
	h.wait(t, func() bool { return !definition.object.Loading() })
	s.submitNavigation(h.services, navigation.DatabaseNode(p.ID, other))
	h.wait(t, func() bool { return s.sessions.Current().Cache[other] != nil && s.sessions.Current().Pending == "" })
	s.submitNavigation(h.services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: other, Category: "集合信息", Object: other})
	unrelated := s.currentTab()
	h.wait(t, func() bool { return !unrelated.object.Loading() })
	s.openQdrantPoints(p.ID, other)
	query := s.currentTab()
	h.tt.Frame()
	query.query.Editor.Buffer.Set("GET /collections/" + name)
	h.tt.Frame()
	s.selectTab(tab.id)
	h.tt.Frame()
	form.Action = "collection_update"
	form.Configuration = `{"metadata":{"n":null,"nested":{"null":null},"exact":18446744073709551615}}`
	h.prepare(t, d, form)
	h.click(t, "确认提交集合操作")
	if !h.tt.HasText("保存查询更改？") || d.Loading() {
		t.Fatal("original REST draft was not guarded")
	}
	h.click(t, "取消")
	if !slices.Contains(s.tabs, data) || !slices.Contains(s.tabs, definition) || !slices.Contains(s.tabs, query) {
		t.Fatal("canceled guard disposed original documents")
	}
	// Returning to the preview revokes the abandoned continuation, allowing a fresh read/plan.
	s.selectTab(tab.id)
	h.tt.Frame()
	h.click(t, "返回编辑")
	h.prepare(t, d, form)
	h.click(t, "确认提交集合操作")
	h.click(t, "放弃")
	h.wait(t, func() bool { return !d.Loading() })
	if d.Error() != "" || slices.Contains(s.tabs, data) || slices.Contains(s.tabs, definition) || slices.Contains(s.tabs, query) || !slices.Contains(s.tabs, unrelated) {
		t.Fatal("original affected-document cleanup", d.Error())
	}
	h.verify(t, d)
	snapshot := s.objectWorkspace.CollectionManagementResult(d).Snapshot
	if len(snapshot.Configurations) != 1 || !strings.Contains(snapshot.Configurations[0].JSON, "18446744073709551615") || strings.Contains(snapshot.Configurations[0].JSON, `"n":9007199254740993`) {
		t.Fatal("native metadata update semantics lost")
	}
	for _, action := range []string{"alias_create", "alias_rename", "alias_retarget", "alias_delete"} {
		form = objectworkspace.CollectionManagementForm{Action: action, Collection: name, Alias: alias, Timeout: "30", DestinationAlias: renamed, DestinationCollection: other}
		if action == "alias_retarget" || action == "alias_delete" {
			form.Alias = renamed
		}
		if action == "alias_delete" {
			form.Collection = other
		}
		h.prepare(t, d, form)
		h.click(t, "确认提交集合操作")
		h.wait(t, func() bool { return !d.Loading() })
		h.verify(t, d)
	}
	// Both reads and writes remain parked until the original profile returns.
	form = objectworkspace.CollectionManagementForm{Action: "collection_update", Collection: name, Timeout: "30", Configuration: `{"metadata":{"parked":true}}`}
	h.prepare(t, d, form)
	s.objectWorkspace.SubmitCollectionManagement(d, h.services)
	s.sessions.Activate(p.ID + 1000)
	s.activeTab = 0
	for i := 0; i < 5; i++ {
		h.tt.Frame()
	}
	if !d.Loading() || s.objectWorkspace.CollectionManagementResult(d).Receipt.HasResult {
		t.Fatal("parked write consumed outside original profile")
	}
	s.sessions.Activate(p.ID)
	s.selectTab(tab.id)
	h.wait(t, func() bool { return !d.Loading() })
	h.verify(t, d)
	form.Action = "collection_delete"
	form.Configuration = ""
	h.prepare(t, d, form)
	h.click(t, "确认提交集合操作")
	h.wait(t, func() bool { return !d.Loading() })
	h.verify(t, d)
	h.wait(t, func() bool { return !slices.Contains(s.sessions.Current().Databases, name) })
}

func TestQdrantNativePublicCollectionManagementGraphicalFaultsOriginalGenerationAndNoReplay(t *testing.T) {
	parsed := qdrantPublicFixture(t, "NYADB_QDRANT_CONFIG")
	for _, fault := range []string{"lost", "canceled", "close", "verify", "replaced", "parked-read", "stale-guard"} {
		t.Run(fault, func(t *testing.T) {
			cfg, _ := connections.QdrantProfileConfig(parsed.Profile)
			admin, err := qdrant.Open(context.Background(), cfg, parsed.Password)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			name := "owned_graphical_fault_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			if _, err = admin.Do(context.Background(), qdrant.Request{Method: "PUT", Path: "/collections/" + name, Body: []byte(`{"vectors":{"size":3,"distance":"Cosine"}}`)}); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = admin.Do(context.Background(), qdrant.Request{Method: "DELETE", Path: "/collections/" + name})
			}()
			var writes atomic.Int32
			var armed atomic.Bool
			committed, acked, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
				if err != nil {
					w.WriteHeader(502)
					return
				}
				// Metadata refresh and explicit verification may overlap. Each
				// relayed request owns its upstream client; the independent
				// native inspector must never compete with proxy workers.
				upstream, err := qdrant.Open(r.Context(), cfg, parsed.Password)
				if err != nil {
					w.WriteHeader(502)
					return
				}
				defer upstream.Close()
				response, err := upstream.Do(r.Context(), qdrant.Request{Method: r.Method, Path: r.URL.RequestURI(), Body: body})
				if err != nil && response.Status == 0 {
					w.WriteHeader(502)
					return
				}
				isWrite := r.Method == "PATCH" && r.URL.Path == "/collections/"+name
				if isWrite {
					writes.Add(1)
				}
				failRead := fault == "verify" && r.Method == "GET" && r.URL.Path == "/collections/"+name && armed.CompareAndSwap(true, false)
				if isWrite && (fault == "lost" || fault == "canceled" || fault == "close") || failRead {
					close(committed)
					if fault == "canceled" || fault == "close" {
						select {
						case <-r.Context().Done():
						case <-release:
						}
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
				_, _ = w.Write(response.Body)
			}))
			defer relay.Close()
			defer close(release)
			ca := filepath.Join(t.TempDir(), "relay-ca.pem")
			if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			endpoint, _ := url.Parse(relay.URL)
			port, _ := strconv.Atoi(endpoint.Port())
			proxy := parsed
			proxy.Profile.Host, proxy.Profile.Port, proxy.Profile.TLSMode = endpoint.Hostname(), uint16(port), "verify-full"
			proxy.Profile.QdrantOptions.ContextPath = ""
			proxy.Profile.QdrantOptions.CAFile, proxy.Profile.QdrantOptions.ServerName = ca, "example.com"
			proxy.Profile.QdrantOptions.ClientCertFile, proxy.Profile.QdrantOptions.ClientKeyFile = "", ""
			h := newQdrantManagementHarness(t, proxy)
			s := h.s
			p := h.profile
			s.openCollectionManagement(h.services, p.ID)
			h.tt.Frame()
			tab := s.currentTab()
			d := tab.object
			form := objectworkspace.CollectionManagementForm{Action: "collection_update", Collection: name, Timeout: "30", Configuration: `{"metadata":{"once":9007199254740993}}`}
			if fault == "parked-read" {
				s.objectWorkspace.SetCollectionManagementForm(d, form)
				ready := make(chan struct{}, 1)
				s.objectWorkspace.ReadCollectionManagement(d, func() { ready <- struct{}{} })
				s.sessions.Activate(p.ID + 1000)
				s.activeTab = 0
				select {
				case <-ready:
				case <-time.After(15 * time.Second):
					t.Fatal("original read did not reply")
				}
				h.tt.Frame()
				if !d.Loading() || len(s.objectWorkspace.CollectionManagementSnapshot(d).Collections) != 0 {
					t.Fatal("parked read consumed")
				}
				s.sessions.Activate(p.ID)
				s.selectTab(tab.id)
				h.wait(t, func() bool { return !d.Loading() })
				if len(s.objectWorkspace.CollectionManagementSnapshot(d).Collections) == 0 {
					t.Fatal("parked original snapshot lost")
				}
				return
			}
			h.prepare(t, d, form)
			if fault == "stale-guard" {
				h.click(t, "返回编辑")
				s.openQdrantPoints(p.ID, name)
				query := s.currentTab()
				h.tt.Frame()
				query.query.Editor.Buffer.Set("GET /collections/" + name)
				h.tt.Frame()
				s.selectTab(tab.id)
				h.prepare(t, d, form)
				h.click(t, "确认提交集合操作")
				if !h.tt.HasText("保存查询更改？") {
					t.Fatal("stale guard was not opened")
				}
				replacement, err := s.runtime.ConnectSession(context.Background(), p, parsed.Password)
				if err != nil {
					t.Fatal(err)
				}
				s.sessions.Current().Generation = replacement.Generation()
				// A newly opened replacement document must survive the original continuation.
				s.openQdrantPoints(p.ID, name)
				fresh := s.currentTab()
				h.tt.Frame()
				s.selectTab(tab.id)
				h.tt.Frame()
				h.click(t, "放弃")
				if !slices.Contains(s.tabs, query) || !slices.Contains(s.tabs, fresh) || writes.Load() != 0 || d.Loading() {
					t.Fatal("stale guard closed/replayed replacement work")
				}
				return
			}
			h.click(t, "确认提交集合操作")
			if fault == "verify" {
				h.wait(t, func() bool { return !d.Loading() })
				armed.Store(true)
				h.click(t, "核验原集合操作")
			}
			if fault != "verify" && fault != "replaced" {
				select {
				case <-committed:
				case <-time.After(15 * time.Second):
					t.Fatal("original management never reached native server")
				}
			}
			if fault == "canceled" {
				s.objectWorkspace.CancelCollectionManagement(d)
				if !d.Loading() {
					t.Fatal("cancellation did not wait for executor")
				}
			}
			if fault == "close" {
				s.objectWorkspace.Close(d)
				select {
				case <-acked:
				case <-time.After(10 * time.Second):
					t.Fatal("original close failed to cancel executor")
				}
				if d.CollectionManagementMode() || writes.Load() != 1 {
					t.Fatal("closed management retained operation")
				}
				return
			}
			if fault == "replaced" {
				h.wait(t, func() bool { return !d.Loading() })
				replacement, err := s.runtime.ConnectSession(context.Background(), p, parsed.Password)
				if err != nil {
					t.Fatal(err)
				}
				s.sessions.Current().Generation = replacement.Generation()
				// The original receipt remains attached to its old publication.
			}
			h.wait(t, func() bool { return !d.Loading() })
			r := s.objectWorkspace.CollectionManagementResult(d)
			if !s.objectWorkspace.CollectionManagementPending(d) || writes.Load() != 1 || s.objectWorkspace.CollectionManagementForm(d) != form {
				t.Fatal("unknown result lost draft or allowed replay", d.Error(), r.State)
			}
			if fault == "verify" || fault == "replaced" {
				if !r.Receipt.HasResult || !r.Receipt.Result {
					t.Fatal("original boolean receipt lost", r.State)
				}
			}
			if fault == "verify" {
				h.capture(t, "unresolved")
			}
			s.objectWorkspace.ReadCollectionManagement(d, nil)
			s.objectWorkspace.SubmitCollectionManagement(d, h.services)
			if s.objectWorkspace.SetCollectionManagementForm(d, form) {
				t.Fatal("uncertain operation became editable before verification")
			}
			s.objectWorkspace.VerifyCollectionManagement(d, nil)
			if d.Loading() {
				h.wait(t, func() bool { return !d.Loading() })
			}
			if writes.Load() != 1 {
				t.Fatal("uncertain management replayed or became editable")
			}
			if fault == "verify" && s.objectWorkspace.CollectionManagementResult(d).State != "applied" {
				t.Fatal("fresh explicit original readback failed")
			}
			info, _, err := admin.Collection(context.Background(), name)
			if err != nil || !strings.Contains(string(info.Result), "9007199254740993") {
				t.Fatal("original native effect missing", err)
			}
			if fault == "replaced" {
				s.objectWorkspace.Close(d)
				session, err := s.runtime.Session(p.ID)
				if err != nil {
					t.Fatal(err)
				}
				provider, err := contract.Require[contract.QueryProvider](session)
				if err != nil {
					t.Fatal(err)
				}
				lease, err := provider.ReserveQuery(9999)
				if err != nil {
					t.Fatal("old close damaged replacement", err)
				}
				defer lease.Release()
				if _, err = lease.Execute(context.Background(), name, "GET /"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
