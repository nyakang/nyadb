package sqlworkspace

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	"strings"
	"unicode/utf8"
)

func hasEnqueued(result contract.SQLResult) bool {
	for _, step := range result.Steps {
		if step.State == contract.StatementEnqueued {
			return true
		}
	}
	return false
}

func executionOrigin(d *Document, statement string) int {
	source := d.Editor.Buffer.String()
	selection := d.Editor.Selection()
	start := min(selection.Anchor, selection.Head)
	if selection.Anchor != selection.Head {
		relative := strings.Index(source[start:max(selection.Anchor, selection.Head)], statement)
		if relative >= 0 {
			return start + relative
		}
	}
	cursor := selection.Head
	from := 0
	found := -1
	for from <= len(source) {
		relative := strings.Index(source[from:], statement)
		if relative < 0 {
			break
		}
		offset := from + relative
		if offset <= cursor && cursor <= offset+len(statement) {
			return offset
		}
		if found >= 0 {
			found = -2
		} else if found != -2 {
			found = offset
		}
		from = offset + len(statement)
	}
	if found >= 0 {
		return found
	}
	return -1
}
func (s *State) acceptQueryDiagnostic(d *Document, r executionResult) {
	d.queryError = r.err
	d.diagnostic = contract.QueryDiagnosis(r.err)
	d.executedSQL = r.statement
	d.executedVersion = r.version
	d.Editor.SetDiagnostics(d.Editor.Buffer.Version(), nil)
	diag := d.diagnostic
	if diag == nil || diag.Position == nil || r.origin < 0 || d.Editor.Buffer.Version() != r.version {
		return
	}
	from := 0
	offset := -1
	if len(r.data.Steps) > 0 {
		for _, step := range r.data.Steps {
			relative := strings.Index(r.statement[from:], step.SQL)
			if relative < 0 {
				return
			}
			offset = from + relative
			from = offset + len(step.SQL)
		}
		if r.data.Steps[len(r.data.Steps)-1].SQL != diag.SQL {
			return
		}
	} else {
		offset = strings.Index(r.statement, diag.SQL)
		if offset < 0 || strings.Count(r.statement, diag.SQL) != 1 {
			return
		}
	}
	pos := r.origin + offset + *diag.Position
	source := d.Editor.Buffer.String()
	if pos < 0 || pos > len(source) {
		return
	}
	end := pos
	if pos < len(source) {
		_, size := utf8.DecodeRuneInString(source[pos:])
		end += size
	}
	d.Editor.SetDiagnostics(r.version, []editor.Diagnostic{{Start: pos, End: end, Severity: editor.SeverityError, Message: r.err.Error(), Source: diag.Code}})
}
func (s *State) queryDiagnosticView(c *ui.Context, d *Document) {
	if d.executedVersion != 0 && d.executedVersion != d.Editor.Buffer.Version() {
		d.Editor.SetDiagnostics(d.Editor.Buffer.Version(), nil)
	}
	if d.diagnostic == nil {
		return
	}
	diag := d.diagnostic
	ui.Row(c).Wrap().Gap(6).Padding(2, 8).Children(func() {
		if diag.Code != "" {
			ui.Text(c, locales.Text("错误码")+": "+diag.Code)
		}
		if diag.Statement > 0 {
			ui.Text(c, fmt.Sprintf(locales.Text("语句 %d"), diag.Statement))
		}
		if diag.Detail != "" {
			ui.Text(c, diag.Detail).Tooltip(diag.Detail)
		}
		if diag.Hint != "" {
			ui.Text(c, diag.Hint).TextColor(theme.Colors(c).Muted)
		}
	})
	if d.executedVersion != d.Editor.Buffer.Version() {
		text := diag.SQL
		ui.TextArea(c, &text).ReadOnly(true).Label(locales.Text("原执行语句")).FillWidth().Height(75)
	}
}
