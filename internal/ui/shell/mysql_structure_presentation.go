package shell

import (
	"strconv"
	"strings"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
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
