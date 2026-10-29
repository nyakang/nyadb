package theme

import "github.com/egoist/mygo/ui"

// ConnectionActionIcon draws each color natively so platform SVG tinting cannot
// turn the plug and its add badge into a single-color silhouette.
func ConnectionActionIcon(c *ui.Context, size float32) {
	palette := Colors(c)
	ui.Box(c).Size(size, size).Draw(func(p *ui.Painter, r ui.Rect) {
		scale := min(r.W, r.H) / 24
		x, y := r.X+(r.W-24*scale)/2, r.Y+(r.H-24*scale)/2
		var cable, pins, body, seam, badge, plus ui.Path
		cable.MoveTo(x+9*scale, y+16*scale).
			LineTo(x+5*scale, y+20*scale).QuadTo(x+3*scale, y+22*scale, x+1*scale, y+20*scale)
		p.StrokePath(&cable, 2.6*scale, palette.Blue)
		pins.MoveTo(x+10*scale, y+8*scale).LineTo(x+14*scale, y+4*scale).
			MoveTo(x+14*scale, y+12*scale).LineTo(x+18*scale, y+8*scale)
		p.StrokePath(&pins, 2.5*scale, palette.Blue)
		body.MoveTo(x+9*scale, y+7*scale).LineTo(x+17*scale, y+15*scale).
			LineTo(x+12*scale, y+20*scale).QuadTo(x+10*scale, y+22*scale, x+8*scale, y+20*scale).
			LineTo(x+4*scale, y+16*scale).QuadTo(x+2*scale, y+14*scale, x+4*scale, y+12*scale).Close()
		p.FillPath(&body, palette.Muted.Mix(palette.Text, .25))
		seam.MoveTo(x+6*scale, y+10*scale).LineTo(x+14*scale, y+18*scale)
		p.StrokePath(&seam, 1.3*scale, palette.Toolbar.Mix(palette.Muted, .25))
		badge.Circle(x+19*scale, y+19*scale, 4.7*scale)
		p.FillPath(&badge, palette.Success)
		plus.MoveTo(x+16.4*scale, y+19*scale).LineTo(x+21.6*scale, y+19*scale).
			MoveTo(x+19*scale, y+16.4*scale).LineTo(x+19*scale, y+21.6*scale)
		p.StrokePath(&plus, 1.5*scale, ui.Hex("#ffffff"))
	})
}
