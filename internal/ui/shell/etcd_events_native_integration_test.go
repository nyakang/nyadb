package shell

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func TestEtcdNativeShellPublicEventsOriginalKeyMenuFixedWindowAndArchives(t *testing.T) {
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := connections.EtcdPrefixBytes(parsed.Profile.EtcdOptions)
	key := append(bytes.Clone(prefix), bytes.Repeat([]byte{255}, 4096-len(prefix))...)
	client := etcdPublicFixtureClient(t, parsed.Profile, parsed.Password)
	put := etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("original-grid-value")}).(*pb.PutResponse)
	s, tt, _, p, grid := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	page := grid.Page()
	if len(page.Rows) != 1 || !page.Rows[0][0].Truncated || page.CurrentCursor == nil {
		t.Fatal("fixture did not capture maximum complete key with truncated display")
	}
	// The menu operates on the value cell in the original row; the displayed
	// key is truncated and cannot be used to author the next request.
	if err := tt.RightClick("0x" + hex.EncodeToString([]byte("original-grid-value"))); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.Text("观察原键事件")); err != nil {
		t.Fatal(err)
	}
	etcdPublicWait(t, tt, func() bool { return s.currentTab().kv != nil && !s.currentTab().kv.Busy() })
	d := s.currentTab().kv
	w, err := kvworkspace.ParseWatchRequest(d.Request().JSON)
	if err != nil || !bytes.Equal(w.Key, key) || w.StartRevision <= put.Header.Revision || d.Request().Method != kvworkspace.WatchMethod {
		t.Fatal("original key action changed complete key or fixed page revision", err)
	}
	if _, ok := d.LastEventReceipt(); ok {
		t.Fatal("opening the key action executed Watch")
	}
	captureEtcdPublic(t, s, "events-watch-template")
	etcdPublicClick(t, tt, "启动 Watch")
	etcdPublicWait(t, tt, func() bool { r, ok := d.LastEventReceipt(); return ok && len(r.Batch.Frames) > 0 })
	value := bytes.Repeat([]byte{0, 255, 7}, 1000)
	later := etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: value}).(*pb.PutResponse)
	etcdPublicWait(t, tt, func() bool {
		r, ok := d.LastEventReceipt()
		if ok {
			for _, f := range r.Batch.Frames {
				if len(f.Events) > 0 {
					return true
				}
			}
		}
		return false
	})
	etcdPublicClick(t, tt, "请求进度")
	etcdPublicWait(t, tt, func() bool {
		r, _ := d.LastEventReceipt()
		for _, f := range r.Batch.Frames {
			if f.Progress {
				return true
			}
		}
		return false
	})
	captureEtcdPublic(t, s, "events-watch-active")
	etcdPublicClick(t, tt, "停止 Watch")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	r, ok := d.LastEventReceipt()
	if !ok || !r.Batch.Ended || r.Batch.Incomplete || r.Scope.Generation != d.Scope().Generation {
		t.Fatal("public stop did not retain original acknowledged receipt", d.Error())
	}
	etcdPublicClick(t, tt, "复制完整事件记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	originalRecord := []byte(tt.Clipboard())
	var record map[string]json.RawMessage
	if json.Unmarshal(originalRecord, &record) != nil || !bytes.Contains(originalRecord, []byte(base64.StdEncoding.EncodeToString(key))) || !bytes.Contains(originalRecord, []byte(base64.StdEncoding.EncodeToString(value))) {
		t.Fatal("public complete copy lost key/value/native frames")
	}
	var exported []byte
	s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "captured-events.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = bytes.Clone(b.Bytes())
		return err
	}})
	etcdPublicClick(t, tt, "导出事件记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	if !bytes.Equal(exported, originalRecord) {
		t.Fatal("public export differed from complete copy")
	}
	captureEtcdPublic(t, s, "events-watch-stopped")
	for _, tab := range s.tabs {
		if tab.data == grid {
			s.selectTab(tab.id)
		}
	}
	tt.Frame()
	if err := tt.RightClick("0x" + hex.EncodeToString([]byte("original-grid-value"))); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.Text("原键固定历史")); err != nil {
		t.Fatal(err)
	}
	etcdPublicWait(t, tt, func() bool { return s.currentTab().kv != nil && s.currentTab().kv != d && !s.currentTab().kv.Busy() })
	hd := s.currentTab().kv
	history, err := kvworkspace.ParseHistoryRequest(hd.Request().JSON)
	if err != nil || !bytes.Equal(history.Key, key) || history.EndRevision != w.StartRevision-1 || history.EndRevision >= later.Header.Revision {
		t.Fatal("history moved original read window", err)
	}
	captureEtcdPublic(t, s, "events-history-template")
	etcdPublicClick(t, tt, "读取历史")
	etcdPublicWait(t, tt, func() bool { return !hd.Busy() })
	hr, ok := hd.LastEventReceipt()
	if !ok || hr.History == nil || !hr.History.Complete || hr.History.CaptureIncomplete || hr.History.Window.EndRevision != history.EndRevision || len(hr.History.Events) != 1 || !bytes.Equal(hr.History.Events[0].Entry.Value, []byte("original-grid-value")) || hr.History.ProgressHeader.Revision < history.EndRevision {
		t.Fatal("public fixed window missing native progress proof", hd.Error())
	}
	captureEtcdPublic(t, s, "events-history-complete")
	// A replaced publication cannot open another tab from the earlier scope.
	old := contract.KVKeyRead{Scope: hr.Scope.Clone(), Key: bytes.Clone(key), Revision: history.EndRevision}
	if err := s.runtime.Connect(context.Background(), p, parsed.Password); err != nil {
		t.Fatal(err)
	}
	before := len(s.tabs)
	if s.OpenKVEvents(old, false) || len(s.tabs) != before {
		t.Fatal("stale key action entered replacement publication")
	}
}

func TestEtcdNativeShellPublicEventsParkedPublicationRecoveryNeverResubscribes(t *testing.T) {
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := connections.EtcdPrefixBytes(parsed.Profile.EtcdOptions)
	key := append(bytes.Clone(prefix), 1)
	client := etcdPublicFixtureClient(t, parsed.Profile, parsed.Password)
	etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("parked-original")})
	s, tt, _, p, grid := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	if err := tt.RightClick("0x" + hex.EncodeToString([]byte("parked-original"))); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.Text("观察原键事件")); err != nil {
		t.Fatal(err)
	}
	etcdPublicWait(t, tt, func() bool { return s.currentTab().kv != nil && !s.currentTab().kv.Busy() })
	eventTab := s.currentTab()
	d := eventTab.kv
	etcdPublicClick(t, tt, "启动 Watch")
	etcdPublicWait(t, tt, func() bool { r, ok := d.LastEventReceipt(); return ok && len(r.Batch.Frames) > 0 })
	original, _ := d.LastEventReceipt()
	authored := d.Request()
	for _, tab := range s.tabs {
		if tab.data == grid {
			s.selectTab(tab.id)
		}
	}
	etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("parked-complete")})
	time.Sleep(100 * time.Millisecond)
	tt.Frame()
	parked, _ := d.LastEventReceipt()
	if len(parked.Batch.Frames) != len(original.Batch.Frames) || !d.Busy() {
		t.Fatal("parked stream entered current grid")
	}
	if err := s.runtime.Connect(context.Background(), p, parsed.Password); err != nil {
		t.Fatal(err)
	}
	s.selectTab(eventTab.id)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	retained, _ := d.LastEventReceipt()
	count := 0
	for _, frame := range retained.Batch.Frames {
		for _, e := range frame.Events {
			if bytes.Equal(e.Entry.Value, []byte("parked-complete")) {
				count++
			}
		}
	}
	if count != 1 || !d.NeedsRecovery() || retained.Scope.Generation != original.Scope.Generation || d.Request() != authored {
		t.Fatal("original parked completion lost or reclassified", d.Error())
	}
	captureEtcdPublic(t, s, "events-original-recovery-required")
	etcdPublicClick(t, tt, "建立新会话（不执行现有请求）")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	recovered, _ := d.LastEventReceipt()
	if d.NeedsRecovery() || recovered.Scope.Generation != original.Scope.Generation || d.Scope().Generation <= original.Scope.Generation || d.Request() != authored || !recovered.Batch.Ended {
		t.Fatal("recovery replayed subscription or changed receipt", d.Error())
	}
	captureEtcdPublic(t, s, "events-recovered-retained")
}
