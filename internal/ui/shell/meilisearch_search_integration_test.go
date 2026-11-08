package shell

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func clickMeilisearchSearchAction(tt *ui.Tester, label string) error {
	if _, ok := tt.Find(label); ok {
		return tt.Click(label)
	}
	// The native selector and the document commands have separate overflow
	// buttons. Target the native bar after its last visible action.
	last, ok := tt.Find(locales.Text("应用 Meilisearch 搜索"))
	if !ok {
		return fmt.Errorf("missing native search action bar")
	}
	for _, action := range []string{"清除 Meilisearch 搜索", "收起 Meilisearch 搜索", "查看原生搜索响应"} {
		if rect, found := tt.Find(locales.Text(action)); found && rect.Y+rect.H/2 == last.Y+last.H/2 && rect.X+rect.W > last.X+last.W {
			last = rect
		}
	}
	tt.ClickAt(last.X+last.W+14, last.Y+last.H/2)
	return tt.ChooseMenuItem(label)
}

func TestMeilisearchNativePublicNativeSearchGridHistoryAndCompleteExport(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	admin, err := meilisearch.Open(ctx, cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	submit := func(method, path, body string) {
		t.Helper()
		r, _, err := admin.Submit(ctx, meilisearch.Request{Method: method, Path: path, Body: []byte(body)})
		if err == nil {
			_, err = admin.WaitTask(ctx, r, 30*time.Second)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	uid := "owned_shell_search_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	submit("POST", "/indexes", `{"uid":"`+uid+`","primaryKey":"id"}`)
	defer submit("DELETE", "/indexes/"+uid, "")
	submit("PATCH", "/indexes/"+uid+"/settings", `{"searchableAttributes":["title"],"filterableAttributes":["category"],"sortableAttributes":["rank"]}`)
	docs := make([]map[string]any, 1017)
	for i := range docs {
		category, title := "dogs", "dog 原生🐶"
		if i < 205 {
			category, title = "cats", "cat 原生🐱"
		}
		docs[i] = map[string]any{"id": fmt.Sprintf("row_%04d", i), "title": title, "category": category, "rank": i, "n": json.Number("9007199254740993"), "unknown": map[string]any{"null": nil, "猫": "🐱"}}
	}
	docs[0]["memo"] = strings.Repeat("猫🐱", 1500)
	raw, _ := json.Marshal(docs)
	submit("POST", "/indexes/"+uid+"/documents", string(raw))
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p := parsed.Profile
	p.ID, p.Name = 0, "Native Meilisearch search"
	p, err = profiles.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	s.sessions.Current().Password = parsed.Password
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("native search UI did not settle")
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
	d := s.currentTab().data
	if d == nil {
		t.Fatal("native search shared grid absent")
	}
	wait(func() bool { return !d.Busy() })
	if err = clickMySQLDataToolbar(tt, "Meilisearch 搜索"); err != nil {
		t.Fatal(err)
	}
	query := `{"q":"cat","filter":"category = cats","sort":["rank:desc"],"facets":["category"],"attributesToRetrieve":["title"],"attributesToHighlight":["title"],"showRankingScore":true}`
	setQuery := func(raw string) {
		t.Helper()
		if err = tt.Click("Meilisearch 搜索 JSON"); err != nil {
			t.Fatal(err)
		}
		tt.Key(ui.Cmd, ui.KeyA)
		tt.Type(raw)
		tt.Frame()
	}
	setQuery(query)
	if err = tt.Click("应用 Meilisearch 搜索"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !d.Busy() })
	if d.Error() != "" || d.Page().Search == nil || d.Page().Search.EstimatedTotalHits != 205 || d.Page().Rows[0][0].Text != "row_0204" || !strings.Contains(d.Page().Rows[0][1].Text, "unknown") || strings.Contains(d.Page().Rows[0][1].Text, "_formatted") {
		t.Fatal("native projected response replaced original grid", d.Error())
	}
	first := d.Page()
	for _, invalid := range []string{`{"q":"cat","q":"dog"}`, `{"offset":100}`} {
		setQuery(invalid)
		if err = tt.Click("应用 Meilisearch 搜索"); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			tt.Frame()
		}
		if d.Busy() || d.Page() != first || !tt.HasText("搜索 JSON 必须为有效对象，且不能包含 offset、limit、page 或 hitsPerPage") {
			t.Fatal("invalid native draft dispatched or erased cache")
		}
	}
	setQuery(query)
	for _, offset := range []int64{100, 200} {
		if err = tt.Click("下一页数据"); err != nil {
			t.Fatal(err)
		}
		wait(func() bool { return !d.Busy() })
		if d.Error() != "" || d.Offset() != offset {
			t.Fatal("native search page", d.Error())
		}
	}
	if len(d.Page().Rows) != 5 || d.Page().HasMore {
		t.Fatal("native search last page")
	}
	if err = tt.Click("上一页数据"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !d.Busy() })
	if d.Offset() != 100 || d.Page().Rows[0][0].Text != "row_0104" {
		t.Fatal("native search back")
	}
	if err = tt.Click("搜索响应"); err != nil {
		t.Fatal(err)
	}
	if err = clickMySQLDataToolbar(tt, "复制完整搜索响应"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != d.Page().Search.RawJSON || !strings.Contains(tt.Clipboard(), "facetDistribution") {
		t.Fatal("native response copy")
	}
	if err = tt.Click("搜索响应"); err != nil {
		t.Fatal(err)
	}
	// The public history menu restores the request text without starting work.
	if err = clickMySQLDataToolbar(tt, "历史"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("历史操作"); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem("查看查询历史…"); err != nil {
		t.Fatal(err)
	}
	page := d.Page()
	if err = tt.Click("恢复规则"); err != nil {
		t.Fatal(err)
	}
	if d.Busy() || d.Page() != page || d.Offset() != 100 {
		t.Fatal("history replayed native search")
	}
	if err = clickMySQLDataToolbar(tt, "历史"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("应用 Meilisearch 搜索"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !d.Busy() })
	if d.Error() != "" || d.Offset() != 0 || d.Page().Search.EstimatedTotalHits != 205 {
		t.Fatal("explicit search history apply", d.Error())
	}
	panel := d.Inspector()
	wait(func() bool { return !panel.InfoLoading() })
	if err = tt.Click("获取整表行数"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !panel.CountLoading() })
	if panel.CountError() != "" || panel.Count() == nil || *panel.Count() != 1017 {
		t.Fatal("search estimate mixed with index count", panel.CountError())
	}
	var exported bytes.Buffer
	s.SetEngineFiles(func(name string) (string, error) { return name, nil }, func(_ string, write func(io.Writer) error) error { return write(&exported) })
	if err = clickMySQLDataToolbar(tt, "导出"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("当前页"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("完整筛选结果"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = tt.Click("下一步"); err != nil {
			t.Fatal(err)
		}
	}
	destination := filepath.Join(t.TempDir(), "search.csv")
	if err = tt.Click("目标文件路径"); err != nil {
		t.Fatal(err)
	}
	tt.Type(destination)
	if err = tt.Click("导出"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !d.Busy() })
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal("native filtered export file", err, d.Error())
	}
	records, err := csv.NewReader(bytes.NewReader(contents)).ReadAll()
	if err != nil || len(records) != 206 || !bytes.Contains(contents, []byte("9007199254740993")) || bytes.Contains(contents, []byte("_formatted")) {
		t.Fatal("native filtered tabular export", len(records), err, d.Error())
	}
	if err = tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	exported.Reset()
	if err = clickMySQLDataToolbar(tt, "导出选项"); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem("整表导出为 JSONL（忽略当前筛选）"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !d.Busy() })
	if d.Error() != "" || len(bytes.Split(bytes.TrimSpace(exported.Bytes()), []byte{'\n'})) != 1017 || !strings.Contains(exported.String(), strings.Repeat("猫🐱", 1500)) {
		t.Fatal("native full JSONL source/export scope", d.Error())
	}
	if output := os.Getenv("NYADB_MEILISEARCH_SEARCH_IMAGES"); output != "" {
		defer i18n.SetLocaleFunc(nil)
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				folder := filepath.Join(output, language)
				if err = os.MkdirAll(folder, 0700); err != nil {
					t.Fatal(err)
				}
				capture := ui.NewTester(s.View, size[0], size[1])
				for _, view := range []string{"search", "search-response"} {
					if view == "search-response" {
						if err = clickMeilisearchSearchAction(capture, locales.Text("查看原生搜索响应")); err != nil {
							t.Fatal(err)
						}
					}
					writeDomainCapture(t, folder, fmt.Sprintf("%s-%dx%d", view, size[0], size[1]), capture)
					rect, ok := capture.Find(locales.Text("应用 Meilisearch 搜索"))
					if !ok || rect.W <= 0 || rect.H <= 0 || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(size[0])+1 || rect.Y+rect.H > float32(size[1])+1 {
						t.Fatal("native localized apply outside viewport", language, view, rect)
					}
				}
				if err = clickMeilisearchSearchAction(capture, locales.Text("查看原生搜索响应")); err != nil {
					t.Fatal(err)
				}
			}
		}
		i18n.SetLocaleFunc(nil)
	}
	t.Log("native shared search grid/100/100/5/back, estimate205 versus count1017, original source with native response copy, explicit history apply, filteredCSV205 and completeJSONL1017; localized 1280x800/720x520 captures are headless UI with real native requests")
}
