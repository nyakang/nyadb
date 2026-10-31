package objectworkspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

type ObjectEvent struct {
	ProfileID  int64
	Generation uint64
	Ref        mysqlsvc.ObjectIdentity
	Action     string
	Outcome    mysqlsvc.ObjectOutcome
}
type objectOperation struct {
	nameDraft string

	sessionGeneration uint64

	profile                              int64
	generation                           uint64
	ref                                  mysqlsvc.ObjectIdentity
	action, text, originalText, err      string
	original                             mysqlsvc.ObjectDefinition
	plan                                 *mysqlsvc.ObjectPlan
	outcome                              *mysqlsvc.ObjectOutcome
	open, loaded, busy, blocked, preview bool
	cancel                               context.CancelFunc
	load                                 <-chan objectLoadResult
	result                               <-chan mysqlsvc.ObjectOutcome
}
type objectLoadResult struct {
	value mysqlsvc.ObjectDefinition
	err   error
}

func (s *State) BeginObject(services ui.Services, profile int64, ref mysqlsvc.ObjectIdentity, action string) {
	if !s.capabilities(profile).DesignTables {
		return
	}
	if s.objectOp != nil && s.objectOp.busy {
		return
	}
	if s.objectOp != nil && (s.objectOp.action == "create" || s.objectOp.text != s.objectOp.originalText) {
		s.report("请先保存、保留或放弃当前对象草稿")
		return
	}
	if s.objectOp != nil && s.objectOp.cancel != nil {
		s.objectOp.cancel()
	}
	if s.parkedObjectOp != nil {
		s.report("已有保留的对象草稿，请先继续或放弃草稿")
		return
	}
	session := s.sessionFor(profile)
	if !session.Connected || s.deps.ObjectExecutor == nil {
		return
	}
	gen := session.Generation
	if s.deps.RuntimeGeneration != nil {
		gen = s.deps.RuntimeGeneration(profile)
	}
	op := &objectOperation{profile: profile, generation: gen, sessionGeneration: session.Generation, ref: ref, action: action, open: true}
	op.nameDraft = ref.Name
	if ref.Kind == "database" || ref.Kind == "数据库" {
		op.nameDraft = ref.Database
	}
	s.objectOp = op
	if action == "create" {
		op.original = mysqlsvc.ObjectDefinition{ObjectIdentity: ref, Generation: gen}
		op.loaded = true
		op.text = objectStarter(ref)
		op.originalText = op.text
		return
	}
	if s.deps.ObjectLoader == nil {
		op.err = "完整定义读取服务不可用"
		return
	}
	op.busy = true
	ctx, cancel := context.WithCancel(context.Background())
	op.cancel = cancel
	ch := make(chan objectLoadResult, 1)
	op.load = ch
	loader := s.deps.ObjectLoader
	go func() { v, e := loader(ctx, profile, ref); ch <- objectLoadResult{v, e}; services.Invalidate() }()
}
func objectStarter(ref mysqlsvc.ObjectIdentity) string {
	q := func(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
	target := q(ref.Database) + "." + q(ref.Name)
	switch ref.Kind {
	case "database", "数据库":
		return "CREATE DATABASE " + q(ref.Database) + " CHARACTER SET utf8mb4"
	case "view", "视图":
		return "CREATE VIEW " + target + " AS SELECT 1 AS value"
	case "function", "函数":
		return "CREATE FUNCTION " + target + "() RETURNS BIGINT DETERMINISTIC RETURN 1"
	case "procedure", "存储过程":
		return "CREATE PROCEDURE " + target + "() BEGIN\n SELECT 1;\nEND"
	case "event", "事件":
		return "CREATE EVENT " + target + " ON SCHEDULE EVERY 1 DAY DISABLE DO SELECT 1"
	}
	return ""
}
func (s *State) consumeObjectOperations(services ui.Services) {
	op := s.objectOp
	if op == nil || op.profile != s.session().ProfileID {
		return
	}
	if op.load != nil {
		select {
		case r := <-op.load:
			op.load = nil
			op.busy = false
			if op.cancel != nil {
				op.cancel()
				op.cancel = nil
			}
			if r.err != nil {
				op.err = r.err.Error()
				return
			}
			session := s.sessionFor(op.profile)
			gen := session.Generation
			if s.deps.RuntimeGeneration != nil {
				gen = s.deps.RuntimeGeneration(op.profile)
			}
			if !session.Connected || gen != op.generation || session.Generation != op.sessionGeneration {
				op.err = "原对象会话已失效"
				return
			}
			op.original = r.value
			op.loaded = true
			op.text = r.value.SQL
			if op.action == "alter" && (op.ref.Kind == "database" || op.ref.Kind == "数据库") {
				op.text = "ALTER" + op.text[len("CREATE"):]
			} else if op.action == "alter" && (op.ref.Kind == "event" || op.ref.Kind == "事件") {
				op.text = "ALTER" + op.text[len("CREATE"):]
			}
			op.originalText = op.text
		default:
		}
	}
	if op.result != nil {
		select {
		case out := <-op.result:
			op.result = nil
			op.busy = false
			if op.cancel != nil {
				op.cancel()
				op.cancel = nil
			}
			op.outcome = &out
			op.blocked = out.State != "not_executed"
			if out.Error != nil {
				op.err = out.Error.Error()
			}
			if s.deps.ObjectChanged != nil {
				s.deps.ObjectChanged(services, ObjectEvent{op.profile, op.sessionGeneration, op.ref, op.action, out})
			}
			if out.Verified {
				if out.ActualDefinition != nil {
					op.original = *out.ActualDefinition
					op.text = op.original.SQL
					op.originalText = op.text
				}
				op.err = "执行完成，已重新读取对象定义"
			}
		default:
		}
	}
}
func (s *State) objectManagementModal(c *ui.Context) {
	op := s.objectOp
	if op == nil {
		return
	}
	p := theme.Colors(c)
	if !op.open {
		op.open = true
	}
	ui.Modal(c, &op.open, func() {
		w, h := c.Size()
		ui.Column(c).Width(min(float32(820), w-40)).Height(min(float32(640), h-40)).Padding(16).Gap(8).Background(p.Header).Children(func() {
			ui.Text(c, "对象定义 · "+op.action+" · "+op.ref.Database+" / "+op.ref.Name).Bold()
			if op.action == "create" {
				if ui.TextInput(c, &op.nameDraft).Label("新对象名称").Disabled(op.busy || op.blocked).Changed() {
					raw, e := mysqlsvc.SetObjectDefinitionName(mysqlsvc.ObjectDefinition{ObjectIdentity: op.ref, SQL: op.text}, op.nameDraft)
					if e != nil {
						op.err = e.Error()
					} else {
						if op.ref.Kind == "database" || op.ref.Kind == "数据库" {
							op.ref.Database = op.nameDraft
						} else {
							op.ref.Name = op.nameDraft
						}
						op.original.ObjectIdentity = op.ref
						op.text = raw
						op.plan = nil
						op.preview = false
						op.err = ""
					}
				}
			}
			if op.err != "" {
				ui.Text(c, op.err).TextColor(p.Error)
			}
			if op.busy {
				ui.Text(c, "等待原会话操作回执…")
			}
			ui.TextArea(c, &op.text).Label("完整公开对象定义").ReadOnly(op.busy || op.action == "drop" || op.blocked).Grow(1)
			if op.plan != nil && op.preview {
				preview := op.plan.SQL
				kind := op.ref.Kind
				if op.action == "alter" && (kind == "function" || kind == "函数" || kind == "procedure" || kind == "存储过程") {
					preview = "先删除原程序，再执行以下完整定义；后续失败保留部分完成结果。\n" + preview
				}
				ui.TextArea(c, &preview).ReadOnly(true).Height(100)
			}
			if op.outcome != nil {
				for i, step := range op.outcome.Steps {
					ui.Text(c, fmt.Sprintf("%d · %s", i+1, step.State)).Tooltip(step.SQL)
				}
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "保留草稿并关闭").Disabled(op.busy).Clicked() {
					op.open = false
					s.parkedObjectOp = op
					s.objectOp = nil
				}
				if ui.Button(c, "放弃草稿").Disabled(op.busy).Clicked() {
					s.objectOp = nil
				}
				if ui.Button(c, "取消操作").Disabled(!op.busy || op.cancel == nil).Clicked() {
					op.cancel()
				}
				if ui.Button(c, "SQL 预览").Disabled(!op.loaded || op.busy || op.blocked).Clicked() {
					plan, e := mysqlsvc.BuildObjectPlan(op.original, op.action, op.text)
					if e != nil {
						op.err = e.Error()
					} else {
						op.plan = &plan
						op.preview = true
						op.err = ""
					}
				}
				if ui.PrimaryButton(c, "确认执行").Disabled(op.plan == nil || !op.preview || op.busy || op.blocked || op.plan.SQL != op.text && op.action != "drop").Clicked() {
					s.executeObjectOperation(c.Services(), op)
				}
			})
		})
	})
}
func (s *State) executeObjectOperation(services ui.Services, op *objectOperation) {
	if op != s.objectOp || op.plan == nil {
		return
	}
	next := func(services ui.Services) {
		if op != s.objectOp || op.busy || !s.sessionFor(op.profile).Connected || s.sessionFor(op.profile).Generation != op.sessionGeneration {
			return
		}
		op.busy = true
		ctx, cancel := context.WithCancel(context.Background())
		op.cancel = cancel
		ch := make(chan mysqlsvc.ObjectOutcome, 1)
		op.result = ch
		plan := *op.plan
		executor := s.deps.ObjectExecutor
		profile := op.profile
		go func() { ch <- executor(ctx, profile, plan); services.Invalidate() }()
	}
	if s.deps.BeforeDDL != nil && s.deps.BeforeDDL(services, op.profile, op.ref.Database, op.ref.Name, next) {
		return
	}
	next(services)
}
func (s *State) ResumeObjectDraft() {
	if s.objectOp == nil && s.parkedObjectOp != nil {
		s.objectOp = s.parkedObjectOp
		s.parkedObjectOp = nil
		s.objectOp.open = true
	}
}
