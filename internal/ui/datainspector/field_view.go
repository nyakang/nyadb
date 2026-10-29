package datainspector

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// The inspector follows the active real data tab rather than the connection
// profile, but never asks the server for an unbounded cell value.
func (s *State) FieldView(c *ui.Context, t *Panel) {
	if t == nil || t.closed {
		return
	}
	palette := theme.Colors(c)

	selection := t.Selection()
	name, cell, selected := selection.Cell()
	row := selection.Row
	if t.chunkField != "" && (!selected || t.chunkField != name || t.chunkRow != row || (t.chunkPage != nil && t.chunkPage != selection.Page)) {
		s.CancelChunk(t)
	}
	canRead := selected && !cell.Null && row >= 0 && row < len(selection.Page.RowRefs) && selection.Page.RowRefs[row] != nil
	chunkShown := selected && t.chunkField == name && t.chunkRow == row && t.chunkNext > 0
	ui.Column(c).Label(locales.Text("数据字段信息面板")).Fill().Background(palette.Panel).Children(func() {
		s.header(c, t, true)
		if selected {
			// Actions stay outside the metadata scroll. A long type declaration,
			// comment, or binary preview must not move the next-fragment control.
			ui.Toolbar(c, func() {
				if ui.Button(c, locales.Text("复制")).Label(locales.Text("复制单元格预览")).Width(50).Height(theme.RowHeight(c, theme.ControlHeight)).FontSize(theme.Font(c, theme.SmallFontSize)).Padding(2, 3).Clicked() {
					c.WriteClipboard(CopyPreview(cell))
				}
				// Toolbar moves buttons to overflow when the translated text
				// exceeds the narrow inspector width. Fixed button widths
				// avoid font-dependent overflow while full labels remain.
				if canRead && ui.Button(c, locales.Text("下段")).Label(locales.Text("读取所选字段下一段")).Width(50).Height(theme.RowHeight(c, theme.ControlHeight)).FontSize(theme.Font(c, theme.SmallFontSize)).Padding(2, 3).
					Disabled(t.chunkLoading || t.chunkDone).Clicked() {
					s.LoadChunk(t, c.Services().Invalidate)
				}
				if chunkShown && ui.Button(c, locales.Text("片段")).Label(locales.Text("复制此片段")).Width(50).Height(theme.RowHeight(c, theme.ControlHeight)).FontSize(theme.Font(c, theme.SmallFontSize)).Padding(2, 3).Clicked() {
					c.WriteClipboard(t.chunkText)
				}
			}).Height(theme.RowHeight(c, theme.CommandBarHeight)).FillWidth().MinWidth(0).Padding(1, 4).Gap(2)
		}
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(12, 10).Gap(9).Children(func() {
				if selection.Page == nil {
					message := "正在读取字段元数据…"
					if selection.Error != "" {
						message = "字段元数据不可用，请重试读取数据"
					} else if !selection.Loading {
						message = "尚未加载字段元数据"
					}
					ui.Text(c, locales.Text(message)).TextColor(palette.Muted)
					return
				}
				field, found, complete := selection.Field()
				if !found {
					ui.Text(c, locales.Text("字段不存在或已被隐藏")).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
					return
				}
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, ColumnTypeGlyph(field.DataType)).Bold().FontSize(theme.Font(c, 17)).TextColor(palette.Blue)
					ui.Text(c, field.Name).Bold().FontSize(theme.Font(c, 14)).SingleLine().Grow(1).MinWidth(0).Tooltip(field.Name)
				})
				ui.Text(c, t.database+"."+t.table).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(palette.Muted).SingleLine().Tooltip(t.database + "." + t.table)
				ui.Divider(c)
				Property(c, "类型", field.Type)
				if !complete {
					ui.Text(c, locales.Text("仅有基础字段类型，完整元数据尚未加载")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
				} else {
					FieldAttributes(c, field)
				}
				ui.Divider(c)
				if !selected {
					ui.Text(c, locales.Text("选择数据单元格以查看值的安全预览")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
					return
				}
				Property(c, "行号", fmt.Sprintf("%d", selection.Offset+int64(selection.Row)+1))
				if cell.Null {
					Property(c, "值状态", "SQL NULL")
				} else if cell.Truncated {
					Property(c, "值状态", locales.Text("已截断的预览"))
				} else {
					Property(c, "值状态", locales.Text("预览完整"))
				}
				value := cell.Text
				if cell.Null {
					value = "NULL"
				} else if value == "" {
					value = "\"\""
				}
				ui.Scroll(c).Height(86).FillWidth().Border(1, palette.Border).Children(func() {
					ui.Text(c, value).FontSize(theme.Font(c, theme.FontSize)).Padding(3)
				})
				if canRead {
					ui.Text(c, locales.Text("大字段按需读取 · 单次最多 1024 字符/字节")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
					if t.chunkLoading {
						ui.Text(c, locales.Text("正在读取字段片段…")).FontSize(theme.Font(c, 11))
					}
					if t.chunkErr != "" {
						ui.Text(c, t.chunkErr).FontSize(theme.Font(c, 11)).TextColor(palette.Error)
					}
					if chunkShown {
						positionProperty(c, locales.Text("已读位置"), fmt.Sprintf("%d / %d", t.chunkNext, t.chunkTotal))
						ui.Scroll(c).Height(112).FillWidth().Border(1, palette.Border).Children(func() {
							ui.Text(c, t.chunkText).FontSize(theme.Font(c, theme.SmallFontSize)).Padding(3)
						})
					}
				}
			})
		})
	})
}

// The reference inspector stacks property labels above values, so long MySQL
// declarations and comments can wrap without shrinking the data grid.
func Property(c *ui.Context, label, value string) {
	palette := theme.Colors(c)

	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, locales.Text(label)).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
		ui.Text(c, value).FontSize(theme.Font(c, 12))
	})
}

func FieldAttributes(c *ui.Context, field mysqlsvc.MySQLStructureColumn) {
	notNull := locales.Text("否")
	if !field.Nullable {
		notNull = locales.Text("是")
	}
	Property(c, "不是 NULL", notNull)
	defaultValue := locales.Text("SQL NULL / 未设置")
	if field.Default != nil {
		defaultValue = *field.Default
		// Make an actual string "NULL" or "" visibly distinct from SQL NULL.
		if mysqlInspectorTextType(field.DataType) {
			defaultValue = strconv.Quote(defaultValue)
		}
	}
	Property(c, "默认值", defaultValue)
	key := "—"
	switch field.Key {
	case "PRI":
		key = locales.Text("主键")
	case "UNI":
		key = locales.Text("唯一索引")
	case "MUL":
		key = locales.Text("索引（MUL）")
	}
	Property(c, "键", key)
	autoIncrement := locales.Text("否")
	if strings.Contains(strings.ToLower(field.Extra), "auto_increment") {
		autoIncrement = locales.Text("是")
	}
	Property(c, "自增", autoIncrement)
	if field.Extra != "" {
		Property(c, "其他属性", field.Extra)
	}
	comment := field.Comment
	if comment == "" {
		comment = "—"
	}
	Property(c, "注释", comment)
	if field.CharacterMaxLength != nil {
		Property(c, "字符长度", strconv.FormatInt(*field.CharacterMaxLength, 10))
	}
	if field.NumericPrecision != nil {
		Property(c, "数值精度", strconv.FormatInt(*field.NumericPrecision, 10))
	}
	if field.NumericScale != nil {
		Property(c, "小数位", strconv.FormatInt(*field.NumericScale, 10))
	}
	if field.DatetimePrecision != nil {
		Property(c, "时间精度", strconv.FormatInt(*field.DatetimePrecision, 10))
	}
	if field.CharacterSet != nil {
		Property(c, "字符集", *field.CharacterSet)
	}
	if field.Collation != nil {
		Property(c, "排序规则", *field.Collation)
	}
}

func mysqlInspectorTextType(kind string) bool {
	return strings.Contains(" char varchar tinytext text mediumtext longtext enum set json ", " "+strings.ToLower(kind)+" ")
}

func positionProperty(c *ui.Context, label, value string) {
	palette := theme.Colors(c)
	ui.Row(c).Gap(10).Children(func() {
		ui.Text(c, label).Width(62).FontSize(theme.Font(c, 12)).TextColor(palette.Muted)
		ui.Text(c, value).FontSize(theme.Font(c, 12)).SingleLine()
	})
}

// ColumnTypeGlyph provides NyaDB-authored type hints for native column headers
// and field information, without proprietary database-manager artwork.
func ColumnTypeGlyph(kind string) string {
	switch strings.ToLower(kind) {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "float", "double", "real", "bit", "bool", "boolean":
		return "#"
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext", "enum", "set":
		return "A"
	case "date", "time", "datetime", "timestamp", "year":
		return "◷"
	case "json":
		return "{}"
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob":
		return "◇"
	case "geometry", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection":
		return "✧"
	}
	return "·"
}
