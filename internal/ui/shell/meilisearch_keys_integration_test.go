package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/locales"
)

func keyPanelAction(tt *ui.Tester, label string, bottom bool) error {
	tt.Frame()
	label = locales.Text(label)
	if _, ok := tt.Find(label); ok {
		return tt.Click(label)
	}
	anchor := "刷新密钥列表"
	names := []string{"刷新密钥列表", "新建 API 密钥", "编辑密钥元数据", "预览删除密钥", "读取选中密钥", "恢复密钥连接", "停止密钥等待", "返回"}
	if bottom {
		anchor = "查看原始密钥"
		names = []string{"查看原始密钥", "复制原始密钥", "复制脱敏密钥详情", "复制密钥请求", "复制脱敏密钥响应"}
	}
	rect, ok := tt.Find(locales.Text(anchor))
	if !ok {
		return fmt.Errorf("missing key toolbar anchor")
	}
	for _, candidate := range names {
		if r, found := tt.Find(locales.Text(candidate)); found && r.Y+r.H/2 == rect.Y+rect.H/2 && r.X+r.W > rect.X+rect.W {
			rect = r
		}
	}
	tt.ClickAt(rect.X+rect.W+14, rect.Y+rect.H/2)
	return tt.ChooseMenuItem(label)
}
func keyPanelState(t *testing.T, p connections.Profile, secret string) (*State, *ui.Tester, *connections.Service, connections.Profile, *ui.Services) {
	t.Helper()
	s, _, profiles, _ := profileTestState(t)
	p.ID, p.Name = 0, "Native key panel "+strconv.FormatInt(time.Now().UnixNano(), 36)
	var err error
	p, err = profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = secret
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	if err = tt.Click(locales.Text("连接所选数据库")); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if s.sessions.StatusFor(p.ID) != "已连接" {
		t.Fatal("native key profile connection", s.sessions.Current().Error)
	}
	return s, tt, profiles, p, &services
}
func keyPanelOpen(t *testing.T, s *State, tt *ui.Tester) *objectworkspace.Document {
	t.Helper()
	if err := tt.Click(locales.Text("工具")); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.Text("管理 API 密钥…")); err != nil {
		t.Fatal(err)
	}
	d := s.currentTab().object
	if !d.KeysMode() {
		t.Fatal("public menu failed to open original key document")
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	return d
}
func keyPanelCapture(t *testing.T, s *State, phase string) {
	t.Helper()
	output := os.Getenv("NYADB_MEILISEARCH_KEY_PANEL_IMAGES")
	if output == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(s.View, size[0], size[1])
			label := "刷新密钥列表"
			switch phase {
			case "list", "recovered":
				if err := tt.Click(locales.Text("密钥列表")); err != nil {
					t.Fatal(err)
				}
			case "create":
				label = "预览密钥更改"
				if err := tt.Click(locales.Text("密钥草稿")); err != nil {
					t.Fatal(err)
				}
			case "preview", "delete":
				label = "确认提交密钥操作"
			case "guard":
				label = "继续处理密钥"
			case "details":
				if err := tt.Click(locales.Text("密钥详情")); err != nil {
					t.Fatal(err)
				}
			case "result":
				if err := tt.Click(locales.Text("密钥请求与响应")); err != nil {
					t.Fatal(err)
				}
			}
			if r, ok := tt.Find(locales.Text(label)); !ok || r.H <= 0 || r.Y < 0 || r.Y+r.H > float32(size[1])-18 {
				t.Fatal("localized key control outside viewport", phase, language, r)
			}
			folder := filepath.Join(output, language)
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			writeDomainCapture(t, folder, fmt.Sprintf("keys-%s-%dx%d", phase, size[0], size[1]), tt)
		}
	}
}
func keyPanelNative(t *testing.T, c *meilisearch.Client, method, path string, body any) map[string]json.RawMessage {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := c.Do(context.Background(), meilisearch.Request{Method: method, Path: path, Body: raw})
	if err != nil {
		t.Fatal("original native key request", err)
	}
	if r.Status == 204 && len(r.Body) == 0 {
		return nil
	}
	var out map[string]json.RawMessage
	if json.Unmarshal(r.Body, &out) != nil {
		t.Fatal("native key JSON unavailable")
	}
	return out
}
func keyPanelNativeString(t *testing.T, fields map[string]json.RawMessage, name string) string {
	t.Helper()
	var value string
	if json.Unmarshal(fields[name], &value) != nil {
		t.Fatal("native key field unavailable")
	}
	return value
}
func keyPanelAssertNoSecret(t *testing.T, s *State, d *objectworkspace.Document, secrets ...string) {
	t.Helper()
	values := []any{s.workspaceSnapshot(), s.objectWorkspace.KeysPage(d), s.objectWorkspace.KeyRecord(d), s.objectWorkspace.KeyResult(d)}
	for _, v := range values {
		raw, _ := json.Marshal(v)
		for _, secret := range secrets {
			if secret != "" && strings.Contains(string(raw), secret) {
				t.Fatal("public key state or workspace persisted a credential")
			}
		}
	}
}

func TestMeilisearchNativePublicKeysSynchronousCRUDSecretAndSnapshot(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	admin, err := meilisearch.Open(context.Background(), cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	prefix := "owned_ui_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	for i := 0; i < 23; i++ {
		keyPanelNative(t, admin, "POST", "/keys", map[string]any{"name": prefix + strconv.Itoa(i), "actions": []string{"search"}, "indexes": []string{"owned*"}, "expiresAt": nil})
	}
	s, tt, _, p, services := keyPanelState(t, parsed.Profile, parsed.Password)
	d := keyPanelOpen(t, s, tt)
	if d.Error() != "" || s.objectWorkspace.KeysPage(d).Total < 23 {
		t.Fatal("native public key list", d.Error())
	}
	owner := s.currentTab()
	if err = tt.RightClick(p.Name); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem(locales.Text("管理 API 密钥…")); err != nil {
		t.Fatal(err)
	}
	if s.currentTab() != owner {
		t.Fatal("connection menu duplicated global key document")
	}
	keyPanelCapture(t, s, "list")
	if err = tt.Click(locales.Text("密钥列表")); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click(locales.Text("下一页密钥")); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if s.objectWorkspace.KeysPage(d).Offset != 20 {
		t.Fatal("native key next offset changed")
	}
	if err = tt.Click(locales.Text("上一页密钥")); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if s.objectWorkspace.KeysPage(d).Offset != 0 {
		t.Fatal("native key previous offset changed")
	}
	if err = keyPanelAction(tt, "新建 API 密钥", false); err != nil {
		t.Fatal(err)
	}
	taskPanelField(t, tt, "密钥名称", prefix+"created")
	taskPanelField(t, tt, "密钥描述", "exact 原生🐱")
	taskPanelField(t, tt, "密钥 UID（新建时可留空）", "6d0c9182-dddc-4489-a135-e74eac3ad0dd")
	taskPanelField(t, tt, "密钥权限（逗号分隔）", "search,documents.get")
	taskPanelField(t, tt, "索引范围（逗号分隔）", "owned*")
	taskPanelField(t, tt, "到期时间（RFC3339，可留空）", "2030-01-01T00:00:00Z")
	keyPanelCapture(t, s, "create")
	if err = tt.Click(locales.Text("预览密钥更改")); err != nil {
		t.Fatal(err)
	}
	keyPanelCapture(t, s, "preview")
	if err = tt.Click(locales.Text("确认提交密钥操作")); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	created := s.objectWorkspace.KeyResult(d)
	if created.State != "completed" || created.Status != 201 || created.UID != "6d0c9182-dddc-4489-a135-e74eac3ad0dd" || created.Record.Name != prefix+"created" || d.DesignDirty() {
		t.Fatal("native public synchronous creation", d.Error(), created.State)
	}
	native := keyPanelNative(t, admin, "GET", "/keys/"+created.UID, nil)
	secret := keyPanelNativeString(t, native, "key")
	keyPanelAssertNoSecret(t, s, d, parsed.Password, secret)
	keyPanelCapture(t, s, "result")
	keyPanelCapture(t, s, "details")
	if err = keyPanelAction(tt, "复制原始密钥", true); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != secret {
		t.Fatal("explicit created-key copy did not return original bytes")
	}
	services.WriteClipboard("[redacted]")
	if err = keyPanelAction(tt, "查看原始密钥", true); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText(secret) {
		t.Fatal("explicit original key reveal missing")
	}
	keyPanelAssertNoSecret(t, s, d, parsed.Password, secret)
	if err = tt.Click(locales.Text("隐藏密钥")); err != nil {
		t.Fatal(err)
	}
	if tt.HasText(secret) {
		t.Fatal("temporary original secret remained visible after hiding")
	}
	if err = keyPanelAction(tt, "编辑密钥元数据", false); err != nil {
		t.Fatal(err)
	}
	taskPanelField(t, tt, "密钥名称", prefix+"updated")
	if err = tt.Click(locales.Text("预览密钥更改")); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click(locales.Text("确认提交密钥操作")); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	updated := s.objectWorkspace.KeyResult(d)
	if updated.State != "completed" || updated.Status != 200 || updated.Record.Name != prefix+"updated" || updated.Record.ExpiresAt != created.Record.ExpiresAt || strings.Join(updated.Record.Actions, ",") != strings.Join(created.Record.Actions, ",") {
		t.Fatal("native public metadata update changed policy or lost synchronous outcome", d.Error())
	}
	if err = keyPanelAction(tt, "恢复密钥连接", false); err != nil {
		t.Fatal(err)
	}
	keyPanelCapture(t, s, "recovered")
	if err = keyPanelAction(tt, "读取选中密钥", false); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if err = keyPanelAction(tt, "预览删除密钥", false); err != nil {
		t.Fatal(err)
	}
	if s.objectWorkspace.CanConfirmKey(d) {
		t.Fatal("deletion accepted without exact original UID confirmation")
	}
	keyPanelCapture(t, s, "delete")
	taskPanelField(t, tt, "输入原密钥 UID 确认删除", updated.UID)
	if !s.objectWorkspace.CanConfirmKey(d) {
		t.Fatal("exact original UID deletion confirmation rejected")
	}
	if err = tt.Click(locales.Text("确认提交密钥操作")); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if result := s.objectWorkspace.KeyResult(d); result.State != "completed" || result.Status != 204 || result.Response != "" {
		t.Fatal("native public deletion became task admission", d.Error())
	}
	r, e := admin.Do(context.Background(), meilisearch.Request{Method: "GET", Path: "/keys/" + updated.UID})
	if e == nil || r.Status != http.StatusNotFound {
		t.Fatal("independent original-native observer did not confirm deletion")
	}
	keyPanelAssertNoSecret(t, s, d, parsed.Password, secret)
	if err = keyPanelAction(tt, "新建 API 密钥", false); err != nil {
		t.Fatal(err)
	}
	s.closeTabID(owner.id)
	tt.Frame()
	if s.currentTab() != owner || !tt.HasText(locales.Text("保留原密钥草稿与证据？")) {
		t.Fatal("key creation draft bypassed existing workbench close guard")
	}
	keyPanelCapture(t, s, "guard")
	if err = tt.Click(locales.Text("放弃本地密钥草稿并离开")); err != nil {
		t.Fatal(err)
	}
	if s.tabIndex(owner.id) >= 0 || d.KeysMode() || !s.runtime.IsConnected(p.ID) {
		t.Fatal("key close leaked document or damaged original session")
	}
	t.Log("public connection and captured Tools menu entry, native key pages/back, pure creation and immutable metadata previews, synchronous201/200/204, exact original UID deletion confirmation, explicit temporary secret/copy, no secret in snapshots and existing close guard")
}

func TestMeilisearchNativePublicKeysAnonymousRefusalAndScope(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	if parsed.Password != "" {
		t.Fatal("requires owned anonymous fixture")
	}
	s, tt, _, _, _ := keyPanelState(t, parsed.Profile, parsed.Password)
	d := keyPanelOpen(t, s, tt)
	if s.objectWorkspace.KeysPage(d).Raw != "" || d.Error() == "" {
		t.Fatal("anonymous public page fabricated native key catalog")
	}
	keyPanelCapture(t, s, "list")
	if err := keyPanelAction(tt, "新建 API 密钥", false); err != nil {
		t.Fatal(err)
	}
	taskPanelField(t, tt, "密钥名称", "anonymous native refusal")
	keyPanelCapture(t, s, "create")
	if err := tt.Click(locales.Text("预览密钥更改")); err != nil {
		t.Fatal(err)
	}
	keyPanelCapture(t, s, "preview")
	if err := tt.Click(locales.Text("确认提交密钥操作")); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	result := s.objectWorkspace.KeyResult(d)
	if result.State != "rejected" || result.Status != 401 || !strings.Contains(result.Response, "missing_master_key") || s.objectWorkspace.KeysUncertain(d) {
		t.Fatal("native anonymous key rejection became unknown/success", d.Error())
	}
	keyPanelCapture(t, s, "result")
	s.closeTabID(s.currentTab().id)
	tt.Frame()
	keyPanelCapture(t, s, "guard")
	if err := tt.Click(locales.Text("继续处理密钥")); err != nil {
		t.Fatal(err)
	}
	if err := keyPanelAction(tt, "恢复密钥连接", false); err != nil {
		t.Fatal(err)
	}
	keyPanelCapture(t, s, "recovered")
	if result = s.objectWorkspace.KeyResult(d); result.State != "rejected" || d.Loading() {
		t.Fatal("anonymous recovery replayed or lost original refusal")
	}
	t.Log("anonymous public key page preserves native HTTP401 missing_master_key, pure preview makes no authorization claim, original rejected request/draft/close guard and explicit recovery remain accurate")
}

func TestMeilisearchNativePublicKeysDelegatedPermissionsAndConfiguredScope(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	admin, err := meilisearch.Open(context.Background(), cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, writable := range []bool{false, true} {
		t.Run(strconv.FormatBool(writable), func(t *testing.T) {
			actions := []string{"indexes.get", "version", "keys.get"}
			if writable {
				actions = append(actions, "keys.create", "keys.update", "keys.delete")
			}
			delegate := keyPanelNative(t, admin, "POST", "/keys", map[string]any{"name": "owned-public-key-delegate", "actions": actions, "indexes": []string{"*"}, "expiresAt": nil})
			secret := keyPanelNativeString(t, delegate, "key")
			uid := keyPanelNativeString(t, delegate, "uid")
			s, tt, _, _, _ := keyPanelState(t, parsed.Profile, secret)
			d := keyPanelOpen(t, s, tt)
			if d.Error() != "" || s.objectWorkspace.KeysPage(d).Raw == "" {
				t.Fatal("delegated native key catalog unavailable", d.Error())
			}
			if err := keyPanelAction(tt, "新建 API 密钥", false); err != nil {
				t.Fatal(err)
			}
			taskPanelField(t, tt, "密钥名称", "owned-delegated-ui-create")
			if err := tt.Click(locales.Text("预览密钥更改")); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click(locales.Text("确认提交密钥操作")); err != nil {
				t.Fatal(err)
			}
			taskPanelWait(t, tt, func() bool { return !d.Loading() })
			result := s.objectWorkspace.KeyResult(d)
			if writable {
				if result.State != "completed" || result.Status != 201 {
					t.Fatal("delegated keys.create was not honored", d.Error())
				}
				keyPanelNative(t, admin, "DELETE", "/keys/"+result.UID, nil)
			} else if result.State != "rejected" || result.Status != 403 || !d.DesignDirty() || s.objectWorkspace.KeysUncertain(d) {
				t.Fatal("native delegated write refusal lost draft or changed credential", d.Error(), result.State)
			}
			keyPanelAssertNoSecret(t, s, d, parsed.Password, secret)
			s.objectWorkspace.DiscardKeyDraft(d)
			// A definite native write refusal still retires its HTTP executor.
			// Recover the same original credential before checking revocation.
			s.objectWorkspace.RecoverKeys(d)
			keyPanelNative(t, admin, "DELETE", "/keys/"+uid, nil)
			if err := keyPanelAction(tt, "刷新密钥列表", false); err != nil {
				t.Fatal(err)
			}
			taskPanelWait(t, tt, func() bool { return !d.Loading() })
			if d.Error() == "" || !strings.Contains(d.Error(), "403") {
				t.Fatal("revoked original key silently switched credentials", d.Error())
			}
		})
	}
	// Even the master credential must respect an explicitly configured index.
	scoped := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_SCOPED_CONFIG")
	s, tt, _, p, services := keyPanelState(t, scoped.Profile, parsed.Password)
	s.openKeys(*services, p.ID)
	if s.currentTab().object != nil && s.currentTab().object.KeysMode() {
		t.Fatal("index-scoped profile expanded into global key management")
	}
	if err := tt.Click(locales.Text("工具")); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.Text("管理 API 密钥…")); err == nil {
		t.Fatal("configured index enabled global key menu")
	}
	snapshot := queries.Workspace{SnapshotVersion: 2, ActiveID: 99, Tabs: []queries.WorkspaceTab{{ID: 99, Kind: uint8(tabStructure), ProfileID: p.ID, ObjectKind: "API 密钥", ObjectName: "keys", Presentation: json.RawMessage(`{"Keys":1}`)}}}
	s.restoreWorkspace(snapshot, true)
	s.selectTab(99)
	placeholder := s.currentTab()
	s.materializeRestored(*services, placeholder)
	if placeholder.placeholder == nil || placeholder.object != nil {
		t.Fatal("restored global key identity escaped configured index scope")
	}
	t.Log("original delegated keys.get/create and native HTTP403 refusal/revocation; no master-credential fallback; explicitly configured index rejects global key management even with master credential")
}

func TestMeilisearchNativePublicKeysWorkspaceRestorationIsExplicit(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	s, tt, _, p, services := keyPanelState(t, parsed.Profile, parsed.Password)
	d := keyPanelOpen(t, s, tt)
	owner := s.currentTab()
	if parsed.Password != "" {
		s.objectWorkspace.SelectKey(d, 0)
		s.objectWorkspace.RevealKey(d)
	}
	snapshot := s.workspaceSnapshot()
	for _, saved := range snapshot.Tabs {
		if saved.ID == owner.id && (saved.ProfileID != p.ID || saved.Catalog != "" || saved.Schema != "" || saved.ObjectKind != "API 密钥" || saved.ObjectName != "keys") {
			t.Fatal("saved key page lost original global identity")
		}
	}
	s.objectWorkspace.HideKeySecret(d)
	s.closeTabID(owner.id)
	s.restoreWorkspace(snapshot, true)
	s.selectTab(owner.id)
	placeholder := s.currentTab()
	if placeholder.id != owner.id || placeholder.placeholder == nil || placeholder.object != nil {
		t.Fatal("key workspace restoration started runtime work")
	}
	s.materializeRestored(*services, placeholder)
	fresh := placeholder.object
	if !fresh.KeysMode() || fresh.Loading() || s.objectWorkspace.KeysPage(fresh).Raw != "" || s.objectWorkspace.KeyRecord(fresh).Token != "" || s.objectWorkspace.KeyResult(fresh).Request != "" || fresh.DesignDirty() {
		t.Fatal("restored key page retained runtime evidence or replayed a read")
	}
	if err := keyPanelAction(tt, "刷新密钥列表", false); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !fresh.Loading() })
	if parsed.Password != "" && s.objectWorkspace.KeysPage(fresh).Raw == "" || parsed.Password == "" && fresh.Error() == "" {
		t.Fatal("explicit restored original key read failed")
	}
	t.Log("saved original global key identity restores a presentation placeholder, then an empty document without lease/read/secret/draft/receipt; only explicit refresh reads the original native catalog")
}
