package sqlworkspace

import (
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
)

type graphVisualState struct {
	propertyPage, fieldIndex         int
	inspectorMode, fieldJSON         string
	matchQuery                       string
	matchKeys                        map[string]bool
	closed                           bool
	listPage                         int
	topology                         graphresult.Topology
	base                             *contract.GraphResult
	observations                     []graphObservation
	observationBytes                 int
	observationIndex                 int
	expansionError                   string
	positions                        map[string]ui.Point
	hidden, pinned                   map[string]bool
	selected, query, detail          string
	records                          bool
	zoom                             float32
	pan                              ui.Point
	width, height                    float32
	fit                              bool
	dragging                         bool
	dragNode                         string
	dragStart, dragPosition, dragPan ui.Point
}

func (d *Document) graphVisualState() *graphVisualState {
	if d.closed {
		return nil
	}
	if d.graphVisual == nil && d.result != nil && d.result.Graph != nil {
		g := &graphVisualState{base: d.result.Graph, topology: graphresult.ExtractTopology(*d.result.Graph), hidden: map[string]bool{}, pinned: map[string]bool{}, positions: map[string]ui.Point{}, zoom: 1, fit: true}
		g.layout()
		d.graphVisual = g
	}
	return d.graphVisual
}
func graphNodeTitle(n graphresult.TopologyNode) string {
	for _, name := range []string{"name", "title", "label"} {
		for _, p := range n.Value.Properties {
			if strings.EqualFold(p.Name, name) && p.Value.Kind == "string" && p.Value.Text != "" {
				return p.Value.Text
			}
		}
	}
	return n.Value.ElementID
}
func graphTitle(n graphresult.TopologyNode) string { return graphLabel(graphNodeTitle(n), 48) }
func graphLabel(s string, count int) string {
	var b strings.Builder
	b.Grow(min(len(s), count*4) + 3)
	seen := 0
	for _, r := range s {
		if seen >= count {
			b.WriteString("…")
			break
		}
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
		seen++
	}
	return b.String()
}
func (g *graphVisualState) visibleNode(key string) bool { return !g.hidden[key] }
func (g *graphVisualState) visibleEdge(e graphresult.TopologyEdge) bool {
	return !g.hidden[e.Key] && !g.hidden[e.Source] && !g.hidden[e.Target]
}
func (g *graphVisualState) node(key string) *graphresult.TopologyNode {
	i, found := slices.BinarySearchFunc(g.topology.Nodes, key, func(n graphresult.TopologyNode, s string) int { return strings.Compare(n.Key, s) })
	if found {
		return &g.topology.Nodes[i]
	}
	return nil
}
func (g *graphVisualState) edge(key string) *graphresult.TopologyEdge {
	i, found := slices.BinarySearchFunc(g.topology.Edges, key, func(e graphresult.TopologyEdge, s string) int { return strings.Compare(e.Key, s) })
	if found {
		return &g.topology.Edges[i]
	}
	return nil
}
func (g *graphVisualState) selectKey(key string) {
	g.selected, g.detail = key, ""
	g.propertyPage, g.fieldIndex = 0, -1
	g.inspectorMode, g.fieldJSON = "", ""
}
func (g *graphVisualState) hideSelected() {
	if g.selected != "" {
		g.hidden[g.selected] = true
	}
	g.selectKey("")
	g.dragging = false
}
func (g *graphVisualState) resetHidden() { g.hidden = map[string]bool{}; g.fit = true }
func (g *graphVisualState) cells() []graphresult.CellRef {
	if n := g.node(g.selected); n != nil {
		return n.Cells
	}
	if e := g.edge(g.selected); e != nil {
		return e.Cells
	}
	return nil
}
func (g *graphVisualState) matches(key string) bool {
	q := strings.ToLower(strings.TrimSpace(g.query))
	if q == "" {
		return false
	}
	if g.matchQuery != q {
		g.matchQuery = q
		g.matchKeys = map[string]bool{}
		for _, n := range g.topology.Nodes {
			if strings.Contains(strings.ToLower(graphNodeTitle(n)), q) || strings.Contains(strings.ToLower(n.Value.ElementID), q) || strings.Contains(strings.ToLower(strings.Join(n.Value.Labels, " ")), q) {
				g.matchKeys[n.Key] = true
			}
		}
		for _, e := range g.topology.Edges {
			if strings.Contains(strings.ToLower(e.Value.Type), q) || strings.Contains(strings.ToLower(e.Value.ElementID), q) {
				g.matchKeys[e.Key] = true
			}
		}
	}
	return g.matchKeys[key]
}

// Deterministic bounded force layout. A spatial grid limits repulsion to nearby
// nodes; fixed nodes retain their exact position. All work runs on the UI thread.
func (g *graphVisualState) layout() {
	n := len(g.topology.Nodes)
	if n == 0 {
		return
	}
	columns := int(math.Ceil(math.Sqrt(float64(n))))
	for i, node := range g.topology.Nodes {
		if !g.pinned[node.Key] {
			g.positions[node.Key] = ui.Point{X: float32(i%columns) * 160, Y: float32(i/columns) * 140}
		}
	}
	g.relax(g.pinned)
	g.fit = true
	g.dragging = false
}

// Fixed positions can include old unpinned nodes during neighbor admission.
// Only explicit relayout is allowed to move those already displayed nodes.
func (g *graphVisualState) relax(fixed map[string]bool) {
	n := len(g.topology.Nodes)
	for iteration := 0; iteration < 60; iteration++ {
		buckets := map[[2]int][]int{}
		for i, node := range g.topology.Nodes {
			p := g.positions[node.Key]
			cell := [2]int{int(math.Floor(float64(p.X / 180))), int(math.Floor(float64(p.Y / 180)))}
			buckets[cell] = append(buckets[cell], i)
		}
		forces := make([]ui.Point, n)
		for i, node := range g.topology.Nodes {
			p := g.positions[node.Key]
			cell := [2]int{int(math.Floor(float64(p.X / 180))), int(math.Floor(float64(p.Y / 180)))}
			for x := -1; x <= 1; x++ {
				for y := -1; y <= 1; y++ {
					for _, j := range buckets[[2]int{cell[0] + x, cell[1] + y}] {
						if i == j {
							continue
						}
						q := g.positions[g.topology.Nodes[j].Key]
						dx, dy := p.X-q.X, p.Y-q.Y
						d2 := max(dx*dx+dy*dy, 100)
						f := float32(1500) / d2
						forces[i].X += dx * f
						forces[i].Y += dy * f
					}
				}
			}
		}
		for _, edge := range g.topology.Edges {
			if edge.Source == edge.Target {
				continue
			}
			a, _ := slices.BinarySearchFunc(g.topology.Nodes, edge.Source, func(n graphresult.TopologyNode, s string) int { return strings.Compare(n.Key, s) })
			b, _ := slices.BinarySearchFunc(g.topology.Nodes, edge.Target, func(n graphresult.TopologyNode, s string) int { return strings.Compare(n.Key, s) })
			p, q := g.positions[edge.Source], g.positions[edge.Target]
			dx, dy := q.X-p.X, q.Y-p.Y
			dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
			factor := float32(.012) * (dist - 170) / max(dist, 1)
			fx, fy := dx*factor, dy*factor
			forces[a].X += fx
			forces[a].Y += fy
			forces[b].X -= fx
			forces[b].Y -= fy
		}
		limit := float32(18) * (1 - float32(iteration)/60)
		for i, node := range g.topology.Nodes {
			if fixed[node.Key] {
				continue
			}
			f := forces[i]
			length := float32(math.Hypot(float64(f.X), float64(f.Y)))
			if length > limit {
				f.X *= limit / length
				f.Y *= limit / length
			}
			p := g.positions[node.Key]
			p.X += f.X
			p.Y += f.Y
			g.positions[node.Key] = p
		}
	}
}
func (g *graphVisualState) fitView(w, h float32) {
	if w <= 0 || h <= 0 {
		return
	}
	minX, minY, maxX, maxY := float32(math.MaxFloat32), float32(math.MaxFloat32), float32(-math.MaxFloat32), float32(-math.MaxFloat32)
	found := false
	for _, n := range g.topology.Nodes {
		if !g.visibleNode(n.Key) {
			continue
		}
		p := g.positions[n.Key]
		minX = min(minX, p.X-70)
		minY = min(minY, p.Y-25)
		maxX = max(maxX, p.X+70)
		maxY = max(maxY, p.Y+45)
		found = true
	}
	base := *g
	base.zoom = 1
	base.pan = ui.Point{}
	lanes := base.edgeLanes()
	for _, e := range base.topology.Edges {
		if !base.visibleEdge(e) {
			continue
		}
		for _, p := range base.edgePoints(e, lanes[e.Key]) {
			minX = min(minX, p.X-12)
			maxX = max(maxX, p.X+12)
			minY = min(minY, p.Y-20)
			maxY = max(maxY, p.Y+12)
		}
	}

	if !found {
		g.zoom = 1
		g.pan = ui.Point{X: w / 2, Y: h / 2}
		g.fit = false
		return
	}
	g.zoom = max(.08, min(1.5, min((w-min(36, w*.08))/max(140, maxX-minX), (h-min(30, h*.12))/max(100, maxY-minY))))
	g.pan = ui.Point{X: w/2 - (minX+maxX)/2*g.zoom, Y: h/2 - (minY+maxY)/2*g.zoom}
	g.fit = false
}
func (g *graphVisualState) screen(p ui.Point) ui.Point {
	return ui.Point{X: p.X*g.zoom + g.pan.X, Y: p.Y*g.zoom + g.pan.Y}
}
func (g *graphVisualState) world(p ui.Point) ui.Point {
	return ui.Point{X: (p.X - g.pan.X) / g.zoom, Y: (p.Y - g.pan.Y) / g.zoom}
}
func (g *graphVisualState) zoomAt(factor float32, p ui.Point) {
	if !finiteGraph(factor) || factor <= 0 {
		return
	}
	q := g.world(p)
	g.zoom = max(.08, min(2, g.zoom*factor))
	g.pan = ui.Point{X: p.X - q.X*g.zoom, Y: p.Y - q.Y*g.zoom}
}
func (g *graphVisualState) release() { *g = graphVisualState{closed: true, zoom: 1} }

func finiteGraph(f float32) bool { return !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0) }
func (g *graphVisualState) input(ev ui.InputEvent) bool {
	if g.closed {
		return false
	}
	if !finiteGraph(ev.X) || !finiteGraph(ev.Y) || ev.X < -100000 || ev.X > 100000 || ev.Y < -100000 || ev.Y > 100000 {
		return false
	}
	p := ui.Point{X: ev.X, Y: ev.Y}
	switch ev.Kind {
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		g.dragging = true
		g.dragStart = p
		g.dragPan = g.pan
		g.dragNode = g.hitNode(p)
		if g.dragNode != "" {
			g.selectKey(g.dragNode)
			g.dragPosition = g.positions[g.dragNode]
		} else {
			g.selectKey(g.hitEdge(p))
		}
		return true
	case ui.InputPointerMove:
		if !g.dragging {
			return false
		}
		dx, dy := p.X-g.dragStart.X, p.Y-g.dragStart.Y
		if g.dragNode != "" {
			g.positions[g.dragNode] = ui.Point{X: g.dragPosition.X + dx/g.zoom, Y: g.dragPosition.Y + dy/g.zoom}
		} else {
			g.pan = ui.Point{X: g.dragPan.X + dx, Y: g.dragPan.Y + dy}
		}
		return true
	case ui.InputPointerUp:
		if !g.dragging {
			return false
		}
		if ev.Canceled {
			g.pan = g.dragPan
			if g.dragNode != "" {
				g.positions[g.dragNode] = g.dragPosition
			}
		}
		g.dragging = false
		g.dragNode = ""
		return true
	case ui.InputScroll:
		if !finiteGraph(ev.DY) {
			return false
		}
		g.zoomAt(float32(math.Exp(float64(max(-300, min(300, -ev.DY)))*.003)), p)
		return true
	case ui.InputZoom:
		g.zoomAt(ev.Scale, p)
		return true
	case ui.InputKeyDown:
		switch ev.Key {
		case ui.KeyEscape:
			g.selectKey("")
			return true
		case ui.KeyDelete:
			g.hideSelected()
			return true
		case ui.KeyHome:
			g.fit = true
			return true
		case ui.KeyLeft:
			g.pan.X += 24
			return true
		case ui.KeyRight:
			g.pan.X -= 24
			return true
		case ui.KeyUp:
			g.pan.Y += 24
			return true
		case ui.KeyDown:
			g.pan.Y -= 24
			return true
		}
	}
	return false
}
func (g *graphVisualState) hitNode(p ui.Point) string {
	for i := len(g.topology.Nodes) - 1; i >= 0; i-- {
		n := g.topology.Nodes[i]
		if !g.visibleNode(n.Key) {
			continue
		}
		q := g.screen(g.positions[n.Key])
		if math.Hypot(float64(p.X-q.X), float64(p.Y-q.Y)) <= float64(max(5, 20*g.zoom)+4) {
			return n.Key
		}
	}
	return ""
}
