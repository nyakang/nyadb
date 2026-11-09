package sqlworkspace

import (
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/locales"
	"strings"
)

func (d *Document) queryText(text string) string {
	if d.cql() {
		text = strings.ReplaceAll(text, "SQL", "CQL")
	}
	if d.cypher() {
		text = strings.ReplaceAll(text, "SQL", "Cypher")
	}
	return locales.Text(text)
}

func (d *Document) cql() bool { return d.commandDialect == "cassandra" }

// Restore and rebind choose the dialect from the original profile, before any
// explicit session recovery. This changes presentation and dispatch only.
func (s *State) applyQueryDialect(d *Document, profile int64) {
	if s.deps.QueryDialect == nil || d == nil || d.Editor == nil {
		return
	}
	d.commandDialect = s.deps.QueryDialect(profile)
	d.nativeCommands = d.restRequests() || d.commandDialect == "mongodb" || d.commandDialect == "redis"
	language := "SQL"
	if d.nativeCommands {
		language = "JSON"
	}
	if d.cypher() {
		d.Editor.Buffer.SetLanguage(cypherLanguage)
		return
	}
	d.Editor.Buffer.SetLanguage(editor.LanguageNamed(language))
}
