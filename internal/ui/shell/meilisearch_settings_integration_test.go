package shell

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func settingsPanelAction(tt *ui.Tester, label, anchor string) error {
	if _, ok := tt.Find(label); ok {
		return tt.Click(label)
	}
	rect, ok := tt.Find(anchor)
	if !ok {
		return fmt.Errorf("missing original settings action anchor")
	}
	for _, name := range []string{"读取当前设置", "编辑设置 JSON", "预览设置更改", "预览重置设置", "放弃设置草稿", "恢复设置连接", "停止设置等待", "复制当前完整设置", "返回", "观察原设置任务（30 秒）", "复制完整设置回执", "复制完整设置任务响应"} {
		if r, found := tt.Find(locales.Text(name)); found && r.Y+r.H/2 == rect.Y+rect.H/2 && r.X+r.W > rect.X+rect.W {
			rect = r
		}
	}
	tt.ClickAt(rect.X+rect.W+14, rect.Y+rect.H/2)
	return tt.ChooseMenuItem(label)
}
func settingsDraft(t *testing.T, tt *ui.Tester, raw string) {
	t.Helper()
	if err := settingsPanelAction(tt, locales.Text("编辑设置 JSON"), locales.Text("读取当前设置")); err != nil {
		t.Fatal(err)
	}
	taskPanelField(t, tt, "完整设置草稿", raw)
}
func settingsDesired(t *testing.T, raw string, fields map[string]any) string {
	t.Helper()
	var values map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &values) != nil {
		t.Fatal("original settings unavailable")
	}
	for k, v := range fields {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		values[k] = b
	}
	out, e := json.Marshal(values)
	if e != nil {
		t.Fatal(e)
	}
	return string(out)
}
func settingsCapture(t *testing.T, s *State, phase string) {
	t.Helper()
	output := os.Getenv("NYADB_MEILISEARCH_SETTINGS_IMAGES")
	if output == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(s.View, size[0], size[1])
			switch phase {
			case "current":
				if err := tt.Click(locales.Text("当前设置")); err != nil {
					t.Fatal(err)
				}
			case "draft":
				if err := tt.Click(locales.Text("设置草稿")); err != nil {
					t.Fatal(err)
				}
			case "receipt":
				if err := tt.Click(locales.Text("设置任务与响应")); err != nil {
					t.Fatal(err)
				}
			}
			folder := filepath.Join(output, language)
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			writeDomainCapture(t, folder, fmt.Sprintf("settings-%s-%dx%d", phase, size[0], size[1]), tt)
			if phase == "preview" || phase == "reset" {
				label := "确认提交设置更改"
				if phase == "reset" {
					label = "确认提交设置重置"
				}
				r, ok := tt.Find(locales.Text(label))
				if !ok || r.H <= 0 || r.Y < 0 || r.Y+r.H > float32(size[1])-18 {
					t.Fatal("localized settings confirmation outside viewport", phase, language, r)
				}
			} else {
				r, ok := tt.Find(locales.Text("观察原设置任务（30 秒）"))
				if !ok || r.H <= 0 || r.Y < 0 || r.Y+r.H > float32(size[1])-18 {
					t.Fatal("original settings observation outside viewport", phase, language, r)
				}
			}
		}
	}
}
func TestMeilisearchNativePublicSettingsEditingPreviewAdmissionResetAndWorkspace(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	admin, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	submit := func(method, path, body string) {
		t.Helper()
		r, _, e := admin.Submit(ctx, meilisearch.Request{Method: method, Path: path, Body: []byte(body)})
		if e == nil {
			_, e = admin.WaitTask(ctx, r, 30*time.Second)
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	uid := "owned_settings_ui_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	submit("POST", "/indexes", `{"uid":"`+uid+`","primaryKey":"id"}`)
	submit("POST", "/indexes/"+uid+"/documents", `[{"id":"owned","title":"原生🐱","integer":9007199254740993}]`)
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p := parsed.Profile
	p.ID, p.Name = 0, "Native settings panel"
	p, err = profiles.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = parsed.Password
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, uid))
	taskPanelWait(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache[uid] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: uid, Category: "索引设置", Object: "settings"})
	owner := s.currentTab()
	d := owner.object
	if !d.SettingsMode() || d.Object().Catalog != uid {
		t.Fatal("settings navigator did not open original index document")
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	source := s.objectWorkspace.SettingsSnapshot(d)
	if source.Raw == "" || d.Error() != "" {
		t.Fatal("native public settings read", d.Error())
	}
	settingsCapture(t, s, "current")
	desired := settingsDesired(t, source.Raw, map[string]any{"searchableAttributes": []string{"title"}, "synonyms": map[string]any{"猫": []string{"cat", "原生🐱"}}})
	settingsDraft(t, tt, desired)
	settingsCapture(t, s, "draft")
	if err = settingsPanelAction(tt, "预览设置更改", "读取当前设置"); err != nil {
		t.Fatal(err)
	}
	settingsCapture(t, s, "preview")
	if err = tt.Click("确认提交设置更改"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	result := s.objectWorkspace.SettingsResult(d)
	if result.State != "enqueued" || result.Receipt.UID == "" || !d.DesignDirty() || s.objectWorkspace.CanPrepareSettings(d) {
		t.Fatal("public settings admission cleared drafts or claimed completion")
	}
	settingsCapture(t, s, "receipt")
	if err = settingsPanelAction(tt, "复制完整设置回执", "观察原设置任务（30 秒）"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != result.Receipt.Raw {
		t.Fatal("original settings receipt copy changed native JSON")
	}
	s.objectWorkspace.RecoverSettings(d)
	if d.Loading() || s.objectWorkspace.SettingsResult(d).State != "enqueued" {
		t.Fatal("settings recovery replayed original mutation")
	}
	if err = settingsPanelAction(tt, "观察原设置任务（30 秒）", "观察原设置任务（30 秒）"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	done := s.objectWorkspace.SettingsResult(d)
	current := s.objectWorkspace.SettingsSnapshot(d)
	if done.State != "succeeded" || done.TaskRaw == "" || !strings.Contains(current.Raw, `"title"`) || !strings.Contains(current.Raw, "原生🐱") || d.DesignDirty() {
		t.Fatal("public original settings task and current settings missing", d.Error())
	}
	if err = settingsPanelAction(tt, "复制当前完整设置", "读取当前设置"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != current.Raw {
		t.Fatal("native complete settings copy changed")
	}
	if err = settingsPanelAction(tt, "预览重置设置", "读取当前设置"); err != nil {
		t.Fatal(err)
	}
	settingsCapture(t, s, "reset")
	if err = tt.Click("确认提交设置重置"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	s.objectWorkspace.ObserveSettings(d, services.Invalidate)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	reset := s.objectWorkspace.SettingsSnapshot(d)
	if !strings.Contains(reset.Raw, `"searchableAttributes":["*"]`) {
		t.Fatal("native public settings reset did not restore defaults", d.Error())
	}
	nativeDocs, err := admin.Do(ctx, meilisearch.Request{Method: "GET", Path: "/indexes/" + uid + "/documents?limit=10"})
	if err != nil || !strings.Contains(string(nativeDocs.Body), "9007199254740993") {
		t.Fatal("settings reset changed original document bytes", err)
	}
	snapshot := s.workspaceSnapshot()
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), result.Receipt.Raw) || strings.Contains(string(raw), desired) || strings.Contains(string(raw), "original-settings-proof") {
		t.Fatal("workspace persisted settings contents or mutation receipt")
	}
	var saved bool
	for _, tab := range snapshot.Tabs {
		if tab.ObjectKind == "索引设置" && tab.Catalog == uid {
			saved = true
			if string(tab.Presentation) != `{"Settings":true}` {
				t.Fatal("settings presentation persisted runtime payload")
			}
		}
	}
	if !saved {
		t.Fatal("qualified settings workspace identity missing")
	}
	t.Log("public qualified native settings editor, recursive patch preview, enqueued original receipt, explicit recovered observation, complete JSON copy, default reset retaining exact documents, inert workspace and 20 localized captures")
}
func TestMeilisearchNativePublicSettingsParkedCancellationAndRecovery(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	upstream, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	uid := "owned_settings_parked_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	r, _, err := upstream.Submit(ctx, meilisearch.Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"` + uid + `","primaryKey":"id"}`)})
	if err == nil {
		_, err = upstream.WaitTask(ctx, r, 30*time.Second)
	}
	if err != nil {
		t.Fatal(err)
	}
	var forwards sync.Mutex
	var reads, writes atomic.Int32
	entered, release, delivered := make(chan struct{}), make(chan struct{}), make(chan struct{})
	canceling, acked := make(chan struct{}), make(chan struct{})
	writeEntered, writeRelease, writeDelivered := make(chan struct{}), make(chan struct{}), make(chan struct{})
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(r.Body)
		if e != nil {
			return
		}
		forwards.Lock()
		response, e := upstream.Do(r.Context(), meilisearch.Request{Method: r.Method, Path: r.URL.RequestURI(), Body: body})
		forwards.Unlock()
		if e != nil && response.Status == 0 {
			w.WriteHeader(502)
			return
		}
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/settings") {
			switch reads.Add(1) {
			case 1:
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			case 2:
				close(canceling)
				<-r.Context().Done()
				close(acked)
				return
			}
		}
		if r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/settings") {
			writes.Add(1)
			close(writeEntered)
			select {
			case <-writeRelease:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/settings") && reads.Load() == 1 {
			close(delivered)
		}
		if r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/settings") {
			close(writeDelivered)
		}
	}))
	defer relay.Close()
	ca := filepath.Join(t.TempDir(), "settings-relay-ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := url.Parse(relay.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	p := parsed.Profile
	p.ID, p.Name, p.Host, p.Port, p.TLSMode = 0, "Original native settings", endpoint.Hostname(), uint16(port), "verify-full"
	p.MeilisearchOptions.CAFile, p.MeilisearchOptions.ServerName, p.MeilisearchOptions.ContextPath = ca, "example.com", ""
	p.MeilisearchOptions.ClientCertFile, p.MeilisearchOptions.ClientKeyFile = "", ""
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p, err = profiles.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	other := p
	other.ID, other.Name = 0, "Parked settings target"
	other, err = profiles.Create(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = parsed.Password
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 900, 700)
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	s.openMySQLStructure(services, uid, "settings")
	owner := s.currentTab()
	d := owner.object
	// Explicitly use the qualified feature identity when index metadata is parked.
	if !d.SettingsMode() {
		s.objectWorkspace.Close(d)
		d = s.objectWorkspace.OpenSettings(owner.id, contract.ObjectRef{ProfileID: p.ID, Catalog: uid, Kind: "settings", Name: "settings"})
		owner.object = d
		s.objectWorkspace.LoadSettings(d, services.Invalidate, false)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("original settings read not started")
	}
	s.selectProfileByName(other.Name)
	close(release)
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("parked settings response not delivered")
	}
	for range 10 {
		tt.Frame()
	}
	if !d.Loading() || s.objectWorkspace.SettingsSnapshot(d).Raw != "" || s.selectedProfileID != other.ID {
		t.Fatal("parked settings read consumed for different profile")
	}
	s.selectProfileByName(p.Name)
	s.selectTab(owner.id)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	source := s.objectWorkspace.SettingsSnapshot(d)
	if source.Raw == "" || reads.Load() != 1 {
		t.Fatal("original parked settings replayed")
	}
	s.objectWorkspace.LoadSettings(d, services.Invalidate, true)
	select {
	case <-canceling:
	case <-ctx.Done():
		t.Fatal("original settings refresh not entered")
	}
	s.objectWorkspace.CancelSettings(d)
	if !d.Loading() {
		t.Fatal("cancel cleared settings busy before executor acknowledgement")
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	select {
	case <-acked:
	case <-ctx.Done():
		t.Fatal("original settings cancellation not acknowledged")
	}
	s.objectWorkspace.RecoverSettings(d)
	if reads.Load() != 2 || d.Loading() {
		t.Fatal("settings recovery replayed read")
	}
	s.objectWorkspace.LoadSettings(d, services.Invalidate, true)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if reads.Load() != 3 {
		t.Fatal("explicit recovered original settings read missing")
	}
	desired := settingsDesired(t, s.objectWorkspace.SettingsSnapshot(d).Raw, map[string]any{"searchableAttributes": []string{"title"}})
	settingsDraft(t, tt, desired)
	if err = s.objectWorkspace.PrepareSettings(d, false); err != nil {
		t.Fatal(err)
	}
	s.objectWorkspace.ApplySettings(d, services.Invalidate)
	select {
	case <-writeEntered:
	case <-ctx.Done():
		t.Fatal("original settings mutation not entered")
	}
	s.selectProfileByName(other.Name)
	close(writeRelease)
	select {
	case <-writeDelivered:
	case <-ctx.Done():
		t.Fatal("original settings admission not delivered")
	}
	for range 10 {
		tt.Frame()
	}
	if !d.Loading() || s.objectWorkspace.SettingsResult(d).Receipt.Raw != "" {
		t.Fatal("parked write receipt consumed for another profile")
	}
	s.selectProfileByName(p.Name)
	s.selectTab(owner.id)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if s.objectWorkspace.SettingsResult(d).State != "enqueued" || !d.DesignDirty() {
		t.Fatal("original parked settings admission lost draft")
	}
	s.objectWorkspace.RecoverSettings(d)
	if writes.Load() != 1 || d.Loading() {
		t.Fatal("original settings receipt recovery replayed mutation")
	}
	s.objectWorkspace.ObserveSettings(d, services.Invalidate)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if writes.Load() != 1 || s.objectWorkspace.SettingsResult(d).State != "succeeded" || d.DesignDirty() {
		t.Fatal("recovered original settings task completion failed", d.Error())
	}
	s.selectProfileByName(other.Name)
	active := s.currentTab().id
	s.closeTabID(owner.id)
	if s.currentTab().id != active || !s.runtime.IsConnected(p.ID) {
		t.Fatal("closing inactive settings document changed active tab or original session")
	}
	t.Log("public settings keeps parked original-profile read/admission, waits original cancel executor, recovers physical leases and proven receipts without replay, confirms original task and closes only its own inactive document")
}
func TestMeilisearchNativePublicSettingsScopedPermissions(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	if cfg.Auth != "bearer" {
		t.Fatal("requires selected authenticated fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	admin, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	uid := "owned_settings_scope_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	r, _, err := admin.Submit(ctx, meilisearch.Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"` + uid + `","primaryKey":"id"}`)})
	if err == nil {
		_, err = admin.WaitTask(ctx, r, 30*time.Second)
	}
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"name": "owned-public-settings-reader", "indexes": []string{uid}, "actions": []string{"indexes.get", "settings.get", "tasks.get"}, "expiresAt": nil})
	response, err := admin.Do(ctx, meilisearch.Request{Method: "POST", Path: "/keys", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	var key map[string]json.RawMessage
	if json.Unmarshal(response.Body, &key) != nil {
		t.Fatal("native scoped settings key unavailable")
	}
	var secret, keyUID string
	_ = json.Unmarshal(key["key"], &secret)
	_ = json.Unmarshal(key["uid"], &keyUID)
	if secret == "" || keyUID == "" {
		t.Fatal("native settings read key incomplete")
	}
	defer func() {
		_, _ = admin.Do(context.Background(), meilisearch.Request{Method: "DELETE", Path: "/keys/" + keyUID})
	}()
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p := parsed.Profile
	p.ID, p.Name, p.MeilisearchOptions.IndexUID = 0, "Scoped native settings", uid
	p, err = profiles.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = secret
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 900, 700)
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, uid))
	taskPanelWait(t, tt, func() bool { return s.sessions.Current().Cache[uid] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: uid, Category: "索引设置", Object: "settings"})
	d := s.currentTab().object
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	source := s.objectWorkspace.SettingsSnapshot(d)
	if source.Raw == "" || source.Object.ProfileID != p.ID {
		t.Fatal("allowed original scoped settings read missing", d.Error())
	}
	foreign := source.Object
	foreign.Catalog = "owned_other"
	if s.objectWorkspace.SettingsScopeSupported(foreign) {
		t.Fatal("public settings scope changed original index")
	}
	desired := settingsDesired(t, source.Raw, map[string]any{"searchableAttributes": []string{"title"}})
	settingsDraft(t, tt, desired)
	if err = s.objectWorkspace.PrepareSettings(d, false); err != nil {
		t.Fatal(err)
	}
	s.objectWorkspace.ApplySettings(d, services.Invalidate)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	denied := s.objectWorkspace.SettingsResult(d)
	if denied.State != "rejected" || s.objectWorkspace.SettingsUncertain(d) || !d.DesignDirty() {
		t.Fatal("original native HTTP403 settings rejection lost draft or became completion", d.Error())
	}
	if err = settingsPanelAction(tt, "复制完整设置任务响应", "观察原设置任务（30 秒）"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tt.Clipboard(), "invalid_api_key") {
		t.Fatal("native HTTP403 complete settings diagnostic missing")
	}
	s.objectWorkspace.RecoverSettings(d)
	s.objectWorkspace.LoadSettings(d, services.Invalidate, true)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if s.objectWorkspace.SettingsSnapshot(d).Raw != source.Raw || !d.DesignDirty() {
		t.Fatal("scoped settings rejection changed native settings or erased draft")
	}
	t.Log("public single-index settings read with original read-only key, foreign scope refused, HTTP403 rejection distinct from uncertain transport, full diagnostics and draft retained, explicit recovery/read without master fallback")
}
