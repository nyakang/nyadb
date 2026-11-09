package sqlworkspace

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/graphresult"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) graphExpansionControls(c *ui.Context, d *Document, g *graphVisualState) {
	if ui.Button(c, locales.Text("扩展邻居")).Disabled(!s.canExpandGraph(d)).Tooltip(locales.Text("使用原会话只读查询最多 200 行邻居；扩展记录与原始查询分开保留")).Clicked() {
		s.expandGraph(d)
	}
	if d.graphExpansion != nil && ui.Button(c, locales.Text("取消扩展")).Disabled(d.graphExpansion.canceled).Clicked() {
		s.cancelGraphExpansion(d)
	}
	if len(g.observations) == 0 {
		return
	}
	ui.Button(c, fmt.Sprintf(locales.Text("扩展记录 · %d"), len(g.observations))).Menu(func(m *ui.Menu) {
		for index, o := range g.observations {
			if m.Item(fmt.Sprintf("%d · %s", index+1, graphLabel(o.Node, 40))).Chosen() {
				g.observationIndex, g.inspectorMode, g.fieldJSON = index, "expansion", ""
			}
		}
	})
	if ui.Button(c, locales.Text("导出扩展 JSON")).Disabled(s.persistence.files.SavePath == nil || s.persistence.files.WriteStream == nil).Clicked() {
		s.exportGraphObservations(d)
	}
}
func (s *State) graphExpansionNotices(c *ui.Context, d *Document, g *graphVisualState) {
	notice := ""
	if d.graphExpansion != nil {
		notice = locales.Text("正在读取邻居；此前图形仍可浏览")
		c.AnimationFrame()
	}
	if g.expansionError != "" {
		notice = g.expansionError
	} else if len(g.observations) > 0 {
		last := g.observations[len(g.observations)-1]
		if !last.PayloadRetained {
			notice = last.Error
		} else if last.Limited {
			notice = locales.Text("邻居查询达到保留上限；可能还有未返回的邻居")
		}
	}
	if g.observationBytes >= graphExpansionBytes-(32<<10) || len(g.observations) >= graphresult.TopologyMaxNodes {
		notice = locales.Text("邻居观察达到保留预算；已有记录仍可查看和导出")
	}
	// Keep the canvas size stable while a request starts/finishes so admitting
	// neighbors does not trigger the resize/fit camera policy.
	ui.Row(c).FillWidth().Height(theme.RowHeight(c, 20)).Children(func() {
		if notice != "" {
			ui.Text(c, notice).SingleLine().Tooltip(notice).TextColor(theme.Colors(c).Warning)
		}
	})
}
