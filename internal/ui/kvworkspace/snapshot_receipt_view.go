package kvworkspace

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Only known application errors are translated for display. Original native
// receipts, file paths and complete exports keep their original bytes/text.
func snapshotDisplayError(value string) string {
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		switch line {
		case "归档文件或目标文件已变化，请重新选择并预览":
			lines[i] = locales.Text("归档文件或目标文件已变化，请重新选择并预览")
		case "归档文件超出大小上限或不是普通文件":
			lines[i] = locales.Text("归档文件超出大小上限或不是普通文件")
		case "原快照归档已关闭":
			lines[i] = locales.Text("原快照归档已关闭")
		case "原快照归档磁盘预算不足":
			lines[i] = locales.Text("原快照归档磁盘预算不足")
		case "原快照文件与完整执行回执不一致":
			lines[i] = locales.Text("原快照文件与完整执行回执不一致")
		}
	}
	return strings.Join(lines, "\n")
}
func snapshotDisplayDocumentError(value string) string {
	for _, language := range []string{"zh-Hans", "en"} {
		prefix := locales.TextIn(language, "KV 文件操作失败") + ": "
		if strings.HasPrefix(value, prefix) {
			return locales.Text("KV 文件操作失败") + ": " + snapshotDisplayError(strings.TrimPrefix(value, prefix))
		}
	}
	return snapshotDisplayError(value)
}

func snapshotFact(c *ui.Context, label, value string) {
	ui.Row(c).FillWidth().MinWidth(0).Gap(8).Children(func() {
		ui.Text(c, label).Width(180).SingleLine().Tooltip(label).TextColor(theme.Colors(c).Muted)
		ui.Text(c, clipped(value)).Grow(1).MinWidth(0).SingleLine().Tooltip(clipped(value))
	})
}
func snapshotBool(v bool) string {
	if v {
		return locales.Text("是")
	}
	return locales.Text("否")
}
func snapshotAck(attempted, acknowledged, unknown bool) string {
	if unknown {
		return locales.Text("未确认")
	}
	if acknowledged {
		return locales.Text("已确认")
	}
	if attempted {
		return locales.Text("未成功")
	}
	return locales.Text("未执行")
}
func snapshotFileLabel(r contract.KVSnapshotFileResult) string {
	return string(r.Original.Choice.Kind) + " · " + snapshotAck(r.PublicationAttempted, r.Published, r.PublicationUnknown) + " · " + r.Original.Choice.Path
}
func (s *State) snapshotReceiptView(c *ui.Context, d *Document) {
	x := d.snapshotState()
	ui.Column(c).Fill().MinHeight(0).Gap(4).Children(func() {
		if x.cleanupError != "" {
			ui.Text(c, clipped(x.cleanupError)).SingleLine().Tooltip(clipped(x.cleanupError)).TextColor(theme.Colors(c).Warning)
		}
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("复制完整 Snapshot 记录")).Disabled(!d.snapshotContent()).Clicked() {
				raw, err := s.SnapshotRecord(d)
				if err != nil {
					d.error = err.Error()
				} else {
					c.WriteClipboard(string(raw))
				}
			}
			if ui.Button(c, locales.Text("导出 Snapshot 记录")).Disabled(!d.snapshotContent() || d.FileBusy() || s.deps.Files.SavePath == nil).Clicked() {
				s.ExportSnapshotRecord(d)
			}
			if d.method == SnapshotMethod && x.showResults && ui.Button(c, locales.Text("编辑 Snapshot 限额")).Disabled(d.Busy() || d.FileBusy()).Clicked() {
				x.showResults = false
				s.SnapshotSplitPercent = 70
			}
		}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("保存原 backend 文件")).Disabled(d.Busy() || d.FileBusy() || x.artifact == nil || x.original == nil || !x.original.Complete || s.deps.SnapshotFiles.SavePath == nil).Clicked() {
				s.SelectSnapshotFile(d, contract.KVSnapshotBackendFile)
			}
			if ui.Button(c, locales.Text("保存完整原帧日志")).Disabled(d.Busy() || d.FileBusy() || x.artifact == nil || x.original == nil || !x.original.Stored || !x.original.FilesSynced || !x.original.Original.JournalComplete || s.deps.SnapshotFiles.SavePath == nil).Clicked() {
				s.SelectSnapshotFile(d, contract.KVSnapshotJournalFile)
			}
			if ui.Button(c, locales.Text("清除原 Snapshot 归档")).Disabled(d.Busy() || d.FileBusy() || !d.snapshotContent()).Clicked() {
				s.ClearSnapshot(d)
			}
		}).FillWidth().MinWidth(0).Padding(0).Gap(5)
		options := []string{locales.Text("原 Snapshot 源"), locales.Text("私有归档校验"), locales.Text("用户文件发布"), locales.Text("完整 Snapshot 记录")}
		if x.recordView < 0 || x.recordView >= len(options) {
			x.recordView = 0
		}
		choice := options[x.recordView]
		if ui.Select(c, &choice, options).Label(locales.Text("Snapshot 证据视图")).FillWidth().Changed() {
			for i, option := range options {
				if choice == option {
					x.recordView = i
				}
			}
		}
		if x.recordView == 3 {
			if x.recordRevision != x.revision || x.recordJSON == "" {
				raw, err := s.SnapshotRecord(d)
				x.recordJSON, x.recordError = clipped(string(raw)), ""
				if err != nil {
					x.recordError = err.Error()
				}
				x.recordRevision = x.revision
			}
			if x.recordError != "" {
				ui.Text(c, clipped(x.recordError)).TextColor(theme.Colors(c).Warning)
				return
			}
			text := x.recordJSON
			area := ui.TextArea(c, &text).Label(locales.Text("完整 Snapshot 记录预览")).ReadOnly(true).Fill().MinHeight(0)
			theme.BindNativeEdit(c, area, fmt.Sprintf("kv/snapshot/record/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
			return
		}
		if x.recordView == 2 {
			if len(x.files) == 0 {
				ui.Text(c, locales.Text("尚无用户文件发布记录")).TextColor(theme.Colors(c).Muted)
				return
			}
			if x.fileRow < 0 || x.fileRow >= len(x.files) {
				x.fileRow = len(x.files) - 1
			}
			labels := make([]string, len(x.files))
			for i, r := range x.files {
				labels[i] = strconv.Itoa(i+1) + " · " + snapshotFileLabel(r)
			}
			selected := labels[x.fileRow]
			if ui.Select(c, &selected, labels).Label(locales.Text("原文件发布记录")).FillWidth().Changed() {
				for i, label := range labels {
					if selected == label {
						x.fileRow = i
					}
				}
			}
			cancelRequested := x.fileRow < len(x.fileCancelRequested) && x.fileCancelRequested[x.fileRow]
			ui.Scroll(c).Fill().MinHeight(0).Children(func() { snapshotFileFacts(c, x.files[x.fileRow], cancelRequested) })
			return
		}
		ui.Scroll(c).Fill().MinHeight(0).Children(func() {
			if x.original != nil {
				if x.recordView == 1 {
					snapshotArtifactFacts(c, *x.original, x.artifact != nil, x.cleanupError)
				} else {
					snapshotSourceFacts(c, x.original.Original, x.captureCanceled)
				}
				return
			}
			if x.recordView == 0 && len(x.recovered) != 0 {
				ui.Text(c, locales.Text("恢复保留原源证据；未重新捕获或发布文件")).TextColor(theme.Colors(c).Muted)
				for _, source := range x.recovered {
					snapshotSourceFacts(c, source, false)
				}
				return
			}
			ui.Text(c, locales.Text("确认捕获后显示原 Snapshot 证据")).TextColor(theme.Colors(c).Muted)
		})
	})
}
func snapshotSourceFacts(c *ui.Context, r contract.KVSnapshotResult, cancelRequested bool) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(4).Children(func() {
		ui.Text(c, locales.Text("原 Snapshot 源")).Bold()
		snapshotFact(c, locales.Text("原连接与会话代次"), fmt.Sprintf("#%d · %d", r.Scope.Original.Namespace.ProfileID, r.Scope.Original.Generation))
		snapshotFact(c, locales.Text("原集群与成员"), fmt.Sprintf("%d · %d", r.Scope.Original.Header.ClusterID, r.Scope.Original.Header.MemberID))
		snapshotFact(c, locales.Text("原文档 ID"), strconv.FormatUint(r.Scope.DocumentID, 10))
		snapshotFact(c, locales.Text("原流完成"), snapshotBool(r.Completed))
		snapshotFact(c, locales.Text("原生终止已观察"), snapshotBool(r.NativeTerminalObserved))
		snapshotFact(c, "EOF", snapshotBool(r.EOF))
		snapshotFact(c, locales.Text("源字节完整"), snapshotBool(r.SourceComplete))
		snapshotFact(c, locales.Text("源校验和匹配"), snapshotBool(r.ChecksumMatched))
		snapshotFact(c, locales.Text("原帧日志完整"), snapshotBool(r.JournalComplete))
		snapshotFact(c, locales.Text("输出写入已确认"), snapshotBool(r.OutputAcknowledged))
		snapshotFact(c, locales.Text("后端字节与帧数"), fmt.Sprintf("%d / %d · %d", r.BodyBytes, r.ExpectedBodyBytes, r.Frames))
		snapshotFact(c, locales.Text("原存储版本"), r.StorageVersion)
		snapshotFact(c, "Body SHA256", hex.EncodeToString(r.BodySHA256))
		snapshotFact(c, locales.Text("原请求取消"), snapshotBool(cancelRequested))
		snapshotFact(c, locales.Text("原执行器确认取消"), snapshotBool(r.Canceled))
		snapshotFact(c, "Code", r.NativeCode)
		snapshotFact(c, locales.Text("原源错误"), snapshotDisplayError(r.LocalError))
		snapshotFact(c, locales.Text("原回执捕获错误"), snapshotDisplayError(r.CaptureError))
	})
}
func snapshotArtifactFacts(c *ui.Context, r contract.KVSnapshotCapture, available bool, closeError string) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(4).Children(func() {
		ui.Text(c, locales.Text("私有归档校验")).Bold()
		snapshotFact(c, locales.Text("原归档已存储"), snapshotBool(r.Stored))
		snapshotFact(c, locales.Text("原归档文件已同步"), snapshotBool(r.FilesSynced))
		snapshotFact(c, locales.Text("完整后端归档"), snapshotBool(r.Complete))
		snapshotFact(c, locales.Text("本地归档仍可保存"), snapshotBool(available))
		snapshotFact(c, locales.Text("后端完整字节数"), strconv.FormatUint(r.SnapshotBytes, 10))
		snapshotFact(c, "Backend SHA256", hex.EncodeToString(r.SnapshotSHA256))
		snapshotFact(c, locales.Text("原日志完整字节数"), strconv.FormatUint(r.JournalBytes, 10))
		snapshotFact(c, "Journal SHA256", hex.EncodeToString(r.JournalSHA256))
		snapshotFact(c, locales.Text("原归档存储错误"), snapshotDisplayError(r.StorageError))
		snapshotFact(c, locales.Text("原归档清理错误"), snapshotDisplayError(r.CleanupError))
		snapshotFact(c, locales.Text("归档关闭清理错误"), snapshotDisplayError(closeError))
		if r.Stored && r.FilesSynced && r.Original.JournalComplete && !r.Complete {
			ui.Text(c, locales.Text("这是完整原失败日志；后端源仍未完成")).TextColor(theme.Colors(c).Warning)
		}
	})
}
func snapshotFileFacts(c *ui.Context, r contract.KVSnapshotFileResult, cancelRequested bool) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(4).Children(func() {
		ui.Text(c, locales.Text("用户文件发布")).Bold()
		snapshotFact(c, locales.Text("原目标文件"), r.Original.Choice.Path)
		snapshotFact(c, locales.Text("原格式"), string(r.Original.Choice.Kind))
		snapshotFact(c, locales.Text("原文档 ID"), strconv.FormatUint(r.Original.Source.Original.Scope.DocumentID, 10))
		snapshotFact(c, locales.Text("发布移动确认"), snapshotAck(r.PublicationAttempted, r.Published, r.PublicationUnknown))
		snapshotFact(c, locales.Text("后来目标内容匹配"), snapshotBool(r.DestinationMatched))
		snapshotFact(c, locales.Text("原目标路径匹配"), snapshotBool(r.PathMatched))
		snapshotFact(c, locales.Text("源归档独立校验"), snapshotBool(r.SourceVerified))
		snapshotFact(c, locales.Text("用户文件已同步"), snapshotBool(r.FileSynced))
		snapshotFact(c, locales.Text("目录已同步"), snapshotBool(r.DirectorySynced))
		snapshotFact(c, locales.Text("写入完整字节数"), strconv.FormatUint(r.BytesWritten, 10))
		snapshotFact(c, "SHA256", hex.EncodeToString(r.SHA256))
		snapshotFact(c, locales.Text("旧文件移出确认"), snapshotAck(r.DisplacementAttempted, r.Displaced, r.DisplacementUnknown))
		snapshotFact(c, locales.Text("旧文件恢复确认"), snapshotAck(r.RestorationAttempted, r.Restored, r.RestorationUnknown))
		snapshotFact(c, locales.Text("旧备份仍保留"), snapshotBool(r.DisplacedRetained))
		snapshotFact(c, locales.Text("临时文件仍保留"), snapshotBool(r.TemporaryRetained))
		snapshotFact(c, locales.Text("原辅助路径匹配"), snapshotBool(r.AuxiliaryPathMatched))
		if r.PathMatched && r.AuxiliaryPathMatched {
			if r.DisplacedRetained {
				snapshotFact(c, locales.Text("保留旧备份位置"), r.DisplacedPath)
			}
			if r.TemporaryRetained {
				snapshotFact(c, locales.Text("保留临时文件位置"), r.TemporaryPath)
			}
		} else if r.DisplacedRetained || r.TemporaryRetained {
			ui.Text(c, locales.Text("原路径身份未匹配；记录位置不代表当前路径")).TextColor(theme.Colors(c).Warning)
		}
		snapshotFact(c, locales.Text("原文件操作结束"), snapshotBool(r.Ended))
		snapshotFact(c, locales.Text("原文件取消请求"), snapshotBool(cancelRequested))
		snapshotFact(c, locales.Text("原文件操作取消"), snapshotBool(r.Canceled))
		snapshotFact(c, locales.Text("原文件操作错误"), snapshotDisplayError(r.Error))
		snapshotFact(c, locales.Text("原文件清理错误"), snapshotDisplayError(r.CleanupError))
		if r.PublicationUnknown || r.DisplacementUnknown || r.RestorationUnknown {
			ui.Text(c, locales.Text("后来内容匹配不会确认丢失的移动回执")).TextColor(theme.Colors(c).Warning)
		}
	})
}

func (s *State) snapshotModals(c *ui.Context) {
	if s.leave != nil {
		return
	}
	for _, d := range s.documents {
		if !s.active(d) || d.snapshot == nil {
			continue
		}
		if p := d.snapshot.confirmation; p != nil {
			theme.Modal(c.Key(fmt.Sprintf("kv-snapshot-confirm-%d", d.id)), &p.open, func() { s.snapshotConfirmationView(c, d, p) })
			if !p.open && d.snapshot.confirmation == p {
				s.CancelSnapshotConfirmation(d)
			}
		}
		if p := d.snapshot.fileConfirmation; p != nil {
			theme.Modal(c.Key(fmt.Sprintf("kv-snapshot-file-confirm-%d", d.id)), &p.open, func() { s.snapshotFileConfirmationView(c, d, p) })
			if !p.open && d.snapshot.fileConfirmation == p {
				s.CancelSnapshotConfirmation(d)
			}
		}
	}
}
