package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func isMySQLProgram(kind string) bool {
	return kind == "函数" || kind == "存储过程" || kind == "事件"
}

func (s *State) openMySQLObject(services ui.Services, database, name, kind string) {
	if isMySQLProgram(kind) {
		s.openMySQLProgram(database, name, kind)
		return
	}
	s.openMySQLData(services, database, name)
}

// Programs expose the server's read-only definition and metadata. Do not
// execute a stored routine on double-click; such calls could mutate data.
func (s *State) openMySQLProgram(database, name, kind string) {
	id := s.mysqlSessionProfileID
	if id == 0 || s.mysqlSessionStateFor(id) != "已连接" {
		s.status = "请先连接 MySQL"
		return
	}
	var item *mysqlsvc.MySQLDBObject
	for i := range s.mysqlObjectCache[database] {
		obj := &s.mysqlObjectCache[database][i]
		if obj.Name == name && obj.Kind == kind {
			item = obj
			break
		}
	}
	if item == nil {
		s.status = "对象元数据不可用，请先刷新：" + name
		return
	}
	for index, t := range s.tabs {
		if t.kind == tabProgram && t.programProfileID == id &&
			t.programDatabase == database && t.program.Name == name && t.program.Kind == kind {
			t.program = *item
			s.activeTab = index
			return
		}
	}
	s.nextTabID++
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabProgram, title: name,
		programProfileID: id, programDatabase: database, program: *item})
	s.activeTab = len(s.tabs) - 1
	s.status = "已打开" + kind + "定义：" + database + "." + name
}

func (s *State) mysqlProgramWorkspace(c *ui.Context, t *tab) {
	palette := theme.Colors(c)
	obj := t.program
	ui.Column(c).Fill().Background(palette.App).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(3, 8).Gap(8).AlignItems(ui.Center).
			Background(palette.Header).Children(func() {
			theme.Icon(c, "function", 14, palette.Muted)
			ui.Text(c, t.programDatabase+" / "+obj.Name).FontSize(theme.Font(c, 12)).Grow(1).MinWidth(0).SingleLine()
			ui.Text(c, obj.Kind+" · 只读").TextColor(palette.Muted).FontSize(theme.Font(c, 11))
		})
		ui.ScrollBoth(c).Grow(1).Children(func() {
			ui.Column(c).Padding(12, 14).Gap(7).MinWidth(480).Children(func() {
				for _, field := range []struct{ label, value string }{
					{"类型", obj.Kind}, {"名称", obj.Name}, {"定义者", obj.Definer},
					{"参数", obj.Parameters}, {"返回类型", obj.ReturnType},
					{"SQL SECURITY", obj.SQLSecurity}, {"状态", obj.Status},
					{"调度", obj.Schedule}, {"开始时间", obj.Starts}, {"结束时间", obj.Ends},
					{"ON COMPLETION", obj.OnCompletion},
					{"创建时间", obj.CreatedAt}, {"修改时间", obj.ModifiedAt}, {"注释", obj.Comment},
				} {
					if field.value == "" {
						continue
					}
					f := field
					ui.Row(c).AlignItems(ui.Start).Gap(12).Children(func() {
						ui.Text(c, f.label).Width(115).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
						ui.Text(c, f.value).FontSize(theme.Font(c, 12)).Grow(1).MinWidth(0)
					})
				}
				ui.Text(c, "对象定义（由服务器返回）").FontSize(theme.Font(c, 12)).Bold()
				definition := strings.TrimSpace(obj.Definition)
				if definition == "" {
					definition = "当前账号未获取到定义，可能受到服务器权限限制"
				}
				// Bound the number of native rows; large bodies are retained in
				// the metadata but should not create thousands of UI elements.
				lines := strings.Split(definition, "\n")
				if len(lines) > 500 {
					lines = append(lines[:500], "… (定义过长，仅展示前 500 行)")
				}
				for _, line := range lines {
					ui.Text(c, line).FontSize(theme.Font(c, 11))
				}
			})
		})
	})
}
