package objectworkspace

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Navicat 18's observed MySQL design Index page lists secondary indexes as
// one row per index; PRIMARY is represented by the key field attributes.
// Keep PRIMARY in the service metadata, filtering only the presentation.
func mysqlStructureDesignIndexes(all []mysqlsvc.MySQLStructureIndex) []mysqlsvc.MySQLStructureIndex {
	indexes := make([]mysqlsvc.MySQLStructureIndex, 0, len(all))
	for _, index := range all {
		if !strings.EqualFold(index.Name, "PRIMARY") {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func mysqlStructureIndexColumnNames(cols []mysqlsvc.MySQLStructureIndexColumn) string {
	names := make([]string, 0, len(cols))
	for _, col := range cols {
		name := col.Name
		if col.SubPart != nil {
			name += "(" + strconv.FormatInt(*col.SubPart, 10) + ")"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

func mysqlStructureFKColumnNames(cols []mysqlsvc.MySQLStructureForeignKeyColumn, referenced bool) string {
	names := make([]string, 0, len(cols))
	for _, col := range cols {
		if referenced {
			names = append(names, col.ReferencedName)
		} else {
			names = append(names, col.Name)
		}
	}
	return strings.Join(names, ", ")
}

func mysqlStructureColumnLength(c mysqlsvc.MySQLStructureColumn) string {
	if c.CharacterMaxLength != nil {
		return strconv.FormatInt(*c.CharacterMaxLength, 10)
	}
	switch c.DataType {
	case "decimal", "numeric", "float", "double":
		if c.NumericPrecision != nil {
			return strconv.FormatInt(*c.NumericPrecision, 10)
		}
	}
	return ""
}

func mysqlStructureColumnDecimals(c mysqlsvc.MySQLStructureColumn) string {
	switch c.DataType {
	case "decimal", "numeric", "float", "double":
		if c.NumericScale != nil {
			return strconv.FormatInt(*c.NumericScale, 10)
		}
	}
	return ""
}

func mysqlStructureColumnDefault(c mysqlsvc.MySQLStructureColumn) string {
	if c.Default == nil {
		// INFORMATION_SCHEMA represents both an absent default and DEFAULT NULL
		// as SQL NULL. In the observed field designer, a NOT NULL identity with
		// no default shows an empty property rather than the text "NULL".
		if !c.Nullable {
			return ""
		}
		return "NULL"
	}
	if *c.Default == "" {
		return "\"\""
	}
	return *c.Default
}

func fieldAttributes(c *ui.Context, field mysqlsvc.MySQLStructureColumn) {
	notNull := locales.Text("否")
	if !field.Nullable {
		notNull = locales.Text("是")
	}
	theme.Property(c, "不是 NULL", notNull)
	defaultValue := locales.Text("SQL NULL / 未设置")
	if field.Default != nil {
		defaultValue = *field.Default
		// Make an actual string "NULL" or "" visibly distinct from SQL NULL.
		if structureTextType(field.DataType) {
			defaultValue = strconv.Quote(defaultValue)
		}
	}
	theme.Property(c, "默认值", defaultValue)
	key := "—"
	switch field.Key {
	case "PRI":
		key = locales.Text("主键")
	case "UNI":
		key = locales.Text("唯一索引")
	case "MUL":
		key = locales.Text("索引（MUL）")
	}
	theme.Property(c, "键", key)
	autoIncrement := locales.Text("否")
	if strings.Contains(strings.ToLower(field.Extra), "auto_increment") {
		autoIncrement = locales.Text("是")
	}
	theme.Property(c, "自增", autoIncrement)
	if field.Extra != "" {
		theme.Property(c, "其他属性", field.Extra)
	}
	comment := field.Comment
	if comment == "" {
		comment = "—"
	}
	theme.Property(c, "注释", comment)
	if field.CharacterMaxLength != nil {
		theme.Property(c, "字符长度", strconv.FormatInt(*field.CharacterMaxLength, 10))
	}
	if field.NumericPrecision != nil {
		theme.Property(c, "数值精度", strconv.FormatInt(*field.NumericPrecision, 10))
	}
	if field.NumericScale != nil {
		theme.Property(c, "小数位", strconv.FormatInt(*field.NumericScale, 10))
	}
	if field.DatetimePrecision != nil {
		theme.Property(c, "时间精度", strconv.FormatInt(*field.DatetimePrecision, 10))
	}
	if field.CharacterSet != nil {
		theme.Property(c, "字符集", *field.CharacterSet)
	}
	if field.Collation != nil {
		theme.Property(c, "排序规则", *field.Collation)
	}
}

func structureTextType(kind string) bool {
	return strings.Contains(" char varchar tinytext text mediumtext longtext enum set json ", " "+strings.ToLower(kind)+" ")
}

// InspectorView presents the selected structure field without exposing grid state.
func (s *State) InspectorView(c *ui.Context, d *Document) {
	if d == nil || d.closed || d.data == nil {
		return
	}
	ui.Text(c, d.Table()).FontSize(theme.Font(c, 14)).Bold()
	theme.Property(c, "数据库", d.Database())
	if field, selected := d.SelectedField(); selected {
		theme.Property(c, "名称", field.Name)
		theme.Property(c, "类型", field.Type)
		fieldAttributes(c, field)
	}
}
