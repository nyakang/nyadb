package dictionary

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const MaxOutputBytes = 32 << 20

type Labels struct {
	Title, Namespace, Name, Kind, Engine, Comment, Fields, Type, Nullable, Default, Key, Extra, Generated, Indexes, Unique, Columns, ForeignKeys, Reference, ReferencedColumns, OnUpdate, OnDelete, Yes, No, Unreported string
}

func EnglishLabels() Labels {
	return Labels{Title: "Data dictionary", Namespace: "Database / schema", Name: "Name", Kind: "Kind", Engine: "Engine", Comment: "Comment", Fields: "Fields", Type: "Type", Nullable: "Nullable", Default: "Default", Key: "Key", Extra: "Extra", Generated: "Generated", Indexes: "Indexes", Unique: "Unique", Columns: "Columns", ForeignKeys: "Foreign keys", Reference: "Referenced object", ReferencedColumns: "Referenced columns", OnUpdate: "On update", OnDelete: "On delete", Yes: "Yes", No: "No", Unreported: "Not reported"}
}

type section struct {
	Title   string
	Headers []string
	Rows    [][]string
}
type exportTable struct {
	Name, Kind, Engine, Comment string
	Sections                    []section
}
type exportDocument struct {
	Labels    Labels
	Namespace string
	Tables    []exportTable
}

func project(d Document, l Labels) exportDocument {
	boolean := func(b bool) string {
		if b {
			return l.Yes
		}
		return l.No
	}
	out := exportDocument{Labels: l, Namespace: d.Namespace.Catalog + " / " + d.Namespace.Schema}
	for _, t := range d.Tables {
		v := exportTable{Name: t.Object.Name, Kind: t.Kind, Engine: t.Engine, Comment: t.Comment}
		fields := section{Title: l.Fields, Headers: []string{l.Name, l.Type, l.Nullable, l.Default, l.Key, l.Generated, l.Extra, l.Comment}}
		for _, c := range t.Columns {
			value := l.Unreported
			if c.DefaultReported {
				value = c.Default
			}
			fields.Rows = append(fields.Rows, []string{c.Name, c.Type, boolean(c.Nullable), value, c.Key, boolean(c.Generated), c.Extra, c.Comment})
		}
		indexes := section{Title: l.Indexes, Headers: []string{l.Name, l.Kind, l.Unique, l.Columns}}
		for _, i := range t.Indexes {
			indexes.Rows = append(indexes.Rows, []string{i.Name, i.Type, boolean(i.Unique), i.Columns})
		}
		fks := section{Title: l.ForeignKeys, Headers: []string{l.Name, l.Columns, l.Reference, l.ReferencedColumns, l.OnUpdate, l.OnDelete}}
		for _, f := range t.ForeignKeys {
			fks.Rows = append(fks.Rows, []string{f.Name, f.Columns, f.Reference, f.ReferencedColumns, f.OnUpdate, f.OnDelete})
		}
		v.Sections = []section{fields, indexes, fks}
		out.Tables = append(out.Tables, v)
	}
	return out
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxOutputBytes {
		return 0, ErrLimit
	}
	return b.Buffer.Write(p)
}

// All user metadata is escaped as text in both formats, including Markdown
// syntax. A nil default means the adapter did not report a value; it does not
// authorize inventing an empty string, SQL NULL, or a missing declaration.
func Render(ctx context.Context, d Document, format string, labels Labels) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	view := project(d, labels)
	b := &limitedBuffer{}
	switch format {
	case "html":
		if err := htmlDocument.Execute(b, view); err != nil {
			return nil, err
		}
	case "markdown":
		var err error
		write := func(value string) {
			if err == nil {
				_, err = b.Write([]byte(value))
			}
		}
		write("# " + markdownText(labels.Title) + "\n\n" + markdownText(labels.Namespace) + ": " + markdownText(view.Namespace) + "\n\n")
		for _, t := range view.Tables {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			write("## " + markdownText(t.Name) + "\n\n")
			for _, pair := range [][2]string{{labels.Kind, t.Kind}, {labels.Engine, t.Engine}, {labels.Comment, t.Comment}} {
				write(markdownText(pair[0]) + ": " + markdownText(pair[1]) + "\n\n")
			}
			for _, s := range t.Sections {
				write("### " + markdownText(s.Title) + "\n\n")
				row := func(values []string) {
					write("| ")
					for _, v := range values {
						write(markdownText(v) + " | ")
					}
					write("\n")
				}
				row(s.Headers)
				separators := make([]string, len(s.Headers))
				for i := range separators {
					separators[i] = "---"
				}
				row(separators)
				for _, r := range s.Rows {
					row(r)
				}
				write("\n")
			}
		}
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unsupported data dictionary format")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func markdownText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;", "\\", "&#92;", "`", "&#96;", "[", "&#91;", "]", "&#93;", "*", "&#42;", "_", "&#95;", "|", "&#124;", "#", "&#35;", "\r\n", "<br>", "\r", "<br>", "\n", "<br>")
	return r.Replace(s)
}

var htmlDocument = template.Must(template.New("dictionary").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Labels.Title}}</title><style>body{font:14px sans-serif;margin:2em;color:#222}table{border-collapse:collapse;width:100%;margin:1em 0}th,td{border:1px solid #bbb;padding:.5em;text-align:left;white-space:pre-wrap;overflow-wrap:anywhere}th{background:#eee}h2{margin-top:2em}dl{display:grid;grid-template-columns:max-content auto;gap:.5em}dd{margin:0;white-space:pre-wrap}</style></head><body>
<h1>{{.Labels.Title}}</h1><p>{{.Labels.Namespace}}: {{.Namespace}}</p>
{{range .Tables}}<h2>{{.Name}}</h2><dl><dt>{{$.Labels.Kind}}</dt><dd>{{.Kind}}</dd><dt>{{$.Labels.Engine}}</dt><dd>{{.Engine}}</dd><dt>{{$.Labels.Comment}}</dt><dd>{{.Comment}}</dd></dl>
{{range .Sections}}<h3>{{.Title}}</h3><table><thead><tr>{{range .Headers}}<th>{{.}}</th>{{end}}</tr></thead><tbody>{{range .Rows}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{end}}</tbody></table>{{end}}{{end}}</body></html>`))

// Write stages in the target directory. Errors and cancellation before the
// final rename preserve an existing output and remove the owned temporary file.
func Write(ctx context.Context, path string, d Document, format string, labels Labels) error {
	data, err := Render(ctx, d, format, labels)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".nyadb-dictionary-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// WriteTo is useful for integrations that own their atomic output staging.
func WriteTo(ctx context.Context, w io.Writer, d Document, format string, labels Labels) error {
	b, err := Render(ctx, d, format, labels)
	if err != nil {
		return err
	}
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}
