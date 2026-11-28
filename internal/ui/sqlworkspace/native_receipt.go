package sqlworkspace

import (
	"io"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type sqlNativeReceipt struct{ name, json string }

func sqlOutcomeNotice(r contract.SQLResult) string {
	if r.NativeJSON == "" {
		return ""
	}
	for _, step := range r.Steps {
		if step.State == "uncertain" {
			return locales.Text("原始执行结果不确定；后续读取不能替代原始回执")
		}
	}
	if r.AffectedRows < 0 {
		return locales.Text("服务器未报告影响行数；请核对预期更改")
	}
	return ""
}
func (s *State) openNativeReceipt(name, raw string) {
	if raw != "" && !s.persistence.closed {
		s.nativeReceipt = &sqlNativeReceipt{name: name, json: raw}
	}
}
func (s *State) exportNativeReceipt() {
	if s.nativeReceipt == nil {
		return
	}
	original := *s.nativeReceipt
	files := s.persistence.files
	if files.SavePath == nil || files.Write == nil && files.WriteStream == nil {
		return
	}
	s.startIO(nil, nil, func() ioResult {
		path, e := files.SavePath(original.name + "-receipt.json")
		if e == nil && path != "" {
			if files.WriteStream != nil {
				e = files.WriteStream(path, func(w io.Writer) error { _, e := io.WriteString(w, original.json); return e })
			} else {
				e = files.Write(path, original.json)
			}
		}
		return ioResult{kind: "export", err: e}
	})
}
func (s *State) nativeReceiptModal(c *ui.Context) {
	if s.nativeReceipt == nil {
		return
	}
	snapshot := *s.nativeReceipt
	open := true
	theme.Modal(c, &open, func() {
		ui.Column(c).Width(760).MaxWidth(760).Gap(8).Children(func() {
			ui.Text(c, locales.Text("原始执行回执")).Bold()
			ui.Text(c, locales.Text("保留原始语句确认、错误和未执行项；后续读取不会更改此回执。")).TextColor(theme.Colors(c).Muted)
			raw := snapshot.json
			if len(raw) > 64<<10 {
				raw = raw[:64<<10]
				for !utf8.ValidString(raw) {
					raw = raw[:len(raw)-1]
				}
				ui.Text(c, locales.Text("仅显示回执前 64 KiB；复制和导出保留完整内容。")).TextColor(theme.Colors(c).Warning)
			}
			ui.TextArea(c, &raw).ReadOnly(true).Label(locales.Text("原始执行回执内容")).Height(360).FillWidth()
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("复制完整回执")).Clicked() {
					c.WriteClipboard(snapshot.json)
				}
				if ui.Button(c, locales.Text("导出完整回执")).Clicked() {
					s.exportNativeReceipt()
				}
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					open = false
				}
			})
		})
	})
	if !open {
		s.nativeReceipt = nil
	}
}
