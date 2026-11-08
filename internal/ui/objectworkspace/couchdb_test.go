package objectworkspace

import (
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"testing"
)

func TestCouchDBDefinitionOwnsQualifiedNativeMetadataAndReadOnlyView(t *testing.T) {
	snapshot := Session{ProfileID: 7, Generation: 8, Connected: true}
	var routed contract.ObjectRef
	s := New(Dependencies{Session: func() Session { return snapshot }, Engine: func(int64) string { return "couchdb" }, OpenStructure: func(_ ui.Services, id int64, db, name string) {
		routed = contract.NamespaceRef{ProfileID: id, Catalog: db}.Object("design_document", name)
	}})
	defer s.CloseAll()
	ref := contract.NamespaceRef{ProfileID: 7, Catalog: "owned"}.Object("design_document", "_design/shared")
	source := `{"_id":"_design/shared","_rev":"1-native","n":9007199254740993,"views":{"v":{"map":"function(doc) { emit(doc._id, doc.n); }"}}}`
	s.Loader = func(context.Context, int64, string, string) (*contract.TableStructure, error) {
		return &contract.TableStructure{Object: ref, Database: ref.Catalog, Name: ref.Name, Kind: ref.Kind, Engine: "Apache CouchDB", Definition: source, Definitions: []contract.Definition{{Name: ref.Name, Kind: ref.Kind, SQL: source}, {Name: "v", Kind: "view", SQL: `{"map":"function(doc) { emit(doc._id, doc.n); }"}`}}}, nil
	}
	d := s.OpenQualifiedStructure(1, ref, "设计文档")
	s.Load(d, nil, false)
	waitStructure(t, s, d)
	if d.Error() != "" || d.Data().Definition != source || d.Data().Editable || d.Object() != ref {
		t.Fatal("qualified native definition changed", d.Error())
	}
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 720, 520)
	if !tt.HasText("CouchDB 原生定义（只读）") || tt.HasText("外键") || tt.HasText("编辑表结构") {
		t.Fatal("native metadata inherited SQL controls")
	}
	if err := tt.Click("选择原生定义"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("视图 · v"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.nativeDefinitionChoice != "视图 · v" || d.Data().Definition != source {
		t.Fatal("native view selection changed full design source")
	}
	s.openObjectFor(ui.Services{}, 7, "owned", ref.Name, "设计文档")
	if routed != ref {
		t.Fatal("native object opened data instead of definition", routed)
	}
	s.Close(d)
	if d.Data() != nil || d.nativeDefinitionChoice != "" {
		t.Fatal("definition close retained native cache")
	}
}
