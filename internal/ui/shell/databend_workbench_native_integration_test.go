package shell

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/databend"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

// This fixture registers only on its original shell manager. Publishing the
// product catalog/editor/URI requires the separate public acceptance matrix.
type databendWorkbenchFixture struct {
	Endpoint, Username, PasswordFile, Archive, SHA256, Version, Directory, Database string
}

func databendWorkbenchOriginal(t *testing.T) databendWorkbenchFixture {
	t.Helper()
	if os.Getenv("NYADB_DATABEND_WORKBENCH_IT") != "1" {
		t.Skip("requires owned actual Databend and original managed workbench fixture")
	}
	var f databendWorkbenchFixture
	raw, err := os.ReadFile(os.Getenv("NYADB_DATABEND_MANAGED_FIXTURE"))
	if err != nil || json.Unmarshal(raw, &f) != nil || f.SHA256 != "7069b794a21d8995b67d6918c6c106967ce94a18ae9a470159401bce18032bfd" || f.Version != "jdbc-0.4.8-agent-0.1.82" || !filepath.IsAbs(f.Directory) {
		t.Fatal("original pinned workbench fixture missing", err)
	}
	return f
}

func databendWorkbenchJournal(t *testing.T, f databendWorkbenchFixture, name string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(f.Directory, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(append(raw, '\n')); err != nil {
		t.Error("original workbench evidence recording", err)
	}
}

func databendWorkbenchSQL(t *testing.T, f databendWorkbenchFixture, source string) [][]string {
	t.Helper()
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"sql": source, "pagination": map[string]int{"wait_time_secs": 1, "max_rows_per_page": 1000}})
	req, _ := http.NewRequest(http.MethodPost, f.Endpoint+"/v1/query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	var rows [][]string
	for i := 0; i < 200; i++ {
		req.SetBasicAuth(f.Username, string(secret))
		reply, err := client.Do(req)
		if err != nil {
			t.Fatal("independent actual Databend query", err)
		}
		raw, err := io.ReadAll(reply.Body)
		reply.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(raw)
		databendWorkbenchJournal(t, f, "workbench-http-receipts.jsonl", map[string]any{"test": t.Name(), "sql": source, "native_bytes": raw, "native_sha256": hex.EncodeToString(hash[:])})
		var value struct {
			Data  [][]string      `json:"data"`
			Next  string          `json:"next_uri"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(raw, &value); err != nil || reply.StatusCode != 200 || (len(value.Error) > 0 && string(value.Error) != "null") {
			t.Fatal("independent native executor failed", err, string(raw))
		}
		rows = append(rows, value.Data...)
		if value.Next == "" {
			return rows
		}
		if !strings.HasPrefix(value.Next, "/v1/query/") {
			t.Fatal("original native page escaped query identity")
		}
		req, _ = http.NewRequest(http.MethodGet, f.Endpoint+value.Next, nil)
	}
	t.Fatal("original independent query did not finish")
	return nil
}

func databendWorkbenchState(t *testing.T, f databendWorkbenchFixture, endpoint string) (*State, *ui.Tester, connections.Profile, *ui.Services) {
	t.Helper()
	store, err := driverstore.New(filepath.Join(f.Directory, t.Name(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.Open(f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Install(context.Background(), archive, f.SHA256)
	archive.Close()
	if err != nil {
		t.Fatal("original archive installation", err)
	}
	s, _, profiles, _ := profileTestState(t)
	s.runtime.Register(connections.Databend, databend.Provider{Store: store, Started: func(pid int) error {
		exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil || !strings.HasPrefix(exe, f.Directory+string(os.PathSeparator)) || !strings.HasSuffix(exe, "/jre/bin/java") {
			return errors.New("original installed Java executable differs")
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) < 20 {
			return errors.New("original process start identity missing")
		}
		databendWorkbenchJournal(t, f, "processes.jsonl", map[string]any{"pid": pid, "start_ticks": fields[19], "executable": exe})
		return nil
	}, Observe: func(method string, r agentbridge.Receipt) {
		raw := r.OriginalResponse()
		hash := sha256.Sum256(raw)
		databendWorkbenchJournal(t, f, "original-receipts.jsonl", map[string]any{"test": t.Name(), "method": "workbench-" + method, "receipt": r, "native_bytes": []byte(raw), "native_sha256": hex.EncodeToString(hash[:])})
	}})
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.Databend)
	p.Name, p.Host, p.Port, p.Username, p.Database = "Original workbench "+strconv.FormatInt(time.Now().UnixNano(), 36), host, uint16(n), f.Username, f.Database
	p.DatabendOptions.DriverVersion, p.DatabendOptions.DriverSHA256 = f.Version, f.SHA256
	p.DatabendOptions.ConnectTimeoutSeconds = 60
	p, err = profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	s.sessions.Current().Password = string(secret)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	databendWorkbenchClick(t, tt, "连接所选数据库")
	databendWorkbenchWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if s.sessions.StatusFor(p.ID) != "已连接" {
		t.Fatal("actual workbench connection", s.sessions.Current().Error)
	}
	return s, tt, p, &services
}

func databendWorkbenchWait(t *testing.T, tt *ui.Tester, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(180 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("original workbench operation did not settle", tt.Texts())
		}
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
}

func databendWorkbenchClick(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	tt.Frame()
	if err := tt.Click(locales.Text(label)); err != nil {
		t.Fatal(err)
	}
}

func databendWorkbenchData(t *testing.T, s *State, tt *ui.Tester, services ui.Services, p connections.Profile, name string) *dataworkspace.Document {
	t.Helper()
	s.openMySQLDatabase(services, p.Database, true)
	databendWorkbenchWait(t, tt, func() bool {
		return s.sessions.Current().Pending == "" && s.sessions.Current().Cache[p.Database] != nil
	})
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: p.Database, Object: name, Category: "表"})
	tab := s.currentTab()
	if tab == nil || tab.data == nil {
		t.Fatal("qualified original data document missing")
	}
	d := tab.data
	databendWorkbenchWait(t, tt, func() bool { return !d.Busy() })
	if d.Page() == nil || d.Error() != "" || d.Object().ProfileID != p.ID || d.Object().Schema != p.Database || d.Object().Name != name {
		t.Fatal("actual shared data page failed", d.Error())
	}
	return d
}

func databendWorkbenchCapture(t *testing.T, f databendWorkbenchFixture, s *State, phase string, labels ...string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(s.View, size[0], size[1])
			out := filepath.Join(f.Directory, "workbench", t.Name(), language)
			if err := os.MkdirAll(out, 0700); err != nil {
				t.Fatal(err)
			}
			writeDomainCapture(t, out, phase+"-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1]), tt)
			for _, label := range labels {
				r, ok := tt.Find(locales.Text(label))
				if !ok && (label == "读取所选字段下一段" || label == "复制此片段") {
					// A narrow inspector uses the native toolbar overflow. Check
					// the actual menu without invoking a read or changing selection.
					if anchor, found := tt.Find(locales.Text("复制单元格预览")); found {
						x, y := anchor.X+anchor.W+14, anchor.Y+anchor.H/2
						tt.ClickAt(x, y)
						for _, item := range tt.Menu() {
							if item == locales.Text(label) {
								ok = true
								r = ui.Rect{X: x - 10, Y: y - 10, W: 20, H: 20}
							}
						}
						tt.CloseMenu()
					}
				}
				if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
					t.Fatal("localized workbench action outside viewport", phase, language, size, label, r)
				}
			}
		}
	}
}

func databendWorkbenchPreview(t *testing.T, s *State, tt *ui.Tester, d *dataworkspace.Document) string {
	t.Helper()
	databendWorkbenchClick(t, tt, "保存数据更改")
	databendWorkbenchWait(t, tt, func() bool { return tt.HasText(locales.Text("确认 Databend 数据更改")) || !d.Busy() })
	if !tt.HasText(locales.Text("确认 Databend 数据更改")) {
		t.Fatal("actual immutable Databend confirmation missing", d.Error(), tt.Texts())
	}
	databendWorkbenchClick(t, tt, "复制完整预览")
	return tt.Clipboard()
}

func databendWorkbenchConfirm(t *testing.T, s *State, tt *ui.Tester, d *dataworkspace.Document) {
	t.Helper()
	databendWorkbenchPreview(t, s, tt, d)
	databendWorkbenchClick(t, tt, "确认并提交更改")
	databendWorkbenchWait(t, tt, func() bool { return !d.Busy() })
	if d.HasDraft() || d.Error() != "" {
		t.Fatal("complete original receipt did not settle draft/readback", d.Error(), tt.Texts())
	}
}

func TestDatabendWorkbenchOriginalTypedGridPagingConfirmationAndReceipts(t *testing.T) {
	f := databendWorkbenchOriginal(t)
	databendWorkbenchSQL(t, f, `CREATE TABLE "`+f.Database+`".workbench_items52 (id UInt64 NOT NULL,txt String NOT NULL,payload Binary NOT NULL,exact Decimal(38,20) NOT NULL,j Nullable(Variant),n Nullable(String),a Array(UInt64) NOT NULL,m Map(String,UInt64) NOT NULL,note String DEFAULT 'native default')`)
	databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".workbench_items52 VALUES (18446744073709551615,'original 猫',unhex('00ff80'),9007199254740993.12345678901234567890,parse_json('null'),NULL,[9007199254740993],MAP([],[]),'native default')`)
	s, tt, p, services := databendWorkbenchState(t, f, f.Endpoint)
	pages := databendWorkbenchData(t, s, tt, *services, p, "managed_pages")
	databendWorkbenchClick(t, tt, "每页行数")
	if err := tt.ChooseMenuItem(fmt.Sprintf("200 %s", locales.Text("行 / 页"))); err != nil {
		t.Fatal(err)
	}
	databendWorkbenchWait(t, tt, func() bool { return !pages.Busy() })
	if len(pages.Page().Rows) != 200 {
		t.Fatal("actual first shared page boundary", len(pages.Page().Rows))
	}
	databendWorkbenchClick(t, tt, "下一页数据")
	databendWorkbenchWait(t, tt, func() bool { return !pages.Busy() })
	if pages.Error() != "" || len(pages.Page().Rows) != 5 {
		t.Fatal("actual final shared page boundary", pages.Error())
	}
	d := databendWorkbenchData(t, s, tt, *services, p, "workbench_items52")
	cells := d.Page().Rows[0]
	if cells[0].Value.Text != "18446744073709551615" || cells[3].Value.Text != "9007199254740993.12345678901234567890" || !bytes.Equal(cells[2].Value.Bytes, []byte{0, 255, 128}) || cells[4].Value.Kind != contract.JSON || cells[4].Value.Text != "null" || cells[5].Value.Kind != contract.Null || cells[6].Value.Kind != contract.JSON || cells[7].Value.Kind != contract.JSON {
		t.Fatal("actual typed grid lost original values", cells)
	}
	databendWorkbenchCapture(t, f, s, "grid")
	databendWorkbenchClick(t, tt, "original 猫")
	databendWorkbenchClick(t, tt, "字段信息")
	databendWorkbenchClick(t, tt, "读取所选字段下一段")
	databendWorkbenchWait(t, tt, func() bool { return !d.Inspector().ChunkLoading() })
	if d.Inspector().ChunkText() != "original 猫" || d.Inspector().ChunkError() != "" {
		t.Fatal("actual explicit original field read", d.Inspector().ChunkError())
	}
	databendWorkbenchCapture(t, f, s, "inspector", "读取所选字段下一段", "复制此片段")
	databendWorkbenchClick(t, tt, "单元格编辑器")
	databendWorkbenchWait(t, tt, func() bool { return tt.HasText(locales.Text("单元格编辑器内容")) && !d.Busy() })
	databendWorkbenchCapture(t, f, s, "editor", "单元格编辑器内容")
	taskPanelField(t, tt, "单元格编辑器内容", "changed 喵")
	s.dataWorkspace.EditCell(*services, d, 0, "payload", contract.Cell{Kind: contract.Binary, Bytes: []byte{0, 255, 1}})
	s.dataWorkspace.EditCell(*services, d, 0, "exact", contract.Cell{Kind: contract.Decimal, Text: "9007199254740993.12345678901234567891"})
	s.dataWorkspace.EditCell(*services, d, 0, "j", contract.Cell{Kind: contract.Null})
	preview := databendWorkbenchPreview(t, s, tt, d)
	if !strings.Contains(preview, "changed 喵") || !strings.Contains(preview, "9007199254740993.12345678901234567891") {
		t.Fatal("immutable exact original preview lost values", preview)
	}
	databendWorkbenchCapture(t, f, s, "preview", "复制完整预览", "取消", "确认并提交更改")
	if rows := databendWorkbenchSQL(t, f, `SELECT txt FROM "`+f.Database+`".workbench_items52`); len(rows) != 1 || rows[0][0] != "original 猫" {
		t.Fatal("preview executed a write", rows)
	}
	databendWorkbenchClick(t, tt, "取消")
	if !d.HasDraft() || !s.dataWorkspace.CanSave(d) {
		t.Fatal("preview cancel discarded original typed draft")
	}
	databendWorkbenchConfirm(t, s, tt, d)
	rows := databendWorkbenchSQL(t, f, `SELECT txt,hex(payload),to_string(exact),j IS NULL,json_pretty(to_variant(m)) FROM "`+f.Database+`".workbench_items52`)
	if len(rows) != 1 || rows[0][0] != "changed 喵" || rows[0][1] != "00ff01" || rows[0][2] != "9007199254740993.12345678901234567891" || rows[0][3] != "1" || rows[0][4] != "{}" {
		t.Fatal("actual UI empty-map typed update changed original values", rows)
	}
	databendWorkbenchClick(t, tt, "原生顺序写入回执")
	databendWorkbenchCapture(t, f, s, "receipt", "复制完整回执", "导出完整回执", "关闭")
	databendWorkbenchClick(t, tt, "复制完整回执")
	receipt := tt.Clipboard()
	var envelope struct {
		Original        []agentbridge.Receipt
		Receipt         contract.DataSequentialWriteReceipt
		CommitConfirmed bool
	}
	if json.Unmarshal([]byte(receipt), &envelope) != nil || envelope.CommitConfirmed || envelope.Receipt.Object != d.Object() || len(envelope.Receipt.Statements) != 1 || envelope.Receipt.Statements[0].State != contract.DataWriteAcknowledged || envelope.Receipt.Statements[0].AffectedRows != 1 {
		t.Fatal("UI receipt lost original native statement ownership", receipt)
	}
	databendWorkbenchClick(t, tt, "关闭")
	databendWorkbenchClick(t, tt, "单元格编辑器")
	databendWorkbenchClick(t, tt, "添加记录")
	for field, value := range map[string]contract.Cell{"id": {Kind: contract.Integer, Text: "7"}, "txt": {Kind: contract.Text, Text: "inserted 猫"}, "payload": {Kind: contract.Binary, Bytes: []byte{0, 255}}, "exact": {Kind: contract.Decimal, Text: "1.00000000000000000001"}, "j": {Kind: contract.JSON, Text: "null"}, "a": {Kind: contract.JSON, Text: "[]"}, "m": {Kind: contract.JSON, Text: "{}"}} {
		s.dataWorkspace.EditCell(*services, d, 1, field, value)
	}
	databendWorkbenchConfirm(t, s, tt, d)
	rows = databendWorkbenchSQL(t, f, `SELECT id,txt,note,j IS NULL FROM "`+f.Database+`".workbench_items52 WHERE id=7`)
	if len(rows) != 1 || rows[0][1] != "inserted 猫" || rows[0][2] != "native default" || rows[0][3] != "0" {
		t.Fatal("actual UI insert invented omitted defaults or lost JSON null", rows)
	}
	databendWorkbenchClick(t, tt, "inserted 猫")
	databendWorkbenchClick(t, tt, "删除记录")
	databendWorkbenchConfirm(t, s, tt, d)
	rows = databendWorkbenchSQL(t, f, `SELECT id FROM "`+f.Database+`".workbench_items52 ORDER BY id`)
	if len(rows) != 1 || rows[0][0] != "18446744073709551615" {
		t.Fatal("original UI empty-map delete crossed rows", rows)
	}
	databendWorkbenchJournal(t, f, "workbench-acceptance.jsonl", map[string]any{"test": t.Name(), "object": d.Object(), "pages": []int{200, 5}, "typed_values": true, "field_read": true, "complete_editor": true, "preview_cancel_kept_draft": true, "native_update_insert_delete": true, "empty_map_ui_writes": true, "omitted_defaults": true, "receipt": json.RawMessage(receipt), "public_provider_registered": false})
}

func TestDatabendWorkbenchOriginalParkedWriteAndRevokedMetadata(t *testing.T) {
	for _, mode := range []string{"park", "metadata"} {
		t.Run(mode, func(t *testing.T) {
			f := databendWorkbenchOriginal(t)
			table := "workbench_" + mode + "52"
			databendWorkbenchSQL(t, f, `CREATE TABLE "`+f.Database+`".`+table+` (id UInt64 NOT NULL,txt String NOT NULL)`)
			databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".`+table+` VALUES (1,'first'),(2,'second')`)
			held, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once, finish sync.Once
			var writes atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				raw, _ := io.ReadAll(req.Body)
				req.Body = io.NopCloser(bytes.NewReader(raw))
				target := req.Method == http.MethodPost && req.URL.Path == "/v1/query" && bytes.Contains(raw, []byte("UPDATE ")) && bytes.Contains(raw, []byte(table))
				code, headers, body := databendWorkbenchForward(t, f, req)
				if target {
					n := writes.Add(1)
					if n == 1 {
						close(held)
						<-release
					}
					if n == 2 {
						finish.Do(func() { close(completed) })
					}
				}
				for k, values := range headers {
					w.Header()[k] = values
				}
				w.WriteHeader(code)
				_, _ = w.Write(body)
			}))
			defer proxy.Close()
			defer once.Do(func() { close(release) })
			s, tt, p, services := databendWorkbenchState(t, f, proxy.URL)
			d := databendWorkbenchData(t, s, tt, *services, p, table)
			originalTab, page := s.currentTab().id, d.Page()
			for row := range 2 {
				s.dataWorkspace.EditCell(*services, d, row, "txt", contract.Cell{Kind: contract.Text, Text: "changed"})
			}
			databendWorkbenchPreview(t, s, tt, d)
			databendWorkbenchClick(t, tt, "确认并提交更改")
			select {
			case <-held:
			case <-time.After(45 * time.Second):
				t.Fatal("original native write never reached held receipt")
			}
			if mode == "park" {
				other := p
				other.ID = 0
				other.Name = "Parked original Databend"
				if _, err := s.profileService.Create(context.Background(), other); err != nil {
					t.Fatal(err)
				}
				s.loadProfiles()
				s.selectProfileByName(other.Name)
				once.Do(func() { close(release) })
				select {
				case <-completed:
				case <-time.After(45 * time.Second):
					t.Fatal("parked original suffix did not execute")
				}
				for range 10 {
					tt.Frame()
				}
				if !d.Busy() || !d.HasDraft() || d.Page() != page {
					t.Fatal("parked receipt published into inactive original grid")
				}
				s.selectProfileByName(p.Name)
				s.activeTab = s.tabIndex(originalTab)
			} else {
				databendWorkbenchSQL(t, f, `ALTER TABLE "`+f.Database+`".`+table+` ADD COLUMN added UInt64 DEFAULT 7`)
				once.Do(func() { close(release) })
			}
			databendWorkbenchWait(t, tt, func() bool { return !d.Busy() })
			rows := databendWorkbenchSQL(t, f, `SELECT id,txt FROM "`+f.Database+`".`+table+` ORDER BY id`)
			if mode == "park" {
				if d.HasDraft() || len(rows) != 2 || rows[0][1] != "changed" || rows[1][1] != "changed" {
					t.Fatal("original activation lost parked write/readback", rows)
				}
			} else {
				if !d.HasDraft() || s.dataWorkspace.CanSave(d) || writes.Load() != 1 || d.Page() != page || rows[0][1] != "changed" || rows[1][1] != "second" {
					t.Fatal("metadata-revoked suffix dispatched or erased evidence", rows)
				}
			}
			databendWorkbenchClick(t, tt, "原生顺序写入回执")
			databendWorkbenchClick(t, tt, "复制完整回执")
			receipt := tt.Clipboard()
			if !strings.Contains(receipt, `"acknowledged"`) || mode == "metadata" && !strings.Contains(receipt, `"not_sent"`) {
				t.Fatal("original suffix receipt missing", receipt)
			}
			databendWorkbenchClick(t, tt, "关闭")
			databendWorkbenchCapture(t, f, s, mode)
			databendWorkbenchJournal(t, f, "workbench-lifecycle.jsonl", map[string]any{"test": t.Name(), "mode": mode, "writes": writes.Load(), "receipt": json.RawMessage(receipt), "original_only": true, "rows": rows})
		})
	}
}

func databendWorkbenchForward(t *testing.T, f databendWorkbenchFixture, req *http.Request) (int, http.Header, []byte) {
	t.Helper()
	upstream, err := http.NewRequestWithContext(req.Context(), req.Method, f.Endpoint+req.URL.RequestURI(), req.Body)
	if err != nil {
		t.Error(err)
		return 502, nil, nil
	}
	upstream.Header = req.Header.Clone()
	reply, err := (&http.Client{Timeout: 60 * time.Second}).Do(upstream)
	if err != nil {
		t.Error("original HTTP forwarding failed", err)
		return 502, nil, nil
	}
	defer reply.Body.Close()
	body, err := io.ReadAll(reply.Body)
	if err != nil {
		t.Error(err)
	}
	return reply.StatusCode, reply.Header.Clone(), body
}

func TestDatabendWorkbenchOriginalUnknownReceiptGuardAndExplicitSQLRecovery(t *testing.T) {
	f := databendWorkbenchOriginal(t)
	databendWorkbenchSQL(t, f, `CREATE TABLE "`+f.Database+`".workbench_unknown52 (id UInt64 NOT NULL)`)
	var writes atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		target := req.Method == http.MethodPost && req.URL.Path == "/v1/query" && bytes.Contains(raw, []byte("INSERT INTO")) && bytes.Contains(raw, []byte("workbench_unknown52"))
		code, headers, body := databendWorkbenchForward(t, f, req)
		if target && writes.Add(1) == 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"original_reply_dropped":true}`))
			return
		}
		for k, values := range headers {
			w.Header()[k] = values
		}
		w.WriteHeader(code)
		_, _ = w.Write(body)
	}))
	defer proxy.Close()
	s, tt, p, services := databendWorkbenchState(t, f, proxy.URL)
	d := databendWorkbenchData(t, s, tt, *services, p, "workbench_unknown52")
	original, page := s.currentTab(), d.Page()
	for i := range 3 {
		databendWorkbenchClick(t, tt, "添加记录")
		s.dataWorkspace.EditCell(*services, d, i, "id", contract.Cell{Kind: contract.Integer, Text: strconv.Itoa(i + 1)})
	}
	databendWorkbenchPreview(t, s, tt, d)
	databendWorkbenchClick(t, tt, "确认并提交更改")
	databendWorkbenchWait(t, tt, func() bool { return !d.Busy() })
	if !d.HasDraft() || s.dataWorkspace.CanSave(d) || d.Page() != page || writes.Load() != 2 {
		t.Fatal("unknown UI batch lost draft or replayed suffix", d.Error())
	}
	databendWorkbenchClick(t, tt, "原生顺序写入回执")
	databendWorkbenchClick(t, tt, "复制完整回执")
	receipt := tt.Clipboard()
	for _, state := range []string{`"acknowledged"`, `"unknown"`, `"not_sent"`} {
		if !strings.Contains(receipt, state) {
			t.Fatal("unknown UI receipt lost ordered state", state, receipt)
		}
	}
	databendWorkbenchCapture(t, f, s, "unknown-receipt", "复制完整回执", "关闭")
	databendWorkbenchClick(t, tt, "关闭")
	rows := databendWorkbenchSQL(t, f, `SELECT id FROM "`+f.Database+`".workbench_unknown52 ORDER BY id`)
	if len(rows) != 2 || rows[0][0] != "1" || rows[1][0] != "2" {
		t.Fatal("lost reply preceded actual write or sent suffix", rows)
	}
	s.dataWorkspace.VerifySave(*services, d)
	s.dataWorkspace.Load(*services, d, true)
	tt.Frame()
	if s.dataWorkspace.CanSave(d) || writes.Load() != 2 {
		t.Fatal("later read reauthorized unknown write")
	}
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !s.runtime.IsConnected(p.ID) || s.tabIndex(original.id) < 0 {
		t.Fatal("unknown document bypassed original disconnect guard")
	}
	if _, ok := tt.Find(locales.Text("确定")); ok {
		databendWorkbenchClick(t, tt, "确定")
	}
	databendWorkbenchClick(t, tt, "关闭原写入文档")
	databendWorkbenchClick(t, tt, "取消")
	if !d.HasDraft() || s.tabIndex(original.id) < 0 {
		t.Fatal("close cancellation discarded original evidence")
	}
	databendWorkbenchClick(t, tt, "关闭原写入文档")
	databendWorkbenchClick(t, tt, "关闭并放弃本地草稿与证据")
	if s.tabIndex(original.id) >= 0 || d.Page() != nil || !s.runtime.IsConnected(p.ID) {
		t.Fatal("explicit document disposal damaged original session")
	}
	s.openQuery()
	q := s.currentTab().query
	databendWorkbenchWait(t, tt, func() bool { return !q.Preparing() && !q.Loading() })
	source := `SELECT count(*) FROM "workbench_unknown52"`
	q.Editor.Buffer.Set(source)
	tt.Frame()
	databendWorkbenchClick(t, tt, "运行当前或选中 SQL")
	databendWorkbenchWait(t, tt, func() bool { return !q.Loading() })
	if q.Result() == nil {
		t.Fatal("independent original SQL lease unusable", q.Error())
	}
	gen := s.runtime.Generation(p.ID)
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !q.NeedsRecovery() || q.Editor.Buffer.String() != source {
		t.Fatal("original SQL text/recovery lost on disconnect")
	}
	databendWorkbenchCapture(t, f, s, "disconnected")
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	s.sessions.Current().Password = string(secret)
	s.sessions.Connect(p, services.Invalidate)
	databendWorkbenchWait(t, tt, func() bool { return s.runtime.IsConnected(p.ID) && s.sessions.StatusFor(p.ID) == "已连接" })
	databendWorkbenchClick(t, tt, "建立新物理会话（不执行现有 SQL）")
	databendWorkbenchWait(t, tt, func() bool { return !q.NeedsRecovery() && !q.Loading() })
	if q.Result() != nil || q.Editor.Buffer.String() != source || writes.Load() != 2 || s.runtime.Generation(p.ID) <= gen {
		t.Fatal("explicit original recovery replayed or lost SQL")
	}
	databendWorkbenchCapture(t, f, s, "recovered")
	databendWorkbenchJournal(t, f, "workbench-lifecycle.jsonl", map[string]any{"test": t.Name(), "mode": "unknown", "writes": writes.Load(), "receipt": json.RawMessage(receipt), "rows": rows, "draft_guard": true, "sql_text_retained": true, "recovery_replayed": false, "original_generation": gen, "replacement_generation": s.runtime.Generation(p.ID)})
}

func TestDatabendWorkbenchOriginalBoundedFieldsCompleteEditingAndExport(t *testing.T) {
	f := databendWorkbenchOriginal(t)
	databendWorkbenchSQL(t, f, `CREATE TABLE "`+f.Database+`".workbench_fields52 (payload String NOT NULL)`)
	databendWorkbenchSQL(t, f, `INSERT INTO "`+f.Database+`".workbench_fields52 VALUES (concat(repeat('猫',2049),'end'))`)
	s, tt, p, services := databendWorkbenchState(t, f, f.Endpoint)
	d := databendWorkbenchData(t, s, tt, *services, p, "workbench_fields52")
	if !d.Page().Rows[0][0].Truncated || len([]rune(d.Page().Rows[0][0].Value.Text)) != 1024 {
		t.Fatal("original shared preview exceeded native bound")
	}
	databendWorkbenchClick(t, tt, d.Page().Rows[0][0].Text)
	databendWorkbenchClick(t, tt, "字段信息")
	var complete strings.Builder
	for _, expected := range []int64{1024, 2048, 2052} {
		databendWorkbenchClick(t, tt, "读取所选字段下一段")
		databendWorkbenchWait(t, tt, func() bool { return !d.Inspector().ChunkLoading() })
		if d.Inspector().ChunkError() != "" || d.Inspector().ChunkNext() != expected || d.Inspector().ChunkTotal() != 2052 || len([]rune(d.Inspector().ChunkText())) > 1024 {
			t.Fatal("bounded Unicode fragment lost original offsets", d.Inspector().ChunkError())
		}
		complete.WriteString(d.Inspector().ChunkText())
	}
	original := strings.Repeat("猫", 2049) + "end"
	if complete.String() != original {
		t.Fatal("bounded original fragments changed Unicode or suffix")
	}
	databendWorkbenchCapture(t, f, s, "bounded-inspector", "读取所选字段下一段", "复制此片段")
	databendWorkbenchClick(t, tt, "单元格编辑器")
	databendWorkbenchWait(t, tt, func() bool { return !d.Busy() && tt.HasText(locales.Text("单元格编辑器内容")) })
	databendWorkbenchClick(t, tt, "单元格编辑器内容")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != original {
		t.Fatal("complete original editor reused bounded preview")
	}
	databendWorkbenchCapture(t, f, s, "complete-editor", "单元格编辑器内容")
	tt.Type(original + " 改")
	tt.Frame()
	databendWorkbenchConfirm(t, s, tt, d)
	if rows := databendWorkbenchSQL(t, f, `SELECT char_length(payload),right(payload,5) FROM "`+f.Database+`".workbench_fields52`); len(rows) != 1 || rows[0][0] != "2054" || rows[0][1] != "end 改" {
		t.Fatal("complete UI edit lost original suffix", rows)
	}
	foreignA, err := s.runtime.Data(p.ID).ReserveDocument(d.Object())
	if err != nil {
		t.Fatal(err)
	}
	defer foreignA.Close()
	foreignB, err := s.runtime.Data(p.ID).ReserveDocument(d.Object())
	if err != nil {
		t.Fatal(err)
	}
	defer foreignB.Close()
	a, b := foreignA.Runtime(), foreignB.Runtime()
	if _, err := a.RowReader(context.Background(), p.ID, p.Database, d.Object().Name, *d.Page().RowRefs[0]); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("signed UI row reference crossed original document", err)
	}
	plan, err := a.PreviewEdits(context.Background(), p.ID, p.Database, d.Object().Name, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"payload": {Kind: contract.Text, Text: "never sent"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	if _, err := b.SubmitEdits(context.Background(), p.ID, p.Database, d.Object().Name, plan); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("original immutable plan crossed native data document", err)
	}
	var export bytes.Buffer
	s.dataWorkspace.SetFiles(func(string) (string, error) { return filepath.Join(f.Directory, "workbench-fields.jsonl"), nil }, func(_ string, write func(io.Writer) error) error { return write(&export) })
	s.dataWorkspace.ExportJSONL(*services, d)
	databendWorkbenchWait(t, tt, func() bool { return !d.Busy() })
	var exported struct {
		Object  contract.ObjectRef `json:"object"`
		Columns []contract.Column  `json:"columns"`
		Values  []contract.Cell    `json:"values"`
	}
	if d.Error() != "" || json.Unmarshal(bytes.TrimSpace(export.Bytes()), &exported) != nil || exported.Object != d.Object() || len(exported.Columns) != 1 || exported.Columns[0].Name != "payload" || len(exported.Values) != 1 || exported.Values[0].Kind != contract.Text || exported.Values[0].Truncated || exported.Values[0].Text != original+" 改" {
		t.Fatal("shared native JSONL export reused bounded grid", d.Error())
	}
	databendWorkbenchJournal(t, f, "workbench-fields.jsonl", map[string]any{"test": t.Name(), "preview_units": 1024, "chunk_boundaries": []int{1024, 2048, 2052}, "complete_edit_units": 2054, "foreign_grant_rejected": true, "foreign_plan_rejected": true, "full_jsonl": json.RawMessage(bytes.TrimSpace(export.Bytes()))})
}
