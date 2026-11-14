package kvworkspace

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	"strconv"
)

func httpArchiveLimitsInput(compressed, expanded string) (contract.KVHTTPArchiveRequest, error) {
	a, e := strconv.ParseUint(compressed, 10, 64)
	if e != nil {
		return contract.KVHTTPArchiveRequest{}, e
	}
	b, e := strconv.ParseUint(expanded, 10, 64)
	return contract.KVHTTPArchiveRequest{CompressedBytes: a, ExpandedBytes: b}, e
}
func (s *State) httpArchiveModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || d.httpArchive == nil || !d.httpArchive.open || s.leave != nil {
			continue
		}
		x := d.httpArchive
		width, height := c.Size()
		theme.Modal(c.Key(fmt.Sprintf("consul-archive-%d", d.id)), &x.open, func() {
			ui.Column(c).Width(min(float32(680), max(float32(1), width-68))).Height(min(float32(530), max(float32(1), height-68))).Gap(6).Children(func() {
				ui.Text(c, locales.Text("Consul 原生 Snapshot")).Bold()
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), d.profileID, d.scope.Generation))
				ui.Text(c, locales.Text("捕获或恢复整个数据中心后端及 ACL 密钥；键前缀、Namespace 和 Partition 不限定范围")).Wrap().TextColor(theme.Colors(c).Warning)
				busy := d.Busy() || d.FileBusy()
				ui.Scroll(c).Fill().MinHeight(0).Children(func() {
					ui.Column(c).FillWidth().Gap(6).Children(func() {
						ui.Text(c, locales.Text("压缩字节限额"))
						if ui.TextInput(c, &x.compressed).Label(locales.Text("压缩字节限额")).FillWidth().Disabled(busy).Changed() {
							x.capturePlan = nil
							x.checked = false
						}
						ui.Text(c, locales.Text("展开字节限额"))
						if ui.TextInput(c, &x.expanded).Label(locales.Text("展开字节限额")).FillWidth().Disabled(busy).Changed() {
							x.capturePlan = nil
							x.checked = false
						}
						if x.capturePlan != nil {
							ui.Text(c, locales.Text("仅捕获一次；原生校验、私有存储同步与用户文件发布分别记录")).Wrap()
							ui.Checkbox(c, &x.checked, locales.Text("我确认捕获此原集群的完整后端"))
							if ui.Button(c, locales.Text("确认捕获 Snapshot")).Disabled(busy || !x.checked).Clicked() {
								s.ConfirmHTTPArchive(d)
							}
						}
						if r := x.original; r != nil {
							ui.Text(c, fmt.Sprintf("HTTP %d · source %t · stored %t · fsync %t · %d bytes", r.Original.Status, r.Original.SourceComplete, r.Stored, r.FilesSynced, r.ArchiveBytes)).Wrap()
							ui.Text(c, "SHA256 "+hex.EncodeToString(r.ArchiveSHA256)).Wrap()
							ui.Checkbox(c, &x.revealed, locales.Text("显示原私有回执"))
							if x.revealed {
								raw, _ := json.MarshalIndent(r, "", "  ")
								v := clipped(string(raw))
								ui.TextArea(c, &v).Label(locales.Text("原私有回执")).Height(140).FillWidth().ReadOnly(true)
							}
							ui.Checkbox(c, &x.checked, locales.Text("允许将完整 Snapshot 或私有回执保存到文件"))
							ui.Toolbar(c, func() {
								if ui.Button(c, locales.Text("保存原 Snapshot 文件")).Disabled(busy || !x.checked || !r.Original.SourceComplete || x.stored == nil).Clicked() {
									s.SelectHTTPArchiveFile(d, contract.KVSnapshotBackendFile)
								}
								if ui.Button(c, locales.Text("导出原私有回执")).Disabled(busy || !x.checked || x.stored == nil).Clicked() {
									s.SelectHTTPArchiveFile(d, contract.KVSnapshotJournalFile)
								}
							}).FillWidth()
						}
						if p := x.filePlan; p != nil {
							ui.Text(c, x.fileObservation.Choice.Path).Wrap()
							if old := x.fileObservation.Existing; old != nil {
								ui.Text(c, fmt.Sprintf("%d bytes · SHA256 %s", old.Size, old.SHA256)).Wrap()
								ui.Text(c, locales.Text("替换已检查文件；保留旧文件，目标变化时停止")).Wrap().TextColor(theme.Colors(c).Warning)
							}
							ui.Checkbox(c, &x.checked, locales.Text("我确认保存此原文件"))
							if ui.Button(c, locales.Text("确认保存 Snapshot 文件")).Disabled(busy || !x.checked).Clicked() {
								s.ConfirmHTTPArchiveFile(d)
							}
						}
						for _, r := range x.files {
							ui.Text(c, fmt.Sprintf("%s · published %t · unknown %t · %s", r.DestinationPath, r.Published, r.PublicationUnknown, r.DisplacedPath)).Wrap()
						}
						if len(x.restoreInput) > 0 {
							ui.Text(c, x.restorePath+" · SHA256 "+hex.EncodeToString(x.restoreHash)).Wrap()
							if p := x.restorePlan; p != nil {
								ui.Text(c, p.TargetURL()).Wrap()
								ui.Text(c, fmt.Sprintf("index %d · term %d · state %d bytes", p.Source().Index, p.Source().Term, p.Source().StateBytes)).Wrap()
								ui.Text(c, locales.Text("恢复将替换完整后端并断开全部 Consul 会话；恢复连接不会重放请求")).Wrap().TextColor(theme.Colors(c).Warning)
								ui.Checkbox(c, &x.checked, locales.Text("我确认用此完整原归档恢复此原连接的数据中心"))
								if ui.Button(c, locales.Text("确认恢复 Snapshot")).Disabled(busy || !x.checked).Clicked() {
									s.ConfirmHTTPArchiveRestore(d)
								}
							}
						}
						if x.restoreLast != nil {
							ui.Text(c, fmt.Sprintf("restore HTTP %d · ACK %t · unknown %t", x.restoreLast.HTTP.Status, x.restoreLast.Completed, x.restoreLast.WriteOutcomeUnknown)).Wrap()
							ui.Checkbox(c, &x.revealed, locales.Text("显示原私有回执"))
							if x.revealed {
								selected := x.restoreLast
								if x.restoreUnknown != nil {
									ui.Select(c, &x.restoreSelection, []string{"last", "unknown"}).Label(locales.Text("原私有回执"))
									if x.restoreSelection == "unknown" {
										selected = x.restoreUnknown
									}
								}
								v := selected.Clone()
								v.Request.Archive = nil
								raw, _ := json.MarshalIndent(v, "", "  ")
								text := clipped(string(raw))
								ui.TextArea(c, &text).Label(locales.Text("原私有回执")).Height(100).FillWidth().ReadOnly(true)
							}
						}
						ui.Checkbox(c, &x.exportConsent, locales.Text("允许将完整 Snapshot 或私有回执保存到文件"))
						if ui.Button(c, locales.Text("导出完整 Snapshot 工具回执")).Disabled(busy || !x.exportConsent || x.original == nil && x.restoreLast == nil).Clicked() {
							s.ExportHTTPArchiveReceipts(d)
						}
					})
				})
				if d.error != "" {
					ui.Text(c, clipped(d.error)).Wrap().TextColor(theme.Colors(c).Warning)
				}
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("预览 Snapshot 捕获")).Disabled(busy || d.NeedsRecovery() || x.original != nil).Clicked() {
						s.PrepareHTTPArchive(d)
					}
					if ui.Button(c, locales.Text("打开原 Snapshot 恢复文件")).Disabled(busy || d.NeedsRecovery()).Clicked() {
						s.OpenHTTPArchiveRestoreFile(d)
					}
				}).FillWidth()
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("预览 Snapshot 恢复")).Disabled(busy || d.NeedsRecovery() || len(x.restoreInput) == 0 || d.hasUnknownWrites()).Clicked() {
						s.PrepareHTTPArchiveRestore(d)
					}
					if ui.Button(c, locales.Text("清除原 Snapshot 归档")).Disabled(busy).Clicked() {
						s.ClearHTTPArchive(d)
					}
					if ui.Button(c, locales.Text("取消")).Disabled(!busy).Clicked() {
						s.RequestCancel(d)
					}
				}).FillWidth()
			})
		})
	}
}
