package objectworkspace

import (
	"context"
	"errors"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"strings"
	"testing"
)

func TestMeilisearchReadonlyDefinitionsAndCapabilityBoundDocumentGrid(t *testing.T) {
	var opened int
	var documents bool
	s := New(Dependencies{Engine: func(int64) string { return "meilisearch" }, Session: func() Session { return Session{ProfileID: 7, Generation: 8, Connected: true} }, Capabilities: func(int64) contract.Capabilities {
		return contract.Capabilities{Query: true, Metadata: true, Objects: true, Documents: documents}
	}, OpenData: func(ui.Services, int64, string, string) { opened++ }})
	defer s.CloseAll()
	source := `{"rankingRules":["words"],"future":{"exact":9007199254740993,"text":"原生🐱"}}`
	ref := contract.NamespaceRef{ProfileID: 7, Catalog: "owned"}.Object("settings", "settings")
	s.Loader = func(context.Context, int64, string, string) (*contract.TableStructure, error) {
		return &contract.TableStructure{Object: ref, Database: ref.Catalog, Name: ref.Name, Kind: ref.Kind, Engine: "Meilisearch", Definition: source, Definitions: []contract.Definition{{Name: ref.Name, Kind: ref.Kind, SQL: source}}}, nil
	}
	d := s.OpenQualifiedStructure(1, ref, "索引设置")
	s.Load(d, nil, false)
	waitStructure(t, s, d)
	if d.Error() != "" || d.Object() != ref || d.Data().Definition != source || d.Data().Editable {
		t.Fatal(d.Error())
	}
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 720, 520)
	if !tt.HasText("Meilisearch 原生定义（只读）") || tt.HasText("外键") || tt.HasText("编辑表结构") {
		t.Fatal("native definition inherited SQL controls")
	}
	s.openObjectFor(ui.Services{}, 7, "owned", "documents", "文档")
	if opened != 0 || s.canOpenObject(7, "文档") {
		t.Fatal("unavailable document grid dispatched")
	}
	documents = true
	s.openObjectFor(ui.Services{}, 7, "owned", "documents", "文档")
	if opened != 1 || !s.canOpenObject(7, "文档") {
		t.Fatal("verified document grid did not dispatch")
	}
	cols := s.objectColumns(7)
	if len(cols) != 4 || cols[1].ID != "kind" || s.objectCell(7, contract.DBObject{CreatedAt: "original-time"}, 2) != "original-time" {
		t.Fatal(cols)
	}
}
func TestMeilisearchDefinitionSearchKeepsAllowedResultsAndOriginalIdentity(t *testing.T) {
	n := contract.Namespace{Ref: contract.NamespaceRef{ProfileID: 7, Catalog: "owned"}, Label: "owned"}
	var reads int
	runtime := ObjectSearchRuntime{Namespaces: func(context.Context) ([]contract.Namespace, error) { return []contract.Namespace{n}, nil }, Browse: func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
		return []contract.DBObject{{Ref: n.Ref.Object("settings", "settings"), Name: "settings", Kind: "索引设置"}, {Ref: n.Ref.Object("statistics", "stats"), Name: "stats", Kind: "索引统计"}}, nil
	}, TableMetadata: func(_ context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
		reads++
		if o.Kind == "settings" {
			return nil, errors.New("HTTP403")
		}
		return &contract.TableStructure{Object: o, Definition: `{"future":"original-native"}`}, nil
	}}
	r := runObjectSearch(context.Background(), runtime, objectSearchRequest{profile: 7, query: "original-native", kind: "全部", mode: "contains", definitions: true})
	if reads != 2 || len(r.entries) != 1 || r.entries[0].Object.Ref.Kind != "statistics" || r.err == nil || !strings.Contains(r.entries[0].Object.Definition, "original-native") {
		t.Fatal(r, reads)
	}
	runtime.TableMetadata = func(_ context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
		o.Catalog = "other"
		return &contract.TableStructure{Object: o, Definition: `{"future":"original-native"}`}, nil
	}
	r = runObjectSearch(context.Background(), runtime, objectSearchRequest{profile: 7, query: "original-native", kind: "全部", mode: "contains", definitions: true})
	if len(r.entries) != 0 || !errors.Is(r.err, contract.ErrRevoked) {
		t.Fatal("foreign definition aliased", r)
	}
}
