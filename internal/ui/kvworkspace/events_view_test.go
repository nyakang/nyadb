package kvworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestKVEventsUIStartProgressStopAndOriginalCompleteCopy(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	s, d, l, _ := eventState(t)
	var run *fakeEventRun
	l.start = func(_ context.Context, r contract.KVWatchRequest) (contract.KVWatchRun, error) {
		run = &fakeEventRun{request: r.Clone()}
		run.push(contract.KVWatchBatch{Frames: []contract.KVEventFrame{{Created: true, WatchID: r.WatchID}}})
		return run, nil
	}
	tt := workspaceTester(s, d, 1280, 800)
	workspaceClick(t, tt, "启动 Watch")
	eventEventually(t, func() bool { tt.Frame(); return eventWatchCreated(d.eventReceipt) })
	workspaceClick(t, tt, "请求进度")
	eventEventually(t, func() bool { return run.progresses.Load() == 1 })
	value := bytes.Repeat([]byte{0, 255, 9}, 900)
	run.push(contract.KVWatchBatch{Frames: []contract.KVEventFrame{{Native: []byte{0, 255, 7}, WatchID: run.request.WatchID, Events: []contract.KVEvent{{Type: "PUT", Entry: contract.KVEntry{Key: []byte{0, 255}, Value: value, ModRevision: math.MaxInt64}}}}}})
	eventEventually(t, func() bool { tt.Frame(); return len(d.eventReceipt.Batch.Frames) == 2 })
	workspaceClick(t, tt, "停止 Watch")
	consume(t, s, d)
	if !d.eventReceipt.Batch.Ended || d.eventReceipt.Batch.Incomplete {
		t.Fatal("UI stopped before original acknowledgement")
	}
	if _, ok := tt.Find(locales.Text("原订阅已停止")); !ok {
		tt.Frame()
		if _, ok = tt.Find(locales.Text("原订阅已停止")); !ok {
			t.Fatal("missing stopped state")
		}
	}
	workspaceClick(t, tt, "复制完整事件记录")
	consume(t, s, d)
	var record map[string]json.RawMessage
	if json.Unmarshal([]byte(tt.Clipboard()), &record) != nil || !strings.Contains(tt.Clipboard(), base64.StdEncoding.EncodeToString(value)) || !strings.Contains(tt.Clipboard(), `"9223372036854775807"`) || !strings.Contains(tt.Clipboard(), `"native_protobuf": "AP8H"`) || strings.Contains(s.EventPreview(d), base64.StdEncoding.EncodeToString(value)) {
		t.Fatal("clipboard copied limited preview instead of original complete receipt")
	}
}

func TestKVEventsCopySnapshotParksAndCancellationDoesNotOverwriteClipboard(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		s, d, _, _ := eventState(t)
		d.eventReceipt = &EventReceipt{Scope: testScope(), Authored: d.Request(), Batch: contract.KVWatchBatch{Frames: []contract.KVEventFrame{{Native: []byte{0, 255}}}}}
		original, err := s.EventRecord(d)
		if err != nil {
			t.Fatal(err)
		}
		active := true
		s.deps.Active = func(*Document) bool { return active }
		tt := ui.NewTester(func(c *ui.Context) {
			if active {
				s.Consume()
			}
			if d.io == nil {
				c.WriteClipboard("original clipboard")
			}
			if d.io == nil {
				s.CopyEventRecord(c.Services(), d)
			}
		}, 1280, 800)
		tt.Frame()
		op := d.io
		active = false
		<-op.finished
		d.eventReceipt.Batch.Frames[0].Native[0] = 99
		s.Consume()
		if tt.Clipboard() != "original clipboard" || d.io != op {
			t.Fatal("parked copy changed clipboard")
		}
		if cancel {
			s.CancelFile(d)
		}
		active = true
		s.Consume()
		want := string(original)
		if cancel {
			want = "original clipboard"
		}
		if tt.Clipboard() != want {
			t.Fatal("copy substituted later receipt or ignored cancellation")
		}
	}
}

func TestKVEventsUIFixedWindowAndDeniedCreationStayExplicitInBothLocales(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, complete := range []bool{false, true} {
			s, d, _, _ := eventState(t)
			s.SetMethod(d, HistoryMethod)
			d.eventReceipt = &EventReceipt{Scope: testScope(), Authored: d.Request(), History: &contract.KVHistoryResult{Authored: contract.KVHistoryRequest{Key: []byte{0, 255}}, Window: contract.KVHistoryRequest{StartRevision: 2, EndRevision: 7}, Complete: complete, ProgressHeader: contract.KVHeader{Revision: 9}, ProgressRequests: 1}}
			d.selectedEvents = true
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				tt := workspaceTester(s, d, size[0], size[1])
				tt.Frame()
				label := "历史窗口尚未确认"
				if complete {
					label = "固定历史窗口已确认"
				}
				if _, ok := tt.Find(locales.Text(label)); !ok {
					t.Fatal("window completeness state missing", language, size, complete)
				}
				if _, ok := tt.Find(locales.Text("复制完整事件记录")); !ok {
					t.Fatal("complete copy control missing", language, size)
				}
			}
		}
	}
	r := &EventReceipt{Watch: &contract.KVWatchRequest{WatchID: 1}, Batch: contract.KVWatchBatch{Frames: []contract.KVEventFrame{{Created: true, Canceled: true, WatchID: -1}}}}
	if eventWatchCreated(r) {
		t.Fatal("denied creation reported as established subscription")
	}
}
