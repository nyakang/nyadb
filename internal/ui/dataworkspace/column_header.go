package dataworkspace

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var mysqlDataZoomLevels = []int{50, 75, 90, 100, 110, 125, 150, 200}

func mysqlDataValidZoom(zoom int) int {
	if !slices.Contains(mysqlDataZoomLevels, zoom) {
		return 100
	}
	return zoom
}

func mysqlDataZoom(t *Document) int { return mysqlDataValidZoom(t.edit.Zoom) }

func (s *State) mysqlDataZoomMenu(t *Document, m *ui.Menu) {
	m.Submenu(locales.Text("缩放"), func(sub *ui.Menu) {
		for _, percent := range mysqlDataZoomLevels {
			if sub.Item(fmt.Sprintf("%d%%", percent)).Checked(mysqlDataZoom(t) == percent).Chosen() {
				t.edit.Zoom = percent
			}
		}
	})
}

func (s *State) mysqlDataHeaderActionMenu(c *ui.Context, t *Document, page *mysqlsvc.MySQLDataPage, source int, m *ui.Menu) {
	if source < 0 || source >= len(page.Columns) {
		return
	}
	ready := !t.loading && !t.edit.Busy && t.edit.Uncertain == nil && t.profileID == s.session().ProfileID && s.session().Connected
	sortable := source < len(t.columns) && t.columns[source].Sortable
	for _, action := range []struct {
		label string
		desc  bool
	}{{"升序排序", false}, {"降序排序", true}} {
		if m.Item(locales.Text(action.label)).Disabled(!ready || !sortable).Chosen() {
			s.applyMySQLDataCellSort(c.Services(), t, page.Columns[source], action.desc)
		}
	}
	if m.Item(locales.Text("移除所有排序")).Disabled(!ready || mysqlDataActiveSortCount(t) == 0).Chosen() {
		s.applyMySQLDataCellSort(c.Services(), t, "", false)
	}
	m.Separator()
	if m.Item(locales.Text("添加筛选")).Chosen() {
		s.mysqlDataAddHeaderFilter(t, page.Columns[source])
	}
}

// Adding a header filter changes only the editor's draft. Applied predicates,
// paging and database reads wait for the explicit Apply action.
func (s *State) mysqlDataAddHeaderFilter(t *Document, name string) {
	t.rulesExpanded = true
	if t.filterColumnDraft == name {
		t.layout.filterFocus = name
		return
	}
	for _, draft := range t.filterExtraDraft {
		if draft.Column == name {
			t.layout.filterFocus = name
			return
		}
	}
	if t.filterColumnDraft == "" || t.filterColumnDraft == "选择字段" {
		t.filterColumnDraft, t.filterModeDraft, t.filterValueDraft = name, "精确匹配", ""
	} else if len(t.filterExtraDraft)+1 < mysqlsvc.MySQLDataMaxFilterRules {
		t.filterExtraDraft = append(t.filterExtraDraft, mysqlDataRuleDraft{Column: name, Mode: "精确匹配"})
	} else {
		t.ruleError = locales.Text("筛选条件已达到上限")
		return
	}
	t.ruleError = ""
	t.layout.filterFocus = name
}

func (s *State) mysqlDataHeaderMenu(c *ui.Context, t *Document, page *mysqlsvc.MySQLDataPage, indices []int, m *ui.Menu, col int) {
	if col < 0 || col >= len(indices) {
		return
	}
	source := indices[col]
	name := page.Columns[source]
	names := mysqlDataSelectedColumns(t)
	hasData := mysqlDataHasSelectedPreview(t)
	if m.Item(locales.Text("复制")).Shortcut(ui.Cmd, ui.KeyC).Disabled(!hasData).Chosen() {
		s.copyMySQLDataSelection(c, t)
	}
	if m.Item(locales.Text("复制字段名称")).Disabled(len(names) == 0).Chosen() {
		c.WriteClipboard(mysqlDataDelimitedPreview(names, nil, '\t', true, false))
	}
	m.Submenu(locales.Text("复制为"), func(sub *ui.Menu) { s.mysqlDataCopyAsMenu(c, t, sub) })
	cursor := slices.Index(page.Columns, t.grid.Cells.Cursor)
	canPaste := !t.loading && !t.edit.Busy && t.profileID == s.session().ProfileID && s.session().Connected && mysqlDataCanEdit(t, t.selectedRow, cursor)
	if canPaste {
		canPaste = cursor < len(page.ColumnMetadata) && !mysqlDataBinaryField(page.ColumnMetadata[cursor]) && !mysqlDataSpatialField(page.ColumnMetadata[cursor])
	}
	if m.Item(locales.Text("粘贴")).Shortcut(ui.Cmd, ui.KeyV).Disabled(!canPaste).Chosen() {
		s.pasteMySQLDataSelection(c, t, t.selectedRow, cursor)
	}
	m.Separator()
	if m.Item(locales.Text("显示或隐藏列")).Chosen() {
		t.toolbar.columnsOpen = !t.toolbar.columnsOpen
	}
	if m.Item(locales.Text("隐藏此列")).Disabled(len(indices) <= 1).Chosen() {
		mysqlDataHideColumn(t, page, name)
	}
	if m.Item(locales.Text("列宽…")).Chosen() {
		mysqlDataOpenColumnWidth(t, source)
	}
	if m.Item(locales.Text("将此列宽度适配当前页")).Chosen() {
		mysqlDataFitColumn(c, t, page, source)
	}
	s.mysqlDataColumnsMenu(c, t, page, m)
}

type mysqlDataColumnWidthState struct {
	Open       bool
	Column     string
	Value, Err string
}

func mysqlDataOpenColumnWidth(t *Document, source int) {
	col := t.columns[source]
	width := col.Width
	if saved, ok := t.grid.Columns.Widths[col.ID]; ok {
		width = saved
	}
	if width <= 0 {
		width = max(col.MinWidth, 155)
	}
	t.layout.columnWidth = mysqlDataColumnWidthState{Open: true, Column: col.ID, Value: fmt.Sprintf("%.0f", width)}
}

func mysqlDataApplyColumnWidth(t *Document) bool {
	draft := &t.layout.columnWidth
	if t.page == nil {
		draft.Err = locales.Text("字段已不存在")
		return false
	}
	source := slices.Index(t.page.Columns, draft.Column)
	if source < 0 || source >= len(t.columns) {
		draft.Err = locales.Text("字段已不存在")
		return false
	}
	col := t.columns[source]
	width, err := strconv.Atoi(strings.TrimSpace(draft.Value))
	if err != nil || width <= 0 || float32(width) < col.MinWidth || col.MaxWidth > 0 && float32(width) > col.MaxWidth {
		draft.Err = locales.Text("请输入符合字段宽度限制的正整数")
		return false
	}
	if t.grid.Columns.Widths == nil {
		t.grid.Columns.Widths = map[string]float32{}
	}
	t.grid.Columns.Widths[draft.Column] = float32(width)
	draft.Open = false
	draft.Err = ""
	return true
}

func (s *State) mysqlDataColumnWidthModal(c *ui.Context, t *Document) {
	draft := &t.layout.columnWidth
	if !draft.Open {
		return
	}
	ui.Modal(c, &draft.Open, func() {
		ui.Column(c).Width(320).MaxWidthPercent(95).Padding(16).Gap(10).Children(func() {
			ui.Text(c, locales.Text("列宽")+" · "+draft.Column).Bold().SingleLine()
			ui.TextInput(c, &draft.Value).Label(locales.Text("列宽（DIP）")).FillWidth()
			if draft.Err != "" {
				ui.Text(c, draft.Err).TextColor(theme.Colors(c).Error)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("取消")).Clicked() {
					*draft = mysqlDataColumnWidthState{}
				}
				if ui.PrimaryButton(c, locales.Text("确定")).Clicked() {
					mysqlDataApplyColumnWidth(t)
				}
			})
		})
	})
}

// Fit only the cached page, using the same font shaping as the actual grid.
// It includes every header line and reserves room for the native action button.
func mysqlDataFitColumn(c *ui.Context, t *Document, page *mysqlsvc.MySQLDataPage, source int) {
	if source < 0 || source >= len(page.Columns) || source >= len(t.columns) {
		return
	}
	_, columns := mysqlVisibleDataColumns(t, page)
	name := page.Columns[source]
	var col ui.TableColumn
	for _, candidate := range columns {
		if candidate.ID == name {
			col = candidate
			break
		}
	}
	if col.ID == "" {
		return
	}
	font := ui.Font{Family: c.Theme().Font, Size: theme.Font(c, mysqlDataEditFont(t))}
	measure := func(text string, size float32, weight int) float32 {
		f := font
		f.Size, f.Weight = size, weight
		var width float32
		for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
			for _, glyph := range ui.Shape(line, f) {
				width = max(width, glyph.X+glyph.Advance)
			}
		}
		return width
	}
	spacingScale := font.Size / theme.Font(c, theme.FontSize)
	space := func(n float32) float32 { return c.Theme().Space(n) * spacingScale }
	pad := space(5)
	actions := space(9)
	width := measure(col.Title, font.Size, 600) + pad + actions
	if col.HeaderBadge != "" {
		width += measure(col.HeaderBadge, font.Size*0.7, 400) + space(1)
	}
	if col.Subtitle != "" {
		typeWidth := measure(col.Subtitle, font.Size*0.8, 400)
		if col.SubtitleIcon != "" {
			typeWidth += measure(col.SubtitleIcon, font.Size*0.8, 600) + space(1)
		}
		width = max(width, typeWidth+pad+actions)
	}
	if col.HeaderDetail != "" {
		width = max(width, measure(strings.NewReplacer("\r", " ", "\n", " ").Replace(col.HeaderDetail), font.Size*0.8, 400)+pad+actions)
	}
	for _, row := range page.Rows {
		if source >= len(row) {
			continue
		}
		text := row[source].Text
		if row[source].Null {
			text = "NULL"
		}
		width = max(width, measure(text, font.Size, 400)+pad)
	}
	width = float32(math.Ceil(float64(max(width, col.MinWidth))))
	if col.MaxWidth > 0 {
		width = min(width, col.MaxWidth)
	}
	if t.grid.Columns.Widths == nil {
		t.grid.Columns.Widths = map[string]float32{}
	}
	t.grid.Columns.Widths[name] = width
}
