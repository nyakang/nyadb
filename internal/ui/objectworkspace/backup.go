package objectworkspace

import (
	"context"
	"fmt"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	"io"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/backups"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type BackupRuntime interface {
	SessionGeneration(int64) uint64
	BackupMySQL(context.Context, int64, mysqlsvc.BackupRequest, io.Writer) (backups.Manifest, error)
	DumpMySQL(context.Context, int64, mysqlsvc.BackupRequest, io.Writer) error
	PlanRestore(context.Context, int64, uint64, string, string, *backups.Reader) (mysqlsvc.RestorePlan, error)
	RestoreMySQL(context.Context, mysqlsvc.RestorePlan) mysqlsvc.DDLOutcome
}
type BackupFiles struct {
	Open  func() (string, error)
	Save  func(string) (string, error)
	Write func(string, func(io.Writer) error) error
}
type backupOperation struct {
	profile                                            int64
	generation, runtimeGeneration                      uint64
	database, table, action, target, policy, path, err string
	open, busy, cancelRequested, data                  bool
	reader                                             *backups.Reader
	plan                                               *mysqlsvc.RestorePlan
	outcome                                            *mysqlsvc.DDLOutcome
	cancel                                             context.CancelFunc
	result                                             <-chan backupReceipt
}
type backupReceipt struct {
	path    string
	reader  *backups.Reader
	plan    *mysqlsvc.RestorePlan
	outcome *mysqlsvc.DDLOutcome
	err     error
}

func (s *State) SetBackupServices(runtime func() BackupRuntime, files BackupFiles) {
	s.backupRuntime, s.backupFiles = runtime, files
}
func (s *State) BeginBackup(services ui.Services, profile int64, database, table, action string) {
	if !s.capabilities(profile).Backup {
		return
	}
	if s.backup != nil && s.backup.busy {
		return
	}
	session := s.sessionFor(profile)
	if !session.Connected || s.backupRuntime == nil || s.backupRuntime() == nil {
		return
	}
	s.closeBackup()
	s.backup = &backupOperation{profile: profile, generation: session.Generation, runtimeGeneration: s.backupRuntime().SessionGeneration(profile), database: database, target: database, table: table, action: action, policy: "stop", open: true, data: action != "sql_structure"}
}
func (s *State) closeBackup() {
	op := s.backup
	if op == nil {
		return
	}
	if op.cancel != nil {
		op.cancel()
	}
	if op.busy && op.result != nil {
		receipt, reader := op.result, op.reader
		go func() {
			result := <-receipt
			if result.reader != nil {
				_ = result.reader.Close()
			}
			if reader != nil && reader != result.reader {
				_ = reader.Close()
			}
		}()
	} else if op.reader != nil {
		_ = op.reader.Close()
	}
	s.backup = nil
}
func (s *State) startBackup(services ui.Services, op *backupOperation) {
	if op == nil || op.busy || s.backupRuntime == nil {
		return
	}
	runtime := s.backupRuntime()
	if runtime == nil || runtime.SessionGeneration(op.profile) != op.runtimeGeneration {
		op.err = "原连接已失效"
		return
	}
	files := s.backupFiles
	profile, db, table, action, data, gen := op.profile, op.database, op.table, op.action, op.data, op.runtimeGeneration
	ctx, cancel := context.WithCancel(context.Background())
	op.cancel = cancel
	op.busy = true
	op.err = ""
	ch := make(chan backupReceipt, 1)
	op.result = ch
	go func() {
		var e error
		var path string
		if files.Save == nil || files.Write == nil {
			e = fmt.Errorf("文件接口不可用")
		} else {
			extension := ".nyabackup"
			if strings.HasPrefix(action, "sql_") {
				extension = ".sql"
			}
			path, e = files.Save(db + extension)
			if e == nil && path != "" {
				e = files.Write(path, func(w io.Writer) error {
					request := mysqlsvc.BackupRequest{Database: db, Generation: gen, Data: data}
					if table != "" {
						request.Tables = []string{table}
					}
					if strings.HasPrefix(action, "sql_") {
						return runtime.DumpMySQL(ctx, profile, request, w)
					}
					_, e := runtime.BackupMySQL(ctx, profile, request, w)
					return e
				})
			} else if e == nil {
				e = context.Canceled
			}
		}
		ch <- backupReceipt{path: path, err: e}
		services.Invalidate()
	}()
}
func (s *State) previewRestore(services ui.Services, op *backupOperation) {
	if op.busy || s.backupRuntime == nil {
		return
	}
	runtime := s.backupRuntime()
	if runtime == nil {
		return
	}
	profile, gen, target, policy, old, path := op.profile, op.runtimeGeneration, op.target, op.policy, op.reader, op.path
	files := s.backupFiles
	op.plan = nil
	op.err = ""
	op.busy = true
	ctx, cancel := context.WithCancel(context.Background())
	op.cancel = cancel
	ch := make(chan backupReceipt, 1)
	op.result = ch
	go func() {
		reader := old
		var e error
		if reader == nil {
			if files.Open == nil {
				e = fmt.Errorf("文件接口不可用")
			} else {
				path, e = files.Open()
			}
			if e == nil && path != "" {
				reader, e = backups.Open(ctx, path)
			} else if e == nil {
				e = context.Canceled
			}
		}
		var plan mysqlsvc.RestorePlan
		if e == nil {
			plan, e = runtime.PlanRestore(ctx, profile, gen, target, policy, reader)
		}
		receipt := backupReceipt{path: path, reader: reader, err: e}
		if e == nil {
			receipt.plan = &plan
		}
		if ctx.Err() != nil && reader != nil && reader != old {
			_ = reader.Close()
			receipt.reader = nil
		}
		ch <- receipt
		services.Invalidate()
	}()
}
func (s *State) executeRestore(services ui.Services, op *backupOperation) {
	if op.busy || op.plan == nil || s.backupRuntime == nil {
		return
	}
	// Coordinate all data drafts/transactions in the original target database.
	if s.deps.BeforeDDL != nil && s.deps.BeforeDDL(services, op.profile, op.target, "", func(next ui.Services) {
		if s.backup == op {
			s.executeRestore(next, op)
		}
	}) {
		return
	}
	runtime := s.backupRuntime()
	if runtime == nil || op.plan.Database != op.target {
		return
	}
	reader := op.reader
	plan := *op.plan
	plan.Steps = append([]mysqlsvc.RestoreStep(nil), plan.Steps...)
	plan.Fingerprints = cloneStringMap(plan.Fingerprints)
	ctx, cancel := context.WithCancel(context.Background())
	op.cancel = cancel
	op.busy = true
	op.plan = nil
	op.err = ""
	ch := make(chan backupReceipt, 1)
	op.result = ch
	go func() {
		out := runtime.RestoreMySQL(ctx, plan)
		ch <- backupReceipt{reader: reader, outcome: &out, err: out.Error}
		services.Invalidate()
	}()
}
func cloneStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
func (s *State) consumeBackup(services ui.Services) {
	op := s.backup
	if op == nil || op.result == nil {
		return
	}
	select {
	case receipt := <-op.result:
		op.busy = false
		op.cancelRequested = false
		op.result = nil
		if op.cancel != nil {
			op.cancel()
			op.cancel = nil
		}
		if receipt.reader != nil {
			op.reader = receipt.reader
		}
		if receipt.path != "" {
			op.path = receipt.path
		}
		op.plan, op.outcome = receipt.plan, receipt.outcome
		if receipt.err != nil {
			op.err = receipt.err.Error()
		} else if op.action != "restore" {
			op.err = "备份已完成：" + op.path
		}
		if receipt.outcome != nil && receipt.outcome.State != "not_executed" && s.deps.ObjectChanged != nil {
			s.deps.ObjectChanged(services, ObjectEvent{ProfileID: op.profile, Generation: op.generation, Ref: mysqlsvc.ObjectIdentity{Database: op.target, Kind: "database"}, Action: "restore", Outcome: mysqlsvc.ObjectOutcome{DDLOutcome: *receipt.outcome}})
		}
		if !op.open {
			s.closeBackup()
		}
	default:
	}
}
func (s *State) backupModal(c *ui.Context) {
	op := s.backup
	if op == nil || !op.open {
		return
	}
	session := s.sessionFor(op.profile)
	if op.plan != nil && op.plan.Database != op.target {
		op.plan = nil
	}
	if session.ProfileID != s.session().ProfileID {
		return
	}
	title := locales.Text("备份与 SQL 转储")
	if op.action == "restore" {
		title = locales.Text("恢复 NyaDB 备份")
	}
	ui.Modal(c, &op.open, func() {
		width, height := c.Size()
		ui.Column(c).Width(min(float32(780), width-40)).Height(min(float32(740), height-40)).Background(theme.Colors(c).Header).Children(func() {
			ui.Text(c, title).Bold()
			ui.Scroll(c).Grow(1).FillWidth().Children(func() {
				ui.Column(c).Padding(12).Gap(8).Children(func() {
					ui.Text(c, fmt.Sprintf("连接 #%d · 数据库 %s", op.profile, op.database))
					if op.action == "restore" {
						ui.TextInput(c, &op.target).Label(locales.Text("目标数据库")).Disabled(op.busy || op.outcome != nil)
						ui.Row(c).Gap(8).Children(func() {
							if ui.Button(c, locales.Text("遇到已有对象停止")).Disabled(op.busy || op.outcome != nil).Clicked() {
								op.policy = "stop"
								op.plan = nil
							}
							if ui.Button(c, locales.Text("替换已有对象")).Disabled(op.busy || op.outcome != nil).Clicked() {
								op.policy = "replace"
								op.plan = nil
							}
							ui.Text(c, "冲突策略："+op.policy)
						})
						if ui.Button(c, locales.Text("读取备份并生成恢复预览")).Disabled(op.busy || op.outcome != nil || !session.Connected).Clicked() {
							s.previewRestore(c.Services(), op)
						}
						if op.reader != nil {
							manifest := op.reader.Manifest()
							ui.Text(c, fmt.Sprintf("来源 %s · %d 个对象", manifest.Database, len(manifest.Objects)))
							for _, warning := range manifest.Warnings {
								ui.Text(c, warning).FontSize(11)
							}
						}
						if op.plan != nil {
							ui.Text(c, locales.Text("DDL 不保证回滚；已完成或结果不确定的步骤不会自动重放。"))
							ui.Scroll(c).MaxHeight(300).Children(func() {
								for _, step := range op.plan.Steps {
									ui.Text(c, fmt.Sprintf("%s %s · %s", step.Kind, step.Name, step.SQL)).Selectable()
								}
							})
							if ui.Button(c, locales.Text("确认按预览恢复")).Disabled(op.busy || !session.Connected || session.Generation != op.generation).Clicked() {
								s.executeRestore(c.Services(), op)
							}
						}
					} else {
						ui.Checkbox(c, &op.data, locales.Text("包含表数据")).Disabled(op.busy)
						if ui.Button(c, locales.Text("选择文件并开始备份")).Disabled(op.busy || !session.Connected || session.Generation != op.generation).Clicked() {
							s.startBackup(c.Services(), op)
						}
					}
					if op.err != "" {
						ui.Text(c, op.err).Selectable()
					}
					if op.outcome != nil {
						ui.Text(c, "结果："+op.outcome.State)
						ui.Scroll(c).MaxHeight(240).Children(func() {
							for _, step := range op.outcome.Steps {
								ui.Text(c, step.State+" · "+step.SQL).Selectable()
							}
						})
					}
					ui.Row(c).Gap(8).Children(func() {
						if op.busy {
							if ui.Button(c, locales.Text("取消并等待回执")).Disabled(op.cancelRequested).Clicked() {
								op.cancelRequested = true
								op.cancel()
							}
						} else if ui.Button(c, "关闭").Clicked() {
							op.open = false
							s.closeBackup()
						}
					})
				})
			})
		})
	})
	if !op.open {
		if op.busy {
			if op.cancel != nil {
				op.cancel()
			}
		} else {
			s.closeBackup()
		}
	}
}
