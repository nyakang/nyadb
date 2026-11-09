package sqlworkspace

import (
	"fmt"
	"hash/fnv"
	"html"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/graphresult"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

type graphShapeNode struct {
	Key, Title                       string
	Point                            ui.Point
	Radius                           float32
	Color                            ui.Color
	Missing, Selected, Match, Pinned bool
}
type graphShapeEdge struct {
	Key, Title      string
	Points          []ui.Point
	Selected, Match bool
}
type graphShape struct {
	Nodes   []graphShapeNode
	Edges   []graphShapeEdge
	Palette theme.Palette
	Zoom    float32
}

func (g *graphVisualState) edgeLanes() map[string]float32 {
	groups := map[[2]string][]string{}
	for _, e := range g.topology.Edges {
		a, b := e.Source, e.Target
		if a > b {
			a, b = b, a
		}
		groups[[2]string{a, b}] = append(groups[[2]string{a, b}], e.Key)
	}
	lanes := map[string]float32{}
	for _, group := range groups {
		for i, key := range group {
			lanes[key] = (float32(i) - float32(len(group)-1)/2) * 32
		}
	}
	return lanes
}
func (g *graphVisualState) edgePoints(e graphresult.TopologyEdge, lane float32) []ui.Point {
	a, b := g.screen(g.positions[e.Source]), g.screen(g.positions[e.Target])
	radius := max(5, 20*g.zoom)
	if e.Source == e.Target {
		loop := radius + max(18, 35*g.zoom) + min(50, float32(math.Abs(float64(lane)))*.15*g.zoom)
		start := ui.Point{X: a.X - radius*.7071, Y: a.Y - radius*.7071}
		end := ui.Point{X: a.X + radius*.7071, Y: a.Y - radius*.7071}
		left, right := ui.Point{X: a.X - loop, Y: a.Y - loop*2}, ui.Point{X: a.X + loop, Y: a.Y - loop*2}
		points := make([]ui.Point, 25)
		for i := range points {
			t := float32(i) / 24
			u := 1 - t
			points[i] = ui.Point{X: u*u*u*start.X + 3*u*u*t*left.X + 3*u*t*t*right.X + t*t*t*end.X, Y: u*u*u*start.Y + 3*u*u*t*left.Y + 3*u*t*t*right.Y + t*t*t*end.Y}
		}
		return points
	}
	dx, dy := b.X-a.X, b.Y-a.Y
	dist := max(1, float32(math.Hypot(float64(dx), float64(dy))))
	nx, ny := -dy/dist, dx/dist
	if e.Source > e.Target {
		nx, ny = -nx, -ny
	}
	offset := max(-160, min(160, lane)) * g.zoom
	control := ui.Point{X: (a.X+b.X)/2 + nx*offset, Y: (a.Y+b.Y)/2 + ny*offset}
	trim := func(p, q ui.Point) ui.Point {
		x, y := q.X-p.X, q.Y-p.Y
		d := max(1, float32(math.Hypot(float64(x), float64(y))))
		return ui.Point{X: p.X + x/d*radius, Y: p.Y + y/d*radius}
	}
	a, b = trim(a, control), trim(b, control)
	points := make([]ui.Point, 17)
	for i := range points {
		t := float32(i) / 16
		u := 1 - t
		points[i] = ui.Point{X: u*u*a.X + 2*u*t*control.X + t*t*b.X, Y: u*u*a.Y + 2*u*t*control.Y + t*t*b.Y}
	}
	return points
}
func (g *graphVisualState) hitEdge(p ui.Point) string {
	lanes := g.edgeLanes()
	for i := len(g.topology.Edges) - 1; i >= 0; i-- {
		e := g.topology.Edges[i]
		if !g.visibleEdge(e) {
			continue
		}
		points := g.edgePoints(e, lanes[e.Key])
		for j := 1; j < len(points); j++ {
			if graphSegmentDistance(p, points[j-1], points[j]) <= 6 {
				return e.Key
			}
		}
	}
	return ""
}
func graphSegmentDistance(p, a, b ui.Point) float64 {
	dx, dy := float64(b.X-a.X), float64(b.Y-a.Y)
	d := dx*dx + dy*dy
	t := float64(0)
	if d > 0 {
		t = max(0, min(1, (float64(p.X-a.X)*dx+float64(p.Y-a.Y)*dy)/d))
	}
	return math.Hypot(float64(p.X-a.X)-t*dx, float64(p.Y-a.Y)-t*dy)
}
func graphArrow(points []ui.Point, size float32) []ui.Point {
	if len(points) < 2 {
		return nil
	}
	p, q := points[len(points)-1], points[len(points)-2]
	dx, dy := p.X-q.X, p.Y-q.Y
	d := max(1, float32(math.Hypot(float64(dx), float64(dy))))
	dx, dy = dx/d, dy/d
	return []ui.Point{{X: p.X - dx*size - dy*size*.45, Y: p.Y - dy*size + dx*size*.45}, p, {X: p.X - dx*size + dy*size*.45, Y: p.Y - dy*size - dx*size*.45}}
}
func (g *graphVisualState) shape(palette theme.Palette) graphShape {
	shape := graphShape{Palette: palette, Zoom: g.zoom}
	lanes := g.edgeLanes()
	colors := []ui.Color{palette.Blue, palette.Green, palette.Purple, palette.Gold}
	for _, n := range g.topology.Nodes {
		if !g.visibleNode(n.Key) {
			continue
		}
		label := ""
		if len(n.Value.Labels) > 0 {
			label = n.Value.Labels[0]
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(label))
		color := colors[h.Sum32()%uint32(len(colors))]
		if n.Missing {
			color = palette.Muted
		}
		shape.Nodes = append(shape.Nodes, graphShapeNode{Key: n.Key, Title: graphTitle(n), Point: g.screen(g.positions[n.Key]), Radius: max(5, 20*g.zoom), Color: color, Missing: n.Missing, Selected: g.selected == n.Key, Match: g.matches(n.Key), Pinned: g.pinned[n.Key]})
	}
	for _, e := range g.topology.Edges {
		if !g.visibleEdge(e) {
			continue
		}
		shape.Edges = append(shape.Edges, graphShapeEdge{Key: e.Key, Title: graphLabel(e.Value.Type, 40), Points: g.edgePoints(e, lanes[e.Key]), Selected: g.selected == e.Key, Match: g.matches(e.Key)})
	}
	return shape
}
func graphPoints(points []ui.Point, r ui.Rect) [][2]float32 {
	out := make([][2]float32, len(points))
	for i, p := range points {
		out[i] = [2]float32{p.X + r.X, p.Y + r.Y}
	}
	return out
}
func paintGraph(p *ui.Painter, r ui.Rect, s graphShape) {
	p.Clip(r, 0, func() {
		p.Fill(r, s.Palette.Panel, 0)
		for _, e := range s.Edges {
			color := s.Palette.Muted
			width := float32(1.5)
			if e.Match {
				color = s.Palette.Warning
				width = 2.5
			}
			if e.Selected {
				color = s.Palette.Accent
				width = 3
			}
			p.StrokePolyline(graphPoints(e.Points, r), width, color)
			p.StrokePolyline(graphPoints(graphArrow(e.Points, max(3, min(9, 9*s.Zoom))), r), width, color)
			if s.Zoom >= .25 && len(e.Points) > 0 {
				mid := e.Points[len(e.Points)/2]
				box := ui.Rect{X: r.X + mid.X - 54, Y: r.Y + mid.Y - 16, W: 108, H: 14}
				p.Fill(box, s.Palette.Panel.Alpha(.92), 2)
				p.Clip(box, 0, func() { p.Text(box.X+2, box.Y, e.Title, 10, color) })
			}
		}
		for _, n := range s.Nodes {
			box := ui.Rect{X: r.X + n.Point.X - n.Radius, Y: r.Y + n.Point.Y - n.Radius, W: 2 * n.Radius, H: 2 * n.Radius}
			if n.Missing {
				p.Fill(box, s.Palette.Panel, n.Radius)
				p.StrokeDashed(box, n.Color, n.Radius, 2)
			} else {
				p.Fill(box, n.Color, n.Radius)
				p.Stroke(box, s.Palette.Panel, n.Radius, 2)
			}
			if n.Match {
				p.Stroke(ui.Rect{X: box.X - 3, Y: box.Y - 3, W: box.W + 6, H: box.H + 6}, s.Palette.Warning, n.Radius+3, 3)
			}
			if n.Selected {
				p.Stroke(ui.Rect{X: box.X - 5, Y: box.Y - 5, W: box.W + 10, H: box.H + 10}, s.Palette.Accent, n.Radius+5, 3)
			}
			if n.Pinned {
				p.Fill(ui.Rect{X: box.X + box.W - 5, Y: box.Y - 2, W: 7, H: 7}, s.Palette.Warning, 3.5)
			}
			if n.Missing && s.Zoom >= .25 {
				p.Text(r.X+n.Point.X-4, r.Y+n.Point.Y-8, "?", 13, n.Color)
			}
			if s.Zoom >= .25 {
				width := min(130, float32(utf8.RuneCountInString(n.Title))*6+8)
				label := ui.Rect{X: r.X + n.Point.X - width/2, Y: box.Y + box.H + 7, W: width, H: 18}
				p.Fill(label, s.Palette.Panel.Alpha(.95), 2)
				p.Clip(label, 0, func() { p.Text(label.X+2, label.Y, n.Title, 11, s.Palette.Text) })
			}
		}
	})
}
func graphColor(c ui.Color) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }
func graphSVG(s graphShape, width, height int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"%d\" height=\"%d\" viewBox=\"0 0 %d %d\"><rect width=\"100%%\" height=\"100%%\" fill=\"%s\"/>", width, height, width, height, graphColor(s.Palette.Panel))
	polyline := func(points []ui.Point, color ui.Color, w float32) {
		b.WriteString("<polyline fill=\"none\" points=\"")
		for _, p := range points {
			fmt.Fprintf(&b, "%.2f,%.2f ", p.X, p.Y)
		}
		fmt.Fprintf(&b, "\" stroke=\"%s\" stroke-width=\"%.2f\"/>", graphColor(color), w)
	}
	for _, e := range s.Edges {
		color := s.Palette.Muted
		if e.Match {
			color = s.Palette.Warning
		}
		if e.Selected {
			color = s.Palette.Accent
		}
		b.WriteString("<g><title>" + html.EscapeString(e.Key) + "</title>")
		polyline(e.Points, color, 2)
		polyline(graphArrow(e.Points, max(3, min(9, 9*s.Zoom))), color, 2)
		mid := e.Points[len(e.Points)/2]
		fmt.Fprintf(&b, "<text x=\"%.2f\" y=\"%.2f\" font-size=\"10\" text-anchor=\"middle\" fill=\"%s\">%s</text></g>", mid.X, mid.Y-6, graphColor(color), html.EscapeString(e.Title))
	}
	for _, n := range s.Nodes {
		fill := graphColor(n.Color)
		dash := ""
		if n.Missing {
			fill = graphColor(s.Palette.Panel)
			dash = " stroke-dasharray=\"4 3\""
		}
		border := n.Color
		if n.Selected {
			border = s.Palette.Accent
		} else if n.Match {
			border = s.Palette.Warning
		}
		fmt.Fprintf(&b, "<g><title>%s</title><circle cx=\"%.2f\" cy=\"%.2f\" r=\"%.2f\" fill=\"%s\" stroke=\"%s\" stroke-width=\"2\"%s/><text x=\"%.2f\" y=\"%.2f\" text-anchor=\"middle\" font-family=\"sans-serif\" font-size=\"11\" fill=\"%s\">%s</text></g>", html.EscapeString(n.Key), n.Point.X, n.Point.Y, n.Radius, fill, graphColor(border), dash, n.Point.X, n.Point.Y+n.Radius+19, graphColor(s.Palette.Text), html.EscapeString(n.Title))
	}
	b.WriteString("</svg>")
	return b.String()
}
