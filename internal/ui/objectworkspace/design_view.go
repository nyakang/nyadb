package objectworkspace

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) designView(c *ui.Context, d *Document) {
	x := d.design
	p := theme.Colors(c)
	ui.Column(c).Fill().MinHeight(0).Gap(5).Children(func() {
		ui.Row(c).Wrap().Padding(5, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, d.database+" / "+d.table).Bold().Grow(1)
			if d.DesignDirty() {
				ui.Text(c, "*")
			}
			if ui.Button(c, locales.Text("SQL 预览")).Disabled(!x.loaded || x.busy || x.blocked).Clicked() {
				_ = s.PrepareDesign(d)
			}
			if ui.Button(c, locales.Text("重新读取结构")).Disabled(x.busy || x.draft.New).Clicked() {
				next := func(services ui.Services) {
					load := s.deps.DesignLoader()
					if load != nil {
						s.loadDesign(d, services, load)
					}
				}
				if !s.GuardDesignLeave(d, next) {
					next(c.Services())
				}
			}
			if ui.Button(c, locales.Text("取消")).Disabled(!x.busy || x.cancelRequested).Clicked() {
				s.cancelDesign(d)
			}
		})
		if x.busy {
			ui.Text(c, locales.Text("正在等待结构操作回执…"))
		}
		if d.err != "" {
			ui.Text(c, d.err).TextColor(p.Error).Tooltip(d.err)
		}
		if x.formError != "" {
			ui.Text(c, x.formError).TextColor(p.Error)
		}
		if x.blocked {
			ui.Text(c, locales.Text("结构操作已有执行步骤；请核对并重新读取，禁止自动重试。")).TextColor(p.Error)
		}
		if !x.loaded {
			ui.Box(c).Grow(1)
			return
		}
		if x.outcome != nil {
			ui.Scroll(c).MaxHeight(120).Children(func() {
				ui.Column(c).Gap(3).Children(func() {
					for i, step := range x.outcome.Steps {
						label := fmt.Sprintf("%d · %s", i+1, step.State)
						if step.Error != nil {
							label += " · " + step.Error.Error()
						}
						ui.Text(c, label).Tooltip(step.SQL)
					}
				})
			})
		}
		names := []string{locales.Text("字段"), locales.Text("索引"), locales.Text("外键"), locales.Text("检查"), locales.Text("触发器"), locales.Text("表选项"), locales.Text("注释"), locales.Text("原始 DDL")}
		selected := names[max(0, min(x.page, len(names)-1))]
		if ui.Select(c, &selected, names).Label(locales.Text("表设计子页")).Disabled(x.busy || x.blocked).Changed() {
			for i, n := range names {
				if n == selected {
					s.changeDesignPage(d, i)
				}
			}
		}
		switch x.page {
		case 5:
			s.designOptions(c, d)
		case 4:
			s.designTriggers(c, d)
		case 6:
			s.designComment(c, d)
		case 7:
			raw := x.original.DDL
			ui.TextArea(c, &raw).ReadOnly(true).Grow(1)
		default:
			s.designClauses(c, d)
		}
	})
	s.designModals(c, d)
}
func (s *State) designClauses(c *ui.Context, d *Document) {
	x := d.design
	accept := func(kind mysqlddl.Kind) bool {
		return x.page == 0 && kind == mysqlddl.Column || x.page == 1 && (kind == mysqlddl.Primary || kind == mysqlddl.Index) || x.page == 2 && kind == mysqlddl.Foreign || x.page == 3 && kind == mysqlddl.Check
	}
	ui.Row(c).Wrap().Gap(5).Padding(3, 8).Children(func() {
		for _, item := range []struct {
			label string
			kind  mysqlddl.Kind
		}{{"添加字段", mysqlddl.Column}, {"添加主键", mysqlddl.Primary}, {"添加索引", mysqlddl.Index}, {"添加外键", mysqlddl.Foreign}, {"添加检查约束", mysqlddl.Check}} {
			if !accept(item.kind) {
				continue
			}
			if ui.Button(c, locales.Text(item.label)).Disabled(x.busy || x.blocked).Clicked() {
				s.addDesignClause(d, item.kind)
			}
		}
	})
	ui.Row(c).Wrap().FillWidth().Gap(5).Children(func() {
		if ui.Button(c, locales.Text("删除定义")).Disabled(x.busy || x.blocked || x.row < 0 || x.row >= len(x.draft.Table.Clauses)).Clicked() {
			c := x.draft.Table.Clauses[x.row]
			unsupported := c.Kind == mysqlddl.Unknown
			if c.Kind != mysqlddl.Column && c.Kind != mysqlddl.Unknown {
				f, err := mysqlddl.ParseConstraint(c.SQL)
				unsupported = err != nil || f.Unsupported
			}
			if unsupported {
				d.err = "未支持的定义必须保持原样"
			} else {
				x.draft.Table.Clauses = append(x.draft.Table.Clauses[:x.row], x.draft.Table.Clauses[x.row+1:]...)
				x.formRow = -1
				x.form = mysqlddl.Field{}
				x.formBase = x.form
				x.constraint = nil
				x.row = min(x.row, len(x.draft.Table.Clauses)-1)
			}
		}
		for _, move := range []struct {
			label string
			delta int
		}{{"上移字段", -1}, {"下移字段", 1}} {
			if x.page != 0 {
				continue
			}
			if ui.Button(c, locales.Text(move.label)).Disabled(x.busy || x.blocked || x.row < 0 || x.row+move.delta < 0 || x.row+move.delta >= len(x.draft.Table.Clauses)).Clicked() {
				if s.applyDesignForms(d) {
					i, j := x.row, x.row+move.delta
					a, b := x.draft.Table.Clauses[i], x.draft.Table.Clauses[j]
					if a.Kind == mysqlddl.Column && b.Kind == mysqlddl.Column {
						x.draft.Table.Clauses[i], x.draft.Table.Clauses[j] = b, a
						x.row = j
						x.formRow = -1
					}
				}
			}
		}
	})
	var indices []int
	x.clauseRow = -1
	for i, clause := range x.draft.Table.Clauses {
		if accept(clause.Kind) {
			if i == x.row {
				x.clauseRow = len(indices)
			}
			indices = append(indices, i)
		}
	}
	x.list.Selected = &x.clauseRow
	theme.Table(c, &x.list, []ui.TableColumn{{Title: locales.Text("类型"), Width: 100}, {Title: locales.Text("名称"), Width: 150}, {Title: locales.Text("完整定义"), Width: 650}}, len(indices), func(row, col int) {
		v := x.draft.Table.Clauses[indices[row]]
		text := v.SQL
		if col == 0 {
			text = string(v.Kind)
		} else if col == 1 {
			text = v.Name
		}
		ui.Text(c, text).SingleLine().Tooltip(text)
	}).Grow(1).MinHeight(80).Shrink(1)
	if x.clauseRow >= 0 && x.clauseRow < len(indices) && x.row != indices[x.clauseRow] {
		if !s.applyDesignForms(d) {
			return
		}
		x.row = indices[x.clauseRow]
	}
	if x.row < 0 || x.row >= len(x.draft.Table.Clauses) {
		return
	}
	clause := &x.draft.Table.Clauses[x.row]
	if clause.Kind != mysqlddl.Column {
		if x.formRow >= 0 && !s.applyDesignForms(d) {
			x.row = x.formRow
			return
		}
		x.formRow = -1
		x.form = mysqlddl.Field{}
		x.formBase = x.form
		s.designConstraintView(c, d)
		return
	}
	if x.formRow != x.row {
		if !s.applyDesignForms(d) {
			x.row = x.formRow
			return
		}
		f, e := mysqlddl.ParseColumn(clause.SQL)
		if e != nil {
			d.err = e.Error()
			return
		}
		x.form, x.formBase = f, f
		x.formRow = x.row
		x.constraint = nil
	}
	if x.form.Unsupported {
		ui.Text(c, locales.Text("此字段含未支持的属性，保留完整原文并禁止修改。"))
		return
	}
	ui.Scroll(c).Height(0).Grow(1).MinHeight(100).MaxHeight(280).Children(func() {
		ui.Column(c).Padding(8).Gap(7).Children(func() {
			ui.Row(c).Wrap().Gap(10).Children(func() {
				ui.TextInput(c, &x.form.Name).Label(locales.Text("字段名称")).ReadOnly(x.busy || x.blocked)
				ui.TextInput(c, &x.form.Type).Label(locales.Text("字段类型")).ReadOnly(x.busy || x.blocked)
				ui.Checkbox(c, &x.form.Nullable, locales.Text("允许 NULL")).Disabled(x.busy || x.blocked)
				ui.Checkbox(c, &x.form.AutoIncrement, locales.Text("自动递增")).Disabled(x.busy || x.blocked)
			})
			ui.Row(c).Wrap().Gap(10).Children(func() {
				values := []string{"none", "null", "literal", "expression"}
				kind := string(x.form.DefaultKind)
				if ui.Select(c, &kind, values).Disabled(x.busy || x.blocked).Changed() {
					x.form.DefaultKind = mysqlddl.DefaultKind(kind)
				}
				ui.TextInput(c, &x.form.Default).Label(locales.Text("默认值或表达式")).ReadOnly(x.busy || x.blocked)
				ui.TextInput(c, &x.form.Comment).Label(locales.Text("字段注释")).ReadOnly(x.busy || x.blocked)
			})
			ui.Row(c).Wrap().Gap(10).Children(func() {
				ui.TextInput(c, &x.form.Charset).Label(locales.Text("字符集")).ReadOnly(x.busy || x.blocked)
				ui.TextInput(c, &x.form.Collation).Label(locales.Text("排序规则")).ReadOnly(x.busy || x.blocked)
			})
			ui.Row(c).Wrap().Gap(10).Children(func() {
				ui.TextInput(c, &x.form.Generation).Label(locales.Text("生成列表达式")).Grow(1).ReadOnly(x.busy || x.blocked)
				ui.Select(c, &x.form.Storage, []string{"", "VIRTUAL", "STORED"}).Disabled(x.busy || x.blocked)
			})
			if x.formError != "" {
				ui.Text(c, x.formError).TextColor(theme.Colors(c).Error)
			}
		})
	})
	if ui.Button(c, locales.Text("应用字段修改")).Disabled(x.busy || x.blocked).Clicked() {
		s.applyDesignForms(d)
	}
}
func (s *State) designOptions(c *ui.Context, d *Document) {
	x := d.design
	ui.Column(c).Grow(1).Padding(12).Gap(10).Children(func() {
		selected := x.optionName
		if ui.Select(c, &selected, []string{"ENGINE", "CHARSET", "COLLATE", "COMMENT", "AUTO_INCREMENT"}).Changed() && s.applyDesignForms(d) {
			x.optionName = selected
			x.optionValue = mysqlddl.Option(x.draft.Table.Options, x.optionName)
			x.optionBase = x.optionValue
		}
		ui.TextInput(c, &x.optionValue).Label(locales.Text("表选项值")).ReadOnly(x.busy || x.blocked)
		if ui.Button(c, locales.Text("应用表选项")).Disabled(x.busy || x.blocked).Clicked() {
			value, e := mysqlddl.SetOption(x.draft.Table.Options, x.optionName, x.optionValue)
			if e != nil {
				d.err = e.Error()
			} else {
				x.draft.Table.Options = value
				x.optionBase = x.optionValue
			}
		}
		raw := x.draft.Table.Options
		ui.TextArea(c, &raw).ReadOnly(true).Grow(1)
	})
}

func (s *State) designComment(c *ui.Context, d *Document) {
	x := d.design
	ui.Column(c).Grow(1).Padding(12).Gap(10).Children(func() {
		ui.TextArea(c, &x.optionValue).Label(locales.Text("表注释")).ReadOnly(x.busy || x.blocked).Grow(1)
		if x.formError != "" {
			ui.Text(c, x.formError).TextColor(theme.Colors(c).Error)
		}
		if ui.Button(c, locales.Text("应用表选项")).Disabled(x.busy || x.blocked).Clicked() {
			s.applyDesignForms(d)
		}
	})
}
func (s *State) designTriggers(c *ui.Context, d *Document) {
	x := d.design
	if x.original.TriggersUnknown {
		ui.Text(c, locales.Text("无法确认完整触发器权限；触发器修改已禁用。"))
		return
	}
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, locales.Text("添加触发器")).Disabled(x.busy || x.blocked).Clicked() {
			n := fmt.Sprintf("trigger_%d", len(x.draft.Triggers)+1)
			x.draft.Triggers = append(x.draft.Triggers, mysqlddl.Trigger{Name: n, SQL: "CREATE TRIGGER `" + n + "` BEFORE INSERT ON `" + strings.ReplaceAll(d.table, "`", "``") + "` FOR EACH ROW BEGIN\n\nEND"})
			x.triggerRow = len(x.draft.Triggers) - 1
		}
		if ui.Button(c, locales.Text("删除触发器")).Disabled(x.busy || x.blocked || x.triggerRow < 0 || x.triggerRow >= len(x.draft.Triggers)).Clicked() {
			x.draft.Triggers = append(x.draft.Triggers[:x.triggerRow], x.draft.Triggers[x.triggerRow+1:]...)
			x.triggerRow = min(x.triggerRow, len(x.draft.Triggers)-1)
		}
	})
	for i, t := range x.draft.Triggers {
		if ui.Button(c, t.Name).Clicked() {
			x.triggerRow = i
		}
	}
	if x.triggerRow < 0 || x.triggerRow >= len(x.draft.Triggers) {
		ui.Box(c).Grow(1)
		return
	}
	t := &x.draft.Triggers[x.triggerRow]
	ui.TextInput(c, &t.Name).Label(locales.Text("触发器名称")).ReadOnly(x.busy || x.blocked)
	ui.TextArea(c, &t.SQL).Label(locales.Text("完整触发器定义")).ReadOnly(x.busy || x.blocked).Grow(1)
}
func (s *State) designModals(c *ui.Context, d *Document) {
	x := d.design
	if x.guard {
		ui.Modal(c, &x.guard, func() {
			ui.Column(c).Width(460).Padding(18).Gap(12).Children(func() {
				ui.Text(c, locales.Text("保存表设计更改？"))
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("保存并预览")).Disabled(x.blocked).Clicked() {
						x.guard = false
						_ = s.PrepareDesign(d)
					}
					if ui.Button(c, locales.Text("放弃")).Clicked() {
						x.guard = false
						next := x.next
						x.next = nil
						x.draft = x.savedDraft.Clone()
						x.baseline = designSignature(x.draft)
						x.form = mysqlddl.Field{}
						x.formBase = x.form
						x.formRow = -1
						x.constraint = nil
						x.optionValue = mysqlddl.Option(x.draft.Table.Options, x.optionName)
						x.optionBase = x.optionValue
						if next != nil {
							next(c.Services())
						}
					}
					if ui.Button(c, locales.Text("取消")).Clicked() {
						x.guard = false
						x.next = nil
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
			ui.Column(c).Width(720).MaxWidthPercent(95).Padding(18).Gap(10).Children(func() {
				ui.Text(c, locales.Text("确认结构 SQL"))
				ui.Text(c, d.database+" / "+d.table)
				ui.Text(c, locales.Text("DDL 可能隐式提交；部分失败不会回滚已经完成的步骤。"))
				text := strings.Join(x.plan.Statements, ";\n\n")
				ui.TextArea(c, &text).ReadOnly(true).Height(300)
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("取消")).Clicked() {
						x.preview = false
						x.next = nil
					}
					if ui.PrimaryButton(c, locales.Text("确认执行结构 SQL")).Clicked() {
						next := x.next
						x.next = nil
						run := func(services ui.Services) {
							if d.closed {
								return
							}
							x.next = next
							s.executeDesign(d, services)
						}
						x.preview = false
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
