package kvworkspace

import (
	"bytes"
	"io"
	"math"
	"strconv"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

func alarmWorkspaceChoose(t *testing.T, tt *ui.Tester, label, value string) {
	t.Helper()
	workspaceClick(t, tt, label)
	workspaceClick(t, tt, value)
}
func alarmWorkspaceSelect(t *testing.T, tt *ui.Tester, id uint64) {
	t.Helper()
	if err := tt.Click(strconv.FormatUint(id, 10)); err != nil {
		t.Fatal("original alarm row could not be selected", err)
	}
	tt.Frame()
	workspaceClick(t, tt, "选择此原告警")
}
func TestAlarmsBilingualPublicControlsImmutablePreviewAndCompleteArchives(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(language+"/"+strconv.Itoa(size[0]), func(t *testing.T) {
				s, d, l, _ := alarmTestState(t)
				var exported []byte
				s.SetFiles(Files{SavePath: func(string) (string, error) { return "alarms.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
					var b bytes.Buffer
					err := write(&b)
					exported = bytes.Clone(b.Bytes())
					return err
				}})
				tt := workspaceTester(s, d, size[0], size[1])
				tt.Frame()
				compactionWorkspaceBounds(t, tt, size, "读取告警列表", "告警类型过滤")
				workspaceClick(t, tt, "读取告警列表")
				consume(t, s, d)
				tt.Frame()
				alarmWorkspaceSelect(t, tt, math.MaxUint64)
				alarmWorkspaceSelect(t, tt, 3)
				compactionWorkspaceBounds(t, tt, size, "选择此原告警", "原集群告警列表")
				workspaceClick(t, tt, "预览解除告警")
				if d.confirmation == nil || l.writes != 0 || len(d.confirmation.alarms.receipt.Targets) != 2 {
					t.Fatal("public preview executed or lost exact selections", d.Error())
				}
				compactionWorkspaceBounds(t, tt, size, "不可变原告警批次", "我确认执行此不可变原告警批次", "确认执行 Disarm 批次", "取消")
				workspaceClick(t, tt, "复制完整告警批次预览")
				if !bytes.Contains([]byte(tt.Clipboard()), []byte("18446744073709551615")) || !bytes.Contains([]byte(tt.Clipboard()), []byte("original_observation")) {
					t.Fatal("public preview copy omitted original evidence")
				}
				workspaceClick(t, tt, "取消")
				if d.confirmation != nil || l.writes != 0 {
					t.Fatal("cancelled preview dispatched")
				}
				workspaceClick(t, tt, "预览解除告警")
				workspaceClick(t, tt, "我确认执行此不可变原告警批次")
				workspaceClick(t, tt, "确认执行 Disarm 批次")
				consume(t, s, d)
				tt.Frame()
				compactionWorkspaceBounds(t, tt, size, "原告警逐步结果", "告警证据视图")
				workspaceClick(t, tt, "复制完整告警记录")
				want, err := s.AlarmsRecord(d)
				if err != nil || string(want) != tt.Clipboard() {
					t.Fatal("UI copied incomplete archive", err)
				}
				workspaceClick(t, tt, "导出告警记录")
				consume(t, s, d)
				if !bytes.Equal(exported, want) || l.writes != 2 {
					t.Fatal("UI export changed complete original receipts")
				}
				alarmWorkspaceChoose(t, tt, "告警证据视图", "原 Disarm ACK")
				compactionWorkspaceBounds(t, tt, size, "告警原生观察")
				alarmWorkspaceChoose(t, tt, "告警证据视图", "派发前 GET")
				if alarmVisibleReceipt(d, 4) == nil || alarmVisibleReceipt(d, 4).Result.Request == alarmVisibleReceipt(d, 3).Result.Request {
					t.Fatal("before GET and original ACK merged")
				}
				if language == "en" && locales.Text("选择此原告警") == "选择此原告警" {
					t.Fatal("public Alarm controls not translated")
				}
			})
		}
	}
}
func TestAlarmsPublicUnknownTypeAndUnknownWriteStayIndependentAcrossMethods(t *testing.T) {
	s, d, l, _ := alarmTestState(t)
	l.mode = "unknown"
	tt := workspaceTester(s, d, 720, 520)
	workspaceClick(t, tt, "读取告警列表")
	consume(t, s, d)
	tt.Frame()
	if err := tt.Click("5"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(locales.Text("选择此原告警")); err == nil {
		tt.Frame()
	}
	_, targets, err := parseAlarmInputs(d.Request())
	if err != nil || len(targets) != 0 {
		t.Fatal("unknown native enum entered write selection")
	}
	alarmWorkspaceSelect(t, tt, math.MaxUint64)
	alarmWorkspaceSelect(t, tt, 3)
	workspaceClick(t, tt, "预览解除告警")
	workspaceClick(t, tt, "我确认执行此不可变原告警批次")
	workspaceClick(t, tt, "确认执行 Disarm 批次")
	consume(t, s, d)
	tt.Frame()
	workspaceClick(t, tt, "原未确认告警回执")
	if alarmVisibleReceipt(d, 3) != d.alarms.unknown || !d.hasUnknownWrites() {
		t.Fatal("unknown receipt view lost original barrier")
	}
	// Change using the actual method selector, then inspect the independent
	// original Alarm archive from the generic request document's receipt bar.
	alarmWorkspaceChoose(t, tt, "KV 方法", "Put")
	d.Editor.Buffer.Set(`{"key":"eA==","value":"eQ=="}`)
	tt.Frame()
	workspaceClick(t, tt, "运行 KV 请求")
	if d.confirmation != nil {
		t.Fatal("cross-method UI authorized an unknown document write")
	}
	workspaceClick(t, tt, "告警回执")
	workspaceClick(t, tt, "原未确认告警回执")
	if !d.selectedAlarms || alarmVisibleReceipt(d, 3) != d.alarms.unknown {
		t.Fatal("method switch lost the original Alarm receipt")
	}
	s.GuardLeave(d, func(ui.Services) { t.Fatal("cancelled leave continued") })
	tt.Frame()
	workspaceClick(t, tt, "取消")
	if s.leave != nil || !d.hasUnknownWrites() || d.alarms.batch == nil {
		t.Fatal("cancelled leave discarded original evidence")
	}
}
