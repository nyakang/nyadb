package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func setSchemaComment(comment **string, value string) {
	if value == "" {
		*comment = nil
		return
	}
	*comment = &value
}

// A nullable comment stays absent while merely viewing the form. The explicit
// checkbox also lets an empty native comment remain distinct from SQL NULL.
func (s *State) nativeSchemaComment(c *ui.Context, d *Document, label string, comment **string, locked bool) {
	present := *comment != nil
	caption := locales.Text(label)
	if ui.Checkbox(c, &present, locales.Text("设置注释")).Label(locales.Text("设置注释") + "/" + caption).Disabled(locked).Changed() {
		if present {
			value := ""
			*comment = &value
		} else {
			*comment = nil
		}
	}
	if *comment != nil {
		s.schemaDesignInput(c, d, label, *comment, locked)
	} else {
		ui.Text(c, caption+":").TextColor(theme.Colors(c).Muted)
	}
}

func (s *State) openGaussSchemaOptions(c *ui.Context, d *Document, locked bool) {
	x := d.schemaDesign
	p := x.draft.OpenGauss
	if p == nil {
		return
	}
	s.nativeSchemaComment(c, d, "表注释", &p.Comment, locked)
	retained := locked || x.original.Schema.Exists
	s.schemaDesignInput(c, d, "表空间", &p.Tablespace, retained)
	persistence := locales.Text("持久表")
	if p.Persistence == "u" {
		persistence = locales.Text("不记录日志的表")
	}
	if schemaDesignChoice(c, "表持久性", &persistence, []string{locales.Text("持久表"), locales.Text("不记录日志的表")}, retained) {
		p.Persistence = "p"
		if persistence == locales.Text("不记录日志的表") {
			p.Persistence = "u"
		}
	}
	for i := range p.Parameters {
		s.schemaDesignInput(c, d, "存储参数", &p.Parameters[i], retained)
	}
	ui.Row(c).Wrap().Gap(8).Children(func() {
		if ui.Button(c, locales.Text("添加存储参数")).Disabled(retained).Clicked() {
			p.Parameters = append(p.Parameters, "orientation=row")
		}
		if ui.Button(c, locales.Text("删除末尾存储参数")).Disabled(retained || len(p.Parameters) == 0).Clicked() {
			p.Parameters = p.Parameters[:len(p.Parameters)-1]
		}
	})
	ui.Text(c, locales.Text("所有者")+": "+p.Owner).TextColor(theme.Colors(c).Muted)
	if x.original.Schema.Exists {
		ui.Text(c, locales.Text("已有表的存储与所有者设置保留；更改需独立操作。"))
	}
	ui.Text(c, locales.Text("此页面保留已有权限、行安全与继承设置。"))
}

func (s *State) openGaussDefinitionProperties(c *ui.Context, d *Document, locked bool) {
	x := d.schemaDesign
	if x.original.Schema.Engine != "opengauss" {
		return
	}
	if x.page == 1 && x.row >= 0 && x.row < len(x.draft.Clauses) {
		if p := x.draft.Clauses[x.row].OpenGauss; p != nil {
			s.nativeSchemaComment(c, d, "约束注释", &p.Comment, locked)
		}
		return
	}
	if x.definitionRow < 0 || x.definitionRow >= len(x.draft.Definitions) {
		return
	}
	definition := &x.draft.Definitions[x.definitionRow]
	p := definition.OpenGauss
	if p == nil {
		return
	}
	if definition.Kind == "trigger" {
		states := map[string]string{"O": locales.Text("正常启用"), "D": locales.Text("禁用"), "R": locales.Text("仅复制时启用"), "A": locales.Text("始终启用")}
		state := states[p.Enabled]
		if state == "" {
			state = states["O"]
		}
		if schemaDesignChoice(c, "触发器状态", &state, []string{states["O"], states["D"], states["R"], states["A"]}, locked) {
			for value, label := range states {
				if state == label {
					p.Enabled = value
					break
				}
			}
		}
		s.nativeSchemaComment(c, d, "触发器注释", &p.Comment, locked)
	} else if definition.Kind == "index" {
		s.nativeSchemaComment(c, d, "索引注释", &p.Comment, locked)
	}
}
