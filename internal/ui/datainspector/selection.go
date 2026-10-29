package datainspector

import (
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

// Selection is a UI-thread snapshot. Its page and hidden-column map are borrowed
// read-only; workers capture a copied row identity and never read this snapshot.
type Selection struct {
	Page    *mysqlsvc.MySQLDataPage
	Column  string
	Row     int
	Hidden  map[string]bool
	Offset  int64
	Loading bool
	Error   string
}

// Field shows the first visible column before a cell is selected. The final
// bool distinguishes full server metadata from legacy DATA_TYPE-only pages.
func (t Selection) Field() (mysqlsvc.MySQLStructureColumn, bool, bool) {
	if t.Page == nil {
		return mysqlsvc.MySQLStructureColumn{}, false, false
	}
	page := t.Page
	name := t.Column
	if name == "" {
		for _, candidate := range page.Columns {
			if !t.Hidden[candidate] {
				name = candidate
				break
			}
		}
	}
	for i, candidate := range page.Columns {
		if candidate != name || t.Hidden[candidate] {
			continue
		}
		if i < len(page.ColumnMetadata) && page.ColumnMetadata[i].Name == name {
			return page.ColumnMetadata[i], true, true
		}
		field := mysqlsvc.MySQLStructureColumn{Name: name}
		if i < len(page.ColumnKinds) {
			field.Type, field.DataType = page.ColumnKinds[i], page.ColumnKinds[i]
		}
		return field, true, false
	}
	return mysqlsvc.MySQLStructureColumn{}, false, false
}

func (t Selection) Cell() (string, mysqlsvc.MySQLDataCell, bool) {
	if t.Page == nil || t.Row < 0 ||
		t.Row >= len(t.Page.Rows) {
		return "", mysqlsvc.MySQLDataCell{}, false
	}
	name := t.Column
	for col, candidate := range t.Page.Columns {
		if name == candidate && col < len(t.Page.Rows[t.Row]) {
			return name, t.Page.Rows[t.Row][col], true
		}
	}
	return "", mysqlsvc.MySQLDataCell{}, false
}

func CopyPreview(cell mysqlsvc.MySQLDataCell) string {
	if cell.Null {
		return "NULL"
	}
	if cell.Truncated {
		return cell.Text + "\n" + locales.Text("[预览已截断；不是完整字段内容]")
	}
	return cell.Text
}
