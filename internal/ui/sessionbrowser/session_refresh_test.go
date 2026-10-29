package sessionbrowser

import (
	"errors"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestMySQLDatabaseRefreshRetainsCacheOnErrorAndPrunesRemovedSchemas(t *testing.T) {
	s := New(Dependencies{})
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
