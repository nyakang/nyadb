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
