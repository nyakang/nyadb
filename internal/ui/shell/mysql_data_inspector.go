package shell

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// The inspector follows the active real data tab rather than the connection
// profile, but never asks the server for an unbounded cell value.
func (s *State) mysqlDataInspector(c *ui.Context, t *tab) {
	name, cell, selected := mysqlSelectedDataCell(t)
	row := t.dataSelectedRow
	if t.dataChunkField != "" && (!selected || t.dataChunkField != name || t.dataChunkRow != row) {
		s.cancelMySQLChunk(t)
	}
	canRead := selected && !cell.Null && row >= 0 && row < len(t.dataPage.RowRefs) && t.dataPage.RowRefs[row] != nil
	chunkShown := selected && t.dataChunkField == name && t.dataChunkRow == row && t.dataChunkNext > 0
	ui.Column(c).Fill().Background(theme.Panel).Children(func() {
		s.mysqlDataInspectorHeader(c, t, true)
		if selected {
			// Actions stay outside the metadata scroll. A long type declaration,
			// comment, or binary preview must not move the next-fragment control.
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("复制")).Label(locales.Text("复制单元格预览")).Width(50).Height(theme.ControlHeight).FontSize(theme.SmallFontSize).Padding(2, 3).Clicked() {
					c.WriteClipboard(mysqlDataCopyPreview(cell))
				}
				// Toolbar moves buttons to overflow when the translated text
				// exceeds the narrow inspector width. Fixed button widths
				// avoid font-dependent overflow while full labels remain.
				if canRead && ui.Button(c, locales.Text("下段")).Label(locales.Text("读取所选字段下一段")).Width(50).Height(theme.ControlHeight).FontSize(theme.SmallFontSize).Padding(2, 3).
					Disabled(t.dataChunkLoading || t.dataChunkDone).Clicked() {
					s.loadMySQLChunk(c.Services(), t)
				}
				if chunkShown && ui.Button(c, locales.Text("片段")).Label(locales.Text("复制此片段")).Width(50).Height(theme.ControlHeight).FontSize(theme.SmallFontSize).Padding(2, 3).Clicked() {
					c.WriteClipboard(t.dataChunkText)
				}
			}).Height(theme.CommandBarHeight).FillWidth().MinWidth(0).Padding(1, 4).Gap(2)
		}
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(12, 10).Gap(9).Children(func() {
				if t.dataPage == nil {
					message := "正在读取字段元数据…"
					if t.dataErr != "" {
						message = "字段元数据不可用，请重试读取数据"
					} else if !t.dataLoading {
						message = "尚未加载字段元数据"
					}
					ui.Text(c, message).TextColor(theme.Muted)
					return
				}
				field, found, complete := mysqlSelectedDataField(t)
				if !found {
					ui.Text(c, "字段不存在或已被隐藏").FontSize(12).TextColor(theme.Muted)
					return
				}
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, mysqlColumnTypeGlyph(field.DataType)).Bold().FontSize(17).TextColor(theme.Blue)
					ui.Text(c, field.Name).Bold().FontSize(14).SingleLine().Grow(1).MinWidth(0).Tooltip(field.Name)
				})
				ui.Text(c, t.dataDatabase+"."+t.dataTable).FontSize(theme.SmallFontSize).TextColor(theme.Muted).SingleLine().Tooltip(t.dataDatabase + "." + t.dataTable)
				ui.Divider(c)
				mysqlInspectorProperty(c, "类型", field.Type)
				if !complete {
					ui.Text(c, "仅有基础字段类型，完整元数据尚未加载").FontSize(11).TextColor(theme.Muted)
				} else {
					mysqlInspectorFieldAttributes(c, field)
				}
				ui.Divider(c)
				if !selected {
					ui.Text(c, "选择数据单元格以查看值的安全预览").FontSize(11).TextColor(theme.Muted)
					return
				}
				mysqlInspectorProperty(c, "行号", fmt.Sprintf("%d", t.dataOffset+int64(t.dataSelectedRow)+1))
				if cell.Null {
					mysqlInspectorProperty(c, "值状态", "SQL NULL")
				} else if cell.Truncated {
					mysqlInspectorProperty(c, "值状态", "已截断的预览")
				} else {
					mysqlInspectorProperty(c, "值状态", "预览完整")
				}
				value := cell.Text
				if cell.Null {
					value = "NULL"
				} else if value == "" {
					value = "\"\""
				}
				ui.Scroll(c).Height(86).FillWidth().Border(1, theme.Border).Children(func() {
					ui.Text(c, value).FontSize(theme.FontSize).Padding(3)
				})
				if canRead {
					ui.Text(c, "大字段按需读取 · 单次最多 1024 字符/字节").FontSize(11).TextColor(theme.Muted)
					if t.dataChunkLoading {
						ui.Text(c, "正在读取字段片段…").FontSize(11)
					}
					if t.dataChunkErr != "" {
						ui.Text(c, t.dataChunkErr).FontSize(11).TextColor(theme.Gold)
					}
					if chunkShown {
						s.property(c, "已读位置", fmt.Sprintf("%d / %d", t.dataChunkNext, t.dataChunkTotal))
						ui.Scroll(c).Height(112).FillWidth().Border(1, theme.Border).Children(func() {
							ui.Text(c, t.dataChunkText).FontSize(theme.SmallFontSize).Padding(3)
						})
					}
				}
			})
		})
	})
}

// The reference inspector stacks property labels above values, so long MySQL
// declarations and comments can wrap without shrinking the data grid.
func mysqlInspectorProperty(c *ui.Context, label, value string) {
	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(theme.Muted)
		ui.Text(c, value).FontSize(12)
	})
}

func mysqlInspectorFieldAttributes(c *ui.Context, field connections.MySQLStructureColumn) {
	notNull := "否"
	if !field.Nullable {
		notNull = "是"
	}
	mysqlInspectorProperty(c, "不是 NULL", notNull)
	defaultValue := "SQL NULL / 未设置"
	if field.Default != nil {
		defaultValue = *field.Default
		// Make an actual string "NULL" or "" visibly distinct from SQL NULL.
		if mysqlInspectorTextType(field.DataType) {
			defaultValue = strconv.Quote(defaultValue)
		}
	}
	mysqlInspectorProperty(c, "默认值", defaultValue)
	key := "—"
	switch field.Key {
	case "PRI":
		key = "主键"
	case "UNI":
		key = "唯一索引"
	case "MUL":
		key = "索引（MUL）"
	}
	mysqlInspectorProperty(c, "键", key)
	autoIncrement := "否"
	if strings.Contains(strings.ToLower(field.Extra), "auto_increment") {
		autoIncrement = "是"
	}
	mysqlInspectorProperty(c, "自增", autoIncrement)
	if field.Extra != "" {
		mysqlInspectorProperty(c, "其他属性", field.Extra)
	}
	comment := field.Comment
	if comment == "" {
		comment = "—"
	}
	mysqlInspectorProperty(c, "注释", comment)
	if field.CharacterMaxLength != nil {
		mysqlInspectorProperty(c, "字符长度", strconv.FormatInt(*field.CharacterMaxLength, 10))
	}
	if field.NumericPrecision != nil {
		mysqlInspectorProperty(c, "数值精度", strconv.FormatInt(*field.NumericPrecision, 10))
	}
	if field.NumericScale != nil {
		mysqlInspectorProperty(c, "小数位", strconv.FormatInt(*field.NumericScale, 10))
	}
	if field.DatetimePrecision != nil {
		mysqlInspectorProperty(c, "时间精度", strconv.FormatInt(*field.DatetimePrecision, 10))
	}
	if field.CharacterSet != nil {
		mysqlInspectorProperty(c, "字符集", *field.CharacterSet)
	}
	if field.Collation != nil {
		mysqlInspectorProperty(c, "排序规则", *field.Collation)
	}
}

func mysqlInspectorTextType(kind string) bool {
	return strings.Contains(" char varchar tinytext text mediumtext longtext enum set json ", " "+strings.ToLower(kind)+" ")
}
