package kvworkspace

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func snapshotInputStrings(r contract.KVRequest) (body, journal, frames string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(r.JSON), &fields) != nil {
		return
	}
	_ = json.Unmarshal(fields["body_bytes"], &body)
	_ = json.Unmarshal(fields["journal_bytes"], &journal)
	_ = json.Unmarshal(fields["frames"], &frames)
	return
}
func (s *State) snapshotView(c *ui.Context, d *Document) {
	x := d.snapshotState()
	colors := theme.Colors(c)
	ready := !d.Busy() && !d.FileBusy() && !d.NeedsRecovery() && d.SnapshotAvailable() && s.deps.SnapshotArtifacts != nil
	ui.Column(c).Fill().MinHeight(0).Padding(4, 8).Gap(4).Children(func() {
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, locales.Text("原集群 Snapshot")).Bold().Grow(1)
			label := locales.Text("限额 JSON")
			if x.showJSON {
				label = locales.Text("Snapshot 限额表单")
			}
			if ui.Button(c, label).Clicked() {
				x.showJSON = !x.showJSON
			}
		})
		if x.showJSON {
			s.snapshotEditor(c, d)
			return
		}
		body, journal, frames := snapshotInputStrings(d.Request())
		for _, field := range []struct {
			label string
			value *string
		}{{locales.Text("后端字节限额"), &body}, {locales.Text("原帧日志字节限额"), &journal}, {locales.Text("原帧数量限额"), &frames}} {
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, field.label).Width(180).SingleLine().Tooltip(field.label)
				if ui.TextInput(c, field.value).Label(field.label).Grow(1).MinWidth(80).Disabled(d.Busy()).Changed() {
					s.SetSnapshotInputs(d, body, journal, frames)
				}
			})
		}
		hint := locales.Text("捕获整个原集群后端及认证数据；连接前缀不限定范围")
		ui.Text(c, hint).SingleLine().Tooltip(hint).TextColor(colors.Warning)
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("预览 Snapshot 捕获")).Disabled(!ready || x.original != nil).Clicked() {
				s.PrepareSnapshot(d)
			}
			if ui.Button(c, locales.Text("清除原 Snapshot 归档")).Disabled(d.Busy() || d.FileBusy() || !d.snapshotContent()).Clicked() {
				s.ClearSnapshot(d)
			}
		}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		if x.original != nil {
			r := x.original
			label := fmt.Sprintf(locales.Text("原集群 %s · 文档 %s · 后端 %s 字节 · 原帧 %s"), strconv.FormatUint(r.Original.Scope.Original.Header.ClusterID, 10), strconv.FormatUint(d.id, 10), strconv.FormatUint(r.SnapshotBytes, 10), strconv.FormatUint(r.Original.Frames, 10))
			ui.Text(c, label).SingleLine().Tooltip(label).TextColor(colors.Muted)
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("保存原 backend 文件")).Disabled(d.Busy() || d.FileBusy() || x.artifact == nil || !r.Complete || s.deps.SnapshotFiles.SavePath == nil).Clicked() {
					s.SelectSnapshotFile(d, contract.KVSnapshotBackendFile)
				}
				if ui.Button(c, locales.Text("保存完整原帧日志")).Disabled(d.Busy() || d.FileBusy() || x.artifact == nil || !r.Stored || !r.FilesSynced || !r.Original.JournalComplete || s.deps.SnapshotFiles.SavePath == nil).Clicked() {
					s.SelectSnapshotFile(d, contract.KVSnapshotJournalFile)
				}
			}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		}
		c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { s.PrepareSnapshot(d) })
	})
}
func (s *State) snapshotEditor(c *ui.Context, d *Document) {
	d.Editor.Label = locales.Text("KV JSON 编辑器")
	d.Editor.ReadOnly = d.Busy()
	view := editor.View(c, d.Editor).Fill().MinHeight(0)
	if s.deps.EditFocus != nil {
		view.FocusBind(s.deps.EditFocus, fmt.Sprintf("kv/editor/%d", d.id))
	}
	handle := ui.Local(view, "kv-editor-handle", func() ui.Handle { return ui.Handle{} })
	view.Bind(handle)
	s.registerEditor(d, view.Focused(), handle)
	c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { s.PrepareSnapshot(d) })
}
func (s *State) snapshotConfirmationView(c *ui.Context, d *Document, p *snapshotConfirmation) {
	colors := theme.Colors(c)
	scope := p.scope
	ui.Column(c).Width(540).Gap(5).Children(func() {
		ui.Text(c, locales.Text("确认捕获原集群 Snapshot")).Bold()
		ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), scope.Original.Namespace.ProfileID, scope.Original.Generation)).SingleLine()
		snapshotFact(c, locales.Text("原集群与成员"), fmt.Sprintf("%d · %d", scope.Original.Header.ClusterID, scope.Original.Header.MemberID))
		snapshotFact(c, locales.Text("原文档 ID"), strconv.FormatUint(scope.DocumentID, 10))
		snapshotFact(c, locales.Text("后端字节限额"), strconv.FormatUint(p.request.BodyBytes, 10))
		snapshotFact(c, locales.Text("原帧日志字节限额"), strconv.FormatUint(p.request.JournalBytes, 10))
		snapshotFact(c, locales.Text("原帧数量限额"), strconv.FormatUint(p.request.Frames, 10))
		for _, label := range []string{locales.Text("捕获整个原集群后端及认证数据；连接前缀不限定范围"), locales.Text("原流校验、私有归档同步与用户文件发布分别记录"), locales.Text("仅捕获一次；恢复不会重新执行 Snapshot")} {
			ui.Text(c, label).SingleLine().Tooltip(label).TextColor(colors.Warning)
		}
		ui.Checkbox(c, &p.checked, locales.Text("我确认捕获此原集群的完整后端"))
		ui.Row(c).Gap(6).Children(func() {
			if ui.Button(c, locales.Text("取消")).Clicked() {
				s.CancelSnapshotConfirmation(d)
			}
			if ui.Button(c, locales.Text("确认捕获 Snapshot")).Disabled(!p.checked || d.Busy() || d.FileBusy()).Clicked() {
				s.ConfirmSnapshot(d)
			}
		})
	})
}
func (s *State) snapshotFileConfirmationView(c *ui.Context, d *Document, p *snapshotFileConfirmation) {
	observation := p.observation
	colors := theme.Colors(c)
	size, sum := observation.Source.SnapshotBytes, observation.Source.SnapshotSHA256
	if observation.Choice.Kind == contract.KVSnapshotJournalFile {
		size, sum = observation.Source.JournalBytes, observation.Source.JournalSHA256
	}
	ui.Column(c).Width(540).Gap(5).Children(func() {
		title, check, action := locales.Text("确认保存原 Snapshot 文件"), locales.Text("我确认保存此原文件"), locales.Text("确认保存 Snapshot 文件")
		if observation.Existing != nil {
			title = locales.Text("确认替换所检查的文件")
			check = locales.Text("我确认替换此已检查文件并保留旧备份")
			action = locales.Text("确认替换 Snapshot 文件")
		}
		ui.Text(c, title).Bold()
		ui.Text(c, observation.Choice.Path).SingleLine().Tooltip(observation.Choice.Path)
		ui.Text(c, fmt.Sprintf(locales.Text("原格式 %s · 完整字节 %s"), string(observation.Choice.Kind), strconv.FormatUint(size, 10))).SingleLine()
		digest := hex.EncodeToString(sum)
		ui.Text(c, "SHA256 "+digest).SingleLine().Tooltip(digest)
		if old := observation.Existing; old != nil {
			label := fmt.Sprintf(locales.Text("已检查旧文件 %s 字节 · SHA256 %s"), strconv.FormatUint(old.Size, 10), old.SHA256)
			ui.Text(c, label).SingleLine().Tooltip(label)
			for _, label := range []string{locales.Text("旧文件保留在独占目录，完成后显示保留位置"), locales.Text("两次排他移动之间，目标文件可短暂不存在"), locales.Text("目标变化或新文件出现时停止，恢复也不会覆盖目标")} {
				ui.Text(c, label).SingleLine().Tooltip(label).TextColor(colors.Warning)
			}
		}
		if !observation.Source.Complete {
			ui.Text(c, locales.Text("这是完整原失败日志；后端源仍未完成")).TextColor(colors.Warning).SingleLine()
		}
		ui.Text(c, locales.Text("保存已捕获的原文件，不重新请求 Snapshot")).TextColor(colors.Muted).SingleLine()
		ui.Checkbox(c, &p.checked, check)
		ui.Row(c).Gap(6).Children(func() {
			if ui.Button(c, locales.Text("取消")).Clicked() {
				s.CancelSnapshotConfirmation(d)
			}
			if ui.Button(c, action).Disabled(!p.checked || d.Busy() || d.FileBusy()).Clicked() {
				s.ConfirmSnapshotFile(d)
			}
		})
	})
}
