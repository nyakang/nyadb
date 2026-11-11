package dataworkspace

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// A prefix operation owns its complete candidate set separately from visible
// grid rows. It cannot become an ordinary row save or a replacement-profile job.
type dataKeyPrefixDeleteState struct {
	page                         *contract.DataPage
	object                       contract.ObjectRef
	generation, requestID        uint64
	drafts                       draftSnapshot
	prefix                       string
	request                      contract.DataKeyPrefixDelete
	open, previewOpen, uncertain bool
	stopping                     bool
	cancel                       context.CancelFunc
	ticket                       *dataMutationTicket
	previewResults               <-chan error
	results                      <-chan mysqlEditResult
	plan                         contract.DataMutationPlan
	snapshot                     contract.DataMutationSnapshot
	submit                       func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error)
}

func (s *State) canDeleteKeyPrefix(d *Document) bool {
	return d != nil && !d.closed && d.page != nil && d.object.ProfileID == d.profileID && d.profileID > 0 && d.editable() && d.capabilities.KeyValues && !d.Busy() && !d.HasDraft() && d.edit.Uncertain == nil && !d.transfer.Uncertain && !dataTransactionUnavailable(d) && !dataTransactionActive(d) && s.canUseSession(d) && s.session().ProfileID == d.profileID && s.runtimeFor(d).PreviewKeyPrefixDelete != nil && s.runtimeFor(d).SubmitEdits != nil
}

// DeleteKeyPrefix opens the original prefix input without reading or deleting.
func (s *State) DeleteKeyPrefix(d *Document) {
	if !s.canDeleteKeyPrefix(d) {
		return
	}
	s.finishMySQLCellEdit(d, false)
	drafts := captureDraft(d)
	drafts.Row = -1
	d.prefixDelete = &dataKeyPrefixDeleteState{page: d.page, object: d.object, generation: s.sessionFor(d).Generation, drafts: drafts, open: true}
	d.edit.Busy, d.edit.Err = true, ""
}

func (s *State) validKeyPrefixDelete(d *Document, m *dataKeyPrefixDeleteState) bool {
	if m == nil || d.closed || s.documents[d.id] != d || d.prefixDelete != m || d.page != m.page || d.object != m.object || d.profileID != m.object.ProfileID || !s.canUseSession(d) || s.sessionFor(d).Generation != m.generation || s.runtimeFor(d).PreviewKeyPrefixDelete == nil || s.runtimeFor(d).SubmitEdits == nil {
		return false
	}
	if m.previewOpen && m.prefix != m.request.Prefix {
		return false
	}
	drafts := captureDraft(d)
	drafts.Row = -1
	return reflect.DeepEqual(drafts, m.drafts)
}
func validKeyPrefixInput(prefix string) bool {
	return prefix != "" && len(prefix) <= 4096 && utf8.ValidString(prefix) && !strings.ContainsRune(prefix, 0)
}

func (s *State) disposeKeyPrefixDelete(d *Document) {
	m := d.prefixDelete
	if m == nil {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	if m.ticket != nil {
		m.ticket.revoke()
	}
	if m.plan != nil {
		_ = m.plan.Close()
	}
	d.prefixDelete = nil
}
func (s *State) cancelKeyPrefixDelete(d *Document) {
	m := d.prefixDelete
	if m == nil || m.uncertain {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	if m.previewResults != nil || m.results != nil {
		m.stopping = true
		if m.ticket != nil {
			m.ticket.revoke()
		}
		m.open, m.previewOpen = false, false
		return // The original executor must acknowledge cancellation.
	}
	s.disposeKeyPrefixDelete(d)
	d.edit.Busy = false
	s.clearDataMutationContinuation(d)
}
func (s *State) cancelKeyPrefixConfirmation(d *Document) {
	m := d.prefixDelete
	if m == nil || m.results != nil {
		return
	}
	if m.plan != nil {
		_ = m.plan.Close()
		m.plan = nil
	}
	if m.ticket != nil {
		m.ticket.revoke()
		m.ticket = nil
	}
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.previewOpen, m.open = false, true
	d.edit.Busy = true
}

func (s *State) previewKeyPrefixDelete(services ui.Services, d *Document) {
	m := d.prefixDelete
	if !s.validKeyPrefixDelete(d, m) || !validKeyPrefixInput(m.prefix) || m.previewResults != nil || m.results != nil || m.uncertain {
		return
	}
	request := contract.DataKeyPrefixDelete{Object: m.object, Prefix: m.prefix}
	runtime := s.runtimeFor(d)
	d.edit.Request++
	m.requestID = d.edit.Request
	m.request = request
	m.open = false
	m.previewOpen = false
	m.snapshot = contract.DataMutationSnapshot{}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ticket := &dataMutationTicket{}
	m.ticket = ticket
	out := make(chan error, 1)
	m.previewResults = out
	preview, id, db, table := runtime.PreviewKeyPrefixDelete, d.profileID, d.database, d.table
	m.submit = runtime.SubmitEdits
	d.edit.Busy, d.edit.Err = true, ""
	go func() {
		plan, err := preview(ctx, id, db, table, request)
		ticket.publish(plan)
		if err == nil {
			err = ctx.Err()
		}
		out <- err
		services.Invalidate()
	}()
}

func validKeyPrefixSnapshot(request contract.DataKeyPrefixDelete, snapshot contract.DataMutationSnapshot) bool {
	if snapshot.Object != request.Object || snapshot.KeyPrefixDelete == nil || *snapshot.KeyPrefixDelete != request || snapshot.NativeJSON == "" || len(snapshot.Edits) == 0 || len(snapshot.Edits) != len(snapshot.Items) {
		return false
	}
	seen := map[string]bool{}
	for i, e := range snapshot.Edits {
		item := snapshot.Items[i]
		if item.EntityID == "" || !strings.HasPrefix(item.EntityID, request.Prefix) || seen[item.EntityID] || e.Original == nil || len(e.Original.Ref.TypedValues) != 1 || e.Original.Ref.TypedValues[0].Kind != contract.Text || e.Original.Ref.TypedValues[0].Truncated || e.Original.Ref.TypedValues[0].Text != item.EntityID || len(e.Original.Ref.Columns) != 1 || e.Original.Ref.Columns[0] == "" || len(e.Original.Ref.Values) != 1 {
			return false
		}
		seen[item.EntityID] = true
		if e.Kind != "delete" || e.Original == nil || e.Original.Ref.Object != request.Object || len(e.Values) != 0 || e.GraphEndpoints != nil || snapshot.Items[i].Kind != "delete" || snapshot.Items[i].Blocked {
			return false
		}
	}
	return true
}

func (s *State) confirmKeyPrefixDelete(services ui.Services, d *Document) {
	m := d.prefixDelete
	if m == nil || !m.previewOpen || m.plan == nil || !s.validKeyPrefixDelete(d, m) || d.edit.Request != m.requestID || m.prefix != m.request.Prefix || !reflect.DeepEqual(m.plan.Snapshot(), m.snapshot) {
		return
	}
	plan, submit := m.plan, m.submit
	m.plan = nil
	snapshot := contract.CloneDataMutationSnapshot(m.snapshot)
	id, db, table, gen, request := d.profileID, d.database, d.table, m.generation, m.requestID
	m.previewOpen = false
	ctx, cancel := context.WithCancel(context.Background())
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = cancel
	out := make(chan mysqlEditResult, 1)
	m.results = out
	history := mysqlDataWriteHistoryRequest(db, table, snapshot.Edits, false)
	for i := range history {
		history[i].SQL, history[i].Summary = locales.Text("已确认的前缀候选键删除"), true
	}
	complete := s.beginActivity(d, "data_write", dataActivitySQL(history))
	go func() {
		started := time.Now()
		saved, err := submit(ctx, id, db, table, plan)
		_ = plan.Close()
		contract.CompleteActivity(complete, dataMutationActivityOutcome(err, snapshot.Object, snapshot.Edits, saved))
		for i := range history {
			history[i].At, history[i].Duration = time.Now(), time.Since(started)
		}
		out <- mysqlEditResult{History: history, Generation: gen, Request: request, Profile: id, MutationEdits: snapshot.Edits, Saved: saved, Err: err}
		services.Invalidate()
	}()
}

func (s *State) consumeKeyPrefixDelete(services ui.Services, d *Document) {
	m := d.prefixDelete
	if m == nil {
		return
	}
	if m.previewResults != nil {
		select {
		case err := <-m.previewResults:
			m.previewResults = nil
			m.plan = m.ticket.take()
			if err == nil && (!s.validKeyPrefixDelete(d, m) || m.requestID != d.edit.Request || m.prefix != m.request.Prefix) {
				err = contract.ErrRevoked
			}
			if err == nil && m.plan == nil {
				err = contract.ErrUnsupported
			}
			if err == nil {
				snapshot := m.plan.Snapshot()
				if !validKeyPrefixSnapshot(m.request, snapshot) {
					err = contract.ErrDataConflict
				} else {
					m.snapshot = contract.CloneDataMutationSnapshot(snapshot)
					d.prefixEvidence.JSON = m.snapshot.NativeJSON
					d.prefixEvidenceRequest = m.request
				}
			}
			if err != nil {
				s.disposeKeyPrefixDelete(d)
				d.edit.Busy = false
				d.edit.Err = err.Error()
				s.clearDataMutationContinuation(d)
			} else {
				m.previewOpen = true
			}
		default:
		}
		return
	}
	if m.results != nil {
		select {
		case r := <-m.results:
			m.results = nil
			if m.cancel != nil {
				m.cancel()
				m.cancel = nil
			}
			complete := r.Err == nil && r.Saved != nil && r.Saved.CommitConfirmed && !r.Saved.CommitUncertain && !r.Saved.Incomplete && r.Saved.Applied == len(m.snapshot.Edits) && dataMutationReceiptComplete(m.object, m.snapshot.Edits, r.Saved)
			original := s.validKeyPrefixDelete(d, m) && r.Generation == s.sessionFor(d).Generation && r.Profile == d.profileID && r.Request == d.edit.Request
			if r.Saved != nil && r.Saved.NativeJSON != "" {
				d.nativeReceipt.JSON = r.Saved.NativeJSON
			}
			historyErr := r.Err
			if historyErr == nil && !complete {
				historyErr = errors.New(locales.Text("提交结果不完整，请核验数据库"))
			}
			s.recordDataWriteHistory(d, r.History, r.Saved, historyErr, !original)
			d.edit.Busy = false
			if !original || errors.Is(r.Err, contract.ErrDataCommitUncertain) || r.Saved != nil && (r.Saved.CommitUncertain || r.Saved.Incomplete || r.Saved.CommitConfirmed && !complete) || r.Err == nil && !complete {
				m.uncertain = true
				m.open, m.previewOpen = false, false
				d.edit.Uncertain = cloneEditResult(r.Saved)
				if d.edit.Uncertain == nil {
					d.edit.Uncertain = &contract.DataEditResult{}
				}
				d.edit.Uncertain.CommitUncertain = true
				d.edit.Err = locales.Text("提交结果仍不确定，禁止重放；请检查数据库")
				if r.Saved != nil && r.Saved.NativeJSON != "" {
					d.nativeReceipt.Open = true
				}
				return
			}
			if r.Err != nil {
				if m.stopping {
					s.disposeKeyPrefixDelete(d)
					s.clearDataMutationContinuation(d)
					d.edit.Err = r.Err.Error()
					return
				}
				m.open = true
				d.edit.Busy = true
				d.edit.Err = r.Err.Error()
				return
			}
			s.disposeKeyPrefixDelete(d)
			d.edit.Err = ""
			s.refreshDataAfterWrite(services, d)
		default:
		}
		return
	}
	if !m.uncertain && !s.validKeyPrefixDelete(d, m) {
		s.disposeKeyPrefixDelete(d)
		d.edit.Busy = false
		d.edit.Err = locales.Text("原数据库会话已失效，请刷新表数据")
	}
}

func (s *State) keyPrefixDeleteModal(c *ui.Context, d *Document) {
	m := d.prefixDelete
	if m == nil || !s.consumeFor(d) {
		return
	}
	if m.open {
		theme.Modal(c, &m.open, func() {
			ui.Column(c).Width(640).MaxWidthPercent(95).Gap(8).Children(func() {
				ui.Text(c, locales.Text("按前缀删除候选键")).FontSize(theme.Font(c, theme.FontSize))
				ui.Text(c, d.database+" · "+d.table)
				ui.Text(c, locales.Text("读取此完整前缀下的候选键并确认后删除。仅删除此次捕获的键，预览后新增的键保留。"))
				ui.Text(c, locales.Text("完整前缀"))
				input := ui.TextInput(c, &m.prefix).Label(locales.Text("输入完整删除前缀")).FillWidth()
				theme.BindNativeEdit(c, input, fmt.Sprintf("data/key-prefix-delete/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return !d.closed && d.prefixDelete == m && m.open }, true, false)
				if d.edit.Err != "" {
					ui.Text(c, d.edit.Err).TextColor(theme.Colors(c).Error)
				}
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("取消")).Clicked() {
						m.open = false
					}
					if ui.Button(c, locales.Text("预览候选键删除")).Disabled(!validKeyPrefixInput(m.prefix)).Clicked() {
						s.previewKeyPrefixDelete(c.Services(), d)
					}
				})
			})
		})
		if !m.open && m.previewResults == nil && m.results == nil && !m.previewOpen && !m.uncertain {
			s.cancelKeyPrefixDelete(d)
		}
	}
	if m.previewOpen {
		theme.Modal(c, &m.previewOpen, func() {
			ui.Column(c).Width(720).MaxWidthPercent(95).Gap(8).Children(func() {
				ui.Text(c, locales.Text("确认候选键删除")).FontSize(theme.Font(c, theme.FontSize))
				ui.Text(c, m.request.Prefix).FillWidth().SingleLine().MinWidth(0).Tooltip(m.request.Prefix)
				ui.Text(c, fmt.Sprintf(locales.Text("将原子删除此次捕获的 %d 个键；之后新增的键保留，候选键变化会取消提交。"), len(m.snapshot.Edits)))
				ui.Scroll(c).Height(260).FillWidth().Children(func() {
					for _, item := range m.snapshot.Items {
						ui.Text(c, locales.Text("删除键")+" · "+item.EntityID)
					}
					ui.Text(c, dataMutationPreview(m.snapshot.NativeJSON)).Label(locales.Text("候选键删除完整预览"))
				})
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("复制完整预览")).Clicked() {
						c.WriteClipboard(m.snapshot.NativeJSON)
					}
					if ui.Button(c, locales.Text("取消")).Clicked() {
						m.previewOpen = false
					}
					if ui.Button(c, locales.Text("确认并删除候选键")).Clicked() {
						s.confirmKeyPrefixDelete(c.Services(), d)
					}
				})
			})
		})
		if !m.previewOpen && m.plan != nil && m.results == nil {
			s.cancelKeyPrefixConfirmation(d)
		}
	}
}

func (s *State) keyPrefixEvidenceActions(c *ui.Context, d *Document) {
	if d != nil && !d.closed && d.prefixEvidence.JSON != "" && theme.IconButton(c, "", "info").Label(locales.Text("前缀候选原始观察")).Tooltip(locales.Text("前缀候选原始观察")).Size(theme.RowHeight(c, 26), theme.RowHeight(c, theme.ControlHeight)).Padding(0).Clicked() {
		d.prefixEvidence.Open = true
	}
}
func (s *State) keyPrefixEvidenceModal(c *ui.Context, d *Document) {
	if d.closed || !d.prefixEvidence.Open || d.prefixEvidence.JSON == "" {
		return
	}
	original := d.prefixEvidence.JSON
	request := d.prefixEvidenceRequest
	theme.Modal(c, &d.prefixEvidence.Open, func() {
		ui.Column(c).Width(720).MaxWidthPercent(95).Gap(8).Children(func() {
			ui.Text(c, locales.Text("前缀候选原始观察")).FontSize(theme.Font(c, theme.FontSize))
			ui.Text(c, request.Prefix).FillWidth().SingleLine().MinWidth(0).Tooltip(request.Prefix)
			ui.Text(c, locales.Text("保留原输入、完整候选观察及计划；此观察不确认写入，写入结果以原事务回执为准。"))
			ui.Scroll(c).Height(280).FillWidth().Children(func() { ui.Text(c, dataMutationPreview(original)).Label(locales.Text("候选键删除完整预览")) })
			if d.prefixEvidence.Err != "" {
				ui.Text(c, d.prefixEvidence.Err).TextColor(theme.Colors(c).Error)
			}
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("复制完整观察")).Clicked() {
					c.WriteClipboard(original)
				}
				if ui.Button(c, locales.Text("导出完整观察")).Disabled(d.prefixEvidence.Results != nil).Clicked() {
					s.exportKeyPrefixEvidence(c.Services(), d, mygo.Dialog.Save)
				}
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					d.prefixEvidence.Open = false
				}
			})
		})
	})
}
func (s *State) exportKeyPrefixEvidence(services ui.Services, d *Document, picker func(mygo.SaveDialogOptions) (string, error)) {
	if d == nil {
		return
	}
	s.exportDataNativeEvidence(services, d, &d.prefixEvidence, picker, "导出完整观察", "key-prefix-observation.json")
}
