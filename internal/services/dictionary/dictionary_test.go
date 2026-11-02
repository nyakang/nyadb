package dictionary

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func fixture() (Reader, contract.NamespaceRef, *contract.TableStructure) {
	n := contract.NamespaceRef{ProfileID: 7, Catalog: "original", Schema: "aux"}
	empty := ""
	prefix := int64(9)
	m := &contract.TableStructure{Object: n.Object("table", "records"), Name: "records", Engine: "fixture", Comment: "<script>evil()</script>\n[link](https://bad)", Columns: []contract.StructureColumn{{Name: "id", Type: "bigint", Position: 1, Key: "PRI"}, {Name: "empty", Type: "text", Nullable: true, Default: &empty, Comment: "a|b\n`code`"}}, Indexes: []contract.StructureIndex{{Name: "primary", Type: "BTREE", Unique: true, Columns: []contract.StructureIndexColumn{{Name: "id", Direction: "DESC", SubPart: &prefix}}}}, ForeignKeys: []contract.StructureForeignKey{{Name: "parent", ReferencedDatabase: "original", ReferencedTable: "parent", OnUpdate: "CASCADE", OnDelete: "RESTRICT", Columns: []contract.StructureForeignKeyColumn{{Name: "id", ReferencedName: "id"}}}}}
	r := Reader{Browse: func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
		return []contract.DBObject{{Ref: n.Object("view", "records_view"), Name: "records_view", Kind: "视图"}, {Ref: m.Object, Name: m.Name, Kind: "表"}, {Name: "skip", Kind: "function"}}, nil
	}, Metadata: func(_ context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
		if o.Kind == "view" {
			return &contract.TableStructure{Object: o, Name: o.Name, Columns: []contract.StructureColumn{{Name: "only_view", Type: "text"}}}, nil
		}
		return m, nil
	}}
	return r, n, m
}

func TestBuildQualifiedCompleteOwnedDictionaryAndDefaultDistinction(t *testing.T) {
	r, n, m := fixture()
	d, err := Build(context.Background(), r, n, nil)
	if err != nil || len(d.Tables) != 2 || d.Tables[0].Object != m.Object || d.Tables[1].Columns[0].Name != "only_view" {
		t.Fatal(d, err)
	}
	if d.Tables[0].Columns[0].DefaultReported || !d.Tables[0].Columns[1].DefaultReported || d.Tables[0].Columns[1].Default != "" || d.Tables[0].Indexes[0].Columns != "id(9) DESC" {
		t.Fatal("lost metadata or default distinction")
	}
	*m.Columns[1].Default = "changed"
	m.Columns[0].Name = "changed"
	m.Indexes[0].Columns[0].Name = "changed"
	if d.Tables[0].Columns[0].Name != "id" || d.Tables[0].Columns[1].Default != "" || d.Tables[0].Indexes[0].Columns != "id(9) DESC" {
		t.Fatal("retained mutable loader pointers")
	}
	clone := d.Clone()
	clone.Tables[0].Columns[0].Name = "other"
	if d.Tables[0].Columns[0].Name != "id" {
		t.Fatal("clone aliases metadata")
	}
	r.Browse = nil
	one, err := Build(context.Background(), r, n, &m.Object)
	if err != nil || len(one.Tables) != 1 {
		t.Fatal("single object requires unrelated browse", err)
	}
}

func TestDictionaryRejectsScopeFailureDuplicatesLimitsAndCancellationWithoutPartialDocument(t *testing.T) {
	r, n, _ := fixture()
	for _, test := range []struct {
		name   string
		change func(*Reader)
	}{
		{"wrong object", func(r *Reader) {
			r.Metadata = func(_ context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
				o.ProfileID++
				return &contract.TableStructure{Object: o, Name: o.Name}, nil
			}
		}},
		{"nil object", func(r *Reader) {
			r.Metadata = func(context.Context, contract.ObjectRef) (*contract.TableStructure, error) { return nil, nil }
		}},
		{"late failure", func(r *Reader) {
			old := r.Metadata
			r.Metadata = func(c context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
				if o.Kind == "view" {
					return nil, errors.New("fixture failure")
				}
				return old(c, o)
			}
		}},
		{"wrong browse", func(r *Reader) {
			r.Browse = func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
				return []contract.DBObject{{Name: "t", Kind: "table", Ref: contract.NamespaceRef{ProfileID: 8, Catalog: "original"}.Object("table", "t")}}, nil
			}
		}},
		{"duplicate", func(r *Reader) {
			r.Browse = func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
				return []contract.DBObject{{Name: "t", Kind: "table"}, {Name: "t", Kind: "table"}}, nil
			}
		}},
		{"table limit", func(r *Reader) {
			r.Browse = func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
				a := make([]contract.DBObject, MaxTables+1)
				for i := range a {
					a[i] = contract.DBObject{Name: "t", Kind: "table"}
				}
				return a, nil
			}
		}},
		{"byte limit", func(r *Reader) {
			r.Metadata = func(_ context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
				return &contract.TableStructure{Object: o, Name: o.Name, Comment: strings.Repeat("x", MaxBytes+1)}, nil
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := r
			test.change(&reader)
			d, err := Build(context.Background(), reader, n, nil)
			if err == nil || d.Tables != nil || d.Namespace != (contract.NamespaceRef{}) {
				t.Fatal("partial or unauthorized dictionary", err)
			}
		})
	}
	c, stop := context.WithCancel(context.Background())
	stop()
	d, err := Build(c, r, n, nil)
	if !errors.Is(err, context.Canceled) || d.Tables != nil {
		t.Fatal("cancel ignored", err)
	}
}

func TestDictionaryEscapesExportAndPreservesOldOutputOnFailure(t *testing.T) {
	r, n, _ := fixture()
	d, err := Build(context.Background(), r, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"markdown", "html"} {
		b, err := Render(context.Background(), d, format, EnglishLabels())
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		viewName := "records_view"
		if format == "markdown" {
			viewName = "records&#95;view"
		}
		if strings.Contains(text, "<script>") || !strings.Contains(text, viewName) || !strings.Contains(text, "Not reported") || !strings.Contains(text, "CASCADE") {
			t.Fatal("unsafe or incomplete artifact", text)
		}
		if format == "markdown" && (!strings.Contains(text, "a&#124;b<br>&#96;code&#96;") || !strings.Contains(text, "&#91;link&#93;")) {
			t.Fatal("Markdown syntax not escaped")
		}
	}
	path := filepath.Join(t.TempDir(), "dictionary.md")
	if err = os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	c, stop := context.WithCancel(context.Background())
	stop()
	for _, spec := range []struct {
		ctx    context.Context
		format string
	}{{c, "markdown"}, {context.Background(), "unsupported"}} {
		if Write(spec.ctx, path, d, spec.format, EnglishLabels()) == nil {
			t.Fatal("invalid export succeeded")
		}
		got, _ := os.ReadFile(path)
		if string(got) != "existing" {
			t.Fatal("failed export replaced output")
		}
	}
	if err = Write(context.Background(), path, d, "markdown", EnglishLabels()); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".nyadb-dictionary-*"))
	if len(files) != 0 {
		t.Fatal("leaked staged file")
	}
	if err = WriteTo(context.Background(), shortWriter{}, d, "html", EnglishLabels()); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write ignored", err)
	}
	if markdownText("'' & # < >") != "&#39;&#39; &amp; &#35; &lt; &gt;" {
		t.Fatal("entity escaping corrupts defaults")
	}
}

type shortWriter struct{}

func TestDictionaryRenderRejectsOversizedEscapedOutput(t *testing.T) {
	doc := Document{Tables: []Table{{Comment: strings.Repeat("&", MaxOutputBytes/5+1)}}}
	for _, format := range []string{"html", "markdown"} {
		data, err := Render(context.Background(), doc, format, EnglishLabels())
		if !errors.Is(err, ErrLimit) || data != nil {
			t.Fatalf("%s emitted oversized output: %d %v", format, len(data), err)
		}
	}
}

func (shortWriter) Write(b []byte) (int, error) { return len(b) - 1, nil }
