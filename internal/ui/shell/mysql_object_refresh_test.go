package shell

import (
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLDatabaseRefreshRetainsCacheOnErrorAndPrunesRemovedSchemas(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	s.mysqlSelectedDatabase = "analytics"
	s.mysqlObjectKind = "表"
	s.mysqlObjectCache = map[string][]connections.MySQLDBObject{
		"analytics": {{Name: "fact", Kind: "表"}},
		"app_db":    {{Name: "orders", Kind: "表"}},
	}
	old := append([]string(nil), s.mysqlSessionDatabases...)
	ch := make(chan mysqlSessionResult, 1)
	s.mysqlSessionResults = ch
	ch <- mysqlSessionResult{generation: s.mysqlSessionGeneration, id: s.mysqlSessionProfileID,
		metadataErr: errors.New("temporary permission failure")}
	tt.Frame()
	if len(s.mysqlSessionDatabases) != len(old) || s.mysqlSelectedDatabase != "analytics" ||
		len(s.mysqlObjectCache) != 2 || s.mysqlSessionError == "" {
		t.Fatalf("refresh failure discarded existing objects: %+v %+v", s.mysqlSessionDatabases, s.mysqlObjectCache)
	}
	next := make(chan mysqlSessionResult, 1)
	s.mysqlSessionResults = next
	next <- mysqlSessionResult{generation: s.mysqlSessionGeneration, id: s.mysqlSessionProfileID,
		names: []string{"app_db"}}
	tt.Frame()
	if s.mysqlSelectedDatabase != "" || len(s.mysqlObjectCache) != 1 ||
		len(s.mysqlSessionDatabases) != 1 || s.mysqlSessionDatabases[0] != "app_db" {
		t.Fatalf("removed databases were retained after successful refresh: %+v", s.mysqlObjectCache)
	}
}
