package dataworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

// GuardLeave defers disposal until the user chooses an explicit draft and
// transaction outcome. Save/commit/cancel wait for database acknowledgement.
func (s *EngineState) GuardLeave(d *EngineDocument, next func(ui.Services)) bool {
	if d == nil || d.closed.Load() || !d.Busy && !d.HasDraft() && !d.HasTransaction() && !d.Uncertain {
		return false
	}
	s.guardDoc, s.guardNext, s.guardOpen = d, next, true
	return true
}
func (s *EngineState) Modals(c *ui.Context) {
	d := s.guardDoc
	if d == nil || d.closed.Load() {
		return
	}
	ui.Modal(c, &s.guardOpen, func() {
		ui.Column(c).Width(460).Padding(16).Gap(10).Children(func() {
			ui.Text(c, locales.Text("关闭数据页")).Bold()
			if d.Busy {
				ui.Text(c, locales.Text("数据库请求尚未回执；取消后仍需等待执行器确认"))
				if ui.Button(c, locales.Text("取消请求")).Disabled(d.CancelRequested).Clicked() {
					s.Cancel(d)
				}
			} else {
				if d.Uncertain {
					ui.Text(c, locales.Text("提交结果不确定；关闭会放弃此页保留的修改。不会重放SQL。"))
				}
				if d.HasTransaction() {
					ui.Text(c, locales.Text("此页有未结束事务"))
					if ui.Button(c, locales.Text("保存并提交后关闭")).Disabled(d.Uncertain || d.writeBlocked).Clicked() {
						if d.HasDraft() {
							s.guardOp = "commitAfterSave"
							s.Save(d)
						} else {
							s.guardOp = "closeAfterCommit"
							s.Commit(d)
						}
					}
					if ui.Button(c, locales.Text("回滚并关闭")).Disabled(d.Uncertain).Clicked() {
						s.guardOp = "closeAfterRollback"
						s.Rollback(d)
					}
				} else {
					if ui.Button(c, locales.Text("保存后关闭")).Disabled(!d.HasDraft() || d.Uncertain || d.writeBlocked).Clicked() {
						s.guardOp = "closeAfterSave"
						s.Save(d)
					}
					if ui.Button(c, locales.Text("放弃修改并关闭")).Clicked() {
						d.drafts = nil
						d.insert = nil
						s.finishGuard(c.Services())
					}
				}
			}
			if ui.Button(c, locales.Text("取消关闭")).Clicked() {
				s.guardDoc, s.guardNext, s.guardOp = nil, nil, ""
				s.guardOpen = false
			}
		})
	})
}
func (s *EngineState) finishGuard(services ui.Services) {
	next := s.guardNext
	s.guardDoc, s.guardNext, s.guardOp = nil, nil, ""
	s.guardOpen = false
	if next != nil {
		next(services)
	}
}
