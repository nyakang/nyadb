package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) postgresSchemaField(c *ui.Context, d *Document, locked bool) {
	x := d.schemaDesign
	f := &x.pgField
	s.schemaDesignInput(c, d, "字段名称", &x.name, locked)
	s.schemaDesignInput(c, d, "字段类型", &f.Type, locked)
	notNullLocked := false
	if x.fieldRow >= 0 && x.fieldRow < len(x.draft.Clauses) && x.original.Schema.Exists {
		if err := schemaddl.PostgreSQLNotNullEditable(x.original.Schema.Postgres, x.draft.Clauses[x.fieldRow].ID); err != nil {
			notNullLocked = true
			ui.Text(c, schemaDesignError(err)).TextColor(theme.Colors(c).Muted)
		}
	}
	ui.Checkbox(c, &f.NotNull, locales.Text("非空")).Disabled(locked || f.Identity != "" || notNullLocked)
	ui.Checkbox(c, &f.HasDefault, locales.Text("设置默认值")).Disabled(locked || f.Identity != "" || f.Generation != "")
	s.schemaDesignInput(c, d, "默认值表达式", &f.Default, locked || !f.HasDefault)
	s.schemaDesignInput(c, d, "排序规则", &f.Collation, locked)
	identity := f.Identity
	if identity == "" {
		identity = locales.Text("无")
	}
	if schemaDesignChoice(c, "标识列模式", &identity, []string{locales.Text("无"), "ALWAYS", "BY DEFAULT"}, locked || f.HasDefault || f.Generation != "") {
		f.Identity = identity
		if identity == locales.Text("无") {
			f.Identity = ""
		} else {
			f.NotNull = true
		}
	}
	s.schemaDesignInput(c, d, "标识序列选项", &f.IdentityOptions, locked || f.Identity == "")
	generation := f.GenerationStorage
	if f.Generation == "" {
		generation = locales.Text("无")
	}
	options := []string{locales.Text("无"), "STORED"}
	if x.original.Schema.Postgres != nil && x.original.Schema.Postgres.ServerVersion >= 180000 {
		options = append(options, "VIRTUAL")
	}
	if schemaDesignChoice(c, "生成列存储", &generation, options, locked || f.HasDefault || f.Identity != "") {
		f.GenerationStorage = generation
		if generation == locales.Text("无") {
			f.Generation, f.GenerationStorage = "", ""
		} else if f.Generation == "" {
			f.Generation = "0"
		}
	}
	s.schemaDesignInput(c, d, "生成表达式", &f.Generation, locked || f.GenerationStorage == "")
	if x.fieldRow >= 0 && x.fieldRow < len(x.draft.Clauses) {
		if p := x.draft.Clauses[x.fieldRow].Postgres; p != nil {
			s.schemaDesignInput(c, d, "字段注释", &p.Comment, locked)
		}
	}
}

func (s *State) postgresSchemaOptions(c *ui.Context, d *Document, locked bool) {
	x := d.schemaDesign
	p := x.draft.Postgres
	if p == nil {
		return
	}
	s.schemaDesignInput(c, d, "表注释", &p.Comment, locked)
	s.schemaDesignInput(c, d, "访问方法", &p.AccessMethod, locked)
	s.schemaDesignInput(c, d, "表空间", &p.Tablespace, locked)
	persistence := p.Persistence
	if persistence == "u" {
		persistence = locales.Text("不记录日志的表")
	} else {
		persistence = locales.Text("持久表")
	}
	if schemaDesignChoice(c, "表持久性", &persistence, []string{locales.Text("持久表"), locales.Text("不记录日志的表")}, locked) {
		p.Persistence = "p"
		if persistence == locales.Text("不记录日志的表") {
			p.Persistence = "u"
		}
	}
	for i := range p.Parameters {
		s.schemaDesignInput(c, d, "存储参数", &p.Parameters[i], locked)
	}
	ui.Row(c).Wrap().Gap(8).Children(func() {
		if ui.Button(c, locales.Text("添加存储参数")).Disabled(locked).Clicked() {
			p.Parameters = append(p.Parameters, "fillfactor=100")
		}
		if ui.Button(c, locales.Text("删除末尾存储参数")).Disabled(locked || len(p.Parameters) == 0).Clicked() {
			p.Parameters = p.Parameters[:len(p.Parameters)-1]
		}
	})
	// These fields are retained as complete catalog data. They are not
	// implicitly rewritten by the common table-property form.
	ui.Text(c, locales.Text("所有者")+": "+p.Owner).TextColor(theme.Colors(c).Muted)
	ui.Text(c, locales.Text("此页面保留已有权限、行安全与继承设置。"))
}

func schemaDesignChoice(c *ui.Context, label string, value *string, choices []string, disabled bool) (changed bool) {
	caption := locales.Text(label)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, caption+":").Width(110).SingleLine().Tooltip(caption)
		changed = ui.Select(c, value, choices).Label(caption).Disabled(disabled).Grow(1).MinWidth(0).Changed()
	})
	return
}
