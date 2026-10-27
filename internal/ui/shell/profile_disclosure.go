package shell

import "github.com/egoist/mygo/ui"

// configureNavigatorDisclosure opts into the NyaDB MyGo fork's native solid
// disclosure triangle without changing the default style of other outlines.
func configureNavigatorDisclosure(state *ui.OutlineState[string]) {
	if state == nil {
		return
	}
	state.Disclosure = ui.OutlineTriangle
	state.DisclosureSize = 10 // independent hit target; visible triangle ~6 DIPs
}
