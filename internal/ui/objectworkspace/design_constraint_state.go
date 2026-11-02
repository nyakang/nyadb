package objectworkspace

import (
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
)

type designConstraintForm struct {
	row, columnRow, prefixRow int
	data, base                mysqlddl.Constraint
	list                      ui.ListState
	candidate, prefix         string
}

func (f *designConstraintForm) dirty() bool {
	if f == nil {
		return false
	}
	if !reflect.DeepEqual(f.data, f.base) {
		return true
	}
	if f.prefixRow >= 0 && f.prefixRow < len(f.data.Columns) {
		prefix := ""
		if f.data.Columns[f.prefixRow].Prefix != 0 {
			prefix = strconv.Itoa(f.data.Columns[f.prefixRow].Prefix)
		}
		return f.prefix != prefix
	}
	return false
}

func (f *designConstraintForm) applyPrefix() error {
	if f.prefixRow < 0 || f.prefixRow >= len(f.data.Columns) {
		return nil
	}
	value := 0
	var err error
	if strings.TrimSpace(f.prefix) != "" {
		value, err = strconv.Atoi(f.prefix)
	}
	if err != nil || value < 0 || value > 65535 {
		return errors.New("索引前缀必须是 0 到 65535 的整数")
	}
	f.data.Columns[f.prefixRow].Prefix = value
	return nil
}

func (s *State) applyDesignForms(d *Document) bool {
	x := d.design
	if !s.applyDesignField(d) {
		return false
	}
	if f := x.constraint; f != nil && f.dirty() {
		if err := f.applyPrefix(); err != nil {
			x.formError = err.Error()
			return false
		}
		if f.row < 0 || f.row >= len(x.draft.Table.Clauses) {
			x.formError = "约束草稿目标已失效"
			return false
		}
		raw, err := f.data.SQL(x.draft.Table)
		if err != nil {
			x.formError = err.Error()
			return false
		}
		clause, err := mysqlddl.ParseClause(raw)
		if err != nil {
			x.formError = err.Error()
			return false
		}
		clause.ID = x.draft.Table.Clauses[f.row].ID
		candidate := x.draft.Table.Clone()
		candidate.Clauses[f.row] = clause
		if err := mysqlddl.Validate(candidate); err != nil {
			x.formError = err.Error()
			return false
		}
		x.draft.Table.Clauses[f.row] = clause
		f.base = f.data.Clone()
	}
	if x.optionValue != x.optionBase {
		options, err := mysqlddl.SetOption(x.draft.Table.Options, x.optionName, x.optionValue)
		if err != nil {
			x.formError = err.Error()
			return false
		}
		x.draft.Table.Options = options
		x.optionBase = x.optionValue
	}
	x.formError = ""
	return true
}

func (s *State) changeDesignPage(d *Document, page int) bool {
	if !s.applyDesignForms(d) {
		return false
	}
	x := d.design
	x.page, x.row, x.formRow = page, -1, -1
	x.form, x.formBase = mysqlddl.Field{}, mysqlddl.Field{}
	x.constraint = nil
	x.list = ui.ListState{}
	if page == 6 {
		x.optionName = "COMMENT"
	}
	x.optionValue = mysqlddl.Option(x.draft.Table.Options, x.optionName)
	x.optionBase = x.optionValue
	return true
}
