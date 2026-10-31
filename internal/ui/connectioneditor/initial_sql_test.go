package connectioneditor

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/services/queries"
	"testing"
)

type initialSQLMemoryStore struct {
	records map[string]queries.InitialSQL
	fail    bool
}

func (s *initialSQLMemoryStore) LoadInitialSQL(_ context.Context, id string) (queries.InitialSQL, error) {
	v, ok := s.records[id]
	if !ok {
		return v, errors.New("missing")
	}
	return v, nil
}
func (s *initialSQLMemoryStore) SaveInitialSQL(_ context.Context, v queries.InitialSQL) error {
	if s.fail {
		return queries.ErrKeyUnavailable
	}
	s.records[v.ID] = v
	return nil
}
func TestInitialSQLMissingKeyPreservesDraftAndRollsBackProfile(t *testing.T) {
	s, _, svc, _ := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind("mysql")
	s.nextConnectionStep()
	s.connectionDraft.Name = "init-key-failed"
	s.mysqlInitialQuery = "SET @v='secret'"
	s.saveConnectionDraft()
	if !s.connectionDialog || s.mysqlInitialQuery == "" || s.connectionError == "" {
		t.Fatal("failed encrypted save must retain draft")
	}
	profiles, e := svc.List(context.Background())
	if e != nil || len(profiles) != 0 {
		t.Fatalf("dangling initial SQL reference persisted: %v %v", profiles, e)
	}
}
func TestInitialSQLStaleLoadCannotReplaceAnotherConfiguration(t *testing.T) {
	s := New(Dependencies{})
	s.active = true
	s.connectionDraft.ID = 1
	s.connectionDraft.MySQLAdvanced.InitialSQLID = "new"
	s.initialSQLRequest = 9
	ch := make(chan initialSQLResult, 1)
	ch <- initialSQLResult{request: 8, profile: 1, config: 0, id: "old", value: queries.InitialSQL{Text: "old secret"}}
	s.initialSQLResults = ch
	s.mysqlInitialQuery = "current"
	s.consumeInitialSQL()
	if s.mysqlInitialQuery != "current" {
		t.Fatal("stale encrypted load overwrote current draft")
	}
}
