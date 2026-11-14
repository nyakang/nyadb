package kvworkspace

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func httpACLMutationImpactSummary(c *ui.Context, o contract.KVHTTPACLImpact) {
	ui.Text(c, fmt.Sprintf(locales.Text("原始读取 %d 次 · 引用 %d 项 · 依赖 %d 项"), len(o.Reads), len(o.References), len(o.Dependencies))).Wrap().FillWidth()
	if o.UpsertIdentity != "" {
		ui.Text(c, locales.Text("同名 AuthMethod 已存在：本次创建将更新原认证方式。")).Wrap().FillWidth().TextColor(theme.Colors(c).Warning)
	}
	ui.Text(c, locales.Text("确认后会重新读取并比较这些记录；内容变化将停止写入。读取与写入之间仍可能发生并发变化。")).Wrap().FillWidth()
	ui.Text(c, locales.Text("引用与依赖仅覆盖本次原连接的读取范围，不代表完整权限计算或其他数据中心的状态。")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
}

func (s *State) httpACLMutationImpactView(c *ui.Context, d *Document) {
	x, f := d.httpACLMutation, d.httpACLMutation.form
	ui.Text(c, locales.Text("ACL 影响预览与复核记录")).Bold()
	if x.error != "" {
		ui.Text(c, locales.Text("ACL 预览或复核未完成；原始记录保留，未授权新的写入。")).Wrap().FillWidth().TextColor(theme.Colors(c).Warning)
	}
	if o := x.observation; o != nil {
		ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), o.Scope.Namespace.ProfileID, o.Scope.Generation)).SingleLine()
		if f.impactPreview == "" {
			ui.Scroll(c).Fill().MinHeight(0).Children(func() {
				ui.Column(c).FillWidth().Gap(5).Children(func() {
					httpACLMutationImpactSummary(c, o.Evidence.Preview)
					ui.Text(c, o.Request.Resource+" · "+o.Request.Operation+" · "+o.Request.Identity).SingleLine()
					s.httpACLMutationReferenceView(c, d, o.Evidence.Preview)
					ui.Text(c, fmt.Sprintf(locales.Text("写前复核读取 %d 次"), len(o.Evidence.Recheck.Reads))).SingleLine()
				})
			})
			if ui.Button(c, locales.Text("显示 ACL 影响记录（含秘密）")).Clicked() {
				f.impactPreview = httpACLMutationImpactPreview(o.Evidence)
			}
		} else {
			ui.TextArea(c, &f.impactPreview).Label(locales.Text("ACL 影响原始记录")).ReadOnly(true).Fill().MinHeight(0)
			if ui.Button(c, locales.Text("隐藏 ACL 写入秘密")).Clicked() {
				f.impactPreview = ""
			}
		}
	}
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("复制完整 ACL 影响记录…")).Disabled(x.observation == nil || d.FileBusy()).Clicked() {
			s.prepareHTTPACLMutationTransfer(d, "copy-acl-write-impact")
		}
		if ui.Button(c, locales.Text("导出完整 ACL 影响记录…")).Disabled(x.observation == nil || d.FileBusy() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil).Clicked() {
			s.prepareHTTPACLMutationTransfer(d, "export-acl-write-impact")
		}
	}).FillWidth().MinWidth(0).Gap(5)
	back := locales.Text("返回 ACL 写入输入")
	if x.confirmation != nil {
		back = locales.Text("返回 ACL 写入确认")
	}
	if ui.Button(c, back).Clicked() {
		f.impactOpen = false
		f.receipt = false
		f.impactPreview = ""
	}
}

func httpACLMutationImpactPreview(e contract.KVHTTPACLMutationImpactEvidence) string {
	var text strings.Builder
	for stage, o := range []contract.KVHTTPACLImpact{e.Preview, e.Recheck} {
		for i, r := range o.Reads {
			fmt.Fprintf(&text, "%d · %d · %s · %s\nSHA-256: %s\n", stage+1, i+1, r.Result.Request.Resource, r.Result.Request.Lookup, r.BodySHA256)
			if h := r.Result.Receipt.HTTP; h != nil {
				text.Write(h.Body[:min(len(h.Body), previewLimit)])
			}
			text.WriteString("\n\n")
			if text.Len() > previewLimit {
				return clipped(text.String())
			}
		}
	}
	return clipped(text.String())
}

func (s *State) httpACLMutationReferenceView(c *ui.Context, d *Document, o contract.KVHTTPACLImpact) {
	f := d.httpACLMutation.form
	total := len(o.References) + len(o.Dependencies)
	if total == 0 {
		return
	}
	const pageSize = 12
	pages := (total + pageSize - 1) / pageSize
	f.impactPage = max(0, min(f.impactPage, pages-1))
	start, end := f.impactPage*pageSize, min((f.impactPage+1)*pageSize, total)
	for i := start; i < end; i++ {
		label := locales.Text("引用")
		var ref contract.KVHTTPACLImpactReference
		if i < len(o.References) {
			ref = o.References[i]
		} else {
			label = locales.Text("依赖")
			ref = o.Dependencies[i-len(o.References)]
		}
		text := label + " · " + ref.Resource + " · " + ref.Identity
		ui.Text(c, text).SingleLine().Tooltip(text)
	}
	ui.Row(c).FillWidth().Gap(5).Children(func() {
		if ui.Button(c, locales.Text("上一页")).Disabled(f.impactPage == 0).Clicked() {
			f.impactPage--
		}
		ui.Text(c, fmt.Sprintf("%d / %d", f.impactPage+1, pages)).SingleLine()
		if ui.Button(c, locales.Text("下一页")).Disabled(f.impactPage+1 >= pages).Clicked() {
			f.impactPage++
		}
	})
}
