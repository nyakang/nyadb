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
	"github.com/nyakang/nyadb/locales"
)

func taskManagementCapture(t *testing.T, s *State, phase string) {
	t.Helper()
	output := os.Getenv("NYADB_MEILISEARCH_TASK_MANAGEMENT_IMAGES")
	if output == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(s.View, size[0], size[1])
			label := "确认提交任务记录删除"
			if phase == "guard" {
				label = "继续确认原任务"
			}
			if phase == "receipt" || phase == "result" {
				if err := tt.Click(locales.Text("管理回执与结果")); err != nil {
					t.Fatal(err)
				}
				label = "复制原管理请求"
			}
			r, ok := tt.Find(locales.Text(label))
			if !ok || r.H <= 0 || r.Y < 0 || r.Y+r.H > float32(size[1])-18 {
				t.Fatal("localized management control outside viewport", phase, language, r)
			}
			folder := filepath.Join(output, language)
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			writeDomainCapture(t, folder, fmt.Sprintf("task-management-%s-%dx%d", phase, size[0], size[1]), tt)
		}
	}
}
func TestMeilisearchNativePublicTaskManagementPreviewAdmissionObservationAndWorkspace(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	admin, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	uid := "owned_management_ui_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	submit := func(method, path, body string) meilisearch.Receipt {
		t.Helper()
		r, _, err := admin.Submit(ctx, meilisearch.Request{Method: method, Path: path, Body: []byte(body)})
		if err == nil {
			_, err = admin.WaitTask(ctx, r, 30*time.Second)
		}
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	submit("POST", "/indexes", `{"uid":"`+uid+`","primaryKey":"id"}`)
	target := submit("POST", "/indexes/"+uid+"/documents", `[{"id":"retained","integer":9007199254740993,"text":"原生🐱"}]`)
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p := parsed.Profile
	p.ID, p.Name = 0, "Native task management"
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
	s.openTasks(services, p.ID, uid)
	doc := s.currentTab()
	d := doc.object
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	page := s.objectWorkspace.TaskPage(d)
	row := -1
	for i, r := range page.Results {
		if r.UID == strconv.FormatUint(uint64(target.UID), 10) {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("original target not loaded")
	}
	s.objectWorkspace.SelectTask(d, row)
	tt.Frame()
	if err = taskPanelAction(tt, "预览删除选中任务记录", "查看任务详情"); err != nil {
		t.Fatal(err)
	}
	taskManagementCapture(t, s, "preview")
	if err = tt.Click("确认提交任务记录删除"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	receipt := s.objectWorkspace.TaskManagementResult(d)
	if receipt.State != "enqueued" || receipt.CountsKnown || receipt.Receipt.Raw == "" || receipt.Receipt.Targets[0] != strconv.FormatUint(uint64(target.UID), 10) {
		t.Fatal("original admission lost", d.Error(), receipt)
	}
	taskManagementCapture(t, s, "receipt")
	if err = tt.Click("管理回执与结果"); err != nil {
		t.Fatal(err)
	}
	if err = taskPanelAction(tt, "复制原管理入队回执", "复制原管理请求"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != receipt.Receipt.Raw {
		t.Fatal("original receipt copying lost native evidence")
	}
	s.closeTabID(doc.id)
	tt.Frame()
	if !d.DesignLeavePending() || s.currentTab().id != doc.id || !tt.HasText("离开原任务管理页面？") {
		t.Fatal("pending admission closed without leave guard")
	}
	taskManagementCapture(t, s, "guard")
	if err = tt.Click("继续确认原任务"); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := json.Marshal(s.workspaceSnapshot())
	if strings.Contains(string(snapshot), "taskUid") || strings.Contains(string(snapshot), receipt.Receipt.Token) || strings.Contains(string(snapshot), "DELETE /tasks") {
		t.Fatal("workspace persisted native management evidence")
	}
	s.objectWorkspace.RecoverTasks(d)
	if d.Loading() || s.objectWorkspace.TaskManagementResult(d).Receipt.Raw != receipt.Receipt.Raw {
		t.Fatal("explicit recovery replayed or dropped original receipt")
	}
	if err = taskPanelAction(tt, "观察原管理任务（30 秒）", "查看任务详情"); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	result := s.objectWorkspace.TaskManagementResult(d)
	if result.State != "succeeded" || !result.CountsKnown || result.Matched != 1 || result.Changed != 1 || result.TaskRaw == "" || s.objectWorkspace.CanManageTasks(d) {
		t.Fatal("original completion/counts missing", d.Error(), result)
	}
	taskManagementCapture(t, s, "result")
	if err = taskPanelAction(tt, "复制原管理任务结果", "复制原管理请求"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != result.TaskRaw {
		t.Fatal("complete management result copy lost")
	}
	s.objectWorkspace.LoadTasks(d, services.Invalidate, true)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	for _, r := range s.objectWorkspace.TaskPage(d).Results {
		if r.UID == strconv.FormatUint(uint64(target.UID), 10) {
			t.Fatal("explicit refresh retained deleted task record")
		}
	}
	response, err := admin.Do(ctx, meilisearch.Request{Method: "GET", Path: "/indexes/" + uid + "/documents?limit=1"})
	if err != nil || !strings.Contains(string(response.Body), "9007199254740993") {
		t.Fatal("task deletion altered original documents", err)
	}
	s.closeTabID(doc.id)
	if !s.runtime.IsConnected(p.ID) {
		t.Fatal("task close damaged original connection")
	}
	t.Log("public selected UID deletion preview, original admission, leave guard, explicit recovery/observation, exact completed counts, original documents retained, inert workspace and localized 720x520/1280x800 views")
}
func TestMeilisearchNativePublicTaskManagementParkedReceiptCancelAndRecovery(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	upstream, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	uid := "owned_management_park_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	submit := func(method, path, body string) meilisearch.Receipt {
		t.Helper()
		r, _, err := upstream.Submit(ctx, meilisearch.Request{Method: method, Path: path, Body: []byte(body)})
		if err == nil {
			_, err = upstream.WaitTask(ctx, r, 30*time.Second)
		}
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	submit("POST", "/indexes", `{"uid":"`+uid+`","primaryKey":"id"}`)
	target := submit("POST", "/indexes/"+uid+"/documents", `[{"id":"retained"}]`)
	entered, release, delivered, observing, handlerRelease := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(handlerRelease) })
	var writes, observations atomic.Int32
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
		if r.Method == "DELETE" && r.URL.Path == "/tasks" {
			writes.Add(1)
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/tasks/") && observations.Add(1) == 1 {
			close(observing)
			<-r.Context().Done()
			<-handlerRelease
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
		if r.Method == "DELETE" {
			close(delivered)
		}
	}))
	defer func() { unblock.Do(func() { close(handlerRelease) }); relay.Close() }()
	ca := filepath.Join(t.TempDir(), "management-relay-ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := url.Parse(relay.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	p := parsed.Profile
	p.ID, p.Name, p.Host, p.Port, p.TLSMode = 0, "Original task management", endpoint.Hostname(), uint16(port), "verify-full"
	p.MeilisearchOptions.CAFile, p.MeilisearchOptions.ServerName, p.MeilisearchOptions.ContextPath = ca, "example.com", ""
	p.MeilisearchOptions.ClientCertFile, p.MeilisearchOptions.ClientKeyFile = "", ""
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p, err = profiles.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	other := p
	other.ID, other.Name = 0, "Parked management target"
	other, err = profiles.Create(ctx, other)
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
	s.openTasks(services, p.ID, uid)
	doc := s.currentTab()
	d := doc.object
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	page := s.objectWorkspace.TaskPage(d)
	row := -1
	for i, r := range page.Results {
		if r.UID == strconv.FormatUint(uint64(target.UID), 10) {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("native target missing")
	}
	if err = s.objectWorkspace.PrepareTaskManagement(d, "delete", []int{row}); err != nil {
		t.Fatal(err)
	}
	s.objectWorkspace.SubmitTaskManagement(d, services.Invalidate)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("management submission not entered")
	}
	s.selectProfileByName(other.Name)
	close(release)
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("original receipt not delivered")
	}
	for range 10 {
		tt.Frame()
	}
	if !d.Loading() || s.objectWorkspace.TaskManagementResult(d).Receipt.Raw != "" || s.selectedProfileID != other.ID {
		t.Fatal("parked management receipt activated foreign profile")
	}
	s.selectProfileByName(p.Name)
	s.selectTab(doc.id)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	receipt := s.objectWorkspace.TaskManagementResult(d).Receipt
	if receipt.Raw == "" || writes.Load() != 1 {
		t.Fatal("parked admission lost or replayed")
	}
	s.objectWorkspace.ObserveTaskManagement(d, services.Invalidate)
	select {
	case <-observing:
	case <-ctx.Done():
		t.Fatal("explicit observer not entered")
	}
	s.objectWorkspace.CancelTasks(d)
	if !d.Loading() || !d.DesignBusy() {
		t.Fatal("cancel settled before executor acknowledgement")
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	unblock.Do(func() { close(handlerRelease) })
	if !s.objectWorkspace.TaskConnectionLost(d) || s.objectWorkspace.TaskManagementResult(d).Receipt.Raw != receipt.Raw {
		t.Fatal("canceled original observation lost receipt")
	}
	s.objectWorkspace.RecoverTasks(d)
	if d.Loading() || writes.Load() != 1 || observations.Load() != 1 {
		t.Fatal("recovery replayed operation")
	}
	s.objectWorkspace.ObserveTaskManagement(d, services.Invalidate)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	result := s.objectWorkspace.TaskManagementResult(d)
	if result.State != "succeeded" || !result.CountsKnown || result.Changed != 1 || writes.Load() != 1 || observations.Load() != 2 {
		t.Fatal("explicit recovered observation lost original result", d.Error(), result)
	}
	s.closeTabID(doc.id)
	if !s.runtime.IsConnected(p.ID) {
		t.Fatal("closing management lease damaged session")
	}
	t.Log("parked original-profile management admission, observer cancellation acknowledgement, retained original receipt, explicit recovery and completed counts without mutation replay")
}
