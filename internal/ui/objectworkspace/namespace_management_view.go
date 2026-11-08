package objectworkspace

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var namespaceActions = []string{"namespace_create", "namespace_primary_key", "namespace_rename", "namespace_swap", "namespace_clear", "namespace_delete"}

func namespaceActionText(action string) string {
	switch action {
	case "namespace_create":
		return "创建索引"
	case "namespace_primary_key":
		return "修改索引主键"
	case "namespace_rename":
		return "重命名索引"
	case "namespace_swap":
		return "原子交换两个索引"
	case "namespace_clear":
		return "清空索引文档"
	case "namespace_delete":
		return "删除索引"
	}
	return "管理索引"
}
func (s *State) namespaceManagementModal(c *ui.Context, d *Document) {
	x := &d.tasks.namespace
	if !x.open {
		return
	}
	theme.Modal(c, &x.open, func() {
		width, height := c.Size()
		ui.Column(c).Width(min(float32(800), width-60)).MaxHeight(height - 60).Gap(8).Children(func() {
			ui.Text(c, locales.Text("管理索引")).Bold()
			ui.Text(c, locales.Text("先读取原索引信息，再预览并确认提交；操作通过原服务器任务完成。")).TextColor(theme.Colors(c).Muted)
			choices := []string{}
			for _, a := range namespaceActions {
				if s.NamespaceManagementSupported(d.object, a) {
					choices = append(choices, locales.Text(namespaceActionText(a)))
				}
			}
			label := locales.Text(namespaceActionText(x.action))
			ui.Select(c, &label, choices).Label(locales.Text("索引操作")).FillWidth().Disabled(d.loading)
			for _, a := range namespaceActions {
				if locales.Text(namespaceActionText(a)) == label && x.action != a {
					x.action = a
					x.records = nil
					x.confirmation = ""
				}
			}
			ui.TextInput(c.Key(fmt.Sprintf("namespace-uid/%d", d.id)), &x.uid).Label(locales.Text("索引 UID")).FillWidth().ReadOnly(d.loading || d.object.Catalog != "")
			if x.action == "namespace_rename" || x.action == "namespace_swap" {
				ui.TextInput(c.Key(fmt.Sprintf("namespace-destination/%d", d.id)), &x.destination).Label(locales.Text("目标索引 UID")).FillWidth().ReadOnly(d.loading)
			}
			if x.action == "namespace_create" || x.action == "namespace_primary_key" {
				ui.TextInput(c.Key(fmt.Sprintf("namespace-pk/%d", d.id)), &x.primaryKey).Label(locales.Text("主键字段（创建时可留空）")).FillWidth().ReadOnly(d.loading)
				ui.Text(c, locales.Text("修改主键要求索引为空；不支持移除已有主键。")).TextColor(theme.Colors(c).Muted)
			}
			if x.action == "namespace_delete" || x.action == "namespace_clear" {
				ui.Text(c, locales.Text("清空会删除全部文档并保留设置；删除索引会同时删除文档与设置。")).TextColor(theme.Colors(c).Error)
			}
			if x.action == "namespace_swap" {
				ui.Text(c, locales.Text("交换会原子交换文档、设置与主键，索引 UID 保持不变；历史任务的索引归属也会交换。")).TextColor(theme.Colors(c).Muted)
			}
			if d.err != "" {
				ui.Text(c, locales.Text(d.err)).SingleLine().Tooltip(locales.Text(d.err)).TextColor(theme.Colors(c).Error)
			}
			if len(x.records) > 0 {
				for _, r := range x.records {
					text := fmt.Sprintf(locales.Text("%s · 文档数 %d · 主键 %s"), r.Ref.Catalog, r.Documents, r.PrimaryKey)
					if !r.Exists {
						text = r.Ref.Catalog + " · " + locales.Text("索引不存在")
					}
					ui.Text(c, text).SingleLine().Tooltip(text)
				}
			}
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("返回")).Disabled(d.loading).Clicked() {
					x.open = false
				}
				if d.loading && ui.Button(c, locales.Text("停止索引信息读取")).Disabled(d.tasks.cancelRequested).Clicked() {
					s.CancelTasks(d)
				}
				if ui.Button(c, locales.Text("读取原索引信息")).Disabled(!s.CanManageNamespace(d, x.action)).Clicked() {
					s.ReadNamespaceManagement(d, c.Services().Invalidate)
				}
				if ui.PrimaryButton(c, locales.Text("预览索引操作")).Disabled(!s.CanManageNamespace(d, x.action) || len(x.records) == 0 || x.draft != s.namespaceDraft(d)).Clicked() {
					_ = s.PrepareNamespaceManagement(d)
				}
			})
		})
	})
}
