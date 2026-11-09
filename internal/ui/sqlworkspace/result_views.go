package sqlworkspace

import (
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type resultView struct {
	Grid      ui.ListState
	Selection ui.Selection[int]
	Row       int
	Hidden    map[string]bool
	FieldOpen bool
	FieldMode string
}
type resultPreference struct {
	Hidden    map[string]bool
	FieldMode string
}
type resultPresentation struct {
	Version     int
	Layouts     map[string]ui.TableLayout
	Preferences map[string]resultPreference
}

func cloneHidden(hidden map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range hidden {
		out[k] = v
	}
	return out
}

func resultSignature(set contract.ResultSet) string {
	if set.LayoutKey != "" {
		return set.LayoutKey
	}
	b, _ := json.Marshal(set.Columns)
	return string(b)
}

func cloneResultLayout(layout ui.TableLayout) ui.TableLayout {
	out := ui.TableLayout{Order: append([]string(nil), layout.Order...)}
	if layout.Widths != nil {
		out.Widths = map[string]float32{}
		for id, width := range layout.Widths {
			out.Widths[id] = width
		}
	}
	return out
}
func (d *Document) saveResultView() {
	if d.resultViews == nil {
		d.resultViews = map[int]*resultView{}
	}
	d.resultViews[d.resultSet] = &resultView{d.grid, d.selection, d.selectedRow, d.hiddenResultColumns, d.resultFieldOpen, d.resultFieldMode}
}
func (d *Document) selectResult(index int) {
	d.saveResultView()
	d.resultSet = index
	x := d.resultViews[index]
	if x == nil {
		x = &resultView{Row: -1, Hidden: map[string]bool{}}
		d.resultViews[index] = x
	}
	d.grid, d.selection, d.selectedRow = x.Grid, x.Selection, x.Row
	d.hiddenResultColumns, d.resultFieldOpen, d.resultFieldMode = x.Hidden, x.FieldOpen, x.FieldMode
}
func (d *Document) rememberResultLayouts() {
	if d.result == nil {
		return
	}
	d.saveResultView()
	if d.resultLayouts == nil {
		d.resultLayouts = map[string]ui.TableLayout{}
	}
	if d.resultPreferences == nil {
		d.resultPreferences = map[string]resultPreference{}
	}
	for index, set := range d.result.ResultSets() {
		if x := d.resultViews[index]; x != nil {
			d.resultLayouts[resultSignature(set)] = cloneResultLayout(x.Grid.Columns)
			d.resultPreferences[resultSignature(set)] = resultPreference{cloneHidden(x.Hidden), x.FieldMode}
		}
	}
}
func (d *Document) setResult(result *contract.SQLResult) {
	d.rememberResultLayouts()
	d.stopGraphExpansion()
	d.graphJSON = ""
	if d.graphVisual != nil {
		d.graphVisual.release()
	}
	d.graphVisual, d.graphVisualOpen = nil, false
	d.result = result
	d.resultViews = map[int]*resultView{}
	if result != nil {
		for index, set := range result.ResultSets() {
			pref, saved := d.resultPreferences[resultSignature(set)]
			if !saved {
				pref.Hidden = map[string]bool{}
				for _, index := range set.DefaultHiddenColumns {
					if index >= 0 && index < len(set.Columns) {
						pref.Hidden[fmt.Sprintf("sql_%d", index)] = true
					}
				}
			}
			d.resultViews[index] = &resultView{Grid: ui.ListState{Columns: cloneResultLayout(d.resultLayouts[resultSignature(set)])}, Row: -1, Hidden: cloneHidden(pref.Hidden), FieldMode: pref.FieldMode}
		}
	}
	d.grid = ui.ListState{}
	d.selection = ui.Selection[int]{}
	d.selectedRow = -1
	d.resultSet = 0
	d.hiddenResultColumns = map[string]bool{}
	d.resultFieldOpen = false
	d.resultFieldMode = ""
	if x := d.resultViews[0]; x != nil {
		d.grid = x.Grid
		d.hiddenResultColumns, d.resultFieldMode = x.Hidden, x.FieldMode
	}
}
