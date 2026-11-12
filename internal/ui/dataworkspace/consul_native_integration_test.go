package dataworkspace

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func consulGridProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_CONSUL_IT") != "1" {
		t.Skip("owned native Consul fixture required")
	}
	u, err := url.Parse(os.Getenv("NYADB_CONSUL_ENDPOINT"))
	if err != nil || u.Hostname() == "" {
		t.Fatal("owned endpoint unavailable")
	}
	p, _ := connections.Defaults(connections.Consul)
	port, _ := strconv.Atoi(u.Port())
	p.ID, p.Name, p.Host, p.Port = 93, "original Consul data", u.Hostname(), uint16(port)
	p.ConsulOptions.Auth, p.ConsulOptions.Datacenter = os.Getenv("NYADB_CONSUL_AUTH"), "dc1"
	p.ConsulOptions.CAFile, p.ConsulOptions.ClientCertFile, p.ConsulOptions.ClientKeyFile = os.Getenv("NYADB_CONSUL_CA"), os.Getenv("NYADB_CONSUL_CERT"), os.Getenv("NYADB_CONSUL_KEY")
	p.ConsulOptions.RequestTimeoutSeconds = 30
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	return p, os.Getenv("NYADB_CONSUL_TOKEN")
}

func consulGridWrite(t *testing.T, writer contract.KVRequestLease, native consul.Request) {
	t.Helper()
	r, err := consul.DocumentRequest(native)
	if err != nil {
		t.Fatal(err)
	}
	p, err := writer.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	result, err := writer.Execute(context.Background(), p)
	if err != nil || !result.Completed || result.HTTP == nil || !result.HTTP.Succeeded {
		t.Fatal("owned original data fixture request failed", err)
	}
}

func consulGridPut(t *testing.T, writer contract.KVRequestLease, key string, value []byte) {
	t.Helper()
	flags := uint64(math.MaxUint64)
	r, err := consul.KeyPutRequest(key, value, &flags, nil)
	if err != nil {
		t.Fatal(err)
	}
	consulGridWrite(t, writer, r)
}

func waitConsulGrid(t *testing.T, s *State, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !done() && time.Now().Before(deadline) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	s.Consume(ui.Services{})
	if !done() {
		t.Fatal("original Consul grid worker did not settle")
	}
}

func captureConsulGrid(t *testing.T, s *State, d *Document, stage string) {
	t.Helper()
	dir := os.Getenv("NYADB_CONSUL_GRID_IMAGES")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	defer i18n.SetLocaleFunc(nil)
	open := d.nativeReceipt.Open
	if strings.HasPrefix(stage, "mutation-unknown-") || strings.HasPrefix(stage, "mutation-warning-") {
		d.nativeReceipt.Open = true
		defer func() { d.nativeReceipt.Open = open }()
	}
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c)
				s.Consume(c.Services())
				if stage == "count" || stage == "field" {
					ui.Row(c).Fill().Children(func() {
						ui.Box(c).Grow(1).MinWidth(0).FillHeight().Children(func() { s.View(c, d) })
						ui.Box(c).Width(300).FillHeight().Children(func() { s.InspectorView(c, d) })
					})
				} else {
					s.View(c, d)
				}
				s.Modals(c)
			}, size[0], size[1])
			tt.Frame()
			var controls []string
			if strings.HasPrefix(stage, "prefix-set-") && strings.HasSuffix(stage, "-form") {
				controls = []string{"输入完整分批删除前缀", "捕获完整候选键", "取消"}
			} else if strings.HasPrefix(stage, "prefix-set-") && strings.HasSuffix(stage, "-review") {
				controls = []string{"复制完整候选键列表", "原始观察", "进度与回执", "停止并保留原观察与回执"}
			} else if strings.HasPrefix(stage, "prefix-set-") && strings.HasSuffix(stage, "-confirmation") {
				controls = []string{"复制完整预览", "取消本批确认", "确认并删除本批候选键"}
			} else if strings.HasPrefix(stage, "prefix-set-") && (strings.HasSuffix(stage, "-ledger") || strings.HasSuffix(stage, "-observation")) {
				controls = []string{"复制完整内容", "导出完整内容", "关闭"}
			} else if strings.HasPrefix(stage, "prefix-set-") && strings.HasSuffix(stage, "-receipt") {
				controls = []string{"复制完整回执", "导出完整回执", "关闭"}
			} else if strings.HasPrefix(stage, "prefix-delete-") && strings.HasSuffix(stage, "-form") {
				controls = []string{"输入完整删除前缀", "预览候选键删除", "取消"}
			} else if strings.HasPrefix(stage, "prefix-delete-") && strings.HasSuffix(stage, "-confirmation") {
				controls = []string{"复制完整预览", "取消", "确认并删除候选键"}
			} else if strings.HasPrefix(stage, "prefix-delete-") && strings.HasSuffix(stage, "-observation") || strings.HasPrefix(stage, "prefix-delete-after-read-") {
				controls = []string{"复制完整观察", "导出完整观察", "关闭"}
			} else if strings.HasPrefix(stage, "prefix-delete-unknown-") || strings.HasPrefix(stage, "prefix-delete-") && strings.HasSuffix(stage, "-saved") {
				controls = []string{"复制完整回执", "导出完整回执", "关闭"}
			} else if strings.HasPrefix(stage, "key-copy-") && strings.HasSuffix(stage, "-form") {
				controls = []string{"输入目标完整键", "预览复制", "取消"}
			} else if stage == "mutation-confirmation" || stage == "mutation-delete-confirmation" || strings.HasPrefix(stage, "key-copy-") && strings.HasSuffix(stage, "-confirmation") {
				controls = []string{"复制完整预览", "取消", "确认并提交更改"}
			} else if strings.HasPrefix(stage, "key-copy-unknown-") || strings.HasPrefix(stage, "mutation-unknown-") || strings.HasPrefix(stage, "mutation-warning-") {
				controls = []string{"复制完整回执", "导出完整回执", "关闭"}
			}
			for _, control := range controls {
				box, ok := tt.Find(locales.Text(control))
				if !ok || box.W <= 0 || box.H <= 0 || box.X < 0 || box.Y < 0 || box.X+box.W > float32(size[0]) || box.Y+box.H > float32(size[1]) {
					t.Fatal("original Consul mutation control clipped", language, size, stage, control, box)
				}
			}
			if len(d.page.Rows) != 0 && !tt.HasText(locales.Text("原生游标读取实时数据；翻页和重读期间数据变化可能导致行重复或遗漏。")) {
				t.Fatal("live cursor semantics missing from actual shared grid", language, stage)
			}
			if stage == "count" && !tt.HasText(locales.Text(s.runtimeFor(d).ReadScopeHint)) {
				t.Fatal("independent visible-prefix scope hint missing", language)
			}
			if stage == "field" && (tt.HasText(locales.Text("SQL NULL / 未设置")) || tt.HasText(locales.Text("默认值")) || tt.HasText(locales.Text("自增"))) {
				t.Fatal("native KV field inspector exposed relational attributes", language)
			}
			if stage == "read-retired" {
				if _, ok := tt.Find(locales.Text("重新打开数据")); !ok {
					t.Fatal("explicit original reopen action missing")
				}
			}
			path := filepath.Join(dir, fmt.Sprintf("%s-%dx%d-data-%s.png", language, size[0], size[1], stage))
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, tt.Image())
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				t.Fatal("shared grid image failed", err, closeErr)
			}
		}
	}
}

func TestConsulNativePublicDataSharedGridLivePagingInspectorExportsParkingAndOriginalClose(t *testing.T) {
	p, secret := consulGridProfile(t)
	p.ConsulOptions.Prefix = "native/grid//" + t.Name() + "/中文/"
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	h, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	provider, _ := m.KVRequests(p.ID)
	writer, err := provider.ReserveKV(99)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	value := bytes.Repeat([]byte{0, 255}, 1024)
	for _, name := range []string{"a", "b", "d"} {
		consulGridPut(t, writer, p.ConsulOptions.Prefix+name, value)
	}
	active := Session{ProfileID: p.ID, Generation: h.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session {
		return Session{ProfileID: id, Generation: active.Generation, Connected: active.Connected}
	},
		RuntimeFor: func(id int64) Runtime { return m.Data(id) }, Inspector: InspectorDependencies{Session: func() Session { return active }, Profile: func(int64) (connections.Profile, bool) { return p, true }}})
	defer s.CloseAll()
	object := m.Namespace(p.ID, "kv").Object("keyspace", "keys")
	d, sibling := s.OpenObject(1, object, h.Capabilities()), s.OpenObject(2, object, h.Capabilities())
	d.limit, sibling.limit = 2, 2
	load := func(doc *Document, refresh bool) {
		t.Helper()
		s.Load(ui.Services{}, doc, refresh)
		waitConsulGrid(t, s, func() bool { return !doc.Busy() })
		if doc.err != "" || doc.page == nil {
			t.Fatal("actual shared Consul page failed", doc.err)
		}
	}
	load(d, false)
	load(sibling, false)
	if d.Kind() != "键" || !d.page.LiveRead || d.page.PagingMode != "cursor" || len(d.page.Rows) != 2 || !d.editable() || d.runtimeLease == nil || len(d.page.Columns) != 7 || d.page.Rows[0][2].Value.Text != "18446744073709551615" || len(d.page.Rows[0][1].Value.Bytes) != 1024 || !d.page.Rows[0][1].Truncated {
		t.Fatal("shared grid lost exact native values or read-only capabilities")
	}
	for _, column := range d.columns {
		if column.Sortable {
			t.Fatal("unsupported native sort exposed")
		}
	}
	if len(s.filterColumns(d, d.page.Columns)) != 0 || d.supportsExportFormat("sql") || d.capabilities.Import || d.capabilities.Transactions {
		t.Fatal("unsupported relational data tools exposed")
	}
	captureConsulGrid(t, s, d, "live-grid")
	cachedSibling := sibling.page
	remove, _ := consul.KeyDeleteRequest(p.ConsulOptions.Prefix+"b", nil, false)
	consulGridWrite(t, writer, remove)
	consulGridPut(t, writer, p.ConsulOptions.Prefix+"c", []byte{3})
	s.changeMySQLDataPage(ui.Services{}, d, 2)
	waitConsulGrid(t, s, func() bool { return !d.Busy() })
	if d.err != "" || len(d.page.Rows) != 2 || d.page.Rows[0][0].Value.Text != p.ConsulOptions.Prefix+"c" || sibling.page != cachedSibling {
		t.Fatal("live cursor hid changed rows or altered sibling cache", d.err)
	}
	captureConsulGrid(t, s, d, "live-next")
	load(d, true)
	captureConsulGrid(t, s, d, "refresh")
	panel := s.inspectorPanel(d)
	s.inspector.LoadInfo(panel, nil, false)
	s.inspector.LoadCount(panel, nil)
	waitConsulGrid(t, s, func() bool { return !panel.InfoLoading() && !panel.CountLoading() })
	if panel.CountError() != "" || panel.Count() == nil || *panel.Count() != 3 {
		t.Fatal("independent original count failed", panel.CountError())
	}
	captureConsulGrid(t, s, d, "count")
	d.selectedRow, d.grid.Cells.Cursor = 0, "value"
	s.ShowField(d)
	s.inspector.LoadChunk(panel, nil)
	waitConsulGrid(t, s, func() bool { return !panel.ChunkLoading() })
	if panel.ChunkError() != "" || panel.ChunkNext() != 1024 || panel.ChunkTotal() != 2048 || len(panel.ChunkText()) != 2050 {
		t.Fatal("bounded original field preview failed", panel.ChunkError())
	}
	captureConsulGrid(t, s, d, "field")
	row, err := s.runtimeFor(d).RowReader(context.Background(), p.ID, "kv", "keys", *d.page.RowRefs[0])
	if err != nil || !bytes.Equal(row.Values["value"].Bytes, value) || !reflect.DeepEqual(row.Snapshot, d.page.RowSnapshots[0]) {
		t.Fatal("selected row export verification confused live HTTP metadata with row identity", err)
	}
	// Exercise the actual atomic export writer for a selected page and for the
	// independent complete observation, including values beyond the preview.
	for _, scope := range []string{"page", "all"} {
		s.openDataExport(d)
		d.transfer.Format, d.transfer.Scope = "JSON", scope
		d.transfer.ExportPath = filepath.Join(t.TempDir(), scope+".json")
		s.startDataExport(ui.Services{}, d)
		waitConsulGrid(t, s, func() bool { return !d.transfer.Busy })
		if d.transfer.Err != "" {
			t.Fatal("actual export failed", scope, d.transfer.Err)
		}
		raw, err := os.ReadFile(d.transfer.ExportPath)
		var rows []map[string]json.RawMessage
		if err != nil || json.Unmarshal(raw, &rows) != nil || len(rows) != map[string]int{"page": 2, "all": 3}[scope] || !bytes.Contains(raw, []byte("18446744073709551615")) {
			t.Fatal("complete original export changed rows or unsigned values", scope, err)
		}
		var encoded string
		if json.Unmarshal(rows[0]["value"], &encoded) != nil || encoded != "0x"+hex.EncodeToString(value) {
			t.Fatal("atomic export truncated or changed complete original bytes", scope)
		}
		s.closeDataTransfer(d)
	}
	captureConsulGrid(t, s, d, "export")
	s.Load(ui.Services{}, d, true)
	active.ProfileID = 94
	parkRetirementPage(t, d)
	s.Consume(ui.Services{})
	if !d.loading {
		t.Fatal("parked completion consumed by another profile")
	}
	active.ProfileID = p.ID
	waitConsulGrid(t, s, func() bool { return !d.Busy() })
	captureConsulGrid(t, s, d, "parked")
	remove, _ = consul.KeyDeleteRequest(p.ConsulOptions.Prefix, nil, true)
	consulGridWrite(t, writer, remove)
	load(d, true)
	if len(d.page.Rows) != 0 || d.page.HasMore || !d.page.LiveRead {
		t.Fatal("empty visible prefix was not a successful live page")
	}
	captureConsulGrid(t, s, d, "empty")
	if token := os.Getenv("NYADB_CONSUL_READ_TOKEN"); token != "" {
		limited := p
		limited.ID, limited.ConsulOptions.Prefix = 95, "allowed/grid/"+t.Name()+"/"
		consulGridPut(t, writer, limited.ConsulOptions.Prefix+"key", []byte{0, 255})
		limitedSession, err := m.ConnectSession(context.Background(), limited, token)
		if err != nil {
			t.Fatal(err)
		}
		active.ProfileID, active.Generation = limited.ID, limitedSession.Generation()
		ld := s.OpenObject(3, m.Namespace(limited.ID, "kv").Object("keyspace", "keys"), limitedSession.Capabilities())
		load(ld, false)
		if len(ld.page.Rows) != 1 || ld.page.Rows[0][0].Value.Text != limited.ConsulOptions.Prefix+"key" {
			t.Fatal("restricted visible grid expanded prefix")
		}
		s.Close(ld)
	}
	active.ProfileID, active.Generation = p.ID, h.Generation()+1
	cached := d.page
	s.Load(ui.Services{}, d, true)
	if d.page != cached || d.loading || d.err == "" {
		t.Fatal("replacement generation overwrote original cached page")
	}
	s.Close(sibling)
	if !sibling.closed || sibling.page != nil || d.page != cached {
		t.Fatal("inactive close cleared active original document")
	}
}

// Forward a completed original native page read and fault only its actual reply.
func consulGridOriginalHTTP(t *testing.T, p connections.Profile) (*http.Client, string) {
	t.Helper()
	secure := &tls.Config{MinVersion: tls.VersionTLS12}
	if p.ConsulOptions.CAFile != "" {
		raw, err := os.ReadFile(p.ConsulOptions.CAFile)
		if err != nil {
			t.Fatal(err)
		}
		secure.RootCAs = x509.NewCertPool()
		if !secure.RootCAs.AppendCertsFromPEM(raw) {
			t.Fatal("invalid original CA")
		}
	}
	if p.ConsulOptions.ClientCertFile != "" {
		pair, err := tls.LoadX509KeyPair(p.ConsulOptions.ClientCertFile, p.ConsulOptions.ClientKeyFile)
		if err != nil {
			t.Fatal(err)
		}
		secure.Certificates = []tls.Certificate{pair}
	}
	transport := &http.Transport{TLSClientConfig: secure, DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cfg, err := connections.ConsulProfileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	return client, cfg.Endpoint
}

func consulGridRelay(t *testing.T, p connections.Profile, mode string) (connections.Profile, *atomic.Bool, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	client, endpoint := consulGridOriginalHTTP(t, p)
	armed, calls := &atomic.Bool{}, &atomic.Int32{}
	processed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		var operations []consul.TxnOperation
		treeRead := r.URL.Path == "/v1/txn" && json.Unmarshal(raw, &operations) == nil && len(operations) == 1 && operations[0].KV != nil && operations[0].KV.Verb == "get-tree"
		dataRead := r.URL.Query().Has("recurse") || treeRead
		request, err := http.NewRequestWithContext(r.Context(), r.Method, endpoint+r.URL.RequestURI(), bytes.NewReader(raw))
		if err != nil {
			return
		}
		request.Header = r.Header.Clone()
		response, err := client.Do(request)
		if err != nil {
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return
		}
		for k, values := range response.Header {
			for _, value := range values {
				w.Header().Add(k, value)
			}
		}
		if dataRead && armed.CompareAndSwap(true, false) {
			calls.Add(1)
			close(processed)
			switch mode {
			case "lost":
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			case "partial":
				w.Header().Set("Content-Length", strconv.Itoa(len(body)+50))
				w.WriteHeader(response.StatusCode)
				w.Write(body[:min(10, len(body))])
				return
			case "cancel", "close", "replace":
				<-r.Context().Done()
				return
			}
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	p.Host, p.Port, p.TLSMode = u.Hostname(), uint16(port), "disable"
	p.ConsulOptions.CAFile, p.ConsulOptions.ClientCertFile, p.ConsulOptions.ClientKeyFile = "", "", ""
	return p, armed, calls, processed
}

func TestConsulNativePublicDataSharedGridProcessedReplyRetirementParkingAndExplicitReopen(t *testing.T) {
	for _, mode := range []string{"lost", "partial", "cancel", "close", "replace"} {
		t.Run(mode, func(t *testing.T) {
			p, secret := consulGridProfile(t)
			p.ConsulOptions.Prefix = "native/grid-fault/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
			seed := dbruntime.New()
			dbruntime.RegisterBuiltins(seed, nil)
			defer seed.Close()
			if _, err := seed.ConnectSession(context.Background(), p, secret); err != nil {
				t.Fatal(err)
			}
			provider, _ := seed.KVRequests(p.ID)
			writer, _ := provider.ReserveKV(99)
			defer writer.Close()
			consulGridPut(t, writer, p.ConsulOptions.Prefix+"key", []byte{0, 255})
			proxy, armed, calls, processed := consulGridRelay(t, p, mode)
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			h, err := m.ConnectSession(context.Background(), proxy, secret)
			if err != nil {
				t.Fatal(err)
			}
			active := Session{ProfileID: p.ID, Generation: h.Generation(), Connected: true}
			var s *State
			var reopened *Document
			s = New(Dependencies{Session: func() Session { return active }, RuntimeFor: func(id int64) Runtime { return m.Data(id) },
				Inspector: InspectorDependencies{Session: func() Session { return active }},
				ReopenDocument: func(services ui.Services, original *Document) {
					if original.Object().ProfileID != p.ID || original.Generation() != active.Generation {
						t.Error("reopen changed original identity")
					}
					object, id, caps := original.Object(), original.id, original.capabilities
					s.Close(original)
					reopened = s.OpenObject(id, object, caps)
					s.Load(services, reopened, false)
				}})
			defer s.CloseAll()
			d := s.OpenObject(1, m.Namespace(p.ID, "kv").Object("keyspace", "keys"), h.Capabilities())
			s.Load(ui.Services{}, d, false)
			waitConsulGrid(t, s, func() bool { return !d.Busy() })
			if d.page == nil || d.err != "" {
				t.Fatal("original initial page failed", d.err)
			}
			cached := d.page
			armed.Store(true)
			s.Load(ui.Services{}, d, true)
			awaitRetirementSignal(t, processed)
			switch mode {
			case "cancel":
				if tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 1280, 800); tt.Click(locales.Text("取消读取数据")) != nil {
					t.Fatal("actual shared-grid cancel action missing")
				}
			case "close":
				s.Close(d)
			case "replace":
				fresh, err := m.ConnectSession(context.Background(), proxy, secret)
				if err != nil {
					t.Fatal(err)
				}
				active.Generation = fresh.Generation()
			}
			if mode == "close" {
				if !d.closed || d.page != nil || calls.Load() != 1 {
					t.Fatal("close retained or replayed original page")
				}
				return
			}
			active.ProfileID = 94
			parkRetirementPage(t, d)
			s.Consume(ui.Services{})
			if !d.loading || d.page != cached {
				t.Fatal("parked original fault was consumed")
			}
			active.ProfileID = p.ID
			waitConsulGrid(t, s, func() bool { return !d.Busy() })
			if d.page != cached || d.err == "" || calls.Load() != 1 {
				t.Fatal("original read fault replaced cached data or replayed", d.err)
			}
			if mode == "replace" {
				if s.CanReopen(d) {
					t.Fatal("old document silently followed replacement generation")
				}
				return
			}
			if !s.CanReopen(d) {
				t.Fatal("explicit original document reopen unavailable")
			}
			if mode == "cancel" {
				captureConsulGrid(t, s, d, "read-retired")
			}
			s.Load(ui.Services{}, d, true)
			if d.loading || calls.Load() != 1 {
				t.Fatal("retired original read automatically repeated")
			}
			s.Reopen(ui.Services{}, d)
			waitConsulGrid(t, s, func() bool { return reopened != nil && !reopened.Busy() })
			if reopened.id != d.id || reopened.err != "" || len(reopened.page.Rows) != 1 || calls.Load() != 1 {
				t.Fatal("explicit original reopen failed or replayed lost executor", reopened.err)
			}
			if mode == "cancel" {
				captureConsulGrid(t, s, reopened, "reopened")
			}
		})
	}
}
