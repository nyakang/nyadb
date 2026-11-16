package shell

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/locales"
)

// Relay real official-product traffic. Only the first original data write
// acknowledgement is held/corrupted; handshake messages are never recorded.
type tdengineWriteRelay struct {
	server                   *httptest.Server
	release                  chan struct{}
	held                     chan json.RawMessage
	once                     sync.Once
	first                    atomic.Bool
	writes, acknowledgements atomic.Int64
	mu                       sync.Mutex
	connections              []*websocket.Conn
}

func tdengineRelayWrites(t *testing.T, p connections.Profile, db, mode string, after func()) (connections.Profile, *tdengineWriteRelay) {
	t.Helper()
	relay := &tdengineWriteRelay{release: make(chan struct{}), held: make(chan json.RawMessage, 1)}
	prefix := "INSERT INTO `" + db + "`.`items`"
	upgrader := websocket.Upgrader{}
	relay.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		down, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer down.Close()
		up, _, err := websocket.DefaultDialer.Dial("ws://"+net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))+"/ws", nil)
		if err != nil {
			return
		}
		defer up.Close()
		relay.mu.Lock()
		relay.connections = append(relay.connections, down, up)
		relay.mu.Unlock()
		var request atomic.Uint64
		clientDone := make(chan struct{})
		go func() {
			defer close(clientDone)
			defer up.Close()
			defer down.Close()
			for {
				kind, raw, err := down.ReadMessage()
				if err != nil {
					return
				}
				if kind == websocket.BinaryMessage && len(raw) >= 30 && binary.LittleEndian.Uint64(raw[16:]) == 6 && strings.HasPrefix(string(raw[30:]), prefix) {
					request.Store(binary.LittleEndian.Uint64(raw))
					relay.writes.Add(1)
				}
				if up.WriteMessage(kind, raw) != nil {
					return
				}
			}
		}()
		defer func() { down.Close(); up.Close(); <-clientDone }()
		for {
			kind, raw, err := up.ReadMessage()
			if err != nil {
				return
			}
			var response struct {
				Request uint64 `json:"req_id"`
				Action  string `json:"action"`
				Code    int    `json:"code"`
			}
			if kind == websocket.TextMessage && json.Unmarshal(raw, &response) == nil && request.Load() != 0 && response.Request == request.Load() && response.Action == "binary_query" {
				relay.acknowledgements.Add(1)
				if relay.first.CompareAndSwap(false, true) {
					relay.held <- append(json.RawMessage(nil), raw...)
					if after != nil {
						after()
					}
					if mode == "malformed" {
						raw, _ = json.Marshal(map[string]any{"req_id": response.Request, "action": "binary_query", "is_update": true, "affected_rows": 1, "injected_missing_code": true})
					} else {
						<-relay.release
						if mode == "cancel" {
							return
						}
					}
				}
			}
			if down.WriteMessage(kind, raw) != nil {
				return
			}
		}
	}))
	t.Cleanup(func() {
		relay.once.Do(func() { close(relay.release) })
		relay.mu.Lock()
		for _, c := range relay.connections {
			_ = c.Close()
		}
		relay.mu.Unlock()
		relay.server.Close()
	})
	u, _ := url.Parse(relay.server.URL)
	port, _ := strconv.ParseUint(u.Port(), 10, 16)
	proxied := p
	proxied.Host, proxied.Port = u.Hostname(), uint16(port)
	return proxied, relay
}

func TestTDengineNativePublicUnknownWriteReceiptCloseGuardAndRecovery(t *testing.T) {
	for _, mode := range []string{"cancel", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			p, password, sql, db := tdenginePublicWriteFixture(t)
			tdenginePublicSQL(t, sql, db, "CREATE TABLE items(ts TIMESTAMP,v INT);INSERT INTO items VALUES(1704067200123456789,11)")
			p, relay := tdengineRelayWrites(t, p, db, mode, nil)
			s, tt, _, saved, services := keyPanelState(t, p, password)
			t.Cleanup(s.CloseMySQLSessions)
			d := tdenginePublicData(t, s, tt, *services, saved, db, "table", "items")
			original := s.currentTab()
			page := d.Page()
			before := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{{Original: page.RowSnapshots[0]}}}).Edits[0].Original
			s.dataWorkspace.EditCell(*services, d, 0, "v", contract.Cell{Kind: contract.Integer, Text: "44"})
			tdengineWritePreview(t, s, tt, d)
			tdengineWriteClick(t, tt, "确认并提交更改")
			var held json.RawMessage
			select {
			case held = <-relay.held:
			case <-time.After(20 * time.Second):
				t.Fatal("original official write acknowledgement not observed")
			}
			if !strings.Contains(string(held), `"affected_rows":1`) {
				t.Fatal("held acknowledgement is not real native accepted write")
			}
			if mode == "cancel" {
				if !d.Busy() || !d.HasDraft() {
					t.Fatal("held original acknowledgement released UI")
				}
				tdengineWriteClick(t, tt, "停止数据操作")
			}
			taskPanelWait(t, tt, func() bool { return !d.Busy() })
			if !d.HasDraft() || s.dataWorkspace.CanSave(d) || d.Page() != page || d.Page().Rows[0][1].Value.Text != "44" || !reflect.DeepEqual(d.Page().RowSnapshots[0], before) {
				t.Fatalf("unknown native acknowledgement changed original snapshot or drafts: draft=%v save=%v same-page=%v same-snapshot=%v", d.HasDraft(), s.dataWorkspace.CanSave(d), d.Page() == page, reflect.DeepEqual(d.Page().RowSnapshots[0], before))
			}
			originalReceipt := tdengineWriteReceipt(t, tt)
			if !strings.Contains(originalReceipt, `"unknown"`) || mode == "malformed" && !strings.Contains(originalReceipt, "injected_missing_code") {
				t.Fatal("original unknown/corrupt receipt not retained", originalReceipt)
			}
			if tdenginePublicSQL(t, sql, db, "SELECT v FROM items")[0][0].Text != "44" {
				t.Fatal("fault preceded actual native write")
			}
			s.dataWorkspace.VerifySave(*services, d)
			tt.Frame()
			if tdengineWriteReceipt(t, tt) != originalReceipt || relay.writes.Load() != 1 || s.dataWorkspace.CanSave(d) {
				t.Fatal("later read promoted acknowledgement or replayed request")
			}
			s.disconnectMySQLProfile(saved.ID)
			tt.Frame()
			if !s.runtime.IsConnected(saved.ID) || s.tabIndex(original.id) < 0 {
				t.Fatal("unknown native document bypassed disconnect guard")
			}
			// The existing leave error can be dismissed without discarding evidence.
			if _, ok := tt.Find(locales.Text("确定")); ok {
				tdengineWriteClick(t, tt, "确定")
			}
			tdengineWriteClick(t, tt, "关闭原写入文档")
			tdengineWriteClick(t, tt, "取消")
			if s.tabIndex(original.id) < 0 || !d.HasDraft() {
				t.Fatal("canceling native close discarded original state")
			}
			tdengineWriteClick(t, tt, "关闭原写入文档")
			tdengineWriteClick(t, tt, "关闭并放弃本地草稿与证据")
			if s.tabIndex(original.id) >= 0 || d.Page() != nil || !s.runtime.IsConnected(saved.ID) || relay.writes.Load() != 1 {
				t.Fatal("explicit close replayed or disconnected sibling session")
			}
			s.openQuery()
			query := s.currentTab().query
			taskPanelWait(t, tt, func() bool { return !query.Preparing() && !query.Loading() })
			text := "SELECT count(*) FROM `items`"
			query.Editor.Buffer.Set(text)
			tt.Frame()
			tdengineWriteClick(t, tt, "运行当前或选中 SQL")
			taskPanelWait(t, tt, func() bool { return !query.Loading() })
			if query.Result() == nil {
				t.Fatal("original unknown write damaged sibling SQL lease", query.Error())
			}
			s.disconnectMySQLProfile(saved.ID)
			tt.Frame()
			if !query.NeedsRecovery() || query.Editor.Buffer.String() != text {
				t.Fatal("disconnect lost SQL text or explicit recovery")
			}
			s.sessions.Current().Password = password
			s.sessions.Connect(saved, services.Invalidate)
			taskPanelWait(t, tt, func() bool { return s.runtime.IsConnected(saved.ID) })
			tdengineWriteClick(t, tt, "建立新物理会话（不执行现有 SQL）")
			taskPanelWait(t, tt, func() bool { return !query.NeedsRecovery() && !query.Loading() })
			if query.Result() != nil || query.Editor.Buffer.String() != text || relay.writes.Load() != 1 {
				t.Fatal("explicit original recovery replayed SQL/data")
			}
		})
	}
}

func TestTDengineNativePublicParkedWriteAndMetadataRevokedSuffix(t *testing.T) {
	for _, mode := range []string{"park", "metadata"} {
		t.Run(mode, func(t *testing.T) {
			p, password, sql, db := tdenginePublicWriteFixture(t)
			tdenginePublicSQL(t, sql, db, "CREATE TABLE items(ts TIMESTAMP,v INT);INSERT INTO items VALUES(1704067200123456789,11),(1704067201123456789,22)")
			var after func()
			metadataResult := make(chan error, 1)
			if mode == "metadata" {
				after = func() {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					_, err := sql.Execute(ctx, db, "ALTER TABLE items ADD COLUMN added INT")
					metadataResult <- err
				}
			}
			p, relay := tdengineRelayWrites(t, p, db, mode, after)
			s, tt, profiles, saved, services := keyPanelState(t, p, password)
			t.Cleanup(s.CloseMySQLSessions)
			d := tdenginePublicData(t, s, tt, *services, saved, db, "table", "items")
			originalTab := s.currentTab().id
			page := d.Page()
			for i := range 2 {
				s.dataWorkspace.EditCell(*services, d, i, "v", contract.Cell{Kind: contract.Integer, Text: "44"})
			}
			tdengineWritePreview(t, s, tt, d)
			tdengineWriteClick(t, tt, "确认并提交更改")
			select {
			case <-relay.held:
			case <-time.After(20 * time.Second):
				t.Fatal("original native first acknowledgement not observed")
			}
			if mode == "park" {
				other := saved
				other.ID = 0
				other.Name = "Parked TDengine profile"
				other, err := profiles.Create(context.Background(), other)
				if err != nil {
					t.Fatal(err)
				}
				s.loadProfiles()
				s.selectProfileByName(other.Name)
				relay.once.Do(func() { close(relay.release) })
				deadline := time.Now().Add(20 * time.Second)
				for relay.acknowledgements.Load() < 2 && time.Now().Before(deadline) {
					tt.Frame()
					time.Sleep(2 * time.Millisecond)
				}
				if relay.acknowledgements.Load() != 2 || !d.Busy() || !d.HasDraft() || d.Page() != page {
					t.Fatal("parked original write updated inactive grid")
				}
				s.selectProfileByName(saved.Name)
				s.activeTab = s.tabIndex(originalTab)
				tt.Frame()
			} else {
				if err := <-metadataResult; err != nil {
					t.Fatal("original native metadata change failed", err)
				}
				relay.once.Do(func() { close(relay.release) })
			}
			taskPanelWait(t, tt, func() bool { return !d.Busy() })
			if mode == "park" {
				if d.HasDraft() || d.Page().Rows[0][1].Value.Text != "44" || d.Page().Rows[1][1].Value.Text != "44" {
					t.Fatal("original activation lost acknowledged write/readback")
				}
			} else {
				if !d.HasDraft() || s.dataWorkspace.CanSave(d) || relay.writes.Load() != 1 || d.Page() != page {
					t.Fatal("revoked suffix dispatched or erased original prefix")
				}
				receipt := tdengineWriteReceipt(t, tt)
				if !strings.Contains(receipt, `"acknowledged"`) || !strings.Contains(receipt, `"not_sent"`) {
					t.Fatal("revoked suffix receipt lost original ordered states", receipt)
				}
				rows := tdenginePublicSQL(t, sql, db, "SELECT v FROM items ORDER BY ts")
				if rows[0][0].Text != "44" || rows[1][0].Text != "22" {
					t.Fatal("revoked suffix wrote to changed metadata", rows)
				}
			}
		})
	}
}
