package objectworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type DatabaseCreateEvent struct {
	Target     contract.DatabaseCreateTarget
	Generation uint64
	Outcome    contract.DatabaseCreateOutcome
}
type databaseCreateReply struct {
	snapshot contract.DatabaseCreateSnapshot
	outcome  *contract.DatabaseCreateOutcome
	err      error
}
type databaseCreateOperation struct {
	profile                               int64
	generation                            uint64
	name, err                             string
	open, busy, applying, cancelRequested bool
	target                                contract.DatabaseCreateTarget
	lease                                 contract.DatabaseCreateDocumentLease
	documentID                            string
	original                              *contract.DatabaseCreateSnapshot
	applied                               *contract.DatabaseCreateSnapshot
	outcome                               *contract.DatabaseCreateOutcome
	cancel                                context.CancelFunc
	results                               <-chan databaseCreateReply
	receiptText, receiptPreview           string
	transfer                              *schemaReceiptTransfer
	transferError                         string
}

func (s *State) databaseCreateTarget(profile int64, name string) contract.DatabaseCreateTarget {
	if name == "" || s.deps.DatabaseCreateTarget == nil {
		return contract.DatabaseCreateTarget{}
	}
	return s.deps.DatabaseCreateTarget(profile, name)
}

func (s *State) CanCreateDatabase(profile int64) bool {
	if s.deps.DatabaseCreateFor == nil || !s.sessionFor(profile).Connected {
		return false
	}
	p, err := s.deps.DatabaseCreateFor(profile)
	return err == nil && p != nil && p.CanCreateDatabase(s.databaseCreateTarget(profile, "new_database"))
}
func (s *State) BeginDatabaseCreate(profile int64) {
	if !s.CanCreateDatabase(profile) || s.session().ProfileID != profile {
		return
	}
	if s.databaseCreate != nil {
		s.report(locales.Text("请先关闭当前数据库创建窗口"))
		return
	}
	s.databaseCreate = &databaseCreateOperation{profile: profile, generation: s.session().Generation, name: "new_database", open: true}
}
func (s *State) databaseCreateValid(x *databaseCreateOperation) bool {
	w := s.session()
	return x != nil && w.ProfileID == x.profile && w.Connected && w.Generation == x.generation && x.target == s.databaseCreateTarget(x.profile, x.name)
}
func (s *State) closeDatabaseCreate() {
	x := s.databaseCreate
	s.databaseCreate = nil
	if x == nil {
		return
	}
	if x.cancel != nil {
		x.cancel()
	}
	if x.transfer != nil {
		x.transfer.cancel()
	}
	if x.lease != nil {
		_ = x.lease.Close()
	}
	x.results, x.original, x.applied, x.outcome, x.lease = nil, nil, nil, nil, nil
	x.receiptText, x.receiptPreview = "", ""
}
func (s *State) readDatabaseCreate(services ui.Services) {
	x := s.databaseCreate
	if x == nil || x.busy || !x.open || s.session().ProfileID != x.profile || !s.CanCreateDatabase(x.profile) {
		return
	}
	target := s.databaseCreateTarget(x.profile, x.name)
	p, err := s.deps.DatabaseCreateFor(x.profile)
	if err != nil || p == nil || !p.CanCreateDatabase(target) {
		x.err = locales.Text("数据库名称或原连接不可用")
		return
	}
	if x.lease == nil || target != x.target {
		if x.lease != nil {
			_ = x.lease.Close()
			x.lease = nil
		}
		id := s.nextSchemaDocumentID("database-create")
		lease, err := p.ReserveDatabaseCreateDocument(contract.DatabaseCreateDocumentRequest{DocumentID: id, Target: target})
		if err != nil {
			x.err = err.Error()
			return
		}
		x.lease, x.target, x.documentID = lease, target, id
	}
	x.generation = s.session().Generation
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan databaseCreateReply, 1)
	x.cancel, x.results, x.busy, x.err = cancel, ch, true, ""
	x.original = nil
	lease := x.lease
	go func() {
		defer cancel()
		v, e := lease.Snapshot(ctx)
		ch <- databaseCreateReply{snapshot: v, err: e}
		services.Invalidate()
	}()
}
func (s *State) applyDatabaseCreate(services ui.Services) {
	x := s.databaseCreate
	if x == nil || x.busy || x.lease == nil || x.original == nil || x.original.Exists || x.outcome != nil || !s.databaseCreateValid(x) {
		return
	}
	before := *x.original
	if before.Target != x.target || before.CreateSQL == "" {
		return
	}
	x.applied = &before
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan databaseCreateReply, 1)
	x.cancel, x.results, x.busy, x.applying, x.err = cancel, ch, true, true, ""
	lease := x.lease
	go func() {
		defer cancel()
		out := lease.Create(ctx, before).Clone()
		ch <- databaseCreateReply{outcome: &out}
		services.Invalidate()
	}()
}
func (s *State) consumeDatabaseCreate(services ui.Services) {
	x := s.databaseCreate
	if x == nil || s.session().ProfileID != x.profile {
		return
	}
	if x.busy && !s.databaseCreateValid(x) && x.cancel != nil {
		x.cancel()
	}
	if x.transfer != nil {
		select {
		case err := <-x.transfer.results:
			x.transfer.cancel()
			x.transfer = nil
			if err != nil {
				x.transferError = err.Error()
			}
		default:
		}
	}
	if x.results == nil {
		return
	}
	select {
	case r := <-x.results:
		x.results = nil
		x.busy, x.applying, x.cancelRequested = false, false, false
		if x.cancel != nil {
			x.cancel()
			x.cancel = nil
		}
		if r.outcome != nil {
			out := r.outcome.Clone()
			if out.Receipt != nil {
				v := out.Receipt
				if x.applied == nil || v.DocumentID != x.documentID || v.Original != *x.applied || v.SQL != x.applied.CreateSQL || v.State != out.State || v.Verified != out.Verified || !reflect.DeepEqual(v.Actual, out.Actual) {
					out.Verified, out.Actual = false, nil
					out.Error = errors.Join(out.Error, contract.ErrDatabaseCreateUnresolved)
				}
			} else if out.State != "not_executed" {
				out.Verified, out.Actual = false, nil
				out.Error = errors.Join(out.Error, contract.ErrDatabaseCreateUnresolved)
			}
			if !s.databaseCreateValid(x) {
				out.Verified, out.Actual = false, nil
				out.Error = errors.Join(out.Error, contract.ErrRevoked)
			}
			if out.Receipt != nil || out.State != "not_executed" {
				x.outcome = &out
				x.receiptText, x.receiptPreview = "", ""
			}
			if out.Error != nil {
				x.err = out.Error.Error()
			}
			if s.deps.DatabaseCreated != nil {
				s.deps.DatabaseCreated(services, DatabaseCreateEvent{Target: x.target, Generation: x.generation, Outcome: out.Clone()})
			}
		} else if r.err != nil {
			x.err = r.err.Error()
		} else if !s.databaseCreateValid(x) || r.snapshot.Target != x.target || r.snapshot.Revision == "" || r.snapshot.CreateSQL == "" {
			x.err = contract.ErrRevoked.Error()
		} else {
			snapshot := r.snapshot
			x.original = &snapshot
			if snapshot.Exists {
				x.err = locales.Text("目标数据库已存在")
			}
		}
	default:
	}
}
func (x *databaseCreateOperation) encodeReceipt() string {
	if x.outcome == nil || x.outcome.Receipt == nil {
		return ""
	}
	if x.receiptText != "" {
		return x.receiptText
	}
	out := x.outcome.Clone()
	message := ""
	if out.Error != nil {
		message = out.Error.Error()
	}
	raw, err := json.MarshalIndent(struct {
		Version     int
		Kind, State string
		Verified    bool
		Error       string
		Actual      *contract.DatabaseCreateSnapshot
		Receipt     *contract.DatabaseCreateReceipt
	}{1, "database-create-original-receipt", out.State, out.Verified, message, out.Actual, out.Receipt}, "", "  ")
	if err != nil {
		return ""
	}
	x.receiptText = string(raw) + "\n"
	end := min(len(x.receiptText), schemaReceiptPreviewLimit)
	for end > 0 && !utf8.ValidString(x.receiptText[:end]) {
		end--
	}
	x.receiptPreview = x.receiptText[:end]
	return x.receiptText
}
func (s *State) exportDatabaseCreateReceipt(services ui.Services, x *databaseCreateOperation) {
	if x == nil || x.transfer != nil || s.schemaReceiptPick == nil || s.schemaReceiptWrite == nil {
		return
	}
	raw := x.encodeReceipt()
	if raw == "" {
		return
	}
	pick, write := s.schemaReceiptPick, s.schemaReceiptWrite
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	x.transfer = &schemaReceiptTransfer{cancel: cancel, results: ch}
	x.transferError = ""
	go func() {
		path, err := pick("database-create-original-receipt.json")
		if err == nil && path != "" {
			err = ctx.Err()
			if err == nil {
				err = write(path, func(w io.Writer) error {
					if e := ctx.Err(); e != nil {
						return e
					}
					_, e := io.WriteString(w, raw)
					if e != nil {
						return e
					}
					return ctx.Err()
				})
			}
		}
		ch <- err
		services.Invalidate()
	}()
}
func (s *State) databaseCreateModal(c *ui.Context) {
	x := s.databaseCreate
	if x == nil || !x.open {
		return
	}
	active := s.session().ProfileID == x.profile
	if s.deps.DatabaseCreateProfileActive != nil {
		active = s.deps.DatabaseCreateProfileActive(x.profile)
	}
	if !active {
		return
	}
	theme.Modal(c, &x.open, func() {
		ui.Column(c).Width(800).MaxWidthPercent(92).Height(620).MaxHeightPercent(90).Gap(8).Children(func() {
			ui.Text(c, locales.Text("新建数据库")).Bold()
			ui.Text(c, fmt.Sprintf("#%d · %s", x.profile, x.name)).SingleLine()
			if !s.sessionFor(x.profile).Connected && s.deps.DatabaseCreateConnection != nil {
				ui.Text(c, locales.Text("恢复原连接"))
				s.deps.DatabaseCreateConnection(c.Key("database-create-connection"), x.profile)
			}
			if x.outcome == nil {
				if ui.TextInput(c, &x.name).Label(locales.Text("数据库名称")).Disabled(x.busy).Changed() {
					x.original = nil
					x.err = ""
				}
			}
			ui.Text(c, locales.Text("创建数据库并核验完整定义。检查不锁定并发更改；原始回执丢失时禁止重放，取消不表示回滚。"))
			if x.err != "" {
				ui.Text(c, x.err).SingleLine().Tooltip(x.err)
			}
			if x.busy {
				ui.Text(c, locales.Text("等待原会话操作回执…"))
			}
			if raw := x.encodeReceipt(); raw != "" {
				if x.outcome.Verified {
					ui.Text(c, locales.Text("原始创建已完成"))
				}
				ui.Text(c, locales.Text("原始写入回执独立保留；后续读取不会改写它，也不会重放操作。"))
				ui.Row(c).Gap(8).Wrap().Children(func() {
					if ui.Button(c, locales.Text("复制完整回执")).Clicked() {
						c.WriteClipboard(raw)
					}
					if ui.Button(c, locales.Text("导出完整回执")).Disabled(x.transfer != nil || s.schemaReceiptPick == nil || s.schemaReceiptWrite == nil).Clicked() {
						s.exportDatabaseCreateReceipt(c.Services(), x)
					}
					if x.transfer != nil && ui.Button(c, locales.Text("取消导出")).Clicked() {
						x.transfer.cancel()
					}
				})
				if len(x.receiptPreview) < len(raw) {
					ui.Text(c, locales.Text("回执预览已截断；可复制或导出完整内容"))
				}
				if x.transferError != "" {
					ui.Text(c, x.transferError).SingleLine().Tooltip(x.transferError)
				}
				preview := x.receiptPreview
				area := ui.TextArea(c.Key("database-create-receipt/"+x.outcome.Receipt.DocumentID), &preview).Label(locales.Text("原始数据库创建回执")).ReadOnly(true).Grow(1).MinHeight(40)
				seen := ui.Local(area, "database-create-receipt-text", func() string { return "" })
				if *seen != preview {
					area.SetTextSelection(0, 0)
					*seen = preview
				}
			} else {
				preview := ""
				if x.original != nil {
					preview = x.original.CreateSQL
				}
				ui.TextArea(c, &preview).Label(locales.Text("数据库创建 SQL 预览")).ReadOnly(true).Grow(1).MinHeight(40)
			}
			ui.Row(c).Wrap().Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("关闭")).Disabled(x.busy).Clicked() {
					s.closeDatabaseCreate()
				}
				if x.busy && ui.Button(c, locales.Text("取消操作")).Disabled(x.cancelRequested).Clicked() {
					x.cancelRequested = true
					if x.cancel != nil {
						x.cancel()
					}
				}
				if ui.Button(c, locales.Text("重新读取数据库")).Disabled(x.busy || !s.CanCreateDatabase(x.profile)).Clicked() {
					s.readDatabaseCreate(c.Services())
				}
				if ui.PrimaryButton(c, locales.Text("确认创建数据库")).Disabled(x.busy || x.outcome != nil || x.original == nil || x.original.Exists || !s.databaseCreateValid(x)).Clicked() {
					s.applyDatabaseCreate(c.Services())
				}
			})
		})
	})
	if !x.open {
		if x.busy {
			x.open = true
			x.cancelRequested = true
			if x.cancel != nil {
				x.cancel()
			}
		} else {
			s.closeDatabaseCreate()
		}
	}
}

func (s *State) DatabaseCreatePending() bool { return s.databaseCreate != nil && s.databaseCreate.busy }
func (s *State) DatabaseCreateDialogOpen() bool {
	return s.databaseCreate != nil && s.databaseCreate.open
}
func (s *State) DatabaseCreateError() string {
	if s.databaseCreate != nil {
		return s.databaseCreate.err
	}
	return ""
}
func (s *State) DatabaseCreateOriginalOutcome() *contract.DatabaseCreateOutcome {
	if s.databaseCreate == nil || s.databaseCreate.outcome == nil {
		return nil
	}
	out := s.databaseCreate.outcome.Clone()
	return &out
}
