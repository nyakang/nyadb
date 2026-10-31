package objectworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type DesignLoader func(context.Context, int64, string, string) (mysqlddl.Snapshot, error)
type DesignExecutor func(context.Context, int64, mysqlddl.Plan) mysqlsvc.DDLOutcome
type DesignEvent struct {
	ProfileID       int64
	Generation      uint64
	Database, Table string
	Outcome         mysqlsvc.DDLOutcome
}
type designReply struct {
	snapshot            mysqlddl.Snapshot
	outcome             *mysqlsvc.DDLOutcome
	generation, request uint64
	err                 error
}
type designDocument struct {
	original                               mysqlddl.Snapshot
	draft                                  mysqlddl.Draft
	savedDraft                             mysqlddl.Draft
	baseline                               string
	loaded, busy, cancelRequested, blocked bool
	generation, request                    uint64
	cancel                                 context.CancelFunc
	results                                <-chan designReply
	plan                                   *mysqlddl.Plan
	preview, guard                         bool
	next                                   func(ui.Services)
	page, row, formRow                     int
	list                                   ui.ListState
	form, formBase                         mysqlddl.Field
	formError                              string
	optionName, optionValue                string
	triggerRow                             int
	outcome                                *mysqlsvc.DDLOutcome
}

func designSignature(draft mysqlddl.Draft) string { b, _ := json.Marshal(draft); return string(b) }
func (d *Document) DesignDirty() bool {
	return d != nil && d.design != nil && d.design.loaded && (designSignature(d.design.draft) != d.design.baseline || !reflect.DeepEqual(d.design.form, d.design.formBase))
}
func (d *Document) DesignBusy() bool { return d != nil && d.design != nil && d.design.busy }
func (s *State) BeginDesign(d *Document, services ui.Services) {
	if d == nil || d.closed || d.objectKind != "表" || d.design != nil || s.deps.DesignLoader == nil {
		return
	}
	load := s.deps.DesignLoader()
	if load == nil {
		return
	}
	w := s.session()
	if !w.Connected || w.ProfileID != d.profileID {
		return
	}
	x := &designDocument{row: -1, formRow: -1, triggerRow: -1, optionName: "ENGINE"}
	d.design = x
	s.loadDesign(d, services, load)
}
func (s *State) OpenNewDesign(id uint64, profile int64, database, name string) *Document {
	d := s.OpenStructure(id, profile, database, name)
	if d == nil {
		return nil
	}
	x := &designDocument{row: 0, formRow: -1, triggerRow: -1, optionName: "ENGINE", loaded: true, generation: s.sessionFor(profile).Generation}
	x.original = mysqlddl.Snapshot{Database: database, Table: name}
	if s.deps.DesignGeneration != nil {
		x.original.RuntimeGeneration = s.deps.DesignGeneration(profile)
	}
	x.draft = mysqlddl.Draft{New: true, Table: mysqlddl.Table{Database: database, Name: name, Options: "ENGINE=InnoDB"}}
	field, _ := mysqlddl.ParseClause("`id` bigint NOT NULL AUTO_INCREMENT")
	pk, _ := mysqlddl.ParseClause("PRIMARY KEY (`id`)")
	x.draft.Table.Clauses = []mysqlddl.Clause{field, pk}
	x.savedDraft = x.draft.Clone()
	d.design = x
	return d
}
func (s *State) loadDesign(d *Document, services ui.Services, load DesignLoader) {
	x := d.design
	if x == nil || x.busy {
		return
	}
	x.request++
	request := x.request
	w := s.session()
	x.generation = w.Generation
	ctx, cancel := context.WithCancel(context.Background())
	x.cancel = cancel
	x.busy = true
	ch := make(chan designReply, 1)
	x.results = ch
	profile, db, table, generation := d.profileID, d.database, d.table, w.Generation
	go func() {
		snapshot, e := load(ctx, profile, db, table)
		ch <- designReply{snapshot: snapshot, generation: generation, request: request, err: e}
		services.Invalidate()
	}()
}
func (s *State) consumeDesign(services ui.Services) {
	w := s.session()
	for _, d := range s.documents {
		x := d.design
		if x == nil || x.results == nil || d.profileID != w.ProfileID {
			continue
		}
		select {
		case r := <-x.results:
			if r.request != x.request {
				continue
			}
			x.results = nil
			x.busy = false
			x.cancelRequested = false
			if x.cancel != nil {
				x.cancel()
				x.cancel = nil
			}
			valid := w.Connected && w.Generation == r.generation
			if r.outcome != nil {
				x.outcome = r.outcome
				if r.outcome.State != "not_executed" {
					x.blocked = true
				}
				if s.deps.Designed != nil {
					s.deps.Designed(services, DesignEvent{ProfileID: d.profileID, Generation: r.generation, Database: d.database, Table: d.table, Outcome: *r.outcome})
				}
				if !valid {
					d.err = "原表设计会话已失效；保留草稿，请核对执行结果"
					x.blocked = true
					continue
				}
				if r.outcome.Error != nil {
					x.next = nil
					d.err = r.outcome.Error.Error()
					continue
				}
				if !r.outcome.Verified || r.outcome.Actual == nil {
					d.err = "DDL 核验未完成；禁止自动重试"
					continue
				}
				r.snapshot = *r.outcome.Actual
			} else if !valid {
				d.err = "原表设计会话已失效；请重新读取"
				continue
			}
			if r.err != nil {
				d.err = r.err.Error()
				continue
			}
			table, e := mysqlddl.Parse(d.database, d.table, r.snapshot.DDL)
			if e != nil {
				d.err = e.Error()
				continue
			}
			x.original = r.snapshot
			x.draft = mysqlddl.Draft{Table: table, Triggers: append([]mysqlddl.Trigger(nil), r.snapshot.Triggers...)}
			x.baseline = designSignature(x.draft)
			x.savedDraft = x.draft.Clone()
			x.loaded = true
			x.blocked = false
			x.formRow = -1
			x.form = mysqlddl.Field{}
			x.formBase = x.form
			d.err = ""
			if x.next != nil {
				next := x.next
				x.next = nil
				next(services)
			}
		default:
		}
	}
}
func (s *State) PrepareDesign(d *Document) error {
	if d == nil || d.design == nil || !d.design.loaded || d.design.busy || d.design.blocked {
		return errors.New("design is not ready or requires explicit re-reading")
	}
	x := d.design
	if !s.applyDesignField(d) {
		return errors.New(x.formError)
	}
	p, e := mysqlddl.Build(x.original, x.draft)
	if e != nil {
		d.err = e.Error()
		return e
	}
	x.plan = &p
	x.preview = true
	d.err = ""
	return nil
}
func (s *State) executeDesign(d *Document, services ui.Services) {
	if d == nil || d.closed || d.design == nil {
		return
	}
	x := d.design
	if x.plan == nil || x.busy || x.blocked || s.deps.DesignExecutor == nil {
		return
	}
	w := s.session()
	if !w.Connected || w.ProfileID != d.profileID || w.Generation != x.generation {
		d.err = "原表设计会话已失效"
		return
	}
	run := s.deps.DesignExecutor()
	if run == nil {
		return
	}
	plan := *x.plan
	plan.Desired = plan.Desired.Clone()
	plan.Original.Triggers = append([]mysqlddl.Trigger(nil), plan.Original.Triggers...)
	plan.Statements = append([]string(nil), plan.Statements...)
	if designSignature(plan.Desired) != designSignature(x.draft) {
		d.err = "预览后草稿已修改，请重新生成预览"
		return
	}
	x.request++
	request, generation, profile := x.request, w.Generation, d.profileID
	ctx, cancel := context.WithCancel(context.Background())
	x.cancel = cancel
	x.busy = true
	x.preview = false
	ch := make(chan designReply, 1)
	x.results = ch
	go func() {
		out := run(ctx, profile, plan)
		ch <- designReply{outcome: &out, generation: generation, request: request}
		services.Invalidate()
	}()
}
func (s *State) cancelDesign(d *Document) {
	if d != nil && d.design != nil && d.design.cancel != nil {
		d.design.cancelRequested = true
		d.design.cancel()
	}
}
func (s *State) closeDesign(d *Document) {
	if d.design == nil {
		return
	}
	s.cancelDesign(d)
	d.design.results = nil
	d.design.next = nil
	d.design.plan = nil
	d.design = nil
}
func (s *State) GuardDesignLeave(d *Document, next func(ui.Services)) bool {
	if d == nil || d.design == nil {
		return false
	}
	if d.design.busy {
		if s.deps.ActivateDesign != nil {
			s.deps.ActivateDesign(d)
		}
		d.err = "请先等待结构操作完成或取消确认"
		return true
	}
	if !d.DesignDirty() {
		return false
	}
	d.design.guard = true
	if s.deps.ActivateDesign != nil {
		s.deps.ActivateDesign(d)
	}
	d.design.next = next
	return true
}
func (s *State) applyDesignField(d *Document) bool {
	x := d.design
	if x.formRow < 0 || reflect.DeepEqual(x.form, x.formBase) {
		return true
	}
	if x.formRow >= len(x.draft.Table.Clauses) {
		return false
	}
	raw, e := x.form.SQL()
	if e != nil {
		x.formError = e.Error()
		return false
	}
	c, e := mysqlddl.ParseClause(raw)
	if e != nil {
		x.formError = e.Error()
		return false
	}
	c.ID = x.draft.Table.Clauses[x.formRow].ID
	x.draft.Table.Clauses[x.formRow] = c
	x.formBase = x.form
	x.formError = ""
	return true
}
func (s *State) addDesignClause(d *Document, kind mysqlddl.Kind) {
	x := d.design
	if !s.applyDesignField(d) {
		return
	}
	n := 1
	prefix := "field"
	switch kind {
	case mysqlddl.Index:
		prefix = "idx"
	case mysqlddl.Foreign:
		prefix = "fk"
	case mysqlddl.Check:
		prefix = "check"
	}
	used := map[string]bool{}
	first := ""
	for _, c := range x.draft.Table.Clauses {
		used[c.Name] = true
		if c.Kind == mysqlddl.Column && first == "" {
			first = c.Name
		}
	}
	name := fmt.Sprintf("%s_%d", prefix, n)
	for used[name] {
		n++
		name = fmt.Sprintf("%s_%d", prefix, n)
	}
	quote := func(v string) string { return "`" + strings.ReplaceAll(v, "`", "``") + "`" }
	var raw string
	switch kind {
	case mysqlddl.Column:
		raw = quote(name) + " varchar(255) DEFAULT NULL"
	case mysqlddl.Primary:
		if used["PRIMARY"] {
			return
		}
		raw = "PRIMARY KEY (" + quote(first) + ")"
	case mysqlddl.Index:
		raw = "KEY " + quote(name) + " (" + quote(first) + ")"
	case mysqlddl.Foreign:
		raw = "CONSTRAINT " + quote(name) + " FOREIGN KEY (" + quote(first) + ") REFERENCES " + quote(d.database) + "." + quote(d.table) + " (" + quote(first) + ")"
	case mysqlddl.Check:
		raw = "CONSTRAINT " + quote(name) + " CHECK (" + quote(first) + " IS NOT NULL)"
	}
	c, e := mysqlddl.ParseClause(raw)
	if e != nil {
		d.err = e.Error()
		return
	}
	c.ID = fmt.Sprintf("new-%d-%s", x.request+uint64(len(x.draft.Table.Clauses)), c.Name)
	at := len(x.draft.Table.Clauses)
	if kind == mysqlddl.Column {
		for i, clause := range x.draft.Table.Clauses {
			if clause.Kind != mysqlddl.Column {
				at = i
				break
			}
		}
	}
	x.draft.Table.Clauses = append(x.draft.Table.Clauses, mysqlddl.Clause{})
	copy(x.draft.Table.Clauses[at+1:], x.draft.Table.Clauses[at:])
	x.draft.Table.Clauses[at] = c
	x.row = at
}
