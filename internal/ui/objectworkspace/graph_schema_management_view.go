package objectworkspace

import (
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var graphSchemaActions = []struct{ action, label string }{{"create_index", "创建图索引"}, {"create_constraint", "创建唯一约束"}, {"drop_index", "删除图索引"}, {"drop_constraint", "删除图约束"}}

func (s *State) graphSchemaView(c *ui.Context, d *Document) {
	x := d.schemaManagement
	p := theme.Colors(c)
	ui.Column(c).Fill().MinHeight(0).Background(p.App).Children(func() {
		ui.Row(c).Shrink(0).Padding(2, 8).Children(func() {
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("读取完整图结构目录")).Disabled(!s.schemaReady(d) || x.pending || x.showingPreview).Clicked() {
					s.ReadGraphSchema(d, c.Services().Invalidate)
				}
				if ui.Button(c, locales.Text("预览图结构操作")).Disabled(!s.schemaReady(d) || x.pending || x.observation == nil || x.showingPreview).Clicked() {
					_ = s.PrepareGraphSchema(d)
				}
				if ui.Button(c, locales.Text("核验原图结构操作")).Disabled(!s.schemaReady(d) || !x.pending).Clicked() {
					s.VerifyGraphSchema(d, c.Services().Invalidate)
				}
				if d.loading && ui.Button(c, locales.Text("停止管理等待")).Disabled(x.cancelRequested).Clicked() {
					s.CancelGraphSchema(d)
				}
			}).Label(locales.Text("图索引与约束管理")).Grow(1).MinWidth(0).Gap(4)
		})
		ui.Row(c).Padding(2, 8).Shrink(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.Text("图索引与约束管理")+" · "+d.object.Catalog).SingleLine().Grow(1).MinWidth(0)
			if ui.Button(c, locales.Text("返回对象列表")).Clicked() && s.deps.Back != nil {
				if !s.guardGraphSchemaLeave(d, func(ui.Services) { s.deps.Back() }) {
					s.deps.Back()
				}
			}
		})
		message := d.err
		if d.loading {
			message = "正在等待原图结构管理请求…"
		}
		if x.cancelRequested {
			message = "正在等待原图结构管理请求停止…"
		}
		if x.abandoned {
			message = "此原操作已放弃，仅供查看；请关闭此页后重新管理"
		}
		if message != "" {
			ui.Text(c, locales.Text(message)).SingleLine().Tooltip(locales.Text(message)).TextColor(p.Error).Padding(2, 8).Shrink(0)
		}
		if x.pending {
			ui.Text(c, locales.Text("原操作尚未核验；保留草稿与回执，不可重放。核验只读取原会话。")).SingleLine().Tooltip(locales.Text("原操作尚未核验；保留草稿与回执，不可重放。核验只读取原会话。")).TextColor(p.Error).Padding(2, 8).Shrink(0)
		}
		ui.Tabs(c, &x.tab, locales.Text("管理草稿"), locales.Text("完整图结构目录"), locales.Text("原请求与核验证据")).FillWidth().Shrink(0)
		switch x.tab {
		case 0:
			s.graphSchemaFormView(c, d)
		case 1:
			s.graphSchemaTextView(c, d, graphSchemaCatalogText(x.catalog), "完整图结构目录")
		default:
			label := "未提交"
			switch x.last.State {
			case "completed":
				label = "收到原生回执，等待核验"
			case "verified":
				label = "原生完成回执与当前目录已核验"
			case "diverged":
				label = "原生操作已完成，当前目录与请求不一致"
			case "uncertain", "receipt_incomplete":
				label = "完成回执未确认，目录匹配也不能证明原操作完成"
			}
			ui.Text(c, locales.Text(label)).SingleLine().Tooltip(locales.Text(label)).Padding(2, 8).Shrink(0)
			ui.Text(c, locales.Text("CREATE 完成不代表索引 ONLINE；请查看目录中的状态与填充进度。")).SingleLine().Tooltip(locales.Text("CREATE 完成不代表索引 ONLINE；请查看目录中的状态与填充进度。")).Padding(2, 8).Shrink(0)
			s.graphSchemaTextView(c, d, graphSchemaEvidence(x), "原请求与核验证据")
		}
		ui.Row(c).Shrink(0).Padding(2, 8).Children(func() {
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("复制完整图结构预览")).Disabled(x.preview.Request.Query == "").Clicked() {
					c.WriteClipboard(graphSchemaJSON(x.preview))
				}
				if ui.Button(c, locales.Text("复制完整核验证据")).Clicked() {
					c.WriteClipboard(graphSchemaEvidence(x))
				}
			}).Label(locales.Text("图结构证据操作")).Grow(1).MinWidth(0).Gap(4)
		})
	})
	s.graphSchemaModals(c, d)
}
func (s *State) graphSchemaFormView(c *ui.Context, d *Document) {
	x := d.schemaManagement
	f := x.form
	editable := s.schemaReady(d) && !x.pending && !x.showingPreview
	ui.Scroll(c).Fill().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Padding(8).Gap(6).Children(func() {
			theme.Field(c, locales.Text("管理操作"), func() {
				choices := []string{}
				action := ""
				for _, a := range graphSchemaActions {
					choices = append(choices, locales.Text(a.label))
					if a.action == f.Action {
						action = locales.Text(a.label)
					}
				}
				ui.Select(c, &action, choices).Disabled(!editable).FillWidth().Label(locales.Text("管理操作"))
				for _, a := range graphSchemaActions {
					if action == locales.Text(a.label) {
						f.Action = a.action
					}
				}
			}).FillWidth()
			theme.Field(c, locales.Text("图结构名称"), func() {
				ui.TextInput(c, &f.Name).Disabled(!editable).FillWidth().Label(locales.Text("图结构名称"))
			}).FillWidth()
			if f.Action == "create_index" || f.Action == "create_constraint" {
				if f.Action == "create_constraint" {
					f.Type = "UNIQUE"
				} else if f.Type == "UNIQUE" {
					f.Type = "RANGE"
				}
				types := []string{"RANGE", "TEXT", "POINT", "FULLTEXT", "VECTOR"}
				if f.Action == "create_constraint" {
					types = []string{"UNIQUE"}
				}
				theme.Field(c, locales.Text("索引或约束类型"), func() {
					ui.Select(c, &f.Type, types).Disabled(!editable).FillWidth().Label(locales.Text("索引或约束类型"))
				}).FillWidth()
				theme.Field(c, locales.Text("图实体类型"), func() {
					ui.Select(c, &f.EntityType, []string{"NODE", "RELATIONSHIP"}).Disabled(!editable).FillWidth().Label(locales.Text("图实体类型"))
				}).FillWidth()
				fields := []struct {
					value *string
					label string
				}{{&f.LabelsJSON, "标签或关系类型 JSON 数组"}, {&f.PropertiesJSON, "有序属性 JSON 数组"}, {&f.OptionsJSON, "索引选项 JSON"}}
				ui.Text(c, locales.Text("标识符请填写 JSON 字符串数组；选项使用原生 JSON，空白表示服务器默认值。")).TextColor(theme.Colors(c).Muted)
				for _, field := range fields {
					theme.Field(c, locales.Text(field.label), func() { ui.TextInput(c, field.value).Disabled(!editable).FillWidth().Label(locales.Text(field.label)) }).FillWidth()
				}
			}
		})
	})
	if editable && f != x.form {
		s.SetGraphSchemaForm(d, f)
	}
}
func (s *State) graphSchemaTextView(c *ui.Context, d *Document, raw, label string) {
	preview, truncated := taskPreview(raw)
	if truncated {
		ui.Text(c, locales.Text("图结构预览已截断，可复制完整内容。")).SingleLine().Padding(2, 8).Shrink(0)
	}
	area := ui.TextArea(c.Key(fmt.Sprintf("graph-schema/%d/%d", d.id, d.schemaManagement.tab)), &preview).ReadOnly(true).Label(locales.Text(label)).Fill().MinHeight(0)
	seen := ui.Local(area, "graph-schema-text", func() string { return "" })
	if *seen != preview {
		area.SetTextSelection(0, 0)
		*seen = preview
	}
}
func (s *State) graphSchemaModals(c *ui.Context, d *Document) {
	x := d.schemaManagement
	if x.showingPreview && x.plan != nil {
		theme.Modal(c, &x.showingPreview, func() {
			width, height := c.Size()
			ui.Column(c).Width(min(float32(760), width-60)).MaxHeight(height - 60).Gap(8).Children(func() {
				ui.Text(c, locales.Text("图结构操作预览")).Bold()
				preview, truncated := taskPreview(graphSchemaPreviewText(x.preview))
				area := ui.TextArea(c.Key(fmt.Sprintf("graph-schema-preview/%d/%d", d.id, x.revision)), &preview).ReadOnly(true).Label(locales.Text("将提交的图结构请求")).Height(min(float32(220), height-340)).MinHeight(40)
				seen := ui.Local(area, "graph-schema-preview-text", func() string { return "" })
				if *seen != preview {
					area.SetTextSelection(0, 0)
					*seen = preview
				}
				if truncated {
					ui.Checkbox(c, &x.reviewed, locales.Text("已复制并检查完整图结构预览"))
				}
				if x.preview.NonAtomicDrop {
					ui.Text(c, locales.Text("删除按当前名称执行；外部同名替换可能被删除。预检和事务均不能原子保护原定义。")).TextColor(theme.Colors(c).Error)
					ui.Checkbox(c, &x.acceptDrop, locales.Text("接受按当前名称删除及同名替换风险"))
					theme.Field(c, locales.Text("输入原名称确认操作"), func() {
						ui.TextInput(c, &x.confirmation).FillWidth().Label(locales.Text("输入原名称确认操作"))
					}).FillWidth()
				}
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("复制完整图结构预览")).Clicked() {
						c.WriteClipboard(graphSchemaJSON(x.preview))
					}
					if ui.Button(c, locales.Text("返回编辑")).Clicked() {
						x.showingPreview = false
						x.plan = nil
						x.revision++
					}
					if ui.PrimaryButton(c, locales.Text("确认提交图结构操作")).Disabled(!s.CanSubmitGraphSchema(d)).Clicked() {
						s.SubmitGraphSchema(d, c.Services().Invalidate)
					}
				})
			})
		})
		if !x.showingPreview {
			x.plan = nil
		}
	}
	if x.guard {
		theme.Modal(c, &x.guard, func() {
			width, _ := c.Size()
			ui.Column(c).Width(min(float32(600), width-60)).Gap(10).Children(func() {
				ui.Text(c, locales.Text("保留原图结构草稿与证据？")).Bold()
				ui.Text(c, locales.Text("离开将放弃本地草稿与证据。已发送操作不会回滚，结果未知的原请求不能重放。"))
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					if ui.Button(c, locales.Text("继续处理图结构操作")).Clicked() {
						x.guard, x.next = false, nil
					}
					if d.loading && ui.Button(c, locales.Text("停止管理等待")).Disabled(x.cancelRequested).Clicked() {
						s.CancelGraphSchema(d)
					}
					if ui.Button(c, locales.Text("放弃原图结构草稿并离开")).Disabled(d.loading).Clicked() {
						next := x.next
						s.abandonGraphSchema(d)
						x.guard, x.next = false, nil
						if next != nil {
							next(c.Services())
						}
					}
				})
			})
		})
		if !x.guard {
			x.next = nil
		}
	}
}

// Present native column names and values; the separate evidence copy retains
// the original typed rows, namespace and completion metadata without projection.
func schemaDisplayValue(v contract.GraphValue) any {
	switch v.Kind {
	case "null":
		return nil
	case "string", "date", "datetime", "local_datetime", "time", "local_time":
		return v.Text
	case "integer", "float":
		if json.Valid([]byte(v.Text)) {
			return json.Number(v.Text)
		}
	case "boolean":
		return v.Text == "true"
	case "map":
		out := map[string]any{}
		for _, p := range v.Properties {
			out[p.Name] = schemaDisplayValue(p.Value)
		}
		return out
	case "list":
		out := make([]any, len(v.Values))
		for i, item := range v.Values {
			out[i] = schemaDisplayValue(item)
		}
		return out
	}
	return v
}
func graphSchemaCatalogText(c contract.GraphSchemaCatalog) string {
	rows := func(r contract.GraphResult) []map[string]any {
		out := make([]map[string]any, 0, len(r.Rows))
		for _, row := range r.Rows {
			entry := map[string]any{}
			for i, column := range r.Columns {
				if i < len(row) {
					entry[column] = schemaDisplayValue(row[i])
				}
			}
			out = append(out, entry)
		}
		return out
	}
	return graphSchemaJSON(struct{ Indexes, Constraints []map[string]any }{rows(c.Indexes), rows(c.Constraints)})
}

// Put the actual immutable native request first in the complete preview.
func graphSchemaPreviewText(p contract.GraphSchemaPreview) string {
	return graphSchemaJSON(struct {
		Request       contract.GraphSchemaRequest
		Before        []contract.GraphSchemaDefinition
		Draft         contract.GraphSchemaDraft
		NonAtomicDrop bool
	}{p.Request, p.Before, p.Draft, p.NonAtomicDrop})
}
