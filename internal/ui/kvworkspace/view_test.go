package kvworkspace

import (
	"context"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestKVUIWritePreviewCheckboxOriginalReceiptAndLeaveGuard(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	s, d, l := testState(t)
	s.SetMethod(d, "/etcdserverpb.KV/Put")
	d.Editor.Buffer.Set(`{"key":"AP8=","value":"AAE="}`)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.Invalidate = c.Services().Invalidate
		s.Consume()
		s.View(c, d)
		s.Modals(c)
	}, 720, 520)
	tt.Frame()
	if err := tt.Click(locales.Text("运行 KV 请求")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if l.executes.Load() != 0 || d.confirmation == nil {
		t.Fatal("run button dispatched a write")
	}
	if err := tt.Click(locales.Text("我确认执行此原生写入请求")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(locales.Text("确认执行 KV 写入")); err != nil {
		t.Fatal(err)
	}
	consume(t, s, d)
	tt.Frame()
	if l.executes.Load() != 1 || d.last == nil || !d.last.Result.Completed {
		t.Fatal("confirmed original response missing")
	}
	if err := tt.Click(locales.Text("复制完整 JSON")); err != nil {
		t.Fatal(err)
	}
	continued := false
	s.GuardLeave(d, func(ui.Services) { continued = true })
	tt.Frame()
	if err := tt.Click(locales.Text("放弃本地内容并继续")); err != nil {
		t.Fatal(err)
	}
	if !continued {
		t.Fatal("leave decision did not continue original action")
	}
}
func TestKVCancelDoesNotEraseAcknowledgedNativeCompletion(t *testing.T) {
	s, d, l := testState(t)
	l.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		return contract.KVResult{Request: p.Snapshot(), Attempted: true, Completed: true, Code: "OK"}, nil
	}
	s.Run(d)
	s.RequestCancel(d)
	consume(t, s, d)
	if d.last == nil || !d.last.Result.Completed || d.last.Result.WriteOutcomeUnknown {
		t.Fatal("late actual completion became cancellation/uncertainty")
	}
}
