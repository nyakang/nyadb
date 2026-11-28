package objectworkspace

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
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
			if x.owner != nil && x.owner.blocked {
				ui.Text(c, locales.Text("原始结构写入未核验；读取不会解除写入保护。")).TextColor(p.Error)
			} else {
				ui.Text(c, locales.Text("结构操作已有执行步骤；请核对并重新读取，禁止自动重试。")).TextColor(p.Error)
			}
		}
		if !x.loaded {
			ui.Box(c).Grow(1)
			return
		}
		if x.original.Schema.Engine == "manticoresearch" {
			s.manticoreSchemaFields(c, d, locked)
			return
		}
		names := []string{locales.Text("字段"), locales.Text("约束"), locales.Text("索引"), locales.Text("触发器"), locales.Text("表选项"), locales.Text("原始 DDL")}
		if x.owner != nil {
			names = append(names, locales.Text("原始结构回执"))
		}
		candidate := &x.pageChoice
		*candidate = x.page
		ui.ScrollHorizontal(c).Height(theme.RowHeight(c, theme.TabHeight)).Children(func() {
			if theme.Tabs(c, candidate, names, locked && x.owner == nil, locales.Text("表设计子页")).Label(locales.Text("表设计子页")).Changed() {
				if locked {
					x.page = *candidate
				} else if err := d.flushInline(); err != nil {
					d.err = err.Error()
					*candidate = x.page
					d.formFocusPending = true
				} else if err := s.applySchemaForms(d); err != nil {
					d.err = schemaDesignError(err)
					*candidate = x.page
					d.formFocusPending = true
				} else {
					x.page = *candidate
					x.selected = -1
					_ = s.schemaSelectRow(d, x.page, -1)
				}
			}
		})
		if x.page < 4 {
			s.schemaDefinitionView(c, d, locked)
		} else if x.page == 4 {
			ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
				ui.Column(c).Padding(8).Gap(8).Children(func() {
					s.schemaDesignInput(c, d, "表名称", &x.draft.Name, locked)
					switch x.original.Schema.Engine {
					case "postgresql":
						s.postgresSchemaOptions(c, d, locked)
					case "opengauss":
						s.openGaussSchemaOptions(c, d, locked)
					case "sqlite":
						s.schemaDesignInput(c, d, "SQLite 表选项", &x.draft.Options, locked)
						ui.Text(c, locales.Text("支持 STRICT、WITHOUT ROWID；现有表的 rowid 模式需要独立迁移。"))
					}
					ui.Text(c, locales.Text("表或字段改名请单独预览并执行，数据库会改写依赖定义。"))
				})
			})
		} else if x.page == 6 && x.owner != nil {
			s.schemaReceiptView(c, x.owner)
		} else {
			raw := x.original.Schema.DDL
			if raw == "" {
				plan, err := s.schemaDesignPlan(d, contract.SchemaDesignRequest{Original: x.original.Clone(), Desired: x.draft.Clone()})
				if err == nil {
					raw = strings.Join(plan.SQL, ";\n\n")
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
				d.removeInlineField(x.draft.Clauses[x.row].ID)
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
	columns := []ui.TableColumn{{Title: locales.Text("名称"), Width: 140}, {Title: locales.Text("完整定义"), Width: 650}}
	if x.page == 0 {
		columns = designFieldColumns(x.original.Schema.Engine)
	}
	theme.Table(c, &x.list, columns, len(indices), func(row, col int) {
		if x.page == 0 {
			s.schemaFieldCell(c, d, indices[row], col, locked)
			return
		}
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
	}).Grow(1).MinHeight(120).Shrink(1)
	if x.page == 0 {
		s.syncInlineSelection(d)
	}
	if x.selected >= 0 && x.selected < len(indices) && indices[x.selected] != x.row {
		if err := s.schemaSelectRow(d, x.page, indices[x.selected]); err != nil {
			d.err = schemaDesignError(err)
		}
	}
	if x.row < 0 {
		return
	}
	if x.page == 0 {
		ui.Checkbox(c, &x.fieldDetails, locales.Text("高级字段属性")).Disabled(locked)
		if !x.fieldDetails {
			return
		}
	}
	if x.structured != nil {
		s.structuredDefinitionView(c, d, locked)
		return
	}
	ui.Scroll(c).Grow(1).MinHeight(100).Children(func() {
		ui.Column(c).Padding(8).Gap(8).Children(func() {
			if x.page == 0 && schemaNativeFieldGrammar(x.original.Schema.Engine) && !x.pgField.Unsupported {
				s.nativeSchemaField(c, d, locked)
			} else if x.page == 0 && x.original.Schema.Engine == "sqlite" && !x.field.Unsupported {
				s.schemaDesignInput(c, d, "字段名称", &x.field.Name, locked)
				s.schemaDesignInput(c, d, "字段类型", &x.field.Type, locked)
				ui.Row(c).Wrap().Gap(8).Children(func() {
					ui.Checkbox(c, &x.field.Primary, locales.Text("主键")).Disabled(locked)
					ui.Checkbox(c, &x.field.AutoIncrement, locales.Text("自动递增")).Disabled(locked)
					ui.Checkbox(c, &x.field.NotNull, locales.Text("非空")).Disabled(locked)
					ui.Checkbox(c, &x.field.Unique, locales.Text("唯一")).Disabled(locked)
				})
				ui.Checkbox(c, &x.field.HasDefault, locales.Text("设置默认值")).Disabled(locked)
				s.schemaDesignInput(c, d, "默认值表达式", &x.field.Default, locked || !x.field.HasDefault)
				s.schemaDesignInput(c, d, "排序规则", &x.field.Collation, locked)
			} else {
				if x.page == 0 {
					ui.Text(c, locales.Text("此字段含高级属性；编辑完整定义时保留所需的全部属性。"))
				}
				if x.page != 1 || schemaNativeFieldGrammar(x.original.Schema.Engine) {
					s.schemaDesignInput(c, d, "定义名称", &x.name, locked)
				}
				ui.Text(c, locales.Text("完整定义"))
				s.designTextArea(c, d, &x.raw).Label(locales.Text("完整定义")).Height(170).ReadOnly(locked)
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

// Label sets an accessibility name in MyGo; native forms also need visible
// captions so empty default and collation inputs can be distinguished.
func (s *State) schemaDesignInput(c *ui.Context, d *Document, label string, value *string, readOnly bool) {
	text := locales.Text(label)
	theme.Field(c, text, func() { s.designTextInput(c, d, value).Label(text).ReadOnly(readOnly).FillWidth() }).FillWidth()
}

func (s *State) schemaDesignModals(c *ui.Context, d *Document) {
	x := d.schemaDesign
	p := theme.Colors(c)
	if x.guard {
		theme.Modal(c, &x.guard, func() {
			width, _ := c.Size()
			ui.Column(c).Width(min(float32(480), width-68)).Gap(10).Children(func() {
				ui.Text(c, locales.Text("表设计有未保存的修改")).Bold()
				preserve := x.owner != nil && x.owner.blocked
				if preserve {
					ui.Text(c, locales.Text("原写入结果尚未核验；离开会保留草稿与回执，禁止再次执行。关闭文档会释放这些内容，请先复制或导出回执。"))
				}
				ui.Row(c).Wrap().Gap(8).Children(func() {
					if ui.Button(c, locales.Text("继续编辑")).Clicked() {
						x.guard = false
						x.next = nil
					}
					label := locales.Text("放弃修改")
					if preserve {
						label = locales.Text("保留草稿并离开")
					}
					if ui.Button(c, label).Clicked() {
						s.resolveSchemaDesignLeave(d, c.Services())
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
		theme.Modal(c, &x.preview, func() {
			width, height := c.Size()
			ui.Column(c).Width(min(float32(780), width-68)).MaxHeight(height - 68).Gap(8).Children(func() {
				ui.Text(c, locales.Text("SQL 预览")).Bold()
				ui.Text(c, d.database+" / "+d.table)
				if x.plan.ScopeHint != "" {
					ui.Text(c, locales.Text(x.plan.ScopeHint))
				}
				raw := strings.Join(x.plan.SQL, ";\n\n")
				ui.TextArea(c, &raw).ReadOnly(true).Height(min(float32(270), height-250)).MinHeight(80)
				if len(x.plan.Dropped) > 0 {
					ui.Text(c, locales.Text("将删除字段：")+strings.Join(x.plan.Dropped, ", ")).TextColor(p.Error)
					s.designFieldInput(c, d, "确认原表名称", &x.confirmation)
				}
				ui.Row(c).Wrap().Justify(ui.End).Gap(8).Shrink(0).Children(func() {
					if ui.Button(c, locales.Text("返回编辑")).Clicked() {
						x.preview = false
						x.next = nil
					}
					if ui.PrimaryButton(c, locales.Text("确认执行结构 SQL")).Disabled(len(x.plan.Dropped) > 0 && x.confirmation != d.table).Clicked() {
						run := func(services ui.Services) { s.executeSchemaDesign(d, services) }
						if x.plan.Mode == "rebuild" && s.deps.BeforeRebuild != nil {
							if !s.deps.BeforeRebuild(c.Services(), d, run) {
								run(c.Services())
							}
						} else if s.deps.BeforeDDL == nil || !s.deps.BeforeDDL(c.Services(), d.profileID, d.database, d.table, run) {
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
