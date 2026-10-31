package sqlworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/locales"
	"strings"
)

func (s *State) Format(d *Document) {
	if d == nil || d.Editor == nil {
		return
	}
	selection := d.Editor.Selection()
	text := d.Editor.SelectedText()
	if text == "" {
		selection.Anchor = 0
		selection.Head = d.Editor.Buffer.Len()
		text = d.Editor.Buffer.String()
	}
	format := sqltext.Format
	if s.deps.FormatSQL != nil {
		format = func(text string) (string, error) { return s.deps.FormatSQL(d.profileID, text) }
	}
	formatted, e := format(text)
	if e != nil {
		d.err = e.Error()
		return
	}
	d.Editor.Select(selection.Anchor, selection.Head)
	d.Editor.Insert(formatted)
}
func (s *State) completions(d *Document, r editor.CompletionRequest) {
	if d.closed || d.Editor == nil || d.Editor.Buffer.Version() != r.Version {
		return
	}
	values := strings.Fields("SELECT FROM WHERE ORDER BY GROUP HAVING LIMIT INSERT INTO VALUES UPDATE SET DELETE JOIN LEFT RIGHT INNER ON NULL IS AND OR AS DISTINCT EXPLAIN")
	if s.deps.Completions != nil {
		values = append(values, s.deps.Completions(d.profileID, d.database)...)
	}
	seen := map[string]bool{}
	var items []editor.Completion
	for _, v := range values {
		if seen[v] || !strings.HasPrefix(strings.ToLower(v), strings.ToLower(r.Prefix)) {
			continue
		}
		seen[v] = true
		items = append(items, editor.Completion{Label: v, Insert: v})
	}
	d.Editor.ShowCompletions(r.Offset-len(r.Prefix), items)
}
func (s *State) Explain(d *Document) {
	if d == nil || d.Editor == nil || d.closed || d.loading || d.recoveryNeeded {
		return
	}
	text := strings.TrimSpace(d.Editor.SelectedText())
	if text == "" {
		text = s.statementAtCaret(d)
	}
	if text == "" {
		return
	}
	validate := sqltext.SingleMySQLStatement
	if s.deps.ValidateExplain != nil {
		validate = func(text string) error { return s.deps.ValidateExplain(d.profileID, text) }
	}
	if err := validate(text); err != nil {
		s.persistence.explainOpen = false
		s.persistence.explainDoc = nil
		s.persistence.explainSQL = ""
		d.err = err.Error()
		return
	}
	s.persistence.explainDoc = d
	s.persistence.explainSQL = "EXPLAIN " + text
	s.persistence.explainOpen = true
}
func (s *State) explainModal(c *ui.Context) {
	p := &s.persistence
	if !p.explainOpen {
		return
	}
	ui.Modal(c, &p.explainOpen, func() {
		ui.Column(c).Width(620).MaxWidthPercent(95).Padding(18).Gap(10).Children(func() {
			ui.Text(c, locales.Text("执行计划 SQL"))
			ui.TextArea(c, &p.explainSQL).ReadOnly(true).Height(180)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					p.explainOpen = false
				}
				if ui.PrimaryButton(c, locales.Text("执行 EXPLAIN")).Clicked() {
					p.explainOpen = false
					s.runStatement(c.Services().Invalidate, p.explainDoc, p.explainSQL)
				}
			})
		})
	})
}
