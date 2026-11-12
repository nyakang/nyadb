package dataworkspace

import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) keyPrefixSetActions(c *ui.Context, d *Document) {
	if d == nil || d.closed {
		return
	}
	for _, item := range []struct {
		label    string
		evidence *nativeWriteReceiptState
	}{
		{"完整候选原始观察", &d.prefixSetObservation}, {"分批删除进度与回执", &d.prefixSetLedger},
	} {
		if item.evidence.JSON != "" && theme.IconButton(c, "", "info").Label(locales.Text(item.label)).Tooltip(locales.Text(item.label)).Size(theme.RowHeight(c, 26), theme.RowHeight(c, theme.ControlHeight)).Padding(0).Clicked() {
			item.evidence.Open = true
		}
	}
}
func (s *State) cancelKeyPrefixSetConfirmation(d *Document) {
	m := d.prefixSet
	if m == nil || m.pending() {
		return
	}
	if m.plan != nil {
		_ = m.plan.Close()
		m.plan = nil
	}
	if m.planTicket != nil {
		m.planTicket.revoke()
		m.planTicket = nil
	}
	m.confirmOpen, m.reviewOpen = false, true
}
func (s *State) keyPrefixSetModal(c *ui.Context, d *Document) {
	m := d.prefixSet
	if m == nil || !s.consumeFor(d) {
		return
	}
	if m.inputOpen {
		theme.Modal(c, &m.inputOpen, func() {
			ui.Column(c).Width(640).MaxWidthPercent(95).Gap(8).Children(func() {
				ui.Text(c, locales.Text("按前缀分批删除候选键")).FontSize(theme.Font(c, theme.FontSize))
				ui.Text(c, d.database+" · "+d.table)
				ui.Text(c, locales.Text("先捕获完整候选，再逐批单独确认。各批分别提交，早批成功后晚批可能失败；之后新增的键保留。"))
				ui.Text(c, locales.Text("完整前缀"))
				input := ui.TextInput(c, &m.prefix).Label(locales.Text("输入完整分批删除前缀")).FillWidth()
				theme.BindNativeEdit(c, input, fmt.Sprintf("data/key-prefix-set/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return !d.closed && d.prefixSet == m && m.inputOpen }, true, false)
				if d.edit.Err != "" {
					ui.Text(c, d.edit.Err).TextColor(theme.Colors(c).Error)
				}
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("取消")).Clicked() {
						m.inputOpen = false
					}
					if ui.Button(c, locales.Text("捕获完整候选键")).Disabled(!validKeyPrefixInput(m.prefix)).Clicked() {
						s.captureKeyPrefixSet(c.Services(), d)
					}
				})
			})
		})
		if !m.inputOpen && !m.pending() && m.set == nil {
			s.stopKeyPrefixSet(d)
		}
	}
	if m.reviewOpen {
		theme.Modal(c, &m.reviewOpen, func() {
			ui.Column(c).Width(720).MaxWidthPercent(95).Gap(8).Children(func() {
				ui.Text(c, locales.Text("完整候选与分批进度")).FontSize(theme.Font(c, theme.FontSize))
				ui.Text(c, m.request.Prefix).FillWidth().SingleLine().MinWidth(0).Tooltip(m.request.Prefix)
				ui.Text(c, prefixSetProgressText(d.prefixSetProgress))
				ui.Text(c, locales.Text("每批需要单独预览和确认；各批分别提交，不会整体回滚或自动执行下一批。后来新增的键保留。"))
				ui.Scroll(c).Height(160).FillWidth().Children(func() {
					ui.Text(c, nativeWriteReceiptPreview(m.keysJSON)).Label(locales.Text("此次完整候选键列表"))
				})
				if d.edit.Err != "" {
					ui.Text(c, d.edit.Err).TextColor(theme.Colors(c).Error)
				}
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("复制完整候选键列表")).Clicked() {
						c.WriteClipboard(m.keysJSON)
					}
					if ui.Button(c, locales.Text("原始观察")).Clicked() {
						d.prefixSetObservation.Open = true
					}
					if ui.Button(c, locales.Text("进度与回执")).Clicked() {
						d.prefixSetLedger.Open = true
					}
				})
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("停止并保留原观察与回执")).Clicked() {
						m.reviewOpen = false
					}
					if ui.Button(c, fmt.Sprintf(locales.Text("预览第 %d 批删除"), m.current.NextBatch+1)).Disabled(d.loading || m.pending() || m.current.Stopped || m.current.Uncertain || m.uncertain).Clicked() {
						s.previewKeyPrefixSetBatch(c.Services(), d)
					}
				})
			})
		})
		if !m.reviewOpen && !m.pending() && !m.confirmOpen && !m.uncertain {
			s.stopKeyPrefixSet(d)
		}
	}
	if m.confirmOpen {
		theme.Modal(c, &m.confirmOpen, func() {
			ui.Column(c).Width(720).MaxWidthPercent(95).Gap(8).Children(func() {
				batch := m.preview.KeyPrefixDeleteBatch
				ui.Text(c, fmt.Sprintf(locales.Text("确认第 %d 批候选键删除"), batch.Index+1)).FontSize(theme.Font(c, theme.FontSize))
				ui.Text(c, prefixSetProgressText(d.prefixSetProgress))
				ui.Text(c, fmt.Sprintf(locales.Text("仅提交本批 %d 个原候选键；已完成的早批不会回滚，其余批次仍需单独确认。"), batch.Count))
				ui.Scroll(c).Height(260).FillWidth().Children(func() {
					ui.Text(c, dataMutationPreview(m.preview.NativeJSON)).Label(locales.Text("本批候选键删除完整预览"))
				})
				if d.edit.Err != "" {
					ui.Text(c, d.edit.Err).TextColor(theme.Colors(c).Error)
				}
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("复制完整预览")).Clicked() {
						c.WriteClipboard(m.preview.NativeJSON)
					}
					if ui.Button(c, locales.Text("取消本批确认")).Clicked() {
						m.confirmOpen = false
					}
					if ui.Button(c, locales.Text("确认并删除本批候选键")).Clicked() {
						s.confirmKeyPrefixSetBatch(c.Services(), d)
					}
				})
			})
		})
		if !m.confirmOpen && m.plan != nil && !m.pending() {
			s.cancelKeyPrefixSetConfirmation(d)
		}
	}
}
func (s *State) keyPrefixSetEvidenceModals(c *ui.Context, d *Document) {
	if !s.consumeFor(d) {
		return
	}
	for _, item := range []struct {
		evidence                     *nativeWriteReceiptState
		title, description, filename string
	}{
		{&d.prefixSetObservation, "完整候选原始观察", "此完整原观察不确认写入；各批的实际结果以其原事务回执为准。", "key-prefix-set-observation.json"},
		{&d.prefixSetLedger, "分批删除进度与回执", "保留每批原结果和未确认范围。未知回执禁止后续提交或重放；之后成功读取不能确认原写入。", "key-prefix-set-receipts.json"},
	} {
		evidence := item.evidence
		if !evidence.Open || evidence.JSON == "" {
			continue
		}
		raw := evidence.JSON
		theme.Modal(c, &evidence.Open, func() {
			ui.Column(c).Width(720).MaxWidthPercent(95).Gap(8).Children(func() {
				ui.Text(c, locales.Text(item.title)).FontSize(theme.Font(c, theme.FontSize))
				ui.Text(c, locales.Text(item.description))
				if evidence == &d.prefixSetLedger {
					ui.Text(c, prefixSetProgressText(d.prefixSetProgress))
				}
				ui.Scroll(c).Height(260).FillWidth().Children(func() { ui.Text(c, nativeWriteReceiptPreview(raw)).Label(locales.Text(item.title) + " JSON") })
				if evidence.Err != "" {
					ui.Text(c, evidence.Err).TextColor(theme.Colors(c).Error)
				}
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("复制完整内容")).Clicked() {
						c.WriteClipboard(raw)
					}
					if ui.Button(c, locales.Text("导出完整内容")).Disabled(evidence.Results != nil).Clicked() {
						s.exportDataNativeEvidence(c.Services(), d, evidence, mygo.Dialog.Save, item.title, item.filename)
					}
					if ui.Button(c, locales.Text("关闭")).Clicked() {
						evidence.Open = false
					}
				})
				if evidence == &d.prefixSetLedger {
					ui.Scroll(c).Height(100).FillWidth().Children(func() {
						for i, record := range d.prefixSetProgress.Batches {
							state := locales.Text("已确认")
							if record.Outcome == "failed" {
								state = locales.Text("失败")
							} else if record.Outcome == "unknown" {
								state = locales.Text("未知")
							}
							ui.Text(c, fmt.Sprintf(locales.Text("第 %d 批：%s（%d 个键）"), record.Index+1, state, record.Count))
							label := fmt.Sprintf(locales.Text("第 %d 批原事务回执"), record.Index+1)
							if ui.Button(c, label).Disabled(record.NativeJSON == "").Clicked() {
								d.prefixSetSelectedReceipt.JSON = d.prefixSetProgress.Batches[i].NativeJSON
								d.prefixSetSelectedReceipt.Open = true
							}
						}
					})
				}
			})
		})
	}
	evidence := &d.prefixSetSelectedReceipt
	if evidence.Open && evidence.JSON != "" {
		raw := evidence.JSON
		theme.Modal(c, &evidence.Open, func() {
			ui.Column(c).Width(720).MaxWidthPercent(95).Gap(8).Children(func() {
				ui.Text(c, locales.Text("本批原事务回执")).FontSize(theme.Font(c, theme.FontSize))
				ui.Scroll(c).Height(280).FillWidth().Children(func() { ui.Text(c, nativeWriteReceiptPreview(raw)).Label(locales.Text("本批原事务回执 JSON")) })
				if evidence.Err != "" {
					ui.Text(c, evidence.Err).TextColor(theme.Colors(c).Error)
				}
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("复制完整回执")).Clicked() {
						c.WriteClipboard(raw)
					}
					if ui.Button(c, locales.Text("导出完整回执")).Disabled(evidence.Results != nil).Clicked() {
						s.exportDataNativeEvidence(c.Services(), d, evidence, mygo.Dialog.Save, "导出完整回执", "key-prefix-batch-receipt.json")
					}
					if ui.Button(c, locales.Text("关闭")).Clicked() {
						evidence.Open = false
					}
				})
			})
		})
	}
}
