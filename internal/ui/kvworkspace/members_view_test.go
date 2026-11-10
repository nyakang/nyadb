package kvworkspace

import (
	"bytes"
	"io"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/locales"
)

func TestMembersBilingualControlsImmutableConfirmationAndFullArchives(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, l, _ := membersTestState(t)
			var exported []byte
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "member.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
				var b bytes.Buffer
				err := write(&b)
				exported = bytes.Clone(b.Bytes())
				return err
			}})
			tt := workspaceTester(s, d, size[0], size[1])
			workspaceClick(t, tt, "读取成员目录")
			consume(t, s, d)
			tt.Frame()
			if err := tt.Click("1"); err != nil {
				t.Fatal("member table cannot select original ID", err)
			}
			tt.Frame()
			if d.members.row != 1 {
				t.Fatal("member ID row was not selected")
			}
			d.members.version = "3.7.3"
			tt.Frame()
			compactionWorkspaceBounds(t, tt, size, "明确成员 endpoint", "此成员预期版本", "选择此原成员", "读取成员目录", "检查所选成员")
			workspaceClick(t, tt, "选择此原成员")
			workspaceClick(t, tt, "检查所选成员")
			consume(t, s, d)
			tt.Frame()
			workspaceClick(t, tt, "预览 Defragment")
			if d.confirmation == nil || len(l.written) != 0 {
				t.Fatal("UI preview failed or executed", d.Error())
			}
			compactionWorkspaceBounds(t, tt, size, "不可变原成员批次", "我确认执行此不可变原成员批次", "确认执行 Defragment 批次")
			workspaceClick(t, tt, "复制完整成员批次预览")
			if !bytes.Contains([]byte(tt.Clipboard()), []byte(`"original_member_scope"`)) {
				t.Fatal("UI preview copied incomplete scopes")
			}
			workspaceClick(t, tt, "我确认执行此不可变原成员批次")
			workspaceClick(t, tt, "确认执行 Defragment 批次")
			consume(t, s, d)
			tt.Frame()
			compactionWorkspaceBounds(t, tt, size, "原成员逐步结果", "成员证据视图")
			workspaceClick(t, tt, "复制完整成员记录")
			want, err := s.MembersRecord(d)
			if err != nil || tt.Clipboard() != string(want) {
				t.Fatal("UI copied incomplete member archive", err)
			}
			workspaceClick(t, tt, "导出成员记录")
			consume(t, s, d)
			if !bytes.Equal(want, exported) {
				t.Fatal("member export lost original evidence")
			}
			if language == "en" && locales.Text("读取成员目录") == "读取成员目录" {
				t.Fatal("member controls are not translated")
			}
		}
	}
}
