package dataworkspace

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func graphImportAnchorsComplete(s contract.GraphImportSnapshot) bool {
	if len(s.Anchors) != len(s.Request.Bindings) {
		return false
	}
	wanted := map[string]string{}
	for _, b := range s.Request.Bindings {
		wanted[b.SourceID] = b.DestinationID
	}
	created := 0
	for _, n := range s.Request.Bundle.Nodes {
		if wanted[n.ElementID] == "" {
			created++
		}
	}
	if s.Nodes != created {
		return false
	}
	seen := map[string]bool{}
	for _, a := range s.Anchors {
		if seen[a.SourceID] || wanted[a.SourceID] == "" || wanted[a.SourceID] != a.Node.ElementID || graphimport.Validate(contract.GraphImportBundle{Format: graphimport.Format, Nodes: []contract.GraphNode{a.Node}}) != nil {
			return false
		}
		seen[a.SourceID] = true
	}
	return true
}

// Graph exchange is another page sequence in the shared transfer wizard. The
// original Document owns its source, mapped anchors, one preview and receipt.
func (s *State) graphTransferWizard(c *ui.Context, d *Document, available bool, title string) {
	tr := &d.transfer
	tr.Step = max(0, min(tr.Step, 3))
	modal := theme.Modal(c, &tr.Open, func() {
		cw, ch := c.Size()
		ui.Column(c).Width(min(float32(760), cw-68)).Height(min(float32(610), ch-68)).Gap(9).Children(func() {
			ui.Text(c, title+" · "+locales.Text("完整图交换")).Bold().FontSize(theme.Font(c, 15))
			ui.Text(c, fmt.Sprintf(locales.Text("目标数据库：%s"), d.database)).SingleLine()
			if !available {
				ui.Text(c, locales.Text("原数据库会话已失效，请关闭向导后重新打开")).TextColor(theme.Colors(c).Error)
			}
			ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
				ui.Column(c).Gap(8).FillWidth().Children(func() {
					if tr.Kind == "export" {
						s.graphExportWizardPage(c, d)
					} else {
						s.graphImportWizardPage(c, d)
					}
					if tr.Err != "" {
						color := theme.Colors(c).Error
						if tr.Completed && !tr.Uncertain {
							color = theme.Colors(c).Muted
						}
						ui.Text(c, tr.Err).TextColor(color)
					}
					if tr.Busy {
						label := locales.Text("正在处理数据…")
						if tr.Canceling {
							label = locales.Text("正在等待取消确认…")
						}
						ui.Text(c, label)
					}
				})
			})
			ui.Row(c).Wrap().Gap(8).Justify(ui.End).Shrink(0).Children(func() {
				if tr.Step > 0 && !tr.Submitted && ui.Button(c, locales.Text("上一步")).Disabled(tr.Busy).Clicked() {
					releaseGraphImportPlan(tr)
					tr.Step--
					tr.Confirmation = ""
					tr.Err = ""
				}
				label := locales.Text("取消")
				if tr.Submitted && !tr.Busy {
					label = locales.Text("关闭")
				}
				if ui.Button(c, label).Disabled(tr.Canceling).Clicked() {
					s.stopDataTransfer(d)
				}
				if tr.Kind == "export" {
					if !tr.Submitted && ui.PrimaryButton(c, locales.Text("导出完整数据库图")).Disabled(!available || tr.Busy).Clicked() {
						s.startGraphExport(c.Services(), d)
					}
				} else if !tr.Submitted {
					switch tr.Step {
					case 0:
						if ui.PrimaryButton(c, locales.Text("下一步")).Disabled(!available || tr.Busy || tr.GraphBundle == nil).Clicked() {
							tr.Step = 1
						}
					case 1:
						if ui.PrimaryButton(c, locales.Text("预览图导入")).Disabled(!available || tr.Busy).Clicked() {
							s.previewGraphTransfer(c.Services(), d)
						}
					case 2:
						if ui.PrimaryButton(c, locales.Text("确认导入完整图")).Disabled(!available || tr.Busy || tr.GraphPlan == nil || tr.Confirmation != d.database).Clicked() {
							s.confirmGraphTransfer(c.Services(), d)
						}
					}
				}
			})
		})
	})
	s.finishDataTransferModal(d, modal.PressedOutside())
}

func (s *State) graphImportWizardPage(c *ui.Context, d *Document) {
	tr := &d.transfer
	switch tr.Step {
	case 0:
		mysqlDataWizardSelect(c, locales.Text("文件格式"), &tr.Format, []string{"JSON", "JSONL"}, func(x string) string { return x }).Width(160).Disabled(tr.Busy || tr.GraphBundle != nil)
		ui.Text(c, locales.Text("图交换文件保留节点、标签、关系方向和完整类型属性。源 ID 只用于关联文件中的实体。"))
		ui.Text(c, locales.Text("最多 10000 个节点和关系、8 MiB，每个完整实体最多 1 MiB；整批导入只提交一个事务。"))
		mysqlDataWizardInput(c, &tr.GraphSourcePath, locales.Text("源文件路径")).Disabled(tr.Busy)
		if ui.Button(c, locales.Text("读取图交换文件")).Disabled(tr.Busy || tr.GraphSourcePath == "").Clicked() {
			s.readGraphImportFile(c.Services(), d, tr.GraphSourcePath)
		}
		ui.Text(c, tr.Path).SingleLine()
		if ui.Button(c, locales.Text("选择文件")).Disabled(tr.Busy).Clicked() {
			s.chooseGraphImportFile(c.Services(), d)
		}
		if tr.GraphBundle != nil {
			ui.Text(c, fmt.Sprintf(locales.Text("已读取 %d 个节点和 %d 条关系"), len(tr.GraphBundle.Nodes), len(tr.GraphBundle.Relationships)))
		}
	case 1:
		if tr.GraphBundle == nil {
			return
		}
		ui.Text(c, locales.Text("默认创建所有源节点。可显式复用已有节点；复用保留目标节点的标签和属性。文件外的端点必须映射。"))
		for _, id := range graphimport.ExternalEndpoints(*tr.GraphBundle)[:min(200, len(graphimport.ExternalEndpoints(*tr.GraphBundle)))] {
			ui.Text(c, locales.Text("必需端点：")+id).SingleLine()
		}
		mysqlDataWizardInput(c, &tr.GraphBindingSource, locales.Text("源节点或外部端点 ID")).Disabled(tr.Busy)
		mysqlDataWizardInput(c, &tr.GraphBindingDestination, locales.Text("目标节点 element ID")).Disabled(tr.Busy)
		if ui.Button(c, locales.Text("添加或替换端点映射")).Disabled(tr.Busy || tr.GraphBindingSource == "" || tr.GraphBindingDestination == "").Clicked() {
			source, destination := tr.GraphBindingSource, tr.GraphBindingDestination
			i := slices.IndexFunc(tr.GraphBindings, func(b contract.GraphImportBinding) bool { return b.SourceID == source })
			binding := contract.GraphImportBinding{SourceID: source, DestinationID: destination}
			if i < 0 {
				tr.GraphBindings = append(tr.GraphBindings, binding)
			} else {
				tr.GraphBindings[i] = binding
			}
			tr.Err = ""
		}
		remove := -1
		for i, binding := range tr.GraphBindings {
			ui.Row(c).Gap(6).Children(func() {
				ui.Text(c, binding.SourceID+" → "+binding.DestinationID).SingleLine().Grow(1).MinWidth(0)
				if ui.Button(c, locales.Text("移除")).Disabled(tr.Busy).Clicked() {
					remove = i
				}
			})
		}
		if remove >= 0 {
			tr.GraphBindings = slices.Delete(tr.GraphBindings, remove, remove+1)
		}
	case 2:
		ui.Text(c, fmt.Sprintf(locales.Text("将创建 %d 个节点和 %d 条关系，复用 %d 个源端点"), tr.GraphSnapshot.Nodes, len(tr.GraphSnapshot.Request.Bundle.Relationships), len(tr.GraphSnapshot.Anchors))).Bold()
		ui.Text(c, locales.Text("请核对完整源数据、端点映射和目标节点观察。目标节点在预览后发生变化会阻止整批提交。"))
		mysqlDataWizardInput(c, &tr.Confirmation, locales.Text("输入目标数据库名以确认")).Disabled(tr.Busy)
		if ui.Button(c, locales.Text("复制完整预览")).Clicked() {
			c.WriteClipboard(tr.GraphSnapshot.NativeJSON)
		}
		ui.Text(c, dataMutationPreview(tr.GraphSnapshot.NativeJSON)).Label(locales.Text("图导入完整预览"))
	case 3:
		if tr.Uncertain {
			ui.Text(c, locales.Text("源文件、预览和回执已保留。请在原数据库核验结果；此向导禁止再次提交。"))
		}
		if tr.GraphResult != nil && tr.GraphResult.NativeJSON != "" {
			if ui.Button(c, locales.Text("复制完整回执")).Clicked() {
				c.WriteClipboard(tr.GraphResult.NativeJSON)
			}
			if ui.Button(c, locales.Text("原生事务回执")).Clicked() {
				d.nativeReceipt.Open = true
			}
			ui.Text(c, nativeWriteReceiptPreview(tr.GraphResult.NativeJSON)).Label(locales.Text("原生事务回执 JSON"))
		}
	}
}

func (s *State) graphExportWizardPage(c *ui.Context, d *Document) {
	tr := &d.transfer
	ui.Text(c, locales.Text("导出整个原数据库中的全部节点和关系，包括无标签节点及其它标签和关系类型。"))
	ui.Text(c, locales.Text("这是有界的实时扫描；并发变化可能使导出失败。文件只在完整类型和端点闭合校验通过后发布。"))
	ui.Text(c, locales.Text("最多 10000 个节点和关系、8 MiB，每个完整实体最多 1 MiB；整批导入只提交一个事务。"))
	if !tr.Submitted {
		mysqlDataWizardSelect(c, locales.Text("文件格式"), &tr.Format, []string{"JSON", "JSONL"}, func(x string) string { return x }).Width(160).Disabled(tr.Busy)
		mysqlDataWizardInput(c, &tr.ExportPath, locales.Text("目标文件路径")).Disabled(tr.Busy)
		if !d.graphTransferOnly && ui.Button(c, locales.Text("返回表格导出")).Disabled(tr.Busy).Clicked() {
			tr.GraphExchange = false
			tr.Format = "CSV"
			tr.Scope = "page"
			tr.Step = 0
		}
	}
}
