package objectworkspace

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) schemaDesignView(c *ui.Context, d *Document) {
	x := d.schemaDesign
	p := theme.Colors(c)
	locked := x.busy || x.blocked
	ui.Column(c).Fill().MinHeight(0).Gap(5).Children(func() {
		ui.Row(c).Wrap().Padding(5, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, d.database+" / "+d.table).Bold().Grow(1)
			if x.dirty() {
				ui.Text(c, "*")
			}
			if ui.Button(c, locales.Text("SQL 预览")).Disabled(!x.loaded || locked).Clicked() {
				_ = s.PrepareSchemaDesign(d)
			}
			if ui.Button(c, locales.Text("重新读取结构")).Disabled(x.busy).Clicked() {
				next := func(services ui.Services) { s.loadSchemaDesign(d, services) }
				if !s.guardSchemaDesign(d, next) {
					next(c.Services())
				}
			}
			if ui.Button(c, locales.Text("取消")).Disabled(!x.busy || x.cancelRequested).Clicked() {
				s.cancelSchemaDesign(d)
			}
		})
		if x.busy {
			ui.Text(c, locales.Text("正在等待结构操作回执…"))
		}
		if d.err != "" {
			ui.Text(c, d.err).TextColor(p.Error).Tooltip(d.err)
		}
		if x.blocked {
			ui.Text(c, locales.Text("结构操作已有执行步骤；请核对并重新读取，禁止自动重试。")).TextColor(p.Error)
		}
		if !x.loaded {
			ui.Box(c).Grow(1)
			return
		}
		names := []string{locales.Text("字段"), locales.Text("约束"), locales.Text("索引"), locales.Text("触发器"), locales.Text("表选项"), locales.Text("原始 DDL")}
		selected := names[x.page]
		if ui.Select(c, &selected, names).Label(locales.Text("表设计子页")).Disabled(locked).Changed() {
			if err := s.applySchemaForms(d); err != nil {
				d.err = err.Error()
			} else {
				for i, name := range names {
					if selected == name {
						x.page = i
						x.selected = -1
						_ = s.schemaSelectRow(d, i, -1)
					}
				}
			}
		}
		if x.page < 4 {
			s.schemaDefinitionView(c, d, locked)
		} else if x.page == 4 {
			ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
				ui.Column(c).Padding(8).Gap(8).Children(func() {
					schemaDesignInput(c, "表名称", &x.draft.Name, locked)
					if x.original.Schema.Engine == "postgresql" {
						s.postgresSchemaOptions(c, d, locked)
					} else {
						schemaDesignInput(c, "SQLite 表选项", &x.draft.Options, locked)
						ui.Text(c, locales.Text("支持 STRICT、WITHOUT ROWID；现有表的 rowid 模式需要独立迁移。"))
					}
					ui.Text(c, locales.Text("表或字段改名请单独预览并执行，数据库会改写依赖定义。"))
				})
			})
		} else {
			raw := x.original.Schema.DDL
			if raw == "" {
				if x.original.Schema.Engine == "postgresql" {
					plan, err := schemaddl.PlanPostgreSQL(x.original.Schema, x.draft, d.object.Schema, true)
					if err == nil {
						raw = strings.Join(plan.SQL, ";\n\n")
					}
				} else {
					raw, _ = schemaddl.CreateSQLite(x.draft, d.object.Schema, d.table)
				}
			}
			ui.TextArea(c, &raw).Label(locales.Text("原始 DDL")).ReadOnly(true).Grow(1)
		}
	})
	s.schemaDesignModals(c, d)
}
func (s *State) schemaDefinitionView(c *ui.Context, d *Document, locked bool) {
	x := d.schemaDesign
	ui.Row(c).Wrap().Gap(5).Padding(3, 8).Children(func() {
		label := []string{"添加字段", "添加约束", "添加索引", "添加触发器"}[x.page]
		if ui.Button(c, locales.Text(label)).Disabled(locked).Clicked() {
			s.schemaAdd(d)
		}
		available := x.row >= 0 && (x.page < 2 && x.row < len(x.draft.Clauses) || x.page >= 2 && x.row < len(x.draft.Definitions))
		if ui.Button(c, locales.Text("删除定义")).Disabled(locked || !available).Clicked() {
			if x.page < 2 {
				x.draft.Clauses = append(x.draft.Clauses[:x.row], x.draft.Clauses[x.row+1:]...)
			} else {
				x.draft.Definitions = append(x.draft.Definitions[:x.row], x.draft.Definitions[x.row+1:]...)
			}
			x.field = x.fieldBase
			x.pgField = x.pgFieldBase
			x.raw = x.rawBase
			x.name = x.nameBase
			_ = s.schemaSelectRow(d, x.page, -1)
			x.selected = -1
		}
	})
	var indices []int
	if x.page < 2 {
		for i, clause := range x.draft.Clauses {
			if x.page == 0 && clause.Kind == "column" || x.page == 1 && clause.Kind == "constraint" {
				indices = append(indices, i)
			}
		}
	} else {
		for i, definition := range x.draft.Definitions {
			if x.page == 2 && definition.Kind == "index" || x.page == 3 && definition.Kind == "trigger" {
				indices = append(indices, i)
			}
		}
	}
	// Map the stable draft selection to the currently visible page rows.
	for i, index := range indices {
		if index == x.row {
			x.selected = i
		}
	}
	x.list.Selected = &x.selected
	theme.Table(c, &x.list, []ui.TableColumn{{Title: locales.Text("名称"), Width: 140}, {Title: locales.Text("完整定义"), Width: 650}}, len(indices), func(row, col int) {
		index := indices[row]
		name, raw := "", ""
		if x.page < 2 {
			name, raw = x.draft.Clauses[index].Name, x.draft.Clauses[index].SQL
		} else {
			name, raw = x.draft.Definitions[index].Name, x.draft.Definitions[index].SQL
		}
		if col == 0 {
			ui.Text(c, name).SingleLine()
		} else {
			ui.Text(c, raw).SingleLine().Tooltip(raw)
		}
	}).Grow(1).MinHeight(80).MaxHeight(180).Shrink(1)
	if x.selected >= 0 && x.selected < len(indices) && indices[x.selected] != x.row {
		if err := s.schemaSelectRow(d, x.page, indices[x.selected]); err != nil {
			d.err = err.Error()
		}
	}
	if x.row < 0 {
		ui.Box(c).Grow(1)
		return
	}
	ui.Scroll(c).Grow(1).MinHeight(100).Children(func() {
		ui.Column(c).Padding(8).Gap(8).Children(func() {
			if x.page == 0 && x.original.Schema.Engine == "postgresql" && !x.pgField.Unsupported {
				s.postgresSchemaField(c, d, locked)
			} else if x.page == 0 && x.original.Schema.Engine != "postgresql" && !x.field.Unsupported {
				schemaDesignInput(c, "字段名称", &x.field.Name, locked)
				schemaDesignInput(c, "字段类型", &x.field.Type, locked)
				ui.Row(c).Wrap().Gap(8).Children(func() {
					ui.Checkbox(c, &x.field.Primary, locales.Text("主键")).Disabled(locked)
					ui.Checkbox(c, &x.field.AutoIncrement, locales.Text("自动递增")).Disabled(locked)
					ui.Checkbox(c, &x.field.NotNull, locales.Text("非空")).Disabled(locked)
					ui.Checkbox(c, &x.field.Unique, locales.Text("唯一")).Disabled(locked)
				})
				ui.Checkbox(c, &x.field.HasDefault, locales.Text("设置默认值")).Disabled(locked)
				schemaDesignInput(c, "默认值表达式", &x.field.Default, locked || !x.field.HasDefault)
				schemaDesignInput(c, "排序规则", &x.field.Collation, locked)
			} else {
				if x.page == 0 {
					ui.Text(c, locales.Text("此字段含高级属性；编辑完整定义时保留所需的全部属性。"))
				}
				if x.page != 1 || x.original.Schema.Engine == "postgresql" {
					schemaDesignInput(c, "定义名称", &x.name, locked)
				}
				ui.Text(c, locales.Text("完整定义"))
				ui.TextArea(c, &x.raw).Label(locales.Text("完整定义")).Height(170).ReadOnly(locked)
			}
		})
	})
	if ui.Button(c, locales.Text("应用定义修改")).Disabled(locked).Clicked() {
		if err := s.applySchemaForms(d); err != nil {
			d.err = err.Error()
		} else {
			d.err = ""
		}
	}
}

// Label sets an accessibility name in MyGo; native forms also need visible
// captions so empty default and collation inputs can be distinguished.
func schemaDesignInput(c *ui.Context, label string, value *string, readOnly bool) {
	text := locales.Text(label)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text+":").Width(110).SingleLine().Tooltip(text)
		ui.TextInput(c, value).Label(text).ReadOnly(readOnly).Grow(1).MinWidth(0)
	})
}

func (s *State) schemaDesignModals(c *ui.Context, d *Document) {
	x := d.schemaDesign
	p := theme.Colors(c)
	if x.guard {
		ui.Modal(c, &x.guard, func() {
			width, _ := c.Size()
			ui.Column(c).Width(min(float32(480), width-40)).Padding(18).Gap(10).Background(p.Header).Children(func() {
				ui.Text(c, locales.Text("表设计有未保存的修改")).Bold()
				ui.Row(c).Wrap().Gap(8).Children(func() {
					if ui.Button(c, locales.Text("继续编辑")).Clicked() {
						x.guard = false
						x.next = nil
					}
					if ui.Button(c, locales.Text("放弃修改")).Clicked() {
						next := x.next
						x.guard = false
						x.next = nil
						x.draft = x.saved.Clone()
						x.field = x.fieldBase
						x.pgField = x.pgFieldBase
						x.raw = x.rawBase
						x.name = x.nameBase
						_ = s.schemaSelectRow(d, x.page, -1)
						x.baseline = schemaddl.Signature(x.draft)
						if next != nil {
							next(c.Services())
						}
					}
					if ui.PrimaryButton(c, locales.Text("SQL 预览")).Disabled(x.blocked).Clicked() {
						if s.PrepareSchemaDesign(d) == nil {
							x.guard = false
						}
					}
				})
			})
		})
		if !x.guard && !x.preview {
			x.next = nil
		}
	}
	if x.preview && x.plan != nil {
		ui.Modal(c, &x.preview, func() {
			width, height := c.Size()
			ui.Column(c).Width(min(float32(780), width-40)).MaxHeight(height - 40).Padding(16).Gap(8).Background(p.Header).Children(func() {
				ui.Text(c, locales.Text("SQL 预览")).Bold()
				ui.Text(c, d.database+" / "+d.table)
				raw := strings.Join(x.plan.SQL, ";\n\n")
				ui.TextArea(c, &raw).ReadOnly(true).Height(min(float32(270), height-250)).MinHeight(80)
				if len(x.plan.Dropped) > 0 {
					ui.Text(c, locales.Text("将删除字段：")+strings.Join(x.plan.Dropped, ", ")).TextColor(p.Error)
					ui.TextInput(c, &x.confirmation).Label(locales.Text("确认原表名称"))
				}
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("返回编辑")).Clicked() {
						x.preview = false
						x.next = nil
					}
					if ui.PrimaryButton(c, locales.Text("确认执行结构 SQL")).Disabled(len(x.plan.Dropped) > 0 && x.confirmation != d.table).Clicked() {
						run := func(services ui.Services) { s.executeSchemaDesign(d, services) }
						if s.deps.BeforeDDL == nil || !s.deps.BeforeDDL(c.Services(), d.profileID, d.database, d.table, run) {
							run(c.Services())
						}
					}
				})
			})
		})
		if !x.preview && !x.busy {
			x.next = nil
		}
	}
}
