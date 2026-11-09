package sqlworkspace

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) Format(d *Document) {
	if d != nil && d.cypher() {
		d.err = locales.Text("Cypher 格式化暂不可用")
		return
	}
	if d != nil && d.cql() {
		d.err = locales.Text("Cassandra 暂不支持 CQL 格式化")
		return
	}
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
		profile, _ := d.sourceScope()
		format = func(text string) (string, error) { return s.deps.FormatSQL(profile, text) }
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
	if d != nil && d.cypher() {
		s.cypherCompletions(d, r)
		return
	}
	if d != nil && d.nativeCommands {
		return
	}
	if d.closed || d.Editor == nil || d.Editor.Buffer.Version() != r.Version {
		return
	}
	profile, database := d.sourceScope()
	if s.deps.CompletionCatalog != nil {
		source := d.Editor.Buffer.String()
		s.updateCompletionMetadata(d)
		request := r
		d.metadata.request = &request
		result := sqltext.CompleteSQL(source, r.Offset, s.completionCatalog(d))
		items := make([]editor.Completion, 0, len(result.Items))
		for _, value := range result.Items {
			kind := editor.CompletionText
			switch value.Kind {
			case "column":
				kind = editor.CompletionField
			case "keyword":
				kind = editor.CompletionKeyword
			case "namespace":
				kind = editor.CompletionModule
			case "alias":
				kind = editor.CompletionVariable
			}
			filter := value.Name
			if result.Start < len(source) && strings.ContainsRune("`\"[", rune(source[result.Start])) {
				filter = value.Insert
			}
			items = append(items, editor.Completion{Label: value.Name, Insert: value.Insert, Detail: value.Detail, Kind: kind, FilterText: filter})
		}
		d.Editor.ShowCompletions(result.Start, items)
		return
	}
	values := strings.Fields("SELECT FROM WHERE ORDER BY GROUP HAVING LIMIT INSERT INTO VALUES UPDATE SET DELETE JOIN LEFT RIGHT INNER ON NULL IS AND OR AS DISTINCT EXPLAIN")
	if d.cql() {
		values = nil
		result := sqltext.CompleteSQL(d.Editor.Buffer.String(), r.Offset, sqltext.CompletionCatalog{Dialect: "cassandra"})
		for _, item := range result.Items {
			values = append(values, item.Name)
		}
	}
	if s.deps.Completions != nil {
		values = append(values, s.deps.Completions(profile, database)...)
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
	if d != nil && d.cypher() {
		d.err = locales.Text("请在 Cypher 编辑器中显式运行原生 EXPLAIN 或 PROFILE")
		return
	}
	if d != nil && d.cql() {
		d.err = locales.Text("Cassandra 不支持 EXPLAIN")
		return
	}
	if d != nil && d.nativeCommands {
		d.err = d.commandText("MongoDB 请使用原生 explain 命令", "Redis 不支持 SQL EXPLAIN", "Elasticsearch 请使用原生 REST API", "Easysearch 请使用原生 REST API", "Solr 请使用原生 REST API", "CouchDB 请使用原生 REST API", "Meilisearch 请使用原生 REST API", "Qdrant 请使用原生 REST API")
		return
	}
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
	s.persistence.explainSource = text
	s.persistence.explainSQL = "EXPLAIN " + text
	if s.deps.BuildExplain != nil {
		request, err := s.deps.BuildExplain(d.profileID, text)
		if err != nil {
			d.err = err.Error()
			return
		}
		s.persistence.explainSQL = request
	}
	s.persistence.explainOpen = true
}
func (s *State) explainModal(c *ui.Context) {
	p := &s.persistence
	if !p.explainOpen {
		return
	}
	theme.Modal(c, &p.explainOpen, func() {
		ui.Column(c).Width(620).MaxWidthPercent(95).Gap(10).Children(func() {
			ui.Text(c, locales.Text("执行计划 SQL"))
			ui.TextArea(c, &p.explainSQL).ReadOnly(true).Height(180)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("取消")).Clicked() {
					p.explainOpen = false
				}
				if s.deps.BuildExplain != nil && ui.Button(c, locales.Text("普通 EXPLAIN")).Clicked() {
					p.explainOpen = false
					text := p.explainSource
					if s.deps.ValidateExplain == nil || s.deps.ValidateExplain(p.explainDoc.profileID, text) == nil {
						s.runStatement(c.Services().Invalidate, p.explainDoc, "EXPLAIN "+text)
					}
				}
				if ui.PrimaryButton(c, locales.Text("执行 EXPLAIN")).Clicked() {
					p.explainOpen = false
					s.runStatement(c.Services().Invalidate, p.explainDoc, p.explainSQL)
				}
			})
		})
	})
}
