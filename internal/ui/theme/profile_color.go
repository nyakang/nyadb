package theme

import (
	"github.com/egoist/mygo/ui"
	"strconv"
)

func ProfileColor(c *ui.Context, value string) ui.Color {
	if len(value) == 7 && value[0] == '#' {
		if n, err := strconv.ParseUint(value[1:], 16, 32); err == nil {
			return ui.RGB(uint8(n>>16), uint8(n>>8), uint8(n))
		}
	}
	return Colors(c).Blue
}
