package dataworkspace

import (
	"fmt"
	"io"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func parseDocumentImport(r io.Reader, format string) (*mysqlDataImportInput, error) {
	docs, err := jsondocument.ParseImport(r, format)
	if err != nil {
		return nil, err
	}
	input := &mysqlDataImportInput{Fields: []string{"document"}}
	for _, doc := range docs {
		input.Rows = append(input.Rows, []contract.DataValue{{Kind: contract.JSON, Text: string(doc)}})
	}
	return input, nil
}

func documentImportRequest(t *Document) (contract.DataImportRequest, error) {
	tr := &t.transfer
	if !tr.DocumentImport || tr.Target != "existing" || tr.TargetTable != "" || tr.Mode != "document_replace" && tr.Mode != "document_merge" || tr.Input == nil || len(tr.Input.Rows) == 0 || len(tr.Input.Rows) > contract.MaxDataImportRows {
		return contract.DataImportRequest{}, contract.ErrUnsupported
	}
	r := contract.DataImportRequest{Mode: tr.Mode}
	for _, row := range tr.Input.Rows {
		if len(row) != 1 || row[0].Kind != contract.JSON || row[0].Null || row[0].Default || row[0].Binary || len(row[0].Bytes) != 0 {
			return r, contract.ErrDataPage
		}
		r.Edits = append(r.Edits, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"document": row[0]}})
	}
	return r, nil
}

func documentImportDescription(d *Document, mode string) string {
	method := "POST"
	if mode == "document_merge" {
		method = "PUT"
	}
	return method + " /indexes/" + d.database + "/documents (JSON import; asynchronous admission)"
}

func (s *State) startDocumentImport(services ui.Services, t *Document) {
	tr := &t.transfer
	importer := s.runtimeFor(t).Importer
	if !s.canUseSession(t) || s.sessionFor(t).Generation != tr.Generation || t.closed || t.loading || t.edit.Busy || t.edit.Uncertain != nil || mysqlDataHasDraft(t) || tr.Busy || tr.Submitted || tr.Uncertain || importer == nil || !mysqlDataImportConfirmed(t) {
		return
	}
	r, err := documentImportRequest(t)
	if err != nil {
		tr.Err = err.Error()
		return
	}
	tr.Submitted, tr.Step = true, 3
	ctx, out, request, gen := s.beginDataTransfer(t)
	profile, db, table := t.profileID, t.database, t.table
	description := documentImportDescription(t, r.Mode)
	complete := s.beginActivity(t, "import", description)
	go func() {
		started := time.Now()
		imported, err := importer(ctx, profile, db, table, r)
		var saved *contract.DataEditResult
		if imported != nil {
			saved = imported.Saved
		}
		contract.CompleteActivity(complete, dataActivityOutcome(err, saved, len(r.Edits), false))
		history := []mysqlDataHistoryEntry{{Kind: "IMPORT", SQL: description, Summary: true, At: time.Now(), Duration: time.Since(started)}}
		out <- mysqlDataTransferResult{Write: true, Imported: imported, Saved: saved, Request: request, Generation: gen, Profile: profile, Rows: len(r.Edits), Destination: table, History: history, Err: err}
		services.Invalidate()
	}()
}

func (s *State) observeDocumentImport(services ui.Services, t *Document) {
	tr := &t.transfer
	verify := s.verifierFor(t)
	if !s.canUseSession(t) || s.sessionFor(t).Generation != tr.Generation || !tr.DocumentImport || !tr.Uncertain || tr.Busy || t.edit.Busy || verify == nil || t.edit.Uncertain == nil {
		return
	}
	result := cloneEditResult(t.edit.Uncertain)
	ctx, out, request, gen := s.beginDataTransfer(t)
	profile, db, table := t.profileID, t.database, t.table
	go func() {
		v, err := verify(ctx, profile, db, table, result)
		out <- mysqlDataTransferResult{Write: true, Request: request, Generation: gen, Profile: profile, DocumentVerification: v, Err: err}
		services.Invalidate()
	}()
}

// Admission and explicit readback share the existing transfer lifecycle. Parked
// results stay on their original private channel; stale generations never clear
// uncertainty, source input, or cached data, even after server success.
func (s *State) consumeDocumentImport(services ui.Services, t *Document, r mysqlDataTransferResult, stale bool) {
	tr := &t.transfer
	saved := r.Saved
	if r.DocumentVerification != nil {
		saved = r.DocumentVerification.Recovered
	}
	if saved != nil && saved.Async != nil {
		evidence := *saved.Async
		t.edit.LastAsync = &evidence
	}
	if len(r.History) != 0 {
		s.recordDataWriteHistory(t, r.History, saved, r.Err, stale)
	}
	if !stale && r.Err == nil && r.DocumentVerification != nil && r.DocumentVerification.Status == "applied" && saved != nil && saved.CommitConfirmed && saved.Verified && !saved.CommitUncertain && !saved.Incomplete && int64(saved.Applied) == tr.TotalRows {
		tr.Completed, tr.Uncertain = true, false
		t.edit.Uncertain = nil
		t.edit.AsyncStatusActive = true
		tr.Err = fmt.Sprintf(locales.Text("已核验导入 %d 份文档"), tr.TotalRows)
		t.edit.Err = tr.Err
		s.appendDataHistory(t, mysqlDataHistoryEntry{Kind: "IMPORT", SQL: documentImportDescription(t, tr.Mode), Summary: true, At: time.Now(), Rows: saved.Applied, Message: locales.Text("已核验：数据更改已提交")})
		s.Load(services, t, true)
		return
	}
	uncertain := stale || r.DocumentVerification != nil || tr.Uncertain || saved != nil && (saved.Async != nil || saved.CommitUncertain || saved.Incomplete) || r.Err == nil
	if uncertain {
		tr.Uncertain = true
		if saved != nil {
			t.edit.Uncertain = cloneEditResult(saved)
		}
		if t.edit.Uncertain == nil {
			t.edit.Uncertain = &contract.DataEditResult{CommitUncertain: true}
		}
		t.edit.AsyncStatusActive = !stale && r.Err == nil
		tr.Err = documentImportStatus(t)
		if stale {
			tr.Err = locales.Text("导入结果不确定，禁止重放；请核验数据库")
		}
		if r.Err != nil {
			tr.Err += " · " + r.Err.Error()
		}
		t.edit.Err = tr.Err
	} else if r.Err != nil {
		tr.Err = locales.Text("导入失败；本次向导不会重放：") + r.Err.Error()
	}
}

func documentImportModeLabel(mode string) string {
	if mode == "document_merge" {
		return locales.Text("合并同 ID 字段")
	}
	return locales.Text("完整替换同 ID 文档")
}

// This is a document page in the sole transfer wizard, not a separate grid or
// document model. Native documents have no tabular mapping or SQL options.
func (s *State) documentImportWizard(c *ui.Context, t *Document, available bool, title string) {
	tr := &t.transfer
	steps := []string{"源文件", "导入模式", "校验确认", "执行结果"}
	tr.Step = max(0, min(tr.Step, len(steps)-1))
	modal := theme.Modal(c, &tr.Open, func() {
		cw, ch := c.Size()
		ui.Column(c).Width(min(float32(760), cw-68)).Height(min(float32(610), ch-68)).Gap(9).Children(func() {
			ui.Text(c, title+" · "+locales.Text(steps[tr.Step])).Bold().FontSize(theme.Font(c, 15))
			ui.Text(c, t.database).SingleLine()
			if !available {
				ui.Text(c, locales.Text("原数据库会话已失效，请关闭向导后重新打开")).TextColor(theme.Colors(c).Error)
			}
			ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
				ui.Column(c).Gap(8).FillWidth().Children(func() {
					switch tr.Step {
					case 0:
						mysqlDataWizardSelect(c, locales.Text("文件格式"), &tr.Format, []string{"JSON", "JSONL"}, func(x string) string { return x }).Width(160).Disabled(tr.Busy || tr.Input != nil)
						ui.Text(c, locales.Text("JSON 接受对象或对象数组；JSONL 每个非空行一份对象。保留嵌套字段与精确数字。"))
						ui.Text(c, locales.Text("单次最多 10000 份文档、8 MiB，每份最多 1 MiB；一次提交一个任务，不自动分批或重试。"))
						ui.Text(c, tr.Path).SingleLine()
						if ui.Button(c, locales.Text("选择文件")).Disabled(tr.Busy).Clicked() {
							s.chooseDataImportFile(c.Services(), t)
						}
						if tr.Input != nil {
							ui.Text(c, fmt.Sprintf(locales.Text("已读取 %d 份文档"), tr.TotalRows))
						}
					case 1:
						mysqlDataWizardSelect(c, locales.Text("导入模式"), &tr.Mode, []string{"document_replace", "document_merge"}, documentImportModeLabel).Width(260)
						ui.Text(c, locales.Text("完整替换会删除同 ID 文档中未提供的字段；合并保留未提供字段，嵌套对象作为整个字段替换。两种模式均可创建新 ID，不清空索引。"))
						ui.Text(c, locales.Text("合并前读取原文档以核对结果；并发变化可能导致核验不通过，不能保证原子比较与交换。"))
					case 2:
						ui.Text(c, fmt.Sprintf(locales.Text("目标 %s · %s · %d 份文档"), t.database, documentImportModeLabel(tr.Mode), tr.TotalRows)).Bold()
						ui.Text(c, locales.Text("目标索引必须已存在并设置主键；缺失或重复 ID、非法 JSON 和超限请求会拒绝。"))
						ui.Text(c, locales.Text("任务受理不代表执行成功；观察只读取原任务，不会重复发送写入。提交前的记录检查不是原子比较与交换。"))
						if tr.Input != nil {
							for i, row := range tr.Input.Rows[:min(3, len(tr.Input.Rows))] {
								ui.Text(c, fmt.Sprintf("%d · %s", i+1, documentImportPreview(row[0].Text))).SingleLine()
							}
						}
						mysqlDataWizardInput(c, &tr.Confirmation, locales.Text("输入目标名称以确认导入")).Placeholder(t.database)
					case 3:
						if t.edit.Uncertain != nil {
							ui.Text(c, documentImportStatus(t))
						}
						if tr.Completed {
							ui.Text(c, fmt.Sprintf(locales.Text("已核验导入 %d 份文档"), tr.TotalRows))
						}
						ui.Text(c, locales.Text("观察成功任务后将逐份读取全部导入文档；失败或结果不符保留待核验状态，禁止重放。"))
					}
					if tr.Err != "" && !tr.Completed && !t.edit.AsyncStatusActive {
						ui.Text(c, tr.Err).TextColor(theme.Colors(c).Error)
					}
					if tr.Busy {
						ui.Text(c, locales.Text("正在处理数据…"))
						if tr.Canceling {
							ui.Text(c, locales.Text("正在等待取消确认…"))
						}
					}
				})
			})
			ui.Row(c).Wrap().Gap(8).Justify(ui.End).Shrink(0).Children(func() {
				if tr.Step > 0 && !tr.Submitted && ui.Button(c, locales.Text("上一步")).Disabled(tr.Busy).Clicked() {
					tr.Step--
				}
				if tr.Uncertain {
					if ui.Button(c, locales.Text("观察任务结果")).Disabled(!available || tr.Busy || t.edit.Busy).Clicked() {
						s.observeDocumentImport(c.Services(), t)
					}
					if ui.Button(c, locales.Text("写入任务证据")).Disabled(t.edit.LastAsync == nil).Clicked() {
						t.edit.AsyncEvidenceOpen = true
						tr.Open = false
					}
				}
				label := locales.Text("取消")
				if tr.Submitted && !tr.Busy {
					label = locales.Text("关闭")
				}
				if ui.Button(c, label).Disabled(tr.Canceling).Clicked() {
					s.stopDataTransfer(t)
				}
				if !tr.Submitted && tr.Step < 3 {
					if tr.Step == 2 {
						if ui.PrimaryButton(c, locales.Text("确认导入")).Disabled(!available || tr.Busy || !mysqlDataImportConfirmed(t) || tr.Input == nil).Clicked() {
							s.startDocumentImport(c.Services(), t)
						}
					} else if ui.PrimaryButton(c, locales.Text("下一步")).Disabled(!available || tr.Busy || tr.Input == nil).Clicked() {
						tr.Step++
					}
				}
			})
		})
	})
	s.finishDataTransferModal(t, modal.PressedOutside())
}

func documentImportPreview(raw string) string {
	// Read at most 241 runes; source stays complete even for a 1 MiB document.
	runes := make([]rune, 0, 241)
	for _, r := range raw {
		if r == '\n' {
			r = ' '
		}
		runes = append(runes, r)
		if len(runes) > 240 {
			return string(runes[:240]) + "…"
		}
	}
	return string(runes)
}

func documentImportStatus(t *Document) string {
	if t.transfer.Completed {
		return fmt.Sprintf(locales.Text("已核验导入 %d 份文档"), t.transfer.TotalRows)
	}
	r := t.edit.Uncertain
	if r == nil || r.Async == nil {
		return locales.Text("导入结果不确定，禁止重放；请核验数据库")
	}
	status := locales.Text("导入任务已受理，源文档与证据已保留；尚未确认完成，禁止重放")
	if r.Async.Status == "failed" || r.Async.Status == "canceled" {
		status = locales.Text("导入任务失败或已取消；源文档与证据已保留，禁止重放")
	} else if r.Async.Status == "succeeded" {
		status = locales.Text("导入任务成功，但完整读回未能确认；源文档与证据已保留，禁止重放")
	}
	return status + " · " + locales.Text("任务编号") + " " + r.Async.TaskID
}
