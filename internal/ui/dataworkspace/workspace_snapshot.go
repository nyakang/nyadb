package dataworkspace

import (
	"encoding/json"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type workspacePresentation struct {
	Layout      mysqlDataSavedLayout
	Condition   *contract.FilterExpression[contract.DataFilterRule]
	Filters     []contract.DataFilterRule
	Sorts       []contract.DataSortRule
	Limit       int
	RulesOpen   bool
	FieldOpen   bool
	FieldMode   string
	Dock        string
	DockHeight  float32
	ColumnsOpen bool
}

func (s *State) WorkspaceSnapshot(d *Document) json.RawMessage {
	if d == nil || d.closed {
		return nil
	}
	filters, sorts := mysqlDataCurrentRules(d)
	if d.filterCondition != nil {
		filters = nil
	}
	saved := workspacePresentation{Layout: mysqlDataSavedLayout{Version: 1, Order: d.grid.Columns.Order, Widths: d.grid.Columns.Widths, HiddenColumns: d.hiddenColumns, ShowFieldTypes: d.showFieldTypes, ShowRowNumbers: d.layout.showRowNumbers, ShowComments: d.layout.showComments, Zoom: mysqlDataZoom(d)}, Condition: cloneDataCondition(d.filterCondition), Filters: filters, Sorts: sorts, Limit: d.limit, RulesOpen: d.rulesExpanded}
	saved.Dock, saved.DockHeight, saved.ColumnsOpen = d.toolbar.dock, d.toolbar.dockHeight, d.toolbar.columnsOpen
	if d.panel != nil {
		saved.FieldOpen = d.panel.info.fieldMode
	}
	b, _ := json.Marshal(saved)
	return b
}
func (s *State) RestoreWorkspace(d *Document, b json.RawMessage) error {
	var saved workspacePresentation
	if err := json.Unmarshal(b, &saved); err != nil {
		return err
	}
	if _, err := contract.EffectiveDataExpression(contract.DataOptions{Filters: saved.Filters, Condition: saved.Condition}); err != nil {
		return err
	}
	d.grid.Columns = ui.TableLayout{Order: saved.Layout.Order, Widths: saved.Layout.Widths}
	d.hiddenColumns = saved.Layout.HiddenColumns
	d.showFieldTypes = saved.Layout.ShowFieldTypes
	d.layout.showRowNumbers = saved.Layout.ShowRowNumbers
	d.layout.showComments = saved.Layout.ShowComments
	d.layout.restored = true
	d.edit.Zoom = mysqlDataValidZoom(saved.Layout.Zoom)
	d.filterRules = saved.Filters
	d.filterCondition = cloneDataCondition(saved.Condition)
	d.groupDraft = cloneDataCondition(saved.Condition)
	d.sortRules = saved.Sorts
	d.limit = saved.Limit
	if d.limit <= 0 || d.limit > contract.DataMaxPageSize {
		d.limit = 100
	}
	d.rulesExpanded = saved.RulesOpen
	d.toolbar.dock, d.toolbar.dockHeight, d.toolbar.columnsOpen = saved.Dock, saved.DockHeight, saved.ColumnsOpen
	s.inspectorPanel(d).info.fieldMode = saved.FieldOpen
	mysqlDataRestoreRuleDrafts(d, saved.Filters, saved.Sorts)
	return nil
}
