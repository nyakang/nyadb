package shell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
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
	"github.com/nyakang/nyadb/internal/services/snapshotfiles"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/locales"
)

func (h *qdrantManagementHarness) prepareSnapshot(t *testing.T, d *objectworkspace.Document, f objectworkspace.SnapshotManagementForm, selected string) {
	t.Helper()
	if !h.s.objectWorkspace.SetSnapshotManagementForm(d, f) {
		t.Fatal("original snapshot form not editable")
	}
	h.tt.Frame()
	h.click(t, "读取快照列表")
	h.wait(t, func() bool { return !d.Loading() })
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
	if selected != "" {
		found := false
		for i, entry := range h.s.objectWorkspace.SnapshotManagementSnapshot(d).Snapshots {
			if entry.Name == selected {
				h.s.objectWorkspace.SelectSnapshot(d, i)
				found = true
			}
		}
		if !found {
			t.Fatal("original archive missing", selected)
		}
	}
	h.click(t, "预览快照操作")
	h.wait(t, func() bool { return !d.Loading() })
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
	confirmation := ""
	if f.Action == "delete" {
		confirmation = selected
	}
	if f.Action == "upload" || f.Action == "recover" {
		confirmation = f.Collection
	}
	if confirmation != "" {
		if h.s.objectWorkspace.CanSubmitSnapshotManagement(d) {
			t.Fatal("destructive snapshot skipped confirmation")
		}
		h.click(t, "输入原名称确认操作")
		h.tt.Type(confirmation)
		h.tt.Frame()
	}
	if !h.s.objectWorkspace.CanSubmitSnapshotManagement(d) {
		t.Fatal("original snapshot preview cannot submit", d.Error())
	}
}
func (h *qdrantManagementHarness) submitSnapshot(t *testing.T, d *objectworkspace.Document) {
	t.Helper()
	h.click(t, "确认提交快照操作")
	h.wait(t, func() bool { return !d.Loading() })
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
}
func (h *qdrantManagementHarness) verifySnapshot(t *testing.T, d *objectworkspace.Document, state string) {
	t.Helper()
	h.click(t, "核验原快照操作")
	h.wait(t, func() bool { return !d.Loading() })
	r := h.s.objectWorkspace.SnapshotManagementResult(d)
	if d.Error() != "" || r.State != state || h.s.objectWorkspace.SnapshotManagementPending(d) {
		t.Fatal("snapshot verification", r.State, d.Error())
	}
}
func (h *qdrantManagementHarness) captureSnapshot(t *testing.T, panel string) {
	t.Helper()
	root := os.Getenv("NYADB_QDRANT_SNAPSHOT_IMAGES")
	if root == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			folder := filepath.Join(root, language)
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			tt := ui.NewTester(h.s.View, size[0], size[1])
			writeDomainCapture(t, folder, fmt.Sprintf("%s-%dx%d", panel, size[0], size[1]), tt)
			control := "读取快照列表"
			if panel == "preview" || panel == "restore-preview" {
				control = "确认提交快照操作"
			}
			if panel == "receipt" || panel == "unresolved" {
				control = "核验原快照操作"
			}
			r, ok := tt.Find(locales.Text(control))
			if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
				t.Fatal("snapshot control outside viewport", panel, language, r)
			}
		}
	}
	i18n.SetLocaleFunc(nil)
	h.tt.Frame()
}

func TestQdrantNativePublicSnapshotGraphicalScopesFilesRecoveryAndGuards(t *testing.T) {
	parsed := qdrantPublicFixture(t, "NYADB_QDRANT_CONFIG")
	h := newQdrantManagementHarness(t, parsed)
	s, p := h.s, h.profile
	name := "owned_snapshot_ui_" + fmt.Sprint(time.Now().UnixNano())
	other := name + "_other"
	for _, n := range []string{name, other} {
		h.run(t, "PUT", "/collections/"+n, `{"vectors":{"size":2,"distance":"Dot"},"shard_number":1}`)
		n := n
		t.Cleanup(func() {
			_, _ = h.admin.Do(context.Background(), qdrant.Request{Method: "DELETE", Path: "/collections/" + n})
		})
	}
	point := `{"points":[{"id":18446744073709551615,"vector":[1,0],"payload":{"n":9007199254740993,"text":"原快照🐱","null":null}}]}`
	h.run(t, "PUT", "/collections/"+name+"/points?wait=true", point)
	root := t.TempDir()
	destination := filepath.Join(root, "original.snapshot")
	var opens, saves int
	s.SetSnapshotFiles(objectworkspace.SnapshotFiles{Open: func() (string, error) { opens++; return destination, nil }, Save: func(string) (string, error) { saves++; return destination, nil }, Operations: snapshotfiles.Operations()})
	s.openSnapshotManagement(h.services, p.ID)
	h.tt.Frame()
	tab := s.currentTab()
	d := tab.object
	if d == nil || !d.SnapshotManagementMode() || s.workspaceTabCaption(tab) != locales.Text("快照管理") {
		t.Fatal("public snapshot document unavailable")
	}
	s.openSnapshotManagement(h.services, p.ID)
	if s.currentTab() != tab {
		t.Fatal("qualified snapshot duplicated")
	}
	f := s.objectWorkspace.SnapshotManagementForm(d)
	f.Collection = name
	f.Limit = "1024"
	f.Timeout = "120"
	s.objectWorkspace.SetSnapshotManagementForm(d, f)
	h.tt.Frame()
	h.captureSnapshot(t, "form")
	for _, scope := range []contract.SnapshotScope{contract.SnapshotCollection, contract.SnapshotShard, contract.SnapshotStorage} {
		f.Scope = scope
		f.Action = "create"
		f.Shard = "0"
		h.prepareSnapshot(t, d, f, "")
		if scope == contract.SnapshotCollection {
			h.captureSnapshot(t, "preview")
		}
		h.submitSnapshot(t, d)
		if !s.objectWorkspace.SnapshotManagementPending(d) {
			t.Fatal("native creation prematurely verified")
		}
		if scope == contract.SnapshotCollection {
			h.captureSnapshot(t, "receipt")
		}
		h.verifySnapshot(t, d, "applied")
		r := s.objectWorkspace.SnapshotManagementResult(d)
		if !r.Matched || r.Receipt.Description == nil {
			t.Fatal("original created archive missing")
		}
		archive := *r.Receipt.Description
		if scope == contract.SnapshotCollection {
			h.click(t, "归档列表")
			h.captureSnapshot(t, "list")
		}
		f.Action = "download"
		f.Destination = destination
		h.prepareSnapshot(t, d, f, archive.Name)
		h.submitSnapshot(t, d)
		r = s.objectWorkspace.SnapshotManagementResult(d)
		file := s.objectWorkspace.SnapshotFileResult(d)
		if r.State != "transferred" || !file.Published || file.Destination != destination || file.Bytes != archive.Size {
			t.Fatal("verified native file publication", r.State, file)
		}
		raw, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != r.Transfer.SHA256 || uint64(len(raw)) != archive.Size {
			t.Fatal("local archive changed native bytes")
		}
		if scope == contract.SnapshotCollection {
			h.captureSnapshot(t, "download")
			// The injected native picker callbacks feed the same guarded form.
			h.click(t, "快照操作")
			h.click(t, "选择下载目标")
			if saves != 1 {
				t.Fatal("download picker not wired")
			}
			f.Action = "upload"
			f.Source = destination
			s.objectWorkspace.SetSnapshotManagementForm(d, f)
			h.tt.Frame()
			h.click(t, "选择源归档")
			if opens != 1 {
				t.Fatal("source picker not wired")
			}
			// Open original affected and unrelated data documents plus a dirty REST query.
			for _, collection := range []string{name, other} {
				s.submitNavigation(h.services, navigation.DatabaseNode(p.ID, collection))
				h.wait(t, func() bool {
					return s.sessions.Current().Cache[collection] != nil && s.sessions.Current().Pending == ""
				})
				s.submitNavigation(h.services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: collection, Category: "向量点", Object: "points"})
				h.wait(t, func() bool { return !s.currentTab().data.Busy() })
			}
			affected, unrelated := s.tabs[len(s.tabs)-2], s.currentTab()
			s.openQdrantPoints(p.ID, name)
			query := s.currentTab()
			h.tt.Frame()
			query.query.Editor.Buffer.Set("GET /collections/" + name)
			h.tt.Frame()
			h.run(t, "PUT", "/collections/"+name+"/points?wait=true", `{"points":[{"id":2,"vector":[0,1]}]}`)
			s.selectTab(tab.id)
			h.tt.Frame()
			h.prepareSnapshot(t, d, f, "")
			h.captureSnapshot(t, "restore-preview")
			h.click(t, "确认提交快照操作")
			if !h.tt.HasText("保存查询更改？") || d.Loading() {
				t.Fatal("restoration bypassed original REST leave guard")
			}
			h.click(t, "取消")
			s.selectTab(tab.id)
			h.tt.Frame()
			h.click(t, "返回编辑")
			h.prepareSnapshot(t, d, f, "")
			h.click(t, "确认提交快照操作")
			h.click(t, "放弃")
			h.wait(t, func() bool { return !d.Loading() })
			if d.Error() != "" || slices.Contains(s.tabs, affected) || slices.Contains(s.tabs, query) || !slices.Contains(s.tabs, unrelated) {
				t.Fatal("restore affected-document cleanup", d.Error())
			}
			h.verifySnapshot(t, d, "observed")
			h.captureSnapshot(t, "restored")
			r = s.objectWorkspace.SnapshotManagementResult(d)
			if r.Matched || !r.HasCount || r.Count != 1 {
				t.Fatal("recovery fabricated archive equivalence", r.Count)
			}
			res, err := h.admin.Do(context.Background(), qdrant.Request{Method: "POST", Path: "/collections/" + name + "/points", Body: []byte(`{"ids":[18446744073709551615],"with_vector":true,"with_payload":true}`)})
			if err != nil || !strings.Contains(string(res.Body), "9007199254740993") || !strings.Contains(string(res.Body), "原快照🐱") {
				t.Fatal("original payload not restored", err)
			}
			f.Action = "recover"
			f.Location = "file:///qdrant/snapshots/" + name + "/" + archive.Name
			f.Checksum = hex.EncodeToString(sum[:])
			h.prepareSnapshot(t, d, f, "")
			h.submitSnapshot(t, d)
			h.verifySnapshot(t, d, "observed")
		}
		if scope == contract.SnapshotShard {
			f.Action = "stream"
			h.prepareSnapshot(t, d, f, "")
			h.submitSnapshot(t, d)
			if !s.objectWorkspace.SnapshotFileResult(d).Published {
				t.Fatal("shard stream not published")
			}
		}
		f.Action = "delete"
		h.prepareSnapshot(t, d, f, archive.Name)
		h.submitSnapshot(t, d)
		h.verifySnapshot(t, d, "applied")
	}
	// Closing an inactive snapshot tab keeps the active document identity.
	snapshot := s.workspaceSnapshot()
	for _, saved := range snapshot.Tabs {
		if saved.ID == tab.id && string(saved.Presentation) != `{"Snapshots":true}` {
			t.Fatal("snapshot persisted drafts or local paths")
		}
	}
	s.selectTab(1)
	active := s.currentTab().id
	s.closeTabID(tab.id)
	if s.currentTab().id != active || d.SnapshotManagementMode() {
		t.Fatal("inactive close changed active document or retained lease")
	}
}

func TestQdrantNativePublicSnapshotGraphicalLostReadbackAndReplacementNoReplay(t *testing.T) {
	parsed := qdrantPublicFixture(t, "NYADB_QDRANT_CONFIG")
	cfg, _ := connections.QdrantProfileConfig(parsed.Profile)
	admin, err := qdrant.Open(context.Background(), cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var writes atomic.Int32
	var armed atomic.Bool
	name := "owned_snapshot_fault_" + fmt.Sprint(time.Now().UnixNano())
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response, e := admin.Do(r.Context(), qdrant.Request{Method: r.Method, Path: r.URL.RequestURI()})
		if e != nil {
			w.WriteHeader(502)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/collections/"+name+"/snapshots" {
			writes.Add(1)
		}
		if r.Method == "GET" && r.URL.Path == "/collections/"+name && armed.CompareAndSwap(true, false) {
			conn, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	defer relay.Close()
	ca := filepath.Join(t.TempDir(), "relay.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(relay.URL)
	port, _ := strconv.Atoi(u.Port())
	proxy := parsed
	proxy.Profile.Host, proxy.Profile.Port, proxy.Profile.TLSMode = u.Hostname(), uint16(port), "verify-full"
	proxy.Profile.QdrantOptions.ContextPath = ""
	proxy.Profile.QdrantOptions.CAFile = ca
	proxy.Profile.QdrantOptions.ServerName = "example.com"
	proxy.Profile.QdrantOptions.ClientCertFile = ""
	proxy.Profile.QdrantOptions.ClientKeyFile = ""
	h := newQdrantManagementHarness(t, proxy)
	s, p := h.s, h.profile
	if _, err = admin.Do(context.Background(), qdrant.Request{Method: "PUT", Path: "/collections/" + name, Body: []byte(`{"vectors":{"size":2,"distance":"Dot"}}`)}); err != nil {
		t.Fatal(err)
	}
	defer admin.Do(context.Background(), qdrant.Request{Method: "DELETE", Path: "/collections/" + name})
	s.openSnapshotManagement(h.services, p.ID)
	h.tt.Frame()
	d := s.currentTab().object
	f := s.objectWorkspace.SnapshotManagementForm(d)
	f.Collection = name
	f.Timeout = "30"
	h.prepareSnapshot(t, d, f, "")
	h.submitSnapshot(t, d)
	original := s.objectWorkspace.SnapshotManagementResult(d)
	armed.Store(true)
	h.click(t, "核验原快照操作")
	h.wait(t, func() bool { return !d.Loading() })
	r := s.objectWorkspace.SnapshotManagementResult(d)
	if !s.objectWorkspace.SnapshotManagementPending(d) || string(r.Receipt.Response.Body) != string(original.Receipt.Response.Body) || d.Error() == "" {
		t.Fatal("readback loss erased original receipt")
	}
	h.captureSnapshot(t, "unresolved")
	h.verifySnapshot(t, d, "applied")
	if writes.Load() != 1 {
		t.Fatal("verification replayed original create")
	}
	h.prepareSnapshot(t, d, f, "")
	h.submitSnapshot(t, d)
	replacement, err := s.runtime.ConnectSession(context.Background(), p, proxy.Password)
	if err != nil {
		t.Fatal(err)
	}
	s.sessions.Current().Generation = replacement.Generation()
	h.tt.Frame()
	s.objectWorkspace.SubmitSnapshotManagement(d, h.services)
	s.objectWorkspace.VerifySnapshotManagement(d, nil)
	if d.Loading() || writes.Load() != 2 || !s.objectWorkspace.SnapshotManagementPending(d) || s.objectWorkspace.SnapshotManagementResult(d).Receipt.Description == nil {
		t.Fatal("old snapshot followed replacement")
	}
	s.objectWorkspace.Close(d)
	provider, err := contract.Require[contract.QueryProvider](replacement)
	if err != nil {
		t.Fatal(err)
	}
	query, err := provider.ReserveQuery(999)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Release()
	if _, err = query.Execute(context.Background(), "", "GET /collections"); err != nil {
		t.Fatal("old close affected replacement", err)
	}
}
