package sqlworkspace

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"maps"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (g *graphVisualState) focus(key string) {
	var point ui.Point
	if n := g.node(key); n != nil {
		point = g.positions[key]
		delete(g.hidden, key)
	} else if e := g.edge(key); e != nil {
		a, b := g.positions[e.Source], g.positions[e.Target]
		point = ui.Point{X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2}
		delete(g.hidden, e.Key)
		delete(g.hidden, e.Source)
		delete(g.hidden, e.Target)
	} else {
		return
	}
	g.selectKey(key)
	g.zoom = max(.65, g.zoom)
	g.pan = ui.Point{X: g.width/2 - point.X*g.zoom, Y: g.height/2 - point.Y*g.zoom}
	g.fit = false
}
func (g *graphVisualState) entries() []string {
	keys := []string{}
	accept := func(key string) bool { return strings.TrimSpace(g.query) == "" || g.matches(key) }
	for _, n := range g.topology.Nodes {
		if g.visibleNode(n.Key) && accept(n.Key) {
			keys = append(keys, n.Key)
		}
	}
	for _, e := range g.topology.Edges {
		if g.visibleEdge(e) && accept(e.Key) {
			keys = append(keys, e.Key)
		}
	}
	return keys
}
func (g *graphVisualState) cloneReading(other *graphVisualState) {
	if other == nil {
		return
	}
	for _, observation := range other.observations {
		observation.Result = observation.Result.Clone()
		g.observations = append(g.observations, observation)
	}
	g.observationBytes, g.observationIndex = other.observationBytes, other.observationIndex
	g.propertyEntities = make(map[string]contract.GraphValue, len(other.propertyEntities))
	for key, value := range other.propertyEntities {
		g.propertyEntities[key] = value.Clone()
	}
	g.expansionError = other.expansionError
	g.rebuildObservations()
	g.positions = maps.Clone(other.positions)
	g.hidden = maps.Clone(other.hidden)
	g.pinned = maps.Clone(other.pinned)
	g.selected, g.query, g.records = other.selected, other.query, other.records
	g.zoom, g.pan = other.zoom, other.pan
	g.width, g.height, g.fit = other.width, other.height, other.fit
	g.inspectorMode, g.fieldIndex, g.propertyPage = other.inspectorMode, other.fieldIndex, other.propertyPage
}
func (g *graphVisualState) detailJSON() string {
	if g.detail != "" {
		return g.detail
	}
	var value any
	if n := g.node(g.selected); n != nil {
		value = n.Value
	} else if e := g.edge(g.selected); e != nil {
		value = e.Value
	} else {
		return ""
	}
	raw, _ := json.MarshalIndent(value, "", "  ")
	if len(raw) > 256<<10 {
		raw = raw[:256<<10]
		for !utf8.Valid(raw) {
			raw = raw[:len(raw)-1]
		}
		raw = append(raw, []byte("\n"+locales.Text("展示限制 256 KiB；完整类型化结果保留在当前文档"))...)
	}
	g.detail = string(raw)
	return g.detail
}
func (s *State) graphVisualView(c *ui.Context, d *Document) bool {
	g := d.graphVisualState()
	if g == nil {
		return false
	}
	palette := theme.Colors(c)
	container := ui.Column(c.Key("graph-visual")).FillWidth().MinHeight(0)
	if g.records {
		container.Height(theme.RowHeight(c, 240)).Shrink(0)
	} else {
		container.Grow(1)
	}
	container.Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, 34)).Gap(5).Padding(2, 6).Children(func() {
			input := ui.TextInput(c, &g.query).Label(locales.Text("图搜索")).Placeholder(locales.Text("图搜索")).MaxLength(1024).Width(160).MinWidth(80).Shrink(1)
			if input.Changed() {
				g.listPage = 0
			}
			if ui.Button(c, locales.Text("定位匹配")).Disabled(len(g.entries()) == 0).Clicked() {
				keys := g.entries()
				index := 0
				for i, key := range keys {
					if key == g.selected {
						index = (i + 1) % len(keys)
						break
					}
				}
				g.focus(keys[index])
			}
			nodes, edges := 0, 0
			for _, n := range g.topology.Nodes {
				if g.visibleNode(n.Key) {
					nodes++
				}
			}
			for _, e := range g.topology.Edges {
				if g.visibleEdge(e) {
					edges++
				}
			}
			ui.Text(c, fmt.Sprintf(locales.Text("%d 节点 · %d 关系"), nodes, edges)).SingleLine().MinWidth(0).Grow(1)
		})
		ui.ScrollHorizontal(c).Height(theme.RowHeight(c, 32)).Children(func() {
			ui.Row(c).Gap(4).Padding(2, 6).Children(func() {
				if ui.Button(c, "+").Label(locales.Text("放大图形")).Clicked() {
					g.zoomAt(1.2, ui.Point{X: g.width / 2, Y: g.height / 2})
				}
				if ui.Button(c, "−").Label(locales.Text("缩小图形")).Clicked() {
					g.zoomAt(1/1.2, ui.Point{X: g.width / 2, Y: g.height / 2})
				}
				if ui.Button(c, locales.Text("适应画布")).Clicked() {
					g.fit = true
				}
				if ui.Button(c, locales.Text("重新布局")).Clicked() {
					g.layout()
				}
				if theme.PageButton(c, locales.Text("图记录"), g.records).Clicked() {
					g.records = !g.records
					g.dragging = false
				}
				if ui.Button(c, locales.Text("恢复隐藏图元")).Disabled(len(g.hidden) == 0).Clicked() {
					g.resetHidden()
				}
				s.graphExpansionControls(c, d, g)
				s.graphPropertyControls(c, d)
				ui.Button(c, locales.Text("导出图像")).Menu(func(m *ui.Menu) {
					for _, format := range []string{"PNG", "SVG"} {
						if m.Item(format).Disabled(len(g.topology.Nodes) == 0 || s.persistence.files.SavePath == nil || s.persistence.files.WriteStream == nil).Chosen() {
							s.exportGraphImage(d, palette, format)
						}
					}
				})
			})
		})
		s.graphExpansionNotices(c, d, g)
		if g.topology.Bounded {
			ui.Text(c, locales.Text("图形展示达到上限；完整保留结果仍可查看和导出")).SingleLine().Tooltip(locales.Text("图形上限：1000 节点、2000 关系、8 MiB；不查询额外数据")).TextColor(palette.Warning)
		}
		if g.topology.Unidentified {
			ui.Text(c, locales.Text("部分图元缺少完整身份；请查看原生结果")).SingleLine().TextColor(palette.Warning)
		}
		ui.Row(c).FillWidth().Height(theme.RowHeight(c, 20)).Children(func() {
			if len(g.propertyEntities) > 0 {
				ui.Text(c, locales.Text("图中包含保存或核对时的原生观察；原始查询记录保持不变")).SingleLine().Tooltip(locales.Text("图中包含保存或核对时的原生观察；原始查询记录保持不变")).TextColor(palette.Muted)
			}
		})
		ui.Row(c).FillWidth().Grow(1).MinHeight(0).Gap(1).Children(func() {
			canvas := ui.Box(c.Key("graph-canvas")).Label(locales.Text("Neo4j 图画布")).FillHeight().Grow(1).MinWidth(150).MinHeight(0).Focusable().HandleInput(g.input)
			if x, y, over := canvas.PointerPosition(); over {
				p := ui.Point{X: x, Y: y}
				key := g.hitNode(p)
				if key == "" {
					key = g.hitEdge(p)
				}
				if n := g.node(key); n != nil {
					canvas.Tooltip(graphLabel(n.Value.ElementID+" · "+strings.Join(n.Value.Labels, ", "), 256))
				} else if e := g.edge(key); e != nil {
					canvas.Tooltip(graphLabel(e.Value.Type+" · "+e.Value.ElementID, 256))
				}
			}
			canvas.Draw(func(p *ui.Painter, r ui.Rect) {
				if r.W != g.width || r.H != g.height {
					g.fit = true
				}
				g.width, g.height = r.W, r.H
				if g.fit {
					g.fitView(r.W, r.H)
				}
				paintGraph(p, r, g.shape(palette))
			})
			ui.Column(c).FillHeight().Width(250).MinWidth(180).Shrink(0).MinHeight(0).Background(palette.Sidebar).Border(1, palette.Border).Children(func() { s.graphInspector(c, d, g) })
		})
	})
	return !g.records
}
func (s *State) graphInspector(c *ui.Context, d *Document, g *graphVisualState) {
	palette := theme.Colors(c)
	if g.inspectorMode == "expansion" {
		s.graphPropertiesInspector(c, d, g)
		return
	}
	if g.selected == "" {
		keys := g.entries()
		g.listPage = max(0, min(g.listPage, max(0, (len(keys)-1)/50)))
		ui.Row(c).Padding(4).Gap(4).Children(func() {
			ui.Text(c, locales.Text("图元列表")).Grow(1)
			if ui.Button(c, "‹").Label(locales.Text("上一页")).Disabled(g.listPage == 0).Clicked() {
				g.listPage--
			}
			if ui.Button(c, "›").Label(locales.Text("下一页")).Disabled((g.listPage+1)*50 >= len(keys)).Clicked() {
				g.listPage++
			}
		})
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Gap(2).Padding(4).Children(func() {
				for _, key := range keys[g.listPage*50 : min(len(keys), (g.listPage+1)*50)] {
					label := key
					if n := g.node(key); n != nil {
						label = graphTitle(*n)
					} else if e := g.edge(key); e != nil {
						label = graphLabel(e.Value.Type+" · "+e.Value.ElementID, 48)
					}
					if ui.Button(c.Key(key), label).Label(key).Tooltip(graphLabel(key, 256)).FillWidth().Clicked() {
						g.focus(key)
					}
				}
				if len(keys) == 0 {
					ui.Text(c, locales.Text("没有匹配的保留图元")).TextColor(palette.Muted)
				}
			})
		})
		return
	}
	title := g.selected
	missing, conflict := false, false
	if n := g.node(g.selected); n != nil {
		title = graphTitle(*n)
		missing, conflict = n.Missing, n.Conflicting
	} else if e := g.edge(g.selected); e != nil {
		title = graphLabel(e.Value.Type, 48)
		conflict = e.Conflicting
	}
	ui.Row(c).Padding(4).Gap(3).Children(func() {
		ui.Text(c, title).Bold().SingleLine().Tooltip(g.selected).Grow(1).MinWidth(0)
		if ui.Button(c, "×").Label(locales.Text("清除图选择")).Clicked() {
			g.selectKey("")
		}
	})
	ui.Row(c).Padding(3).Gap(4).Children(func() {
		if theme.PageButton(c, locales.Text("固定节点"), g.pinned[g.selected]).Disabled(g.node(g.selected) == nil).Clicked() {
			g.pinned[g.selected] = !g.pinned[g.selected]
		}
		if ui.Button(c, locales.Text("隐藏所选图元")).Clicked() {
			g.hideSelected()
		}
	})
	if missing {
		ui.Text(c, locales.Text("端点节点未包含在保留结果中")).SingleLine().Tooltip(locales.Text("端点节点未包含在保留结果中")).TextColor(palette.Warning)
	}
	if conflict {
		ui.Text(c, locales.Text("同一图元存在不同观察值；显示首次观察，请定位原始记录")).SingleLine().Tooltip(locales.Text("同一图元存在不同观察值；显示首次观察，请定位原始记录")).TextColor(palette.Warning)
	}
	s.graphPropertiesInspector(c, d, g)
	refs := g.cells()
	ui.Button(c, locales.Text("定位原始记录")).Disabled(len(refs) == 0).Menu(func(m *ui.Menu) {
		for _, ref := range refs[:min(50, len(refs))] {
			column := fmt.Sprint(ref.Column + 1)
			if ref.Column < len(d.result.Graph.Columns) {
				column = d.result.Graph.Columns[ref.Column]
			}
			if m.Item(fmt.Sprintf(locales.Text("行 %d / %s"), ref.Row+1, column)).Chosen() {
				d.graphRecord(ref)
			}
		}
	})
	if observed := g.observationRefs(); len(observed) > 0 {
		ui.Button(c, locales.Text("定位扩展记录")).Menu(func(m *ui.Menu) {
			for _, ref := range observed[:min(50, len(observed))] {
				if m.Item(fmt.Sprintf(locales.Text("扩展 %d / 行 %d / 列 %d"), ref.Observation+1, ref.Row+1, ref.Column+1)).Chosen() {
					g.observationIndex, g.inspectorMode, g.fieldJSON = ref.Observation, "expansion", ""
				}
			}
		})
	}
}
func (d *Document) graphRecord(ref graphresult.CellRef) {
	if d.result == nil || d.result.Graph == nil || ref.Row < 0 || ref.Row >= len(d.result.Graph.Rows) || ref.Column < 0 || ref.Column >= len(d.result.Graph.Rows[ref.Row]) {
		return
	}
	d.selectedRow = ref.Row
	d.selection.Clear()
	d.selection.Add(ref.Row)
	key := fmt.Sprintf("sql_%d", ref.Column)
	if d.hiddenResultColumns == nil {
		d.hiddenResultColumns = map[string]bool{}
	}
	d.hiddenResultColumns[key] = false
	d.grid.Cells = ui.ColumnRange{Cursor: key, Anchor: key}
	d.grid.ScrollIntoView(ref.Row)
	d.graphVisualState().records = true
}
func (g *graphVisualState) keyForCell(row, column int, edgeFirst bool) string {
	ref := graphresult.CellRef{Row: row, Column: column}
	node := func() string {
		for _, n := range g.topology.Nodes {
			for _, cell := range n.Cells {
				if cell == ref {
					return n.Key
				}
			}
		}
		return ""
	}
	edge := func() string {
		for _, e := range g.topology.Edges {
			for _, cell := range e.Cells {
				if cell == ref {
					return e.Key
				}
			}
		}
		return ""
	}
	if edgeFirst {
		if key := edge(); key != "" {
			return key
		}
		return node()
	}
	if key := node(); key != "" {
		return key
	}
	return edge()
}
func (d *Document) graphCellKey(row, column int) string {
	set := selectedResult(d)
	if len(set.OriginColumns) == len(set.Columns) && column >= 0 && column < len(set.OriginColumns) {
		column = set.OriginColumns[column]
	}
	if d.result == nil || d.result.Graph == nil || row < 0 || row >= len(d.result.Graph.Rows) || column < 0 || column >= len(d.result.Graph.Rows[row]) {
		return ""
	}
	g := d.graphVisualState()
	if g == nil {
		return ""
	}
	return g.keyForCell(row, column, d.result.Graph.Rows[row][column].Kind == "relationship")
}
func (d *Document) graphLocate(row, column int) bool {
	key := d.graphCellKey(row, column)
	if key == "" {
		return false
	}
	d.graphVisualState().focus(key)
	d.graphVisualOpen = true
	d.graphNative = false
	return true
}
func (s *State) exportGraphImage(d *Document, palette theme.Palette, format string) {
	if d == nil || d.closed || d.result == nil || d.result.Graph == nil {
		return
	}
	g := d.graphVisualState()
	f := s.persistence.files
	if g == nil || f.SavePath == nil || f.WriteStream == nil {
		return
	}
	// Capture only owned display geometry on the UI thread. Rendering and writing
	// cannot borrow live topology, camera state, document or a native session.
	copy := *g
	copy.fitView(1280, 800)
	shape := copy.shape(palette)
	name := d.Name() + "-graph." + strings.ToLower(format)
	s.startIO(d, nil, func() ioResult {
		path, e := f.SavePath(name)
		if e != nil || path == "" {
			return ioResult{kind: "export", err: e}
		}
		e = f.WriteStream(path, func(w io.Writer) error {
			switch format {
			case "SVG":
				_, e := io.WriteString(w, graphSVG(shape, 1280, 800))
				return e
			case "PNG":
				img := ui.Render(func(c *ui.Context) { ui.Box(c).Fill().Draw(func(p *ui.Painter, r ui.Rect) { paintGraph(p, r, shape) }) }, 1280, 800, 2)
				return png.Encode(w, img)
			default:
				return fmt.Errorf("unsupported graph image format: %s", format)
			}
		})
		return ioResult{kind: "export", err: e}
	})
}

type graphDetailReading struct {
	Owner *graphVisualState
	Key   string
}

func (g *graphVisualState) properties() []contract.GraphProperty {
	if n := g.node(g.selected); n != nil {
		return n.Value.Properties
	}
	if e := g.edge(g.selected); e != nil {
		return e.Value.Properties
	}
	return nil
}
func graphPropertyPreview(v contract.GraphValue) string {
	switch v.Kind {
	case "null":
		return "NULL"
	case "bytes":
		encoded := v.Text
		maxEncoded := min(len(encoded), 44)
		raw, err := base64.StdEncoding.DecodeString(encoded[:maxEncoded])
		if err != nil {
			return "bytes"
		}
		text := "0x" + hex.EncodeToString(raw)
		if len(encoded) > maxEncoded {
			text += "…"
		}
		return text
	case "list":
		return fmt.Sprintf("list [%d]", len(v.Values))
	case "map":
		return fmt.Sprintf("map [%d]", len(v.Properties))
	}
	if v.Text != "" {
		return graphLabel(v.Text, 96)
	}
	parts := []string{}
	for _, p := range v.Properties[:min(6, len(v.Properties))] {
		parts = append(parts, graphLabel(p.Name, 24)+": "+graphLabel(p.Value.Text, 32))
	}
	return strings.Join(parts, ", ")
}
func boundedGraphJSON(value any) string {
	raw, _ := json.MarshalIndent(value, "", "  ")
	if len(raw) > 256<<10 {
		raw = raw[:256<<10]
		for !utf8.Valid(raw) {
			raw = raw[:len(raw)-1]
		}
		raw = append(raw, []byte("\n"+locales.Text("展示限制 256 KiB；完整类型化结果保留在当前文档"))...)
	}
	return string(raw)
}
func (s *State) graphPropertiesInspector(c *ui.Context, d *Document, g *graphVisualState) {
	if g.inspectorMode != "" {
		if ui.Button(c, locales.Text("返回图属性")).Clicked() {
			g.inspectorMode = ""
			g.fieldJSON = ""
			c.AnimationFrame()
		}
		text := g.detailJSON()
		label := locales.Text("当前图元的原生 JSON")
		if g.inspectorMode == "expansion" {
			text = g.observationDetail()
			label = locales.Text("邻居扩展原生记录 JSON")
		}
		if g.inspectorMode == "field" {
			props := g.properties()
			if g.fieldIndex >= 0 && g.fieldIndex < len(props) {
				if g.fieldJSON == "" {
					g.fieldJSON = boundedGraphJSON(props[g.fieldIndex].Value)
				}
				text = g.fieldJSON
			}
		}
		area := ui.TextArea(c, &text).Label(label).ReadOnly(true).Grow(1).MinHeight(0)
		reading := ui.Local(area, "graph-detail-reading", func() graphDetailReading { return graphDetailReading{} })
		key := g.selected + "/" + g.inspectorMode + "/" + fmt.Sprint(g.fieldIndex) + "/" + fmt.Sprint(g.observationIndex)
		if reading.Owner != g || reading.Key != key {
			area.SetTextSelection(0, 0)
			*reading = graphDetailReading{Owner: g, Key: key}
		}
		return
	}
	props := g.properties()
	g.propertyPage = max(0, min(g.propertyPage, max(0, (len(props)-1)/25)))
	ui.Row(c).Padding(3).Gap(3).Children(func() {
		ui.Text(c, fmt.Sprintf("%s · %d", locales.Text("属性"), len(props))).Grow(1)
		if ui.Button(c, "‹").Label(locales.Text("上一页")).Disabled(g.propertyPage == 0).Clicked() {
			g.propertyPage--
		}
		if ui.Button(c, "›").Label(locales.Text("下一页")).Disabled((g.propertyPage+1)*25 >= len(props)).Clicked() {
			g.propertyPage++
		}
	})
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Padding(4).Gap(4).Children(func() {
			for index := g.propertyPage * 25; index < min(len(props), (g.propertyPage+1)*25); index++ {
				p := props[index]
				ui.Column(c.Key(index)).FillWidth().Border(1, theme.Colors(c).Border).Padding(4).Gap(2).Children(func() {
					ui.Row(c).Gap(3).Children(func() {
						ui.Text(c, p.Name).Bold().SingleLine().Tooltip(p.Name).Grow(1).MinWidth(0)
						ui.Text(c, p.Value.Kind).FontSize(10).TextColor(theme.Colors(c).Muted)
					})
					ui.Text(c, graphPropertyPreview(p.Value)).SingleLine().Selectable()
					if ui.Button(c, locales.Text("完整属性值")).Label(p.Name + " " + locales.Text("完整属性值")).Clicked() {
						g.fieldIndex = index
						g.fieldJSON = ""
						g.inspectorMode = "field"
						c.AnimationFrame()
					}
					if ui.Button(c, locales.Text("编辑图属性")).Label(p.Name + " " + locales.Text("编辑图属性")).Tooltip(locales.Text("仅支持已有字符串、布尔值、整数和有限浮点属性；失效会话、冲突观察和快照不可编辑")).Disabled(!s.canEditGraphProperty(d, g, p)).Clicked() {
						s.openGraphPropertyDraft(d, g, p)
					}
				})
			}
		})
	})
	if ui.Button(c, locales.Text("图元原生 JSON")).Clicked() {
		g.inspectorMode = "entity"
		c.AnimationFrame()
	}
}
