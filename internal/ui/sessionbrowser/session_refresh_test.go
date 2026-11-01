package sessionbrowser

import (
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestMySQLDatabaseRefreshRetainsCacheOnErrorAndPrunesRemovedSchemas(t *testing.T) {
	s := newTestState(Dependencies{})
	s.Current().ProfileID, s.Current().Status = 1, "已连接"
	s.Current().Databases = []string{"app_db", "analytics"}
	s.Current().Database = "analytics"
	s.Current().Cache = map[string][]mysqlsvc.MySQLDBObject{
		"analytics": {{Name: "fact", Kind: "表"}},
		"app_db":    {{Name: "orders", Kind: "表"}},
	}
	old := append([]string(nil), s.Current().Databases...)
	ch := make(chan sessionResult, 1)
	s.Current().sessionResults = ch
	ch <- sessionResult{generation: s.Current().Generation, id: s.Current().ProfileID,
		metadataErr: errors.New("temporary permission failure")}
	s.Consume()
	if len(s.Current().Databases) != len(old) || s.Current().Database != "analytics" ||
		len(s.Current().Cache) != 2 || s.Current().Error == "" {
		t.Fatalf("refresh failure discarded existing objects: %+v %+v", s.Current().Databases, s.Current().Cache)
	}
	next := make(chan sessionResult, 1)
	s.Current().sessionResults = next
	next <- sessionResult{generation: s.Current().Generation, id: s.Current().ProfileID,
		names: []string{"app_db"}}
	s.Consume()
	if s.Current().Database != "" || len(s.Current().Cache) != 1 ||
		len(s.Current().Databases) != 1 || s.Current().Databases[0] != "app_db" {
		t.Fatalf("removed databases were retained after successful refresh: %+v", s.Current().Cache)
	}
}

func TestNamespaceRefreshKeepsQualifiedIdentityAndCachesOnFailure(t *testing.T) {
	s := New(Dependencies{})
	w := s.Current()
	w.ProfileID = 5
	w.Status = "已连接"
	w.Generation = 9
	a := contract.NamespaceRef{ProfileID: 5, Catalog: "fixed_db", Schema: "a"}
	b := contract.NamespaceRef{ProfileID: 5, Catalog: "fixed_db", Schema: "b"}
	w.Namespaces = map[string]contract.NamespaceRef{"a": a, "b": b}
	w.Databases = []string{"a", "b"}
	w.Cache = map[string][]contract.DBObject{"a": {{Ref: a.Object("table", "items"), Name: "items"}}, "b": {{Ref: b.Object("table", "items"), Name: "items"}}}
	w.QualifiedCache = map[contract.NamespaceRef][]contract.DBObject{a: w.Cache["a"], b: w.Cache["b"]}
	failed := make(chan sessionResult, 1)
	w.sessionResults = failed
	failed <- sessionResult{id: 5, generation: 9, namespaces: []contract.Namespace{{Ref: a, Label: "a"}}, metadataErr: errors.New("partial metadata failure")}
	s.Consume()
	if len(w.Namespaces) != 2 || len(w.QualifiedCache) != 2 || len(w.Cache) != 2 {
		t.Fatal("failed refresh pruned qualified identity")
	}
	success := make(chan sessionResult, 1)
	w.sessionResults = success
	success <- sessionResult{id: 5, generation: 9, names: []string{"a"}, namespaces: []contract.Namespace{{Ref: a, Label: "a"}}}
	s.Consume()
	if w.Namespaces["a"] != a || len(w.QualifiedCache) != 1 || w.QualifiedCache[a][0].Ref != a.Object("table", "items") {
		t.Fatal("qualified namespace refresh lost original object identity")
	}
	if _, ok := w.QualifiedCache[b]; ok {
		t.Fatal("removed namespace retained cache")
	}
}
