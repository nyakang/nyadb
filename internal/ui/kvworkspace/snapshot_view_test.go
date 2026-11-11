package kvworkspace

import (
	"bytes"
	"io"
	"strconv"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestSnapshotBilingualPublicConfirmationsIndependentReceiptsAndCompleteExport(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(language+"/"+strconv.Itoa(size[0]), func(t *testing.T) {
				s, d, l, _, files := snapshotTestState(t)
				var exported []byte
				s.SetFiles(Files{SavePath: func(string) (string, error) { return "original-record.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
					var b bytes.Buffer
					err := write(&b)
					exported = bytes.Clone(b.Bytes())
					return err
				}})
				tt := workspaceTester(s, d, size[0], size[1])
				tt.Frame()
				compactionWorkspaceBounds(t, tt, size, "后端字节限额", "原帧日志字节限额", "原帧数量限额", "限额 JSON")
				workspaceClick(t, tt, "限额 JSON")
				compactionWorkspaceBounds(t, tt, size, "KV JSON 编辑器", "Snapshot 限额表单")
				workspaceClick(t, tt, "Snapshot 限额表单")
				workspaceClick(t, tt, "预览 Snapshot 捕获")
				if d.snapshot.confirmation == nil || d.snapshot.confirmation.checked || l.calls.Load() != 0 {
					t.Fatal("UI capture preview dispatched or was checked")
				}
				compactionWorkspaceBounds(t, tt, size, "我确认捕获此原集群的完整后端", "确认捕获 Snapshot", "取消")
				workspaceClick(t, tt, "取消")
				workspaceClick(t, tt, "预览 Snapshot 捕获")
				workspaceClick(t, tt, "我确认捕获此原集群的完整后端")
				workspaceClick(t, tt, "确认捕获 Snapshot")
				consume(t, s, d)
				tt.Frame()
				compactionWorkspaceBounds(t, tt, size, "Snapshot 证据视图", "保存原 backend 文件", "保存完整原帧日志", "编辑 Snapshot 限额")
				unknown := &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true, JSON: `{"original_write":"unknown"}`}}
				d.unknown = unknown
				workspaceClick(t, tt, "原未确认回执")
				if d.selectedSnapshot || s.selectedReceipt(d) != unknown || !d.hasUnknownWrites() {
					t.Fatal("Snapshot UI concealed or settled the earlier write")
				}
				workspaceClick(t, tt, "Snapshot 回执")
				for _, existing := range []bool{false, true} {
					files.existing = existing
					workspaceClick(t, tt, "保存原 backend 文件")
					consume(t, s, d)
					tt.Frame()
					check, action := "我确认保存此原文件", "确认保存 Snapshot 文件"
					if existing {
						check, action = "我确认替换此已检查文件并保留旧备份", "确认替换 Snapshot 文件"
					}
					compactionWorkspaceBounds(t, tt, size, check, action, "取消")
					if d.snapshot.fileConfirmation == nil || d.snapshot.fileConfirmation.checked {
						t.Fatal("UI file confirmation was implicitly checked")
					}
					workspaceClick(t, tt, check)
					workspaceClick(t, tt, action)
					consume(t, s, d)
					tt.Frame()
				}
				alarmWorkspaceChoose(t, tt, "Snapshot 证据视图", "用户文件发布")
				compactionWorkspaceBounds(t, tt, size, "原文件发布记录")
				workspaceClick(t, tt, "复制完整 Snapshot 记录")
				want, err := s.SnapshotRecord(d)
				if err != nil || string(want) != tt.Clipboard() || !bytes.Contains(want, []byte("9007199254740995")) {
					t.Fatal("UI copy lost complete original evidence", err)
				}
				workspaceClick(t, tt, "导出 Snapshot 记录")
				consume(t, s, d)
				if !bytes.Equal(exported, want) || l.calls.Load() != 1 || files.publishes.Load() != 2 || d.unknown != unknown {
					t.Fatal("UI export replayed or changed original evidence")
				}
				if language == "en" && locales.Text("我确认捕获此原集群的完整后端") == "我确认捕获此原集群的完整后端" {
					t.Fatal("Snapshot controls untranslated")
				}
			})
		}
	}
}
