package objectworkspace

import (
	"strconv"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) objectColumns(profile int64) []ui.TableColumn {
	engine := "mysql"
	if s.deps.Engine != nil {
		engine = s.deps.Engine(profile)
	}
	columns := append([]ui.TableColumn(nil), mysqlObjectColumns...)
	if engine == "mongodb" || engine == "redis" || (engine == "elasticsearch" || engine == "easysearch") {
		columns = []ui.TableColumn{{ID: "name", Title: "名称", Width: 265}, {ID: "kind", Title: "类型", Width: 100}}
	}
	if engine == "postgresql" {
		columns = []ui.TableColumn{{ID: "name", Title: "名称", Width: 265}, {ID: "kind", Title: "类型", Width: 100}, {ID: "rows", Title: "估算行数", Width: 100}, {ID: "size", Title: "大小", Width: 110}, {ID: "comment", Title: "注释", Width: 200}}
	}
	if engine == "sqlite" {
		columns = []ui.TableColumn{{ID: "name", Title: "名称", Width: 265}, {ID: "kind", Title: "类型", Width: 100}, {ID: "indexes", Title: "有索引", Width: 90}, {ID: "triggers", Title: "有触发器", Width: 90}}
	}
	for i := range columns {
		if columns[i].ID == "" {
			columns[i].ID = columns[i].Title
		}
		columns[i].Title = locales.Text(columns[i].Title)
	}
	return columns
}
func (s *State) objectCell(profile int64, obj contract.DBObject, col int) string {
	engine := "mysql"
	if s.deps.Engine != nil {
		engine = s.deps.Engine(profile)
	}
	if engine == "mongodb" || engine == "redis" || (engine == "elasticsearch" || engine == "easysearch") {
		if col == 0 {
			return obj.Name
		}
		if col == 1 {
			return locales.Text(obj.Kind)
		}
		return ""
	}
	unknown := locales.Text("未知")
	number := func(v *int64) string {
		if v == nil {
			return unknown
		}
		return strconv.FormatInt(*v, 10)
	}
	flag := func(v *bool) string {
		if v == nil {
			return unknown
		}
		if *v {
			return locales.Text("是")
		}
		return locales.Text("否")
	}
	if engine == "postgresql" {
		switch col {
		case 0:
			return obj.Name
		case 1:
			return locales.Text(obj.Kind)
		case 2:
			if obj.RowsEstimate == nil {
				return unknown
			}
			return "≈" + number(obj.RowsEstimate)
		case 3:
			return number(obj.DataLength)
		case 4:
			return obj.Comment
		}
	}
	if engine == "sqlite" {
		switch col {
		case 0:
			return obj.Name
		case 1:
			return locales.Text(obj.Kind)
		case 2:
			return flag(obj.HasIndexes)
		case 3:
			return flag(obj.HasTriggers)
		case 4:
			return number(obj.RootPage)
		}
	}
	return mysqlObjectCellText(obj, col)
}
