package dataworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestDataPrefixSetBilingualControlsExplicitClicksAndCompleteEvidence(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(fmt.Sprintf("%s/%dx%d", language, size[0], size[1]), func(t *testing.T) {
				s, d, _, f := prefixSetUIWorkspace(t, "fingerprints")
				tt := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c)
					ui.Column(c).FillWidth().Children(func() { s.mysqlDataToolbar(c, d) })
					s.Modals(c)
				}, size[0], size[1])
				click := func(label string) {
					t.Helper()
					tt.Frame()
					box, ok := tt.Find(locales.Text(label))
					if !ok || box.W <= 0 || box.H <= 0 || box.X < 0 || box.Y < 0 || box.X+box.W > float32(size[0]) || box.Y+box.H > float32(size[1]) {
						t.Fatal("batch control clipped", label, box)
					}
					if err := tt.Click(locales.Text(label)); err != nil {
						t.Fatal(err)
					}
				}
				tt.Frame()
				s.DeleteKeyPrefixSet(d)
				click("输入完整分批删除前缀")
				tt.Command("selectAll")
				tt.Type("scope/")
				click("捕获完整候选键")
				waitPrefixSetUI(t, s, d)
				observation := d.prefixSetObservation.JSON
				click("复制完整候选键列表")
				var keys []string
				if json.Unmarshal([]byte(tt.Clipboard()), &keys) != nil || len(keys) != 43 {
					t.Fatal("complete JSON key list lost")
				}
				click("原始观察")
				click("复制完整内容")
				if tt.Clipboard() != observation {
					t.Fatal("capture clipboard truncated")
				}
				click("关闭")
				click(fmt.Sprintf(locales.Text("预览第 %d 批删除"), 1))
				waitPrefixSetUI(t, s, d)
				click("复制完整预览")
				if tt.Clipboard() != d.prefixSet.preview.NativeJSON || f.submits.Load() != 0 {
					t.Fatal("preview lost original or submitted")
				}
				click("取消本批确认")
				if d.prefixSet.plan != nil || !d.prefixSet.reviewOpen {
					t.Fatal("cancel lost capture or retained plan")
				}
				click(fmt.Sprintf(locales.Text("预览第 %d 批删除"), 1))
				waitPrefixSetUI(t, s, d)
				click("确认并删除本批候选键")
				waitPrefixSetUI(t, s, d)
				if f.submits.Load() != 1 || !d.prefixSet.reviewOpen {
					t.Fatal("explicit batch advanced automatically")
				}
				record := d.prefixSetProgress.Batches[0]
				click("进度与回执")
				click("复制完整内容")
				if tt.Clipboard() != d.prefixSetLedger.JSON {
					t.Fatal("ledger clipboard changed")
				}
				click(fmt.Sprintf(locales.Text("第 %d 批原事务回执"), 1))
				click("复制完整回执")
				if tt.Clipboard() != record.NativeJSON {
					t.Fatal("individual original receipt lost")
				}
				click("关闭")
				click("关闭")
				click("停止并保留原观察与回执")
				if d.prefixSet != nil || d.HasDraft() || d.prefixSetObservation.JSON != observation || !reflect.DeepEqual(record, d.prefixSetProgress.Batches[0]) {
					t.Fatal("stop lost independent evidence")
				}
			})
		}
	}
}

func TestDataPrefixSetToolbarKeepsOriginalReadCancelVisibleBeforeOverflow(t *testing.T) {
	for _, width := range []int{1280, 720, 360} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			s, d, active, f := prefixSetUIWorkspace(t, "fingerprints")
			d.boundRuntime.PageFailureRequiresReopen = true
			d.loading = true
			canceled := false
			d.cancel = func() { canceled = true }
			ack := make(chan mysqlDataResult, 1)
			d.results = ack
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c)
				ui.Column(c).FillWidth().Children(func() { s.mysqlDataToolbar(c, d) })
			}, width, 520)
			tt.Frame()
			box, ok := tt.Find(locales.Text("取消读取数据"))
			if !ok || box.X < 0 || box.X+box.W > float32(width) {
				t.Fatal("original cancellation moved into native overflow", box)
			}
			if err := tt.Click(locales.Text("取消读取数据")); err != nil {
				t.Fatal(err)
			}
			s.Consume(ui.Services{})
			if !canceled || !d.loading || !d.readStopping || f.captures.Load() != 0 {
				t.Fatal("cancel lost original read ACK or started capture")
			}
			ack <- mysqlDataResult{generation: active.Generation, profileID: d.profileID, request: d.request, err: context.Canceled}
			s.Consume(ui.Services{})
			if d.loading || !d.readRetired || d.err == "" {
				t.Fatal("original read acknowledgement failed to retire scope")
			}
		})
	}
}
