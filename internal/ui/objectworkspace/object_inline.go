package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func (s *State) BeginInlineRename(name, kind string) {
	b := s.currentBrowser()
	b.renameName, b.renameKind, b.renameDraft = name, kind, name
	b.renameEditing = true
}
func (s *State) objectNameCell(c *ui.Context, obj contract.DBObject) {
	if obj.Kind != "表" || !s.CanRenameTable(s.currentBrowser().ProfileID, s.currentBrowser().Database, obj.Name) {
		ui.Text(c, obj.Name).SingleLine()
		return
	}
	name := obj.Name
	if ui.EditableText(c, &name).Label("对象名称："+obj.Name).Changed() && name != obj.Name {
		if s.canNativeRename(s.currentBrowser().ProfileID, s.currentBrowser().Database, obj.Name) {
			s.beginSchemaRenameOperation(s.currentBrowser().ProfileID, s.currentBrowser().Database, obj.Name)
			if x := s.schemaRename; x != nil {
				x.destination = name
			}
			return
		}
		s.BeginTableChange(s.currentBrowser().ProfileID, s.currentBrowser().Database, obj.Name, mysqlsvc.MySQLTableRename)
		s.tableNewName = name

		profile, database, original, destination := s.tableProfileID, s.tableDatabase, s.tableName, s.tableNewName
		next := func(services ui.Services) {
			if s.tableProfileID != profile || s.tableDatabase != database || s.tableName != original || s.tableNewName != destination {
				return
			}
			s.startMySQLTableChange(services)
			s.tableDialog = false
		}
		if s.canSchemaDesign(s.currentBrowser().ProfileID, s.currentBrowser().Database, obj.Name) || s.deps.BeforeDDL == nil || !s.deps.BeforeDDL(c.Services(), s.currentBrowser().ProfileID, s.currentBrowser().Database, obj.Name, next) {
			next(c.Services())
		}
	}
	if group := s.ObjectGroup(s.currentBrowser().ProfileID, s.currentBrowser().Database, obj.Kind, obj.Name); group != "" {
		ui.Text(c, " · "+group).SingleLine()
	}
}
