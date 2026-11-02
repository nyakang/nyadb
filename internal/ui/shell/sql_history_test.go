package shell

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/queries"
)

func TestSQLHistoryOpenKeepsOriginalProfileAndOtherDirtyDocument(t *testing.T) {
	s, tt, profiles, _ := profileTestState(t)
	original, err := profiles.Create(context.Background(), connections.Profile{Name: "History Original", Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "original.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	other, err := profiles.Create(context.Background(), connections.Profile{Name: "History Other", Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "other.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(other.Name)
	s.openQuery()
	dirty := s.currentTab().query
	dirty.Editor.Buffer.Set("SELECT unsaved")
	calls := 0
	s.sqlWorkspace.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) {
		calls++
		return contract.SQLResult{}, nil
	}
	s.openHistoryQuery(queries.HistoryEntry{ProfileID: original.ID, Database: "main", SQL: "UPDATE target SET v=1"})
	tt.Frame()
	opened := s.currentTab().query
	if opened == dirty || !opened.NeedsRecovery() || opened.RecoveryProfile() != original.ID || opened.RecoverySchema() != "main" || opened.QueryID() != "" || calls != 0 {
		t.Fatal("history replayed SQL, reused a dirty tab, or rebound the current profile")
	}
	if dirty.Editor.Buffer.String() != "SELECT unsaved" || !dirty.Dirty() {
		t.Fatal("history replaced another document's draft")
	}
}
