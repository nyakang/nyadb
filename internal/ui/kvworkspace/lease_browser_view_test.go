package kvworkspace

import (
	"bytes"
	"encoding/base64"
	"io"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
)

func TestKVLeaseBrowserBilingualListSelectionCompleteKeysAndArchives(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, l := browserTestState(t)
			d.Editor.Buffer.Set(`{"limit":2}`)
			tt := workspaceTester(s, d, size[0], size[1])
			workspaceClick(t, tt, "读取租约观察")
			consume(t, s, d)
			tt.Frame()
			workspaceClick(t, tt, "9007199254740993")
			tt.Frame()
			if d.browserSelectedID != 9007199254740993 {
				t.Fatal("list click rounded original ID")
			}
			workspaceClick(t, tt, "读取所选租约详情")
			consume(t, s, d)
			tt.Frame()
			if l.details.Load() != 1 || d.browserDetail == nil {
				t.Fatal("explicit selection did not read original detail", d.Error())
			}
			d.browserKeyRow = 1
			tt.Frame()
			workspaceClick(t, tt, "复制完整键 Base64")
			if tt.Clipboard() != base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{255}, 4096)) {
				t.Fatal("copy truncated original key")
			}
			workspaceClick(t, tt, "复制完整租约记录")
			consume(t, s, d)
			want, _ := s.LeaseBrowserRecord(d)
			if tt.Clipboard() != string(want) {
				t.Fatal("clipboard differs from full record")
			}
			workspaceClick(t, tt, "租约列表")
			workspaceClick(t, tt, "下一个 ID 窗口")
			consume(t, s, d)
			tt.Frame()
			if len(d.browserPage.Page.Rows) != 1 || d.browserSelectedID != 0 {
				t.Fatal("window did not reset selection")
			}
			workspaceClick(t, tt, "本次浏览回执")
			workspaceClick(t, tt, "清除浏览回执")
			if d.browserLast != nil {
				t.Fatal("explicit receipt clearing failed")
			}
		}
	}
}
func TestKVLeaseBrowserFileSnapshotsParkingCancellationAndClose(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		s, d, _ := browserTestState(t)
		s.Run(d)
		consume(t, s, d)
		want, _ := s.LeaseBrowserRecord(d)
		active := true
		s.deps.Active = func(*Document) bool { return active }
		tt := ui.NewTester(func(c *ui.Context) { c.WriteClipboard("original"); s.CopyLeaseBrowserRecord(c.Services(), d) }, 1280, 800)
		tt.Frame()
		op := d.io
		active = false
		await(t, op.finished)
		d.browserPage.Page.List.Native[0] = 8
		s.Consume()
		if d.io != op || tt.Clipboard() != "original" {
			t.Fatal("parked clipboard changed")
		}
		if cancel {
			s.CancelFile(d)
		}
		active = true
		s.Consume()
		if !cancel && tt.Clipboard() != string(want) || cancel && tt.Clipboard() != "original" {
			t.Fatal("copy substituted mutable receipt")
		}
		entered, release := make(chan struct{}), make(chan struct{})
		var exported []byte
		s.SetFiles(Files{SavePath: func(string) (string, error) { close(entered); <-release; return "original.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
			var b bytes.Buffer
			err := write(&b)
			exported = bytes.Clone(b.Bytes())
			return err
		}})
		want, _ = s.LeaseBrowserRecord(d)
		s.ExportLeaseBrowserRecord(d)
		await(t, entered)
		d.browserPage.Page.List.Native[0] = 9
		if cancel {
			s.CancelFile(d)
		}
		close(release)
		consume(t, s, d)
		if cancel && len(exported) != 0 || !cancel && !bytes.Equal(want, exported) {
			t.Fatal("export changed snapshot or ignored cancellation")
		}
	}
}
