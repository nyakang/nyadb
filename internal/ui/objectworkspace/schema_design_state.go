package objectworkspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/locales"
)

type SchemaDesignEvent struct {
	DocumentID, Generation uint64
	Database               string
	Destination            string
	Original               contract.SchemaDesignTarget
	Outcome                contract.SchemaDesignOutcome
}
type schemaDesignReply struct {
	request, generation uint64
	snapshot            contract.SchemaDesignSnapshot
	outcome             *contract.SchemaDesignOutcome
	err                 error
}
type schemaDesignDocument struct {
	pageChoice                             int
	fieldDetails                           bool
	structured                             *structuredDefinition
	original                               contract.SchemaDesignSnapshot
	draft, saved                           schemaddl.Table
	baseline                               string
	loaded, busy, blocked, cancelRequested bool
	isNew                                  bool
	generation, request, nextID            uint64
	cancel                                 context.CancelFunc
	results                                <-chan schemaDesignReply
	page, row, selected, definitionRow     int
	list                                   ui.ListState
	field, fieldBase                       schemaddl.SQLiteField
	pgField, pgFieldBase                   schemaddl.PostgreSQLField
	fieldRow                               int
	raw, rawBase                           string
	name, nameBase                         string
	prepared                               *contract.SchemaDesignRequest
	plan                                   *schemaddl.Plan
	preview, guard                         bool
	next                                   func(ui.Services)
	confirmation                           string
	renameTo                               string
}

func (x *schemaDesignDocument) dirty() bool {
	return x.loaded && (schemaddl.Signature(x.draft) != x.baseline || x.formsDirty())
}
func (x *schemaDesignDocument) formsDirty() bool {
	return x.field != x.fieldBase || x.pgField != x.pgFieldBase || x.raw != x.rawBase || x.name != x.nameBase || x.structured.dirty()
}
func (s *State) schemaBinding(profile int64, database, table string) (contract.SchemaDesignOperations, contract.SchemaDesignTarget) {
	if s.deps.SchemaDesignFor == nil || s.deps.SchemaTarget == nil {
		return contract.SchemaDesignOperations{}, contract.SchemaDesignTarget{}
	}
	return s.deps.SchemaDesignFor(profile), s.deps.SchemaTarget(profile, database, table)
}
func (s *State) canSchemaDesign(profile int64, database, table string) bool {
	w := s.session()
	if !w.Connected || w.ProfileID != profile {
		return false
	}
	ops, target := s.schemaBinding(profile, database, table)
	return target.Generation != 0 && s.operationAvailable("design_table", target) && ops.Snapshot != nil && ops.Apply != nil && ops.CanApply != nil && ops.CanApply(target)
}
func (s *State) CanEditDesign(d *Document) bool {
	if d == nil || d.closed || d.objectKind != "表" || !s.session().Connected || s.session().ProfileID != d.profileID {
		return false
	}
	if s.canSchemaDesign(d.profileID, d.database, d.table) {
		return true
	}
	return s.capabilities(d.profileID).DesignTables && s.deps.DesignLoader != nil && s.deps.DesignLoader() != nil && s.canAdministration(d.profileID)
}

// CanCreateTable is distinct from MySQL table administration availability.
func (s *State) CanCreateTable(profile int64, database string) bool {
	if s.nativeTablePending() {
		return false
	}
	return s.CanChangeTable(profile) || s.canSchemaDesign(profile, database, "new_table")
}

func (s *State) CanRenameTable(profile int64, database, table string) bool {
	if s.nativeTablePending() {
		return false
	}
	return s.CanChangeTable(profile) || s.canNativeRename(profile, database, table) || s.deps.RenameSchema != nil && s.canSchemaDesign(profile, database, table)
}
func (s *State) BeginSchemaRename(d *Document, name string, services ui.Services) {
	if d == nil || d.closed {
		return
	}
	if d.schemaDesign == nil {
		s.beginSchemaDesign(d, services)
	}
	x := d.schemaDesign
	if x == nil {
		return
	}
	if x.loaded && !x.busy {
		next := func(ui.Services) { x.draft.Name = name; x.page = 4 }
		if !s.guardSchemaDesign(d, next) {
			next(services)
		}
	} else {
		x.renameTo = name
	}
}
func (s *State) beginSchemaDesign(d *Document, services ui.Services) {
	if d == nil || d.closed || d.design != nil || d.schemaDesign != nil || !s.canSchemaDesign(d.profileID, d.database, d.table) {
		return
	}
	x := &schemaDesignDocument{row: -1, selected: -1, fieldRow: -1, definitionRow: -1}
	d.schemaDesign = x
	s.loadSchemaDesign(d, services)
}
func (s *State) OpenNewSchemaDesign(id uint64, o contract.ObjectRef, services ui.Services) *Document {
	if !s.canSchemaDesign(o.ProfileID, o.Schema, o.Name) {
		return nil
	}
	d := s.OpenQualifiedStructure(id, o, "表")
	if d == nil {
		return nil
	}
	s.beginSchemaDesign(d, services)
	d.schemaDesign.isNew = true
	return d
}
func (s *State) loadSchemaDesign(d *Document, services ui.Services) {
	x := d.schemaDesign
	if x == nil || x.busy || d.closed {
		return
	}
	ops, target := s.schemaBinding(d.profileID, d.database, d.table)
	if ops.Snapshot == nil || target.Object != d.object {
		d.err = locales.Text("原表设计范围不可用")
		return
	}
	w := s.session()
	if !w.Connected || w.ProfileID != d.profileID {
		return
	}
	x.request++
	request, generation := x.request, w.Generation
	x.generation = generation
	x.busy = true
	x.preview = false
	x.prepared = nil
	x.plan = nil
	ctx, cancel := context.WithCancel(context.Background())
	x.cancel = cancel
	ch := make(chan schemaDesignReply, 1)
	x.results = ch
	go func() {
		snapshot, err := ops.Snapshot(ctx, target)
		ch <- schemaDesignReply{request: request, generation: generation, snapshot: snapshot, err: err}
		services.Invalidate()
	}()
}
func (s *State) consumeSchemaDesign(services ui.Services) {
	w := s.session()
	for _, d := range s.documents {
		x := d.schemaDesign
		if x == nil || x.results == nil || d.profileID != w.ProfileID {
			continue
		}
		select {
		case reply := <-x.results:
			if reply.request != x.request {
				continue
			}
			x.results = nil
			x.busy = false
			x.cancelRequested = false
			if x.cancel != nil {
				x.cancel()
				x.cancel = nil
			}
			valid := w.Connected && w.Generation == reply.generation
			if reply.outcome != nil {
				out := *reply.outcome
				expected := x.original.Target
				destination := x.draft.Name
				if x.prepared != nil {
					destination = x.prepared.Desired.Name
				}
				expected.Object.Name = destination
				if out.Verified && (out.State != "committed" || out.Actual == nil || out.Actual.Target != expected || !out.Actual.Schema.Exists || out.Actual.Schema.Table.Name != destination || out.Actual.Schema.Engine != x.original.Schema.Engine || out.Error != nil) {
					out.Verified, out.Actual = false, nil
					out.Error = errors.Join(out.Error, errors.New(locales.Text("结构回执与原连接或已预览目标不一致；保留草稿，禁止重试。")))
				}
				if out.State == "uncertain" || out.State == "committed" {
					x.blocked = true
				}
				if s.deps.SchemaDesigned != nil {
					s.deps.SchemaDesigned(services, SchemaDesignEvent{DocumentID: d.id, Generation: reply.generation, Database: d.database, Destination: destination, Original: x.original.Target, Outcome: out})
				}
				if !valid {
					d.err = locales.Text("原表设计会话已失效；保留草稿，请核对执行结果")
					x.blocked = true
					x.next = nil
					continue
				}
				if out.Error != nil {
					d.err = schemaDesignError(out.Error)
					x.next = nil
					continue
				}
				if !out.Verified || out.Actual == nil {
					d.err = locales.Text("结构核验未完成；禁止自动重试")
					x.blocked = true
					x.next = nil
					continue
				}
				reply.snapshot = out.Actual.Clone()
				// The original identity changes only after a verified receipt. The
				// host already received the captured original target above.
				d.object = reply.snapshot.Target.Object
				d.table = d.object.Name
				// Revoke any inspector read of the old definition before loading
				// metadata for the verified identity below.
				s.cancel(d)
				d.data = nil
				d.fieldRow = -1
			} else if !valid {
				d.err = locales.Text("原表设计会话已失效；请重新读取")
				x.next = nil
				continue
			}
			if reply.err != nil {
				d.err = schemaDesignError(reply.err)
				x.next = nil
				continue
			}
			if x.isNew && reply.outcome == nil && reply.snapshot.Schema.Exists {
				d.err = locales.Text("表名称已存在，请使用其他名称新建表")
				x.next = nil
				continue
			}
			d.inline = nil
			x.original = reply.snapshot.Clone()
			x.draft = reply.snapshot.Schema.Table.Clone()
			if !reply.snapshot.Schema.Exists {
				if schemaNativeFieldGrammar(reply.snapshot.Schema.Engine) {
					primary := postgresDefaultPrimaryName(d.table)
					x.draft = schemaddl.Table{Name: d.table, Clauses: []schemaddl.Clause{
						{ID: "new:id", Kind: "column", Name: "id", SQL: "bigint NOT NULL"},
						{ID: "new:primary", Kind: "constraint", Name: primary, SQL: "CONSTRAINT " + schemaddl.Quote(primary) + " PRIMARY KEY (id)"},
					}}
					if reply.snapshot.Schema.Engine == "opengauss" {
						x.draft.OpenGauss = &schemaddl.OpenGaussTableProperties{Persistence: "p"}
						for i := range x.draft.Clauses {
							x.draft.Clauses[i].OpenGauss = &schemaddl.OpenGaussColumnProperties{}
						}
					} else {
						x.draft.Postgres = &schemaddl.PostgreSQLTableProperties{Kind: "r", Persistence: "p"}
					}
				} else if reply.snapshot.Schema.Engine == "sqlite" {
					x.draft, _ = schemaddl.ParseSQLite("CREATE TABLE " + schemaddl.Quote(d.table) + " (id INTEGER PRIMARY KEY)")
				} else {
					d.err = schemaDesignError(schemaddl.ErrUnsupported)
					x.next = nil
					continue
				}
				x.baseline = ""
			} else {
				x.baseline = schemaddl.Signature(x.draft)
			}
			x.saved = x.draft.Clone()
			if reply.snapshot.Schema.Exists {
				x.isNew = false
			}
			x.loaded = true
			x.blocked = false
			x.row = -1
			x.fieldRow = -1
			x.definitionRow = -1
			x.selected = -1
			x.field = schemaddl.SQLiteField{}
			x.fieldBase = x.field
			x.pgField = schemaddl.PostgreSQLField{}
			x.pgFieldBase = x.pgField
			x.raw = ""
			x.rawBase = ""
			x.name = ""
			x.nameBase = ""
			d.err = ""
			if x.renameTo != "" {
				x.draft.Name = x.renameTo
				x.renameTo = ""
				x.page = 4
			}
			if reply.outcome != nil {
				s.Load(d, services.Invalidate, true)
			}
			if x.next != nil {
				next := x.next
				x.next = nil
				next(services)
			}
		default:
		}
	}
}
func (s *State) applySchemaForms(d *Document) error {
	x := d.schemaDesign
	if err := s.applyStructuredDefinition(d); err != nil {
		return err
	}
	if schemaNativeFieldGrammar(x.original.Schema.Engine) && x.fieldRow >= 0 && (x.pgField != x.pgFieldBase || x.name != x.nameBase) && !x.pgField.Unsupported {
		if x.fieldRow >= len(x.draft.Clauses) {
			return errors.New(locales.Text("字段选择已改变"))
		}
		if err := schemaddl.PostgreSQLIdentifier(x.name); err != nil {
			return err
		}
		clause := &x.draft.Clauses[x.fieldRow]
		if x.pgField != x.pgFieldBase {
			if x.pgField.NotNull != x.pgFieldBase.NotNull {
				if err := schemaNotNullEditable(x.original.Schema, clause.ID); err != nil {
					return err
				}
			}
			body, err := x.pgField.SQL()
			if err != nil {
				return err
			}
			if _, err := parseSchemaNativeField(x.original.Schema.Engine, body); err != nil {
				return err
			}
			clause.SQL = body
		}
		clause.Name = x.name
		x.pgFieldBase = x.pgField
		x.nameBase = x.name
	}
	if x.fieldRow >= 0 && x.field != x.fieldBase {
		if x.fieldRow >= len(x.draft.Clauses) {
			return errors.New(locales.Text("字段选择已改变"))
		}
		pending := x.field
		pending.Name = x.fieldBase.Name
		body, err := x.field.SQL()
		if pending == x.fieldBase {
			body = x.draft.Clauses[x.fieldRow].SQL
			err = nil
		}
		if err != nil {
			return err
		}
		clause := &x.draft.Clauses[x.fieldRow]
		clause.Name = x.field.Name
		clause.SQL = body
		x.field.Definition = body
		x.fieldBase = x.field
	}
	if x.raw != x.rawBase || x.name != x.nameBase {
		if x.page == 1 && x.row >= 0 && x.row < len(x.draft.Clauses) {
			clause := x.draft.Clauses[x.row]
			clause.SQL = x.raw
			if schemaNativeFieldGrammar(x.original.Schema.Engine) && x.name != x.nameBase {
				var err error
				clause, err = schemaddl.RebindPostgreSQLConstraint(clause, d.object.Schema, x.draft.Name, x.draft.Name, x.name)
				if err != nil {
					return err
				}
				x.raw = clause.SQL
			}
			x.draft.Clauses[x.row] = clause
		} else if (x.page == 2 || x.page == 3) && x.definitionRow >= 0 && x.definitionRow < len(x.draft.Definitions) {
			definition := &x.draft.Definitions[x.definitionRow]
			definition.Name = x.name
			definition.SQL = x.raw
		} else if x.page == 0 && x.row >= 0 && x.row < len(x.draft.Clauses) && (x.field.Unsupported || x.pgField.Unsupported) {
			x.draft.Clauses[x.row].Name = x.name
			x.draft.Clauses[x.row].SQL = x.raw
		} else {
			return errors.New(locales.Text("定义选择已改变"))
		}
		x.rawBase, x.nameBase = x.raw, x.name
	}
	return nil
}
func (s *State) PrepareSchemaDesign(d *Document) error {
	if d == nil || d.closed || d.schemaDesign == nil {
		return errors.New(locales.Text("设计文档不可用"))
	}
	x := d.schemaDesign
	if !x.loaded || x.busy || x.blocked {
		return errors.New(locales.Text("请先完成结构读取或核对原操作"))
	}
	if err := d.flushInline(); err != nil {
		d.err = err.Error()
		return err
	}
	if err := s.applySchemaForms(d); err != nil {
		d.err = schemaDesignError(err)
		return err
	}
	request := contract.SchemaDesignRequest{Original: x.original.Clone(), Desired: x.draft.Clone(), AllowColumnDrop: true}
	plan, err := planSchemaDesign(request.Original.Schema, request.Desired, d.object.Schema)
	if err != nil {
		d.err = schemaDesignError(err)
		return err
	}
	if plan.Mode == "noop" {
		d.err = locales.Text("结构没有修改")
		return errors.New(d.err)
	}
	x.prepared = &request
	x.plan = &plan
	x.preview = true
	x.confirmation = ""
	d.err = ""
	return nil
}
func (s *State) executeSchemaDesign(d *Document, services ui.Services) {
	if d == nil || d.closed || d.schemaDesign == nil {
		return
	}
	x := d.schemaDesign
	if x.busy || x.blocked || x.prepared == nil || x.plan == nil {
		return
	}
	w := s.session()
	if !w.Connected || w.ProfileID != d.profileID || w.Generation != x.generation {
		d.err = locales.Text("原表设计会话已失效")
		return
	}
	if x.formsDirty() || schemaddl.Signature(x.draft) != schemaddl.Signature(x.prepared.Desired) {
		d.err = locales.Text("预览后草稿已修改，请重新生成预览")
		return
	}
	if len(x.plan.Dropped) > 0 && x.confirmation != d.table {
		d.err = locales.Text("请输入原表名称以确认删除字段")
		return
	}
	ops, target := s.schemaBinding(d.profileID, d.database, d.table)
	if ops.Apply == nil || ops.CanApply == nil || target != x.original.Target || !ops.CanApply(target) {
		d.err = locales.Text("原表设计范围不可用")
		return
	}
	request := x.prepared.Clone()
	x.request++
	sequence, generation := x.request, w.Generation
	ctx, cancel := context.WithCancel(context.Background())
	x.cancel = cancel
	x.busy = true
	x.preview = false
	ch := make(chan schemaDesignReply, 1)
	x.results = ch
	complete := s.beginActivity(d.profileID, generation, d.database, d.table, "structure", "APPLY TABLE DESIGN")
	go func() {
		out := ops.Apply(ctx, request)
		activity := managementActivityOutcome(out.Error)
		if out.State == "uncertain" {
			activity.State = "uncertain"
		}
		contract.CompleteActivity(complete, activity)
		ch <- schemaDesignReply{request: sequence, generation: generation, outcome: &out}
		services.Invalidate()
	}()
}
func (s *State) cancelSchemaDesign(d *Document) {
	if d != nil && d.schemaDesign != nil && d.schemaDesign.cancel != nil {
		d.schemaDesign.cancelRequested = true
		d.schemaDesign.cancel()
	}
}
func (s *State) closeSchemaDesign(d *Document) {
	if d == nil || d.schemaDesign == nil {
		return
	}
	s.cancelSchemaDesign(d)
	d.schemaDesign.results = nil
	d.schemaDesign.next = nil
	d.schemaDesign = nil
}
func (s *State) guardSchemaDesign(d *Document, next func(ui.Services)) bool {
	x := d.schemaDesign
	if x.busy {
		if s.deps.ActivateDesign != nil {
			s.deps.ActivateDesign(d)
		}
		d.err = locales.Text("请先等待结构操作完成或取消确认")
		return true
	}
	if !x.dirty() && !d.inlineDirty() {
		return false
	}
	x.guard = true
	x.next = next
	if s.deps.ActivateDesign != nil {
		s.deps.ActivateDesign(d)
	}
	return true
}
func (s *State) schemaSelectRow(d *Document, page, row int) error {
	x := d.schemaDesign
	if err := s.applySchemaForms(d); err != nil {
		return err
	}
	x.row = row
	x.structured = nil
	x.fieldRow = -1
	x.definitionRow = -1
	x.field = schemaddl.SQLiteField{}
	x.fieldBase = x.field
	x.pgField = schemaddl.PostgreSQLField{}
	x.pgFieldBase = x.pgField
	x.raw = ""
	x.rawBase = ""
	x.name = ""
	x.nameBase = ""
	if page == 0 && row >= 0 && row < len(x.draft.Clauses) {
		x.fieldRow = row
		if schemaNativeFieldGrammar(x.original.Schema.Engine) {
			f, err := parseSchemaNativeField(x.original.Schema.Engine, x.draft.Clauses[row].SQL)
			if err != nil {
				return err
			}
			x.pgField, x.pgFieldBase = f, f
			x.name = x.draft.Clauses[row].Name
			if f.Unsupported {
				x.raw = x.draft.Clauses[row].SQL
			}
		} else {
			f, err := schemaddl.ParseSQLiteField(x.draft.Clauses[row])
			if err != nil {
				return err
			}
			x.field, x.fieldBase = f, f
			if f.Unsupported {
				x.raw = f.Definition
				x.name = f.Name
			}
		}
	}
	if page == 1 && row >= 0 && row < len(x.draft.Clauses) {
		x.raw = x.draft.Clauses[row].SQL
		if schemaNativeFieldGrammar(x.original.Schema.Engine) {
			x.name = x.draft.Clauses[row].Name
		}
	}
	if (page == 2 || page == 3) && row >= 0 && row < len(x.draft.Definitions) {
		definition := x.draft.Definitions[row]
		x.definitionRow = row
		x.name = definition.Name
		x.raw = definition.SQL
	}
	x.rawBase, x.nameBase = x.raw, x.name
	s.readStructuredDefinition(d, page)
	return nil
}
func (s *State) schemaAdd(d *Document) {
	x := d.schemaDesign
	if err := s.applySchemaForms(d); err != nil {
		d.err = schemaDesignError(err)
		return
	}
	x.nextID++
	id := fmt.Sprintf("new:%d", x.nextID)
	if x.page == 0 {
		used := map[string]bool{}
		for _, c := range x.draft.Columns() {
			name := c.Name
			if !schemaNativeFieldGrammar(x.original.Schema.Engine) {
				name = strings.ToLower(name)
			}
			used[name] = true
		}
		name := "field_1"
		for i := 1; used[name]; i++ {
			name = fmt.Sprintf("field_%d", i+1)
		}
		clause := schemaddl.Clause{ID: id, Kind: "column", Name: name, SQL: "TEXT"}
		if x.original.Schema.Engine == "opengauss" {
			clause.OpenGauss = &schemaddl.OpenGaussColumnProperties{}
		}
		x.draft.Clauses = append(x.draft.Clauses, clause)
		_ = s.schemaSelectRow(d, 0, len(x.draft.Clauses)-1)
	} else if x.page == 1 {
		sql := "CHECK (1)"
		if schemaNativeFieldGrammar(x.original.Schema.Engine) {
			name := fmt.Sprintf("check_%d", x.nextID)
			sql = "CONSTRAINT " + schemaddl.Quote(name) + " CHECK (true)"
		}
		name := ""
		if schemaNativeFieldGrammar(x.original.Schema.Engine) {
			name = fmt.Sprintf("check_%d", x.nextID)
		}
		clause := schemaddl.Clause{ID: id, Kind: "constraint", Name: name, SQL: sql}
		if x.original.Schema.Engine == "opengauss" {
			clause.OpenGauss = &schemaddl.OpenGaussColumnProperties{}
		}
		x.draft.Clauses = append(x.draft.Clauses, clause)
		_ = s.schemaSelectRow(d, 1, len(x.draft.Clauses)-1)
	} else if x.page == 2 || x.page == 3 {
		kind := "index"
		if x.page == 3 {
			kind = "trigger"
		}
		name := fmt.Sprintf("%s_%d", kind, x.nextID)
		columns := x.draft.Columns()
		if len(columns) == 0 {
			return
		}
		sql := "CREATE INDEX " + schemaddl.Quote(name) + " ON " + schemaddl.Quote(d.table) + " (" + schemaddl.Quote(columns[0].Name) + ")"
		if schemaNativeFieldGrammar(x.original.Schema.Engine) {
			table := d.table
			if !x.original.Schema.Exists {
				table = x.draft.Name
			}
			target := schemaddl.Quote(d.object.Schema) + "." + schemaddl.Quote(table)
			sql = "CREATE INDEX " + schemaddl.Quote(name) + " ON " + target + " (" + schemaddl.Quote(columns[0].Name) + ")"
			if kind == "trigger" {
				invocation := "FUNCTION"
				if x.original.Schema.Engine == "opengauss" {
					invocation = "PROCEDURE"
				}
				sql = "CREATE TRIGGER " + schemaddl.Quote(name) + " BEFORE UPDATE ON " + target + " FOR EACH ROW EXECUTE " + invocation + " " + schemaddl.Quote(d.object.Schema) + "." + schemaddl.Quote("trigger_function") + "()"
			}
		} else if kind == "trigger" {
			sql = "CREATE TRIGGER " + schemaddl.Quote(name) + " AFTER UPDATE ON " + schemaddl.Quote(d.table) + " BEGIN SELECT 1; END"
		}
		definition := schemaddl.Definition{Name: name, Kind: kind, SQL: sql}
		if x.original.Schema.Engine == "opengauss" {
			definition.OpenGauss = &schemaddl.OpenGaussDefinitionProperties{}
		}
		x.draft.Definitions = append(x.draft.Definitions, definition)
		_ = s.schemaSelectRow(d, x.page, len(x.draft.Definitions)-1)
	}
	x.selected = -1
	d.err = ""
}

func postgresDefaultPrimaryName(table string) string {
	const suffix = "_pkey"
	for len(table) > 63-len(suffix) {
		_, size := utf8.DecodeLastRuneInString(table)
		table = table[:len(table)-size]
	}
	return table + suffix
}
