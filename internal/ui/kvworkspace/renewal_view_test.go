package kvworkspace

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestKVRenewalUIExplicitAuthorizationStartStopAndCompleteReceiptInBothLocales(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, l, _ := renewalState(t)
			var run *fakeRenewalRun
			l.start = func(_ context.Context, p contract.KVRenewalPreparation) (contract.KVRenewalRun, error) {
				run = renewalInitial(p)
				return run, nil
			}
			tt := workspaceTester(s, d, size[0], size[1])
			workspaceClick(t, tt, "开始持续续租")
			if d.renewalConfirmation == nil || l.starts.Load() != 0 {
				t.Fatal("public start bypassed authorization")
			}
			workspaceClick(t, tt, "确认开始持续续租")
			if d.Busy() {
				t.Fatal("unchecked run dispatched")
			}
			workspaceClick(t, tt, "我授权持续续租此原租约")
			workspaceClick(t, tt, "确认开始持续续租")
			eventEventually(t, func() bool { tt.Frame(); return renewalAcknowledged(d.renewalReceipt) == 1 })
			if _, ok := tt.Find(locales.Text("正在持续续租原租约")); !ok {
				t.Fatal("missing original active renewal state")
			}
			workspaceClick(t, tt, "停止持续续租")
			consume(t, s, d)
			tt.Frame()
			if !d.renewalReceipt.Batch.Ended || run.stops.Load() == 0 {
				t.Fatal("UI stop failed to await original run")
			}
			if _, ok := tt.Find(locales.Text("原持续续租已停止")); !ok {
				t.Fatal("missing original stopped state")
			}
			workspaceClick(t, tt, "复制完整续租记录")
			consume(t, s, d)
			if !strings.Contains(tt.Clipboard(), `"original_lease_id": "9007199254740993"`) || !strings.Contains(tt.Clipboard(), `"native_protobuf": "AP8="`) {
				t.Fatal("clipboard lost complete original renewal record")
			}
			workspaceClick(t, tt, "清除续租回执")
			consume(t, s, d)
			if d.renewalReceipt != nil || l.clears.Load() != 1 {
				t.Fatal("UI failed explicit settled disposal")
			}
		}
	}
}
func TestKVRenewalCopyAndExportCaptureOriginalReceiptBeforeParkingPickerAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		s, d, _, _ := renewalState(t)
		d.renewalReceipt = &RenewalReceipt{Scope: testScope(), Authored: d.Request(), Batch: contract.KVRenewalBatch{Request: contract.KVRenewalRequest{LeaseID: 9007199254740993}, Receipts: []contract.KVRenewalReceipt{{Native: []byte{0, 255}, WriteOutcomeUnknown: true}}}}
		want, err := s.RenewalRecord(d)
		if err != nil {
			t.Fatal(err)
		}
		active := true
		s.deps.Active = func(*Document) bool { return active }
		tt := ui.NewTester(func(c *ui.Context) { c.WriteClipboard("original clipboard"); s.CopyRenewalRecord(c.Services(), d) }, 1280, 800)
		tt.Frame()
		op := d.io
		active = false
		await(t, op.finished)
		d.renewalReceipt.Batch.Receipts[0].Native[0] = 7
		s.Consume()
		if tt.Clipboard() != "original clipboard" || d.io != op {
			t.Fatal("parked copy admitted")
		}
		if cancel {
			s.CancelFile(d)
		}
		active = true
		s.Consume()
		expected := string(want)
		if cancel {
			expected = "original clipboard"
		}
		if tt.Clipboard() != expected {
			t.Fatal("copy changed original snapshot or ignored cancellation")
		}
		entered, release := make(chan struct{}), make(chan struct{})
		var exported []byte
		s.SetFiles(Files{SavePath: func(string) (string, error) { close(entered); <-release; return "original.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
			var b bytes.Buffer
			err := write(&b)
			exported = bytes.Clone(b.Bytes())
			return err
		}})
		want, _ = s.RenewalRecord(d)
		s.ExportRenewalRecord(d)
		await(t, entered)
		d.renewalReceipt.Batch.Receipts[0].Native[0] = 9
		if cancel {
			s.CancelFile(d)
		}
		close(release)
		consume(t, s, d)
		if cancel {
			if len(exported) != 0 {
				t.Fatal("canceled picker export published")
			}
		} else if !bytes.Equal(exported, want) {
			t.Fatal("export substituted later receipt")
		}
	}
}
func TestKVRenewalUnknownUIOffersSeparateOriginalReceiptAndExplicitLeave(t *testing.T) {
	s, d, _, _ := renewalState(t)
	d.renewalReceipt = &RenewalReceipt{Scope: testScope(), Authored: d.Request(), Batch: contract.KVRenewalBatch{Request: contract.KVRenewalRequest{LeaseID: 9007199254740993}, Ended: true, Terminal: contract.KVRenewalTerminal{WriteOutcomeUnknown: true}}}
	d.renewalUnknown = d.renewalReceipt
	d.selectedRenewal = true
	tt := workspaceTester(s, d, 720, 520)
	tt.Frame()
	if _, ok := tt.Find(locales.Text("原未确认续租回执")); !ok {
		t.Fatal("unknown renewal not independently selectable")
	}
	if _, ok := tt.Find(locales.Text("原续租未确认；此文档禁止继续写入")); !ok {
		t.Fatal("unknown renewal omitted")
	}
	workspaceClick(t, tt, "开始持续续租")
	if d.renewalConfirmation != nil {
		t.Fatal("UI authorized unknown replay")
	}
	if !s.GuardLeave(d, func(ui.Services) {}) {
		t.Fatal("unknown renewal omitted from leave guard")
	}
	tt.Frame()
	workspaceClick(t, tt, "取消")
	if s.leave != nil || d.renewalUnknown == nil {
		t.Fatal("cancel leave discarded unknown")
	}
}
