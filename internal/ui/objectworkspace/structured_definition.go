package objectworkspace

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type structuredDefinition struct {
	Form     schemaddl.BasicDefinition
	Base     string
	Advanced bool
}

func (f *structuredDefinition) dirty() bool {
	return f != nil && !f.Advanced && schemaddl.Signature(f.Form) != f.Base
}

func (s *State) readStructuredDefinition(d *Document, page int) {
	x := d.schemaDesign
	if page != 1 && page != 2 || x.row < 0 {
		return
	}
	kind := "constraint"
	if page == 2 {
		kind = "index"
	}
	f, err := schemaddl.ParseBasicDefinition(x.original.Schema.Engine, kind, x.raw)
	if err != nil {
		return
	}
	if f.Kind == "index" {
		if f.Schema == "" {
			f.Schema = d.object.Schema
		}
		if x.original.Schema.Engine == "sqlite" && strings.EqualFold(f.Schema, d.object.Schema) && strings.EqualFold(f.Table, x.draft.Name) {
			f.Schema, f.Table = d.object.Schema, x.draft.Name
		}
		if f.Schema != d.object.Schema || f.Table != x.draft.Name {
			return
		}
	}
	x.structured = &structuredDefinition{Form: f, Base: schemaddl.Signature(f)}
}

func (s *State) applyStructuredDefinition(d *Document) error {
	x := d.schemaDesign
	f := x.structured
	if !f.dirty() {
		return nil
	}
	for _, column := range f.Form.Columns {
		found := false
		for _, c := range x.draft.Columns() {
			found = found || c.Name == column.Name || x.original.Schema.Engine == "sqlite" && strings.EqualFold(c.Name, column.Name)
		}
		if !found {
			return fmt.Errorf("%s: %s", locales.Text("字段已不存在"), column.Name)
		}
	}
	raw, err := f.Form.SQL(x.original.Schema.Engine)
	if err != nil {
		return err
	}
	x.raw, x.name = raw, f.Form.Name
	f.Base = schemaddl.Signature(f.Form)
	return nil
}

func (s *State) structuredDefinitionView(c *ui.Context, d *Document, locked bool) {
	x := d.schemaDesign
	f := x.structured
	ui.Row(c).Gap(6).Children(func() {
		label := "完整定义（高级）"
		if f.Advanced {
			label = "图形编辑"
		}
		if ui.Button(c, locales.Text(label)).Disabled(locked).Clicked() {
			if err := s.applySchemaForms(d); err != nil {
				d.err = schemaDesignError(err)
			} else if f.Advanced {
				x.structured = nil
				s.readStructuredDefinition(d, x.page)
				if x.structured == nil {
					d.err = locales.Text("此定义包含高级属性，请继续编辑完整定义")
				}
			} else {
				f.Advanced = true
			}
		}
	})
	if x.structured != f {
		return
	}
	ui.Scroll(c).Grow(1).MinHeight(120).Children(func() {
		ui.Column(c).Padding(8).Gap(8).Children(func() {
			if f.Advanced {
				s.schemaDesignInput(c, d, "定义名称", &x.name, locked)
				s.designTextArea(c, d, &x.raw).Label(locales.Text("完整定义")).Height(170).ReadOnly(locked)
				s.openGaussDefinitionProperties(c, d, locked)
				return
			}
			s.schemaDesignInput(c, d, "定义名称", &f.Form.Name, locked)
			if f.Form.Kind == "index" {
				ui.Checkbox(c, &f.Form.Unique, locales.Text("唯一索引")).Disabled(locked)
			} else {
				kindLabels := map[string]string{"primary": locales.Text("主键"), "unique": locales.Text("唯一"), "foreign": locales.Text("外键"), "check": locales.Text("检查")}
				kind := kindLabels[f.Form.Kind]
				if schemaDesignChoice(c, "约束类型", &kind, []string{kindLabels["primary"], kindLabels["unique"], kindLabels["foreign"], kindLabels["check"]}, locked) {
					for value, label := range kindLabels {
						if label == kind {
							f.Form.Kind = value
							break
						}
					}
					kind = f.Form.Kind
					if kind != "check" && len(f.Form.Columns) == 0 && len(x.draft.Columns()) > 0 {
						f.Form.Columns = []schemaddl.BasicIndexColumn{{Name: x.draft.Columns()[0].Name}}
					}
					if kind == "check" && f.Form.Expression == "" {
						f.Form.Expression = "1=1"
					}
				}
			}
			if f.Form.Kind == "check" {
				s.schemaDesignInput(c, d, "检查表达式", &f.Form.Expression, locked)
			} else {
				choices := []string{}
				for _, col := range x.draft.Columns() {
					choices = append(choices, col.Name)
				}
				remove := -1
				for i := range f.Form.Columns {
					i := i
					column := &f.Form.Columns[i]
					ui.Row(c).Key(fmt.Sprint(i)).Gap(5).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("%s %d", locales.Text("字段"), i+1)).Width(70)
						ui.Select(c, &column.Name, choices).Label(fmt.Sprintf("%s/%d", locales.Text("索引或约束字段"), i)).Width(160).Disabled(locked)
						if f.Form.Kind == "index" {
							ui.Checkbox(c, &column.Descending, locales.Text("降序")).Disabled(locked)
						}
						if theme.IconButton(c, locales.Text("上移字段"), "expand").Label(fmt.Sprintf("%s/%d", locales.Text("上移字段"), i)).Disabled(locked || i == 0).Clicked() {
							f.Form.Columns[i], f.Form.Columns[i-1] = f.Form.Columns[i-1], f.Form.Columns[i]
							if f.Form.Kind == "foreign" && len(f.Form.ReferencedColumns) == len(f.Form.Columns) {
								f.Form.ReferencedColumns[i], f.Form.ReferencedColumns[i-1] = f.Form.ReferencedColumns[i-1], f.Form.ReferencedColumns[i]
							}
						}
						if theme.IconButton(c, locales.Text("移除字段"), "close").Label(fmt.Sprintf("%s/%d", locales.Text("移除字段"), i)).Disabled(locked).Clicked() {
							remove = i
						}
					})
				}
				if remove >= 0 {
					if f.Form.Kind == "foreign" && len(f.Form.ReferencedColumns) == len(f.Form.Columns) {
						f.Form.ReferencedColumns = slices.Delete(f.Form.ReferencedColumns, remove, remove+1)
					}
					f.Form.Columns = slices.Delete(f.Form.Columns, remove, remove+1)
				}
				if ui.Button(c, locales.Text("添加索引或约束字段")).Disabled(locked || len(f.Form.Columns) >= len(choices)).Clicked() {
					for _, name := range choices {
						if !slices.ContainsFunc(f.Form.Columns, func(col schemaddl.BasicIndexColumn) bool { return col.Name == name }) {
							f.Form.Columns = append(f.Form.Columns, schemaddl.BasicIndexColumn{Name: name})
							break
						}
					}
				}
			}
			if f.Form.Kind == "foreign" {
				if schemaNativeFieldGrammar(x.original.Schema.Engine) {
					s.schemaDesignInput(c, d, "引用模式", &f.Form.ReferencedSchema, locked)
				}
				s.schemaDesignInput(c, d, "引用表", &f.Form.ReferencedTable, locked)
				for len(f.Form.ReferencedColumns) < len(f.Form.Columns) {
					f.Form.ReferencedColumns = append(f.Form.ReferencedColumns, "")
				}
				f.Form.ReferencedColumns = f.Form.ReferencedColumns[:len(f.Form.Columns)]
				for i := range f.Form.ReferencedColumns {
					s.schemaDesignInput(c, d, fmt.Sprintf("%s %d", locales.Text("引用字段"), i+1), &f.Form.ReferencedColumns[i], locked)
				}
				choices := []string{"", "NO ACTION", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT"}
				schemaDesignChoice(c, "删除时", &f.Form.OnDelete, choices, locked)
				schemaDesignChoice(c, "更新时", &f.Form.OnUpdate, choices, locked)
			}
			s.openGaussDefinitionProperties(c, d, locked)
		})
	})
	if ui.Button(c, locales.Text("应用定义修改")).Disabled(locked).Clicked() {
		if err := s.applySchemaForms(d); err != nil {
			d.err = schemaDesignError(err)
		} else {
			d.err = ""
		}
	}
}
