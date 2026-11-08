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
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func taskPanelAction(tt *ui.Tester, label, anchor string) error {
	tt.Frame()
	if _, ok := tt.Find(label); ok {
		return tt.Click(label)
	}
	rect, ok := tt.Find(anchor)
	if !ok {
		return fmt.Errorf("missing task action bar anchor")
	}
	for _, candidate := range []string{"刷新任务", "重置任务筛选", "高级任务筛选", "恢复任务连接", "复制完整任务响应", "停止任务观察", "返回", "查看任务详情", "刷新选中任务", "观察选中任务（30 秒）", "复制完整任务详情", "预览取消选中任务", "预览删除选中任务记录", "观察原管理任务（30 秒）", "查看管理任务回执和结果", "复制原管理请求", "复制原管理入队回执", "复制原管理任务结果"} {
		if r, found := tt.Find(locales.Text(candidate)); found && r.Y+r.H/2 == rect.Y+rect.H/2 && r.X+r.W > rect.X+rect.W {
			rect = r
		}
	}
	tt.ClickAt(rect.X+rect.W+14, rect.Y+rect.H/2)
	return tt.ChooseMenuItem(label)
}
func taskPanelField(t *testing.T, tt *ui.Tester, label, text string) {
	t.Helper()
	if err := tt.Click(locales.Text(label)); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(text)
	tt.Frame()
}
func taskPanelWait(t *testing.T, tt *ui.Tester, done func() bool) {
	t.Helper()
	end := time.Now().Add(60 * time.Second)
	for !done() && time.Now().Before(end) {
		tt.Frame()
		time.Sleep(2 * time.Millisecond)
	}
	if !done() {
		t.Fatal("native task UI not settled")
	}
	tt.Frame()
}

func TestMeilisearchNativePublicTaskPanelNavigationFiltersJSONAndWorkspace(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	admin, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	uid := "owned_tasks_ui_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	submit := func(method, path string, body []byte) meilisearch.Receipt {
		t.Helper()
		r, _, err := admin.Submit(ctx, meilisearch.Request{Method: method, Path: path, Body: body})
		if err == nil {
			_, err = admin.WaitTask(ctx, r, 30*time.Second)
		}
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	submit("POST", "/indexes", []byte(`{"uid":"`+uid+`","primaryKey":"id"}`))
	for i := 0; i < 24; i++ {
		submit("PATCH", "/indexes/"+uid+"/settings", []byte(`{"stopWords":[]}`))
	}
	body, _ := json.Marshal([]map[string]any{{"large": strings.Repeat("原生🐱", 7000), "integer": json.Number("9007199254740993")}})
	failed, _, err := admin.Submit(ctx, meilisearch.Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	failure, err := admin.WaitTask(ctx, failed, 30*time.Second)
	if err == nil || failure.Status != "failed" {
		t.Fatal("native failure fixture unavailable")
	}
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p := parsed.Profile
	p.ID, p.Name = 0, "Native task panel"
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
	if s.sessions.StatusFor(p.ID) != "已连接" {
		t.Fatal(s.sessions.Current().Error)
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, uid))
	taskPanelWait(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache[uid] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: uid, Category: "任务", Object: "tasks"})
	doc := s.currentTab()
	d := doc.object
	if d == nil || d.Object().Kind != "tasks" || d.Object().Catalog != uid {
		t.Fatal("native task navigator did not open qualified document")
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	page := s.objectWorkspace.TaskPage(d)
	if d.Error() != "" || page == nil || page.Total != 26 || len(page.Results) != 20 || page.Next == "" {
		t.Fatal("native public task first page", d.Error())
	}
	firstUID := page.Results[0].UID
	if err = tt.Click("下一页任务"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if page = s.objectWorkspace.TaskPage(d); len(page.Results) != 6 || page.Next != "" {
		t.Fatal("native task last page")
	}
	if err = tt.Click("上一页任务"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if s.objectWorkspace.TaskPage(d).Results[0].UID != firstUID {
		t.Fatal("native public task back changed original cursor")
	}
	taskPanelField(t, tt, "任务 UID 筛选", strconv.FormatUint(uint64(failed.UID), 10))
	if err = tt.Click("应用任务筛选"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	page = s.objectWorkspace.TaskPage(d)
	if d.Error() != "" || len(page.Results) != 1 || page.Results[0].Status != "failed" || page.Results[0].Raw != string(failure.Raw) {
		t.Fatal("native UID filtering lost full failure", d.Error())
	}
	s.objectWorkspace.SelectTask(d, 0)
	tt.Frame()
	if err = taskPanelAction(tt, "查看任务详情", "查看任务详情"); err != nil {
		t.Fatal(err)
	}
	if err = taskPanelAction(tt, "复制完整任务详情", "查看任务详情"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != string(failure.Raw) || !strings.Contains(tt.Clipboard(), "missing_document_id") || len(tt.Clipboard()) < 20480 || !tt.HasText("任务 JSON 预览已截断；可复制完整内容。") {
		t.Fatal("complete task copy/preview boundary lost native error")
	}
	if err = taskPanelAction(tt, "刷新选中任务", "查看任务详情"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if s.objectWorkspace.TaskDetail(d).UID != strconv.FormatUint(uint64(failed.UID), 10) {
		t.Fatal("original detail refresh changed UID")
	}
	if err = taskPanelAction(tt, "观察选中任务（30 秒）", "查看任务详情"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if d.Error() != "" || s.objectWorkspace.TaskDetail(d).Status != "failed" {
		t.Fatal("failed task observation confused with failed read", d.Error())
	}
	snapshot := s.workspaceSnapshot()
	serialized, _ := json.Marshal(snapshot)
	if strings.Contains(string(serialized), "missing_document_id") || strings.Contains(string(serialized), "9007199254740993") {
		t.Fatal("task workspace persisted native error/body")
	}
	saved := s.objectWorkspace.DocumentSnapshot(d)
	s.closeTabID(doc.id)
	s.openTasks(services, p.ID, uid)
	fresh := s.currentTab().object
	taskPanelWait(t, tt, func() bool { return !fresh.Loading() })
	s.objectWorkspace.RestoreDocumentPresentation(fresh, saved)
	tt.Frame()
	if !tt.HasText("任务筛选已恢复；请显式应用筛选") || len(s.objectWorkspace.TaskPage(fresh).Results) != 20 {
		t.Fatal("task snapshot silently replayed UID filter")
	}
	if err = tt.Click("应用任务筛选"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !fresh.Loading() })
	if len(s.objectWorkspace.TaskPage(fresh).Results) != 1 {
		t.Fatal("explicit restored filter unavailable")
	}
	s.objectWorkspace.SelectTask(fresh, 0)
	if output := os.Getenv("NYADB_MEILISEARCH_TASK_IMAGES"); output != "" {
		defer i18n.SetLocaleFunc(nil)
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				capture := ui.NewTester(s.View, size[0], size[1])
				folder := filepath.Join(output, language)
				if err = os.MkdirAll(folder, 0700); err != nil {
					t.Fatal(err)
				}
				for _, view := range []string{"tasks", "task-detail", "task-response", "task-filters"} {
					switch view {
					case "tasks":
						if err = capture.Click(locales.Text("任务列表")); err != nil {
							t.Fatal(err)
						}
					case "task-detail":
						if err = taskPanelAction(capture, locales.Text("查看任务详情"), locales.Text("查看任务详情")); err != nil {
							t.Fatal(err)
						}
					case "task-response":
						if err = capture.Click(locales.Text("任务响应")); err != nil {
							t.Fatal(err)
						}
					case "task-filters":
						if err = taskPanelAction(capture, locales.Text("高级任务筛选"), locales.Text("刷新任务")); err != nil {
							t.Fatal(err)
						}
					}
					writeDomainCapture(t, folder, fmt.Sprintf("%s-%dx%d", view, size[0], size[1]), capture)
					rect, ok := capture.Find(locales.Text("应用任务筛选"))
					if !ok || rect.W <= 0 || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(size[0])+1 || rect.Y+rect.H > float32(size[1])+1 {
						t.Fatal("task filter action outside localized viewport", language, view, rect)
					}
					footer, ok := capture.Find(locales.Text("下一页任务"))
					if !ok || footer.H <= 0 || footer.Y < 0 || footer.Y+footer.H > float32(size[1])-18 {
						t.Fatal("task pagination outside localized viewport", language, view, footer)
					}
				}
				if err = taskPanelAction(capture, locales.Text("高级任务筛选"), locales.Text("刷新任务")); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// A global task document remains independent of the original index tab and
	// survives pruning unrelated index metadata. Closing either releases itself.
	s.openTasks(services, p.ID, "")
	global := s.currentTab()
	taskPanelWait(t, tt, func() bool { return !global.object.Loading() })
	if global.object.Object().Catalog != "" || s.objectWorkspace.TaskPage(global.object).Total < 26 {
		t.Fatal("global native tasks unavailable")
	}
	s.pruneMySQLTabsFor(p.ID, map[string]bool{uid: true})
	if s.currentTab().id != global.id {
		t.Fatal("global task tab pruned as an unknown index")
	}
	s.closeTabID(global.id)
	if !s.runtime.IsConnected(p.ID) {
		t.Fatal("closing task document closed original session")
	}
	t.Log("public qualified index/global task tabs, native UID paging/back, full failed-task JSON copy, bounded preview, explicit observations/restored filters and localized four-view evidence")
}

func TestMeilisearchNativePublicTaskPanelScopedPermissions(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_SCOPED_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p := parsed.Profile
	p.ID, p.Name = 0, "Native scoped tasks"
	p, err := profiles.Create(ctx, p)
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
	uid := p.MeilisearchOptions.IndexUID
	s.openTasks(services, p.ID, uid)
	d := s.currentTab().object
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if d.Error() != "" || s.objectWorkspace.TaskPage(d) == nil {
		t.Fatal("scoped task panel unavailable", d.Error())
	}
	for _, record := range s.objectWorkspace.TaskPage(d).Results {
		if record.Index != uid {
			t.Fatal("scoped panel exposed foreign/global task")
		}
	}
	before := s.currentTab().id
	s.openTasks(services, p.ID, "")
	if s.currentTab().id != before {
		t.Fatal("scoped profile opened global task document")
	}
	s.objectWorkspace.SelectTask(d, 0)
	s.objectWorkspace.ReadTask(d, services.Invalidate, false)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if d.Error() != "" {
		t.Fatal("scoped original task observation failed", d.Error())
	}
	t.Log("public single-index read-only task panel and original detail; global scope rejected with original key, no master fallback")
}

func TestMeilisearchNativePublicTaskPanelParkedReadsCancelCloseAndRecovery(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	upstream, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	uid := "owned_task_park_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	r, _, err := upstream.Submit(ctx, meilisearch.Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"` + uid + `","primaryKey":"id"}`)})
	if err == nil {
		_, err = upstream.WaitTask(ctx, r, 30*time.Second)
	}
	if err != nil {
		t.Fatal(err)
	}
	entered, release, delivered, closing, acked := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var attempts atomic.Int32
	var forwarding sync.Mutex
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		forwarding.Lock()
		response, err := upstream.Do(r.Context(), meilisearch.Request{Method: r.Method, Path: r.URL.RequestURI(), Body: body})
		forwarding.Unlock()
		if err != nil && response.Status == 0 {
			w.WriteHeader(502)
			return
		}
		if r.URL.Path == "/tasks" {
			switch attempts.Add(1) {
			case 1:
				close(entered)
				select {
				case <-release:
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
		if r.URL.Path == "/tasks" && attempts.Load() == 1 {
			close(delivered)
		}
	}))
	defer relay.Close()
	ca := filepath.Join(t.TempDir(), "task-relay-ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := url.Parse(relay.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	p := parsed.Profile
	p.ID, p.Name, p.Host, p.Port, p.TLSMode = 0, "Original native tasks", endpoint.Hostname(), uint16(port), "verify-full"
	p.MeilisearchOptions.CAFile, p.MeilisearchOptions.ServerName, p.MeilisearchOptions.ContextPath = ca, "example.com", ""
	p.MeilisearchOptions.ClientCertFile, p.MeilisearchOptions.ClientKeyFile = "", ""
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p, err = profiles.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	other := p
	other.ID, other.Name = 0, "Parked task target"
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
	s.openTasks(services, p.ID, uid)
	doc := s.currentTab()
	d := doc.object
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("original task read not entered")
	}
	s.selectProfileByName(other.Name)
	close(release)
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("parked original task read not delivered")
	}
	for range 10 {
		tt.Frame()
	}
	if !d.Loading() || s.objectWorkspace.TaskPage(d) != nil || s.selectedProfileID != other.ID {
		t.Fatal("parked task result activated another profile")
	}
	s.selectProfileByName(p.Name)
	s.selectTab(doc.id)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if d.Error() != "" || attempts.Load() != 1 || s.objectWorkspace.TaskPage(d).Results[0].Index != uid {
		t.Fatal("parked native task read replayed", d.Error())
	}
	s.objectWorkspace.LoadTasks(d, services.Invalidate, true)
	select {
	case <-closing:
	case <-ctx.Done():
		t.Fatal("original task refresh not started")
	}
	s.objectWorkspace.CancelTasks(d)
	if !d.Loading() || !s.objectWorkspace.TaskCancelRequested(d) {
		t.Fatal("task cancellation cleared busy before ack")
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	select {
	case <-acked:
	case <-ctx.Done():
		t.Fatal("original canceled task executor not acknowledged")
	}
	if !s.objectWorkspace.TaskConnectionLost(d) || s.objectWorkspace.TaskPage(d) == nil {
		t.Fatal("canceled task read erased cache or reused lost client")
	}
	s.objectWorkspace.RecoverTasks(d)
	if attempts.Load() != 2 || d.Loading() {
		t.Fatal("task recovery replayed original read")
	}
	s.objectWorkspace.LoadTasks(d, services.Invalidate, true)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if d.Error() != "" || attempts.Load() != 3 {
		t.Fatal("explicit original-profile task read failed", d.Error())
	}
	s.closeTabID(doc.id)
	if !s.runtime.IsConnected(p.ID) {
		t.Fatal("closing original task tab damaged session")
	}
	t.Log("public tasks keep parked original-profile results, wait canceled original executor, preserve cache, recover without replay and close only the original task lease")
}
