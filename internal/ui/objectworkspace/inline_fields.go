package objectworkspace

import (
	"errors"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	"strings"
)

type designCell struct {
	Text, Base, Error string
	Focused           bool
	Commit            func(string) error
}

func (d *Document) DesignMode() bool { return d != nil && (d.design != nil || d.schemaDesign != nil) }
func (d *Document) inlineDirty() bool {
	for _, cell := range d.inline {
		if cell.Text != cell.Base {
			return true
		}
	}
	return false
}
func (d *Document) flushInline() error {
	for _, cell := range d.inline {
		if cell.Text != cell.Base {
			if err := cell.Commit(cell.Text); err != nil {
				cell.Error = err.Error()
				return err
			}
			cell.Base = cell.Text
			cell.Error = ""
		}
	}
	return nil
}

func (s *State) fieldTextCell(c *ui.Context, d *Document, key, value string, locked bool, types []string, commit func(string) error) {
	if d.inline == nil {
		d.inline = map[string]*designCell{}
	}
	cell := d.inline[key]
	if cell == nil {
		cell = &designCell{Text: value, Base: value}
		d.inline[key] = cell
	} else if cell.Text == cell.Base && value != cell.Base {
		cell.Text, cell.Base = value, value
	}
	cell.Commit = commit
	var input ui.Element
	if len(types) > 0 {
		input = ui.Combobox(c, &cell.Text, types).Disabled(locked)
	} else {
		input = ui.TextInput(c, &cell.Text).ReadOnly(locked)
	}
	input.Label(key).FillWidth().MinWidth(0)
	theme.BindNativeEdit(c, input, fmt.Sprintf("object/%d/%s", d.id, key), s.deps.EditFocus, s.deps.EditTarget, func() bool {
		return !d.closed && d.inline[key] == cell && (d.design != nil && d.design.page == 0 || d.schemaDesign != nil && d.schemaDesign.page == 0)
	}, !locked, false)
	if cell.Error != "" {
		input.Border(1, theme.Colors(c).Error).Tooltip(cell.Error)
	}
	if input.Shortcut(0, ui.KeyEscape) {
		cell.Text = cell.Base
		cell.Error = ""
	}
	focused := input.FocusWithin()
	if !locked && cell.Text != cell.Base && (input.Submitted() || cell.Focused && !focused) {
		if err := commit(cell.Text); err != nil {
			cell.Error = err.Error()
			input.Focus()
			d.err = cell.Error
		} else {
			cell.Base = cell.Text
			cell.Error = ""
			d.err = ""
		}
	}
	cell.Focused = focused
}

func (s *State) fieldTypes(profile int64) []string {
	if s.deps.FieldTypes != nil {
		return s.deps.FieldTypes(profile)
	}
	return nil
}

// Inline inputs can receive focus without the table selecting their row.
// Resolve their stable clause identity after all cells have handled blur commits.
func (s *State) syncInlineSelection(d *Document) {
	for key, cell := range d.inline {
		if !cell.Focused {
			continue
		}
		parts := strings.Split(key, "/")
		if len(parts) != 3 || parts[0] != "field" {
			continue
		}
		if err := s.selectInlineField(d, parts[1]); err != nil {
			d.err = err.Error()
		}
		return
	}
}

func (s *State) selectInlineField(d *Document, id string) error {
	if x := d.schemaDesign; x != nil {
		visible := 0
		for index, clause := range x.draft.Clauses {
			if clause.Kind != "column" {
				continue
			}
			if clause.ID == id {
				if x.row != index || x.fieldRow != index {
					if err := s.schemaSelectRow(d, 0, index); err != nil {
						return err
					}
				}
				x.selected = visible
				return nil
			}
			visible++
		}
	}
	if x := d.design; x != nil {
		visible := 0
		for index, clause := range x.draft.Table.Clauses {
			if clause.Kind != mysqlddl.Column {
				continue
			}
			if clause.ID == id {
				if x.row != index || x.formRow != index {
					if !s.applyDesignForms(d) {
						return errors.New(x.formError)
					}
					field, err := mysqlddl.ParseColumn(clause.SQL)
					if err != nil {
						return err
					}
					x.row, x.formRow = index, index
					x.form, x.formBase = field, field
				}
				x.clauseRow = visible
				return nil
			}
			visible++
		}
	}
	return errors.New("field removed")
}

// Show the active cell draft without applying it to the validated design.
func (d *Document) inlineFieldPreview(id string, out *contract.StructureColumn) {
	for key, cell := range d.inline {
		if !cell.Focused || !strings.HasPrefix(key, "field/"+id+"/") {
			continue
		}
		switch strings.TrimPrefix(key, "field/"+id+"/") {
		case "0":
			out.Name = cell.Text
		case "1":
			out.Type = cell.Text
		case "3":
			out.Default = nil
			if cell.Text != "" {
				value := cell.Text
				out.Default = &value
			}
		case "5":
			if d.design != nil {
				out.Comment = cell.Text
			}
		}
	}
}

func (s *State) commitSchemaCell(d *Document, id string, col int, value string) error {
	if d.closed || d.schemaDesign == nil {
		return errors.New("design closed")
	}
	x := d.schemaDesign
	index := -1
	for i, clause := range x.draft.Clauses {
		if clause.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return errors.New("field removed")
	}
	if err := s.selectInlineField(d, id); err != nil {
		return err
	}
	beforeField, beforePG, beforeName := x.field, x.pgField, x.name
	if x.original.Schema.Engine == "postgresql" {
		switch col {
		case 0:
			x.name = value
		case 1:
			x.pgField.Type = value
		case 2:
			x.pgField.NotNull = value == "true"
		case 3:
			x.pgField.HasDefault = value != ""
			x.pgField.Default = value
		case 4:
			x.pgField.Collation = value
		}
	} else {
		switch col {
		case 0:
			x.field.Name = value
		case 1:
			x.field.Type = value
		case 2:
			x.field.NotNull = value == "true"
		case 3:
			x.field.HasDefault = value != ""
			x.field.Default = value
		case 4:
			x.field.Primary = value == "true"
		case 5:
			x.field.AutoIncrement = value == "true"
		case 6:
			x.field.Unique = value == "true"
		case 7:
			x.field.Collation = value
		}
	}
	if err := s.applySchemaForms(d); err != nil {
		x.field, x.pgField, x.name = beforeField, beforePG, beforeName
		return err
	}
	return nil
}

func (s *State) schemaFieldCell(c *ui.Context, d *Document, index, col int, locked bool) {
	x := d.schemaDesign
	clause := x.draft.Clauses[index]
	name, typ, def, collation := "", "", "", ""
	primary, autoIncrement, unique := false, false, false
	notNull, unsupported := false, false
	if x.original.Schema.Engine == "postgresql" {
		field, err := schemaddl.ParsePostgreSQLField(clause.SQL)
		if err != nil {
			ui.Text(c, clause.SQL)
			return
		}
		name, typ, notNull, unsupported = clause.Name, field.Type, field.NotNull, field.Unsupported
		collation = field.Collation
		if field.HasDefault {
			def = field.Default
		}
		if col == 2 {
			locked = locked || field.Identity != ""
			if x.original.Schema.Postgres != nil && x.original.Schema.Exists {
				locked = locked || schemaddl.PostgreSQLNotNullEditable(x.original.Schema.Postgres, clause.ID) != nil
			}
		}
		if col == 3 {
			locked = locked || field.Identity != "" || field.Generation != ""
		}
	} else {
		field, err := schemaddl.ParseSQLiteField(clause)
		if err != nil {
			ui.Text(c, clause.SQL)
			return
		}
		name, typ, notNull, unsupported = field.Name, field.Type, field.NotNull, field.Unsupported
		collation, primary, autoIncrement, unique = field.Collation, field.Primary, field.AutoIncrement, field.Unique
		if field.HasDefault {
			def = field.Default
		}
	}
	locked = locked || unsupported
	if col == 2 || x.original.Schema.Engine != "postgresql" && col >= 4 && col <= 6 {
		checked := notNull
		if col == 4 {
			checked = primary
		}
		if col == 5 {
			checked = autoIncrement
		}
		if col == 6 {
			checked = unique
		}
		if ui.Checkbox(c, &checked, "").Label(fmt.Sprintf("field/%s/%d", clause.ID, col)).Disabled(locked).Changed() {
			if err := s.commitSchemaCell(d, clause.ID, col, fmt.Sprint(checked)); err != nil {
				d.err = err.Error()
			}
		}
		return
	}
	value := collation
	if col < 4 {
		value = []string{name, typ, "", def}[col]
	}
	types := []string(nil)
	if col == 1 {
		types = s.fieldTypes(d.profileID)
	}
	s.fieldTextCell(c, d, fmt.Sprintf("field/%s/%d", clause.ID, col), value, locked, types, func(value string) error { return s.commitSchemaCell(d, clause.ID, col, value) })
}

func mysqlDefaultExpression(field mysqlddl.Field) string {
	switch field.DefaultKind {
	case mysqlddl.NoDefault:
		return ""
	case mysqlddl.NullDefault:
		return "NULL"
	case mysqlddl.LiteralDefault:
		return "'" + strings.ReplaceAll(field.Default, "'", "''") + "'"
	default:
		return field.Default
	}
}
func (s *State) commitMySQLCell(d *Document, id string, col int, value string) error {
	if d.closed || d.design == nil {
		return errors.New("design closed")
	}
	x := d.design
	if !s.applyDesignForms(d) {
		return errors.New(x.formError)
	}
	index := -1
	for i, clause := range x.draft.Table.Clauses {
		if clause.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return errors.New("field removed")
	}
	field, err := mysqlddl.ParseColumn(x.draft.Table.Clauses[index].SQL)
	if err != nil {
		return err
	}
	if field.Unsupported {
		return errors.New("field contains unsupported attributes")
	}
	switch col {
	case 0:
		field.Name = value
	case 1:
		field.Type = value
	case 2:
		field.Nullable = value != "true"
	case 3:
		field.DefaultKind = mysqlddl.ExpressionDefault
		field.Default = value
		if value == "" {
			field.DefaultKind = mysqlddl.NoDefault
		} else if strings.EqualFold(value, "NULL") {
			field.DefaultKind = mysqlddl.NullDefault
		}
	case 4:
		field.AutoIncrement = value == "true"
	case 5:
		field.Comment = value
	case 6:
		field.Collation = value
	}
	raw, err := field.SQL()
	if err != nil {
		return err
	}
	x.draft.Table.Clauses[index].Name = field.Name
	x.draft.Table.Clauses[index].SQL = raw
	x.row, x.formRow = index, index
	x.form, x.formBase = field, field
	return s.selectInlineField(d, id)
}
func (s *State) mysqlFieldCell(c *ui.Context, d *Document, index, col int) {
	x := d.design
	clause := x.draft.Table.Clauses[index]
	field, err := mysqlddl.ParseColumn(clause.SQL)
	if err != nil {
		ui.Text(c, clause.SQL)
		return
	}
	locked := x.busy || x.blocked || field.Unsupported
	if col == 2 || col == 4 {
		notNull := !field.Nullable
		if col == 4 {
			notNull = field.AutoIncrement
		}
		if ui.Checkbox(c, &notNull, "").Label(locales.Text("非空") + "/" + clause.ID).Disabled(locked).Changed() {
			if err := s.commitMySQLCell(d, clause.ID, col, fmt.Sprint(notNull)); err != nil {
				d.err = err.Error()
			}
		}
		return
	}
	value := []string{field.Name, field.Type, "", mysqlDefaultExpression(field), "", field.Comment, field.Collation}[col]
	types := []string(nil)
	if col == 1 {
		types = s.fieldTypes(d.profileID)
	}
	s.fieldTextCell(c, d, fmt.Sprintf("field/%s/%d", clause.ID, col), value, locked, types, func(value string) error { return s.commitMySQLCell(d, clause.ID, col, value) })
}

func designFieldColumns(engine string) []ui.TableColumn {
	cols := []ui.TableColumn{{ID: "name", Title: locales.Text("名称"), Width: 160}, {ID: "type", Title: locales.Text("类型"), Width: 180}, {ID: "not_null", Title: locales.Text("非空"), Width: 70}, {ID: "default", Title: locales.Text("默认值表达式"), Width: 230}}
	if engine == "mysql" || engine == "mariadb" || engine == "tidb" {
		cols = append(cols, ui.TableColumn{ID: "auto_increment", Title: locales.Text("自动递增"), Width: 85}, ui.TableColumn{ID: "comment", Title: locales.Text("字段注释"), Width: 160})
	}
	if engine == "sqlite" {
		cols = append(cols, ui.TableColumn{ID: "primary", Title: locales.Text("主键"), Width: 70}, ui.TableColumn{ID: "auto_increment", Title: locales.Text("自动递增"), Width: 85}, ui.TableColumn{ID: "unique", Title: locales.Text("唯一"), Width: 70})
	}
	return append(cols, ui.TableColumn{ID: "collation", Title: locales.Text("排序规则"), Width: 130})
}

func (d *Document) removeInlineField(id string) {
	for key := range d.inline {
		if strings.HasPrefix(key, "field/"+id+"/") {
			delete(d.inline, key)
		}
	}
}
