package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"testing"
)

func TestGraphDataActionsKeepSameNameKindsAndSeparateDefinitions(t *testing.T) {
	s, b, session := browserFixture()
	defer s.CloseAll()
	b.Database = "neo4j"
	n := contract.NamespaceRef{ProfileID: 7, Catalog: "neo4j"}
	b.Objects = []contract.DBObject{{Name: "same", Kind: "图标签", Ref: n.Object("label", "same")}, {Name: "same", Kind: "关系类型", Ref: n.Object("relationship", "same")}, {Name: "same", Kind: "图索引", Ref: n.Object("index", "same")}}
	s.deps.Engine = func(int64) string { return "neo4j" }
	enabled := true
	s.deps.Capabilities = func(int64) contract.Capabilities { return contract.Capabilities{Documents: enabled} }
	var opened []contract.ObjectRef
	var definitions []string
	s.deps.OpenData = func(ui.Services, int64, string, string) { t.Error("graph action discarded kind") }
	s.deps.OpenQualifiedData = func(_ ui.Services, generation uint64, database string, o contract.ObjectRef) {
		if generation != session.Generation || database != "neo4j" {
			t.Error("graph action lost original session")
		}
		opened = append(opened, o)
	}
	s.deps.OpenProgram = func(id int64, db, name, kind string) { definitions = append(definitions, kind) }
	s.openObjectFor(ui.Services{}, 7, "neo4j", "same", "图标签")
	s.openObjectFor(ui.Services{}, 7, "neo4j", "same", "关系类型")
	s.openObjectFor(ui.Services{}, 7, "neo4j", "same", "图索引")
	if len(opened) != 2 || opened[0] != b.Objects[0].Ref || opened[1] != b.Objects[1].Ref || len(definitions) != 1 || definitions[0] != "图索引" {
		t.Fatal("default graph actions conflated same-name kinds or metadata")
	}
	s.SelectRow(7, 0)
	tt, _ := browserTester(s, 1280)
	if err := tt.Click("浏览选中图数据"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("查看 Neo4j 元数据"); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 3 || len(definitions) != 2 || definitions[1] != "图标签" {
		t.Fatal("toolbar did not separate data and definition")
	}
	enabled = false
	tt.Frame()
	if err := tt.Click("浏览选中图数据"); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 3 {
		t.Fatal("disabled graph capability dispatched")
	}
}

func TestGraphDataContextRejectsChangedKindGenerationAndForeignReferences(t *testing.T) {
	s, b, session := browserFixture()
	defer s.CloseAll()
	b.Database = "neo4j"
	n := contract.NamespaceRef{ProfileID: 7, Catalog: "neo4j"}
	b.Objects = []contract.DBObject{{Name: "same", Kind: "图标签", Ref: n.Object("label", "same")}, {Name: "same", Kind: "关系类型", Ref: n.Object("relationship", "same")}}
	s.deps.Engine = func(int64) string { return "neo4j" }
	s.deps.Capabilities = func(int64) contract.Capabilities { return contract.Capabilities{Documents: true} }
	calls := 0
	s.deps.OpenQualifiedData = func(ui.Services, uint64, string, contract.ObjectRef) { calls++ }
	kind := "图标签"
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Button(c, "Graph menu").Menu(func(m *ui.Menu) { s.ContextMenu(c.Services(), m, 7, "neo4j", "same", kind) })
	}, 900, 600)
	if err := tt.Click("Graph menu"); err != nil {
		t.Fatal(err)
	}
	kind = "关系类型"
	if err := tt.ChooseMenuItem("浏览图数据（只读）"); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("old menu dispatched same-name replacement kind")
	}
	if err := tt.Click("Graph menu"); err != nil {
		t.Fatal(err)
	}
	session.Generation++
	if err := tt.ChooseMenuItem("浏览图数据（只读）"); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("old menu dispatched replacement generation")
	}
	if err := tt.Click("Graph menu"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("浏览图数据（只读）"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("explicit current graph menu did not dispatch")
	}
	for _, ref := range []contract.ObjectRef{{}, n.Object("table", "same"), (contract.NamespaceRef{ProfileID: 8, Catalog: "neo4j"}).Object("relationship", "same"), n.Object("relationship", "different"), (contract.NamespaceRef{ProfileID: 7, Catalog: "other"}).Object("relationship", "same")} {
		b.Objects[1].Ref = ref
		s.openQualifiedData(ui.Services{}, 7, "neo4j", "same", "关系类型")
	}
	if calls != 1 {
		t.Fatal("unqualified or foreign cached graph object dispatched")
	}
}

func TestNativeKeyActionsUseOriginalQualifiedReferenceInToolbarAndContext(t *testing.T) {
	for _, engine := range []string{"consul", "etcd"} {
		t.Run(engine, func(t *testing.T) {
			s, b, session := browserFixture()
			defer s.CloseAll()
			b.Database = "kv"
			object := (contract.NamespaceRef{ProfileID: 7, Catalog: "kv"}).Object("keyspace", "keys")
			b.Objects = []contract.DBObject{{Name: "keys", Kind: "键", Ref: object}}
			s.deps.Engine = func(int64) string { return engine }
			s.deps.Capabilities = func(int64) contract.Capabilities { return contract.Capabilities{KeyValues: true} }
			calls := 0
			s.deps.OpenData = func(ui.Services, int64, string, string) { t.Error("key action discarded qualified identity") }
			s.deps.OpenQualifiedData = func(_ ui.Services, generation uint64, database string, got contract.ObjectRef) {
				if generation != session.Generation || database != "kv" || got != object {
					t.Error("key action lost original object or generation")
				}
				calls++
			}
			s.SelectRow(7, 0)
			tt, _ := browserTester(s, 1280)
			if err := tt.Click("打开选中键数据"); err != nil {
				t.Fatal(err)
			}
			s.openObjectFor(ui.Services{}, 7, "kv", "keys", "键")
			if calls != 2 {
				t.Fatal("key actions bypassed qualified callback", calls)
			}
			for _, ref := range []contract.ObjectRef{{}, object.Namespace().Object("table", "keys"), {ProfileID: 8, Catalog: "kv", Kind: "keyspace", Name: "keys"}} {
				b.Objects[0].Ref = ref
				s.openObjectFor(ui.Services{}, 7, "kv", "keys", "键")
			}
			if calls != 2 {
				t.Fatal("foreign key identity dispatched")
			}
		})
	}
}

func TestNativeKeyContextPreservesOpeningGenerationForDataAndStructure(t *testing.T) {
	for _, engine := range []string{"consul", "etcd"} {
		for _, action := range []string{"打开键", "查看键结构（只读）"} {
			for _, change := range []string{"none", "generation", "database", "profile", "kind"} {
				t.Run(engine+"/"+action+"/"+change, func(t *testing.T) {
					s, b, session := browserFixture()
					defer s.CloseAll()
					b.Database = "kv"
					object := (contract.NamespaceRef{ProfileID: 7, Catalog: "kv"}).Object("keyspace", "keys")
					b.Objects = []contract.DBObject{{Name: "keys", Kind: "键", Ref: object}}
					s.deps.Engine = func(int64) string { return engine }
					s.deps.Capabilities = func(int64) contract.Capabilities { return contract.Capabilities{KeyValues: true} }
					calls := 0
					s.deps.OpenQualifiedData = func(ui.Services, uint64, string, contract.ObjectRef) { calls++ }
					s.deps.OpenStructure = func(ui.Services, int64, string, string) { calls++ }
					kind := "键"
					tt := ui.NewTester(func(c *ui.Context) {
						ui.Button(c, "Key menu").Menu(func(m *ui.Menu) { s.ContextMenu(c.Services(), m, 7, "kv", "keys", kind) })
					}, 900, 600)
					if err := tt.Click("Key menu"); err != nil {
						t.Fatal(err)
					}
					switch change {
					case "generation":
						session.Generation++
					case "database":
						b.Database = "other"
					case "profile":
						session.ProfileID = 8
					case "kind":
						kind = "视图"
					}
					if err := tt.ChooseMenuItem(action); err != nil {
						t.Fatal(err)
					}
					if (calls != 0) != (change == "none") {
						t.Fatalf("original %s menu followed %s: calls=%d", action, change, calls)
					}
				})
			}
		}
	}
}
