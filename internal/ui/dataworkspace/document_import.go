package dataworkspace

import (
	"fmt"
	"io"
	"slices"
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

func (s *State) documentImportRequest(t *Document) (contract.DataImportRequest, error) {
	tr := &t.transfer
	if !tr.DocumentImport || tr.Target != "existing" || tr.TargetTable != "" || !slices.Contains(s.documentImportModes(t), tr.Mode) || tr.Input == nil || len(tr.Input.Rows) == 0 || len(tr.Input.Rows) > contract.MaxDataImportRows {
		return contract.DataImportRequest{}, contract.ErrUnsupported
	}
	r := contract.DataImportRequest{Mode: tr.Mode}
	for _, row := range tr.Input.Rows {
		if len(row) != 1 || row[0].Kind != contract.JSON || row[0].Null || row[0].Default || row[0].Binary || len(row[0].Bytes) != 0 {
			return r, contract.ErrDataPage
		}
		r.Edits = append(r.Edits, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{s.documentImportColumn(t): row[0]}})
	}
	return r, nil
}

func (s *State) documentImportDescription(d *Document, mode string) string {
	if description := s.runtimeFor(d).ImportDescription; description != nil {
		return description(mode)
	}
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
	r, err := s.documentImportRequest(t)
	if err != nil {
		tr.Err = err.Error()
		return
	}
	tr.Submitted, tr.Step = true, 3
	ctx, out, request, gen := s.beginDataTransfer(t)
	profile, db, table := t.profileID, t.database, t.table
	description := s.documentImportDescription(t, r.Mode)
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
		tr.Err = s.importedRecordsMessage(t, "done")
		t.edit.Err = tr.Err
		s.appendDataHistory(t, mysqlDataHistoryEntry{Kind: "IMPORT", SQL: s.documentImportDescription(t, tr.Mode), Summary: true, At: time.Now(), Rows: saved.Applied, Message: locales.Text("已核验：数据更改已提交")})
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
	switch mode {
	case "insert_only":
		return locales.Text("仅插入新 ID 的点")
	case "update_only":
		return locales.Text("仅完整替换已有 ID 的点")
	case "upsert":
		return locales.Text("插入或完整替换同 ID 的点")
	}

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
						if s.pointImport(t) {
							ui.Text(c, locales.Text("单次最多 10000 个点、8 MiB，每个点最多 1 MiB；只提交一个原生批次，不自动分批或重试。"))
						} else {
							ui.Text(c, locales.Text("单次最多 10000 份文档、8 MiB，每份最多 1 MiB；一次提交一个任务，不自动分批或重试。"))
						}
						ui.Text(c, tr.Path).SingleLine()
						if ui.Button(c, locales.Text("选择文件")).Disabled(tr.Busy).Clicked() {
							s.chooseDataImportFile(c.Services(), t)
						}
						if tr.Input != nil {
							ui.Text(c, s.importedRecordsMessage(t, "read"))
						}
					case 1:
						mysqlDataWizardSelect(c, locales.Text("导入模式"), &tr.Mode, s.documentImportModes(t), documentImportModeLabel).Width(260)
						if s.pointImport(t) {
							ui.Text(c, locales.Text("插入模式要求 ID 尚不存在；更新模式要求 ID 已存在；upsert 可创建或完整替换。同 ID 点中未提供的 payload 或命名向量会被清除，不清空集合。"))
						} else {
							ui.Text(c, locales.Text("完整替换会删除同 ID 文档中未提供的字段；合并保留未提供字段，嵌套对象作为整个字段替换。两种模式均可创建新 ID，不清空索引。"))
						}
						if s.pointImport(t) {
							ui.Text(c, locales.Text(s.asyncWriteLabels(t).hint))
						} else {
							ui.Text(c, locales.Text("合并前读取原文档以核对结果；并发变化可能导致核验不通过，不能保证原子比较与交换。"))
						}
					case 2:
						target := locales.Text("目标 %s · %s · %d 份文档")
						if s.pointImport(t) {
							target = locales.Text("目标 %s · %s · %d 个点")
						}
						ui.Text(c, fmt.Sprintf(target, t.database, documentImportModeLabel(tr.Mode), tr.TotalRows)).Bold()
						if s.pointImport(t) {
							ui.Text(c, locales.Text("目标必须是已存在的物理集合；必须提供 uint64 或 UUID ID 和 vector；重复 ID、非法 JSON、维度或类型不符及超限请求会拒绝。"))
						} else {
							ui.Text(c, locales.Text("目标索引必须已存在并设置主键；缺失或重复 ID、非法 JSON 和超限请求会拒绝。"))
						}
						ui.Text(c, locales.Text(s.asyncWriteLabels(t).hint))
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
							ui.Text(c, s.importedRecordsMessage(t, "done"))
						}
						if s.pointImport(t) {
							ui.Text(c, locales.Text("核验会分段读取全部原点；批次未完成、点缺失或结果不符会保留源文件与证据，禁止重发。"))
						} else {
							ui.Text(c, locales.Text("观察成功任务后将逐份读取全部导入文档；失败或结果不符保留待核验状态，禁止重放。"))
						}
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
					if ui.Button(c, locales.Text(s.asyncWriteLabels(t).verify)).Disabled(!available || tr.Busy || t.edit.Busy).Clicked() {
						s.observeDocumentImport(c.Services(), t)
					}
					if ui.Button(c, locales.Text(s.asyncWriteLabels(t).evidence)).Disabled(t.edit.LastAsync == nil).Clicked() {
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
	if t.transfer.Mode == "insert_only" || t.transfer.Mode == "update_only" || t.transfer.Mode == "upsert" {
		if t.transfer.Completed {
			return fmt.Sprintf(locales.Text("已核验导入 %d 个点"), t.transfer.TotalRows)
		}
		return asyncDataWriteStatus(t.edit.Uncertain)
	}
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

func (s *State) documentImportModes(t *Document) []string {
	modes := s.runtimeFor(t).DocumentImportModes
	if modes == nil {
		return []string{"document_replace", "document_merge"}
	}
	return slices.Clone(modes)
}
func (s *State) documentImportColumn(t *Document) string {
	column := s.runtimeFor(t).DocumentImportColumn
	if column == "" {
		column = "document"
	}
	return column
}
func (s *State) pointImport(t *Document) bool { return s.runtimeFor(t).Selector.Dialect == "qdrant" }
func (s *State) importedRecordsMessage(t *Document, key string) string {
	if s.pointImport(t) {
		switch key {
		case "read":
			key = "已读取 %d 个点"
		case "done":
			key = "已核验导入 %d 个点"
		}
	} else {
		switch key {
		case "read":
			key = "已读取 %d 份文档"
		case "done":
			key = "已核验导入 %d 份文档"
		}
	}
	return fmt.Sprintf(locales.Text(key), t.transfer.TotalRows)
}
