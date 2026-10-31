package theme

import (
	"github.com/egoist/mygo/ui"
	"math"
)

func luminance(c ui.Color) float64 {
	linear := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(c.R) + 0.7152*linear(c.G) + 0.0722*linear(c.B)
}

func contrast(a, b ui.Color) float64 {
	l, h := luminance(a), luminance(b)
	if l > h {
		l, h = h, l
	}
	return (h + 0.05) / (l + 0.05)
}

func onColor(background ui.Color) ui.Color {
	black, white := ui.Hex("#000000"), ui.Hex("#ffffff")
	if contrast(background, black) >= contrast(background, white) {
		return black
	}
	return white
}

func readableBackground(background, text ui.Color) ui.Color {
	target := ui.Hex("#000000")
	if text.R == 0 {
		target = ui.Hex("#ffffff")
	}
	for i := 0; i < 20 && contrast(background, text) < 4.5; i++ {
		background = background.Mix(target, 0.1)
	}
	return background
}

// readableInk retains the hue where possible while keeping syntax legible on
// the editor's normal, active-line and selected backgrounds.
func readableInk(ink ui.Color, backgrounds ...ui.Color) ui.Color {
	return readableInkMinimum(ink, 4.5, backgrounds...)
}

func readableInkMinimum(ink ui.Color, minimum float64, backgrounds ...ui.Color) ui.Color {
	readable := func(c ui.Color) bool {
		for _, bg := range backgrounds {
			if contrast(c, bg) < minimum {
				return false
			}
		}
		return true
	}
	if readable(ink) {
		return ink
	}
	for i := 1; i <= 100; i++ {
		for _, target := range []ui.Color{ui.Hex("#000000"), ui.Hex("#ffffff")} {
			candidate := ink.Mix(target, float32(i)/100)
			if readable(candidate) {
				return candidate
			}
		}
	}
	return ink // Callers test all supported background combinations.
}

// Desaturate in linear RGB toward an equal-luminance neutral. This reduces
// chroma without making dark workspaces darker or light workspaces brighter.
func neutralSurface(c ui.Color, amount float64) ui.Color {
	y := luminance(c)
	channel := func(v uint8) uint8 {
		x := float64(v) / 255
		if x <= 0.04045 {
			x /= 12.92
		} else {
			x = math.Pow((x+0.055)/1.055, 2.4)
		}
		x = x*(1-amount) + y*amount
		if x <= 0.0031308 {
			x *= 12.92
		} else {
			x = 1.055*math.Pow(x, 1/2.4) - 0.055
		}
		return uint8(math.Round(x * 255))
	}
	return ui.Color{R: channel(c.R), G: channel(c.G), B: channel(c.B), A: c.A}
}
