package dataworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func captureDocumentImport(t *testing.T, view func(*ui.Context), phase, action string) {
	t.Helper()
	root := os.Getenv("NYADB_MEILISEARCH_IMPORT_IMAGES")
	if root == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(view, size[0], size[1])
			r, ok := tt.Find(locales.Text(action))
			if !ok || r.X < 0 || r.Y < 0 || r.W <= 0 || r.H <= 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
				t.Fatal("native import action clipped", phase, language, r)
			}
			folder := filepath.Join(root, language)
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			f, err := os.Create(filepath.Join(folder, fmt.Sprintf("document-import-%s-%dx%d.png", phase, size[0], size[1])))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, tt.Image())
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
		}
	}
}

func TestMeilisearchNativePublicDocumentImportWizardJSONJSONLObservationAndEvidence(t *testing.T) {
	if os.Getenv("NYADB_MEILISEARCH_PUBLIC_IT") != "1" {
		t.Skip("requires owned native Meilisearch public fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_MEILISEARCH_CONFIG"))
	if err != nil {
		t.Fatal("private configuration unavailable")
	}
	var probe struct {
		Endpoint, Auth string
		CAFile         string `json:"ca_file"`
		ServerName     string `json:"server_name"`
		KeyFile        string `json:"key_file"`
		ClientCertFile string `json:"client_cert_file"`
		ClientKeyFile  string `json:"client_key_file"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private configuration")
	}
	secret := ""
	if probe.KeyFile != "" {
		raw, err = os.ReadFile(probe.KeyFile)
		if err != nil {
			t.Fatal("private key unavailable")
		}
		secret = string(raw)
	}
	cfg := connections.MeilisearchConfig{Endpoint: probe.Endpoint, Auth: probe.Auth, CAFile: probe.CAFile, ServerName: probe.ServerName, ClientCertFile: probe.ClientCertFile, ClientKeyFile: probe.ClientKeyFile}
	admin, err := meilisearch.Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	uid := "owned_import_ui_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	submit := func(method, path, body string) {
		t.Helper()
		r, _, e := admin.Submit(context.Background(), meilisearch.Request{Method: method, Path: path, Body: []byte(body)})
		if e == nil {
			_, e = admin.WaitTask(context.Background(), r, 30*time.Second)
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	submit("POST", "/indexes", `{"uid":"`+uid+`","primaryKey":"id"}`)
	defer submit("DELETE", "/indexes/"+uid, "")
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	p, err := connections.Defaults(connections.Meilisearch)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(endpoint.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Name, p.Host, p.Port = 73, "Native document import", endpoint.Hostname(), uint16(port)
	p.TLSMode = "disable"
	if endpoint.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	p.MeilisearchOptions = connections.MeilisearchOptions{Version: 1, Auth: cfg.Auth, ContextPath: endpoint.Path, CAFile: cfg.CAFile, ServerName: cfg.ServerName, ClientCertFile: cfg.ClientCertFile, ClientKeyFile: cfg.ClientKeyFile}
	manager := dbruntime.New()
	dbruntime.RegisterBuiltins(manager, nil)
	defer manager.Close()
	connected, err := manager.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	active := Session{ProfileID: p.ID, Generation: connected.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(int64) Session { return active }, Runtime: func() Runtime { return manager.Data(p.ID) }, RuntimeFor: manager.Data})
	defer s.CloseAll()
	d := s.OpenObject(1, contract.NamespaceRef{ProfileID: p.ID, Catalog: uid}.Object("documents", "documents"), connected.Capabilities())
	view := func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, d); s.Modals(c) }
	tt := ui.NewTester(view, 1280, 800)
	deadlineWait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			tt.Frame()
			if done() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("original import did not settle", d.transfer.Err)
	}
	s.Load(ui.Services{}, d, true)
	deadlineWait(func() bool { return !d.Busy() })
	if d.page == nil || !d.capabilities.Import || !tt.HasText(locales.Text("导入")) {
		t.Fatal("public native import capability/button absent")
	}
	var docs []string
	for i := 0; i < 251; i++ {
		docs = append(docs, fmt.Sprintf(`{"id":"row_%d","n":9007199254740993,"nested":{"old":true}}`, i))
	}
	path := filepath.Join(t.TempDir(), "documents.json")
	if err = os.WriteFile(path, []byte("["+strings.Join(docs, ",")+"]"), 0600); err != nil {
		t.Fatal(err)
	}
	click := func(label string) {
		t.Helper()
		if e := tt.Click(locales.Text(label)); e != nil {
			t.Fatal(e)
		}
		tt.Frame()
	}
	click("导入")
	d.transfer.OpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{path}, nil }
	captureDocumentImport(t, view, "source", "选择文件")
	click("选择文件")
	deadlineWait(func() bool { return !d.transfer.Busy })
	if d.transfer.TotalRows != 251 || d.transfer.Input == nil {
		t.Fatal("native JSON source lost")
	}
	click("下一步")
	captureDocumentImport(t, view, "mode", "下一步")
	click("下一步")
	d.transfer.Confirmation = uid
	tt.Frame()
	captureDocumentImport(t, view, "review", "确认导入")
	click("确认导入")
	deadlineWait(func() bool { return !d.Busy() })
	if d.transfer.Completed || !d.transfer.Uncertain || d.edit.Uncertain == nil || d.edit.LastAsync == nil {
		t.Fatal("admission inferred completion")
	}
	captureDocumentImport(t, view, "admitted", "观察任务结果")
	click("写入任务证据")
	captureDocumentImport(t, view, "evidence", "复制写入任务受理")
	click("复制写入任务受理")
	if !strings.Contains(tt.Clipboard(), `"taskUid"`) {
		t.Fatal("original task receipt not copied")
	}
	click("关闭")
	deadline := time.Now().Add(40 * time.Second)
	for !d.transfer.Completed && time.Now().Before(deadline) {
		s.VerifySave(ui.Services{}, d)
		deadlineWait(func() bool { return !d.Busy() })
		time.Sleep(10 * time.Millisecond)
	}
	if !d.transfer.Completed || d.transfer.Uncertain || d.edit.Uncertain != nil || d.transfer.Input == nil {
		t.Fatal("native readback/source settlement", d.transfer.Err)
	}
	d.transfer.Open = true
	tt.Frame()
	captureDocumentImport(t, view, "saved", "关闭")
	click("关闭")
	// Exercise JSONL through the same native picker and wizard with field merge.
	path = filepath.Join(t.TempDir(), "documents.jsonl")
	if err = os.WriteFile(path, []byte("{\"id\":\"row_0\",\"nested\":{\"new\":true}}\n{\"id\":\"jsonl_new\",\"n\":9007199254740993}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	click("导入")
	d.transfer.Format = "JSONL"
	d.transfer.OpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{path}, nil }
	click("选择文件")
	deadlineWait(func() bool { return !d.transfer.Busy })
	click("下一步")
	d.transfer.Mode = "document_merge"
	click("下一步")
	d.transfer.Confirmation = uid
	click("确认导入")
	deadlineWait(func() bool { return !d.Busy() })
	for !d.transfer.Completed && time.Now().Before(deadline) {
		s.VerifySave(ui.Services{}, d)
		deadlineWait(func() bool { return !d.Busy() })
		time.Sleep(10 * time.Millisecond)
	}
	if !d.transfer.Completed {
		t.Fatal("JSONL merge not confirmed", d.transfer.Err)
	}
	raw, _, err = admin.ReadDocument(context.Background(), uid, "row_0")
	if err != nil || !strings.Contains(string(raw), `"n":9007199254740993`) || strings.Contains(string(raw), `"old"`) || !strings.Contains(string(raw), `"new":true`) {
		t.Fatal("JSONL native merge semantics", err)
	}
	t.Log("public shared grid toolbar and JSON/JSONL native picker/wizard, 251-document batch, original receipts, explicit whole-import readback and native merge verified")
}
