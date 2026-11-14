package kvworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func httpCatalogRequestTool(kind contract.KVRequestToolKind) bool {
	switch kind {
	case contract.KVToolHTTPCatalogDatacenters, contract.KVToolHTTPCatalogNodes, contract.KVToolHTTPCatalogServices, contract.KVToolHTTPCatalogService, contract.KVToolHTTPCatalogNode:
		return true
	}
	return false
}

func (s *State) httpCatalogToolInputs(c *ui.Context, d *Document, kind contract.KVRequestToolKind, input *contract.KVRequestToolInput) {
	disabled := d.Busy() || d.FileBusy()
	a := &input.HTTPCatalog
	switch kind {
	case contract.KVToolHTTPCatalogService:
		ui.TextInput(c, &a.Service).Label(locales.Text("Catalog 原服务名")).Placeholder(locales.Text("Catalog 原服务名")).FillWidth().Disabled(disabled)
	case contract.KVToolHTTPCatalogNode:
		ui.TextInput(c, &a.Node).Label(locales.Text("Catalog 原节点名")).Placeholder(locales.Text("Catalog 原节点名")).FillWidth().Disabled(disabled)
	}
	if kind != contract.KVToolHTTPCatalogDatacenters {
		if ui.Checkbox(c, &a.IncludeFilter, locales.Text("包含原生 filter")).Disabled(disabled).Changed() && !a.IncludeFilter {
			a.Filter = ""
		}
		ui.TextInput(c, &a.Filter).Label(locales.Text("Catalog 原生 filter")).Placeholder(locales.Text("Catalog 原生 filter")).FillWidth().Disabled(disabled || !a.IncludeFilter)
		if kind == contract.KVToolHTTPCatalogNodes || kind == contract.KVToolHTTPCatalogService {
			if ui.Checkbox(c, &a.IncludeNear, locales.Text("包含原生 near")).Disabled(disabled).Changed() && !a.IncludeNear {
				a.Near = ""
			}
			ui.TextInput(c, &a.Near).Label(locales.Text("Catalog 原生 near")).Placeholder(locales.Text("Catalog 原生 near")).FillWidth().Disabled(disabled || !a.IncludeNear)
		}
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, "index").SingleLine()
			ui.TextInput(c, &a.Index).Label(locales.Text("原生 index（留空省略）")).Grow(1).MinWidth(0).Disabled(disabled)
			ui.Text(c, "wait").SingleLine()
			ui.TextInput(c, &a.Wait).Label(locales.Text("原生 wait（需要 index）")).Grow(1).MinWidth(0).Disabled(disabled)
		})
	}
	ui.Text(c, locales.Text("使用原连接的 DC 与一致性配置；Catalog 读取不使用键前缀")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
	ui.Text(c, locales.Text("只生成 Catalog 读取请求；运行后查看原响应")).Wrap().FillWidth().TextColor(theme.Colors(c).Muted)
}
