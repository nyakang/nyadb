package shell

import (
	"bytes"
	"context"
	"errors"
	"github.com/egoist/mygo/ui"
	"path/filepath"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
)

func setupInitialCleanup(t *testing.T) (*State, *connections.Service, *queries.Store, *localdb.SQLRepository) {
	t.Helper()
	s, _, svc, _ := profileTestState(t)
	db, err := localdb.Open(filepath.Join(t.TempDir(), "sql.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repo := localdb.NewSQLRepository(db)
	store := queries.NewStore(repo, &assetTestKey{key: bytes.Repeat([]byte{1}, 32)})
	s.SetInitialSQLStore(store)
	return s, svc, store, repo
}

func TestInitialSQLCleanupPreservesSharedReferencesAndHistoricalOrphans(t *testing.T) {
	s, svc, store, repo := setupInitialCleanup(t)
	ctx := context.Background()
	p, err := svc.Create(ctx, connections.Profile{Name: "original", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: cleanupAdvanced("shared")})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"shared", "historical"} {
		if err := store.SaveInitialSQL(ctx, queries.InitialSQL{ID: id, ProfileID: p.ID, Revision: 1, Text: "SET @v=1"}); err != nil {
			t.Fatal(err)
		}
	}
	copy, err := svc.Duplicate(ctx, p.ID, "copy")
	if err != nil {
		t.Fatal(err)
	}
	s.pendingDeleteID = p.ID
	s.deleteProfile()
	if _, exists, err := repo.GetRecord(ctx, "initial_sql", "shared"); err != nil || !exists {
		t.Fatal("shared initial SQL deleted", err)
	}
	s.pendingDeleteID = copy.ID
	s.deleteProfile()
	if _, exists, err := repo.GetRecord(ctx, "initial_sql", "shared"); err != nil || exists {
		t.Fatal("retired initial SQL retained", err)
	}
	if _, exists, err := repo.GetRecord(ctx, "initial_sql", "historical"); err != nil || !exists {
		t.Fatal("historical orphan swept", err)
	}
}

type listFailProfiles struct{ *connections.Service }

func (r listFailProfiles) List(context.Context) ([]connections.Profile, error) {
	return nil, errors.New("injected reference scan failure")
}

func TestInitialSQLCleanupFailureRetainsCandidateAndSupportsRetry(t *testing.T) {
	for _, failure := range []string{"scan", "key"} {
		t.Run(failure, func(t *testing.T) {
			s, svc, store, repo := setupInitialCleanup(t)
			ctx := context.Background()
			if err := store.SaveInitialSQL(ctx, queries.InitialSQL{ID: "retired", ProfileID: 7, Revision: 1, Text: "SET @v=1"}); err != nil {
				t.Fatal(err)
			}
			if failure == "scan" {
				s.profileService = connections.NewService(listFailProfiles{svc})
			} else {
				s.SetInitialSQLStore(queries.NewStore(repo, &assetTestKey{key: bytes.Repeat([]byte{2}, 32)}))
			}
			if err := s.queueInitialSQLCleanup([]string{"retired"}); err == nil {
				t.Fatal("cleanup failure hidden")
			}
			if !s.initialSQLCleanup.candidates["retired"] {
				t.Fatal("failed cleanup lost retry candidate")
			}
			if _, exists, err := repo.GetRecord(ctx, "initial_sql", "retired"); err != nil || !exists {
				t.Fatal("failed cleanup discarded record", err)
			}
			s.profileService = svc
			s.SetInitialSQLStore(store)
			tt := ui.NewTester(s.View, 1280, 800)
			tt.Frame()
			if err := tt.Click("重试清理初始 SQL"); err != nil || len(s.initialSQLCleanup.candidates) != 0 {
				t.Fatal("retry entry failed", err)
			}
			if s.status != "初始 SQL 清理完成" {
				t.Fatal("successful retry left failure status")
			}
		})
	}
}

func TestInitialSQLCleanupWaitsForParkedHandshakeOriginalRead(t *testing.T) {
	s, svc, store, repo := setupInitialCleanup(t)
	ctx := context.Background()
	p, err := svc.Create(ctx, connections.Profile{Name: "handshake", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: cleanupAdvanced("captured")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveInitialSQL(ctx, queries.InitialSQL{ID: "captured", ProfileID: p.ID, Revision: 1, Text: "SET @v=1"}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	read := make(chan error, 1)
	s.sessions.Opener = func(ctx context.Context, settings connections.ActiveSettings, _ string) error {
		close(started)
		<-release
		_, err := store.LoadInitialSQL(ctx, settings.MySQLAdvanced.InitialSQLID)
		read <- err
		return err
	}
	s.sessions.Connect(p, nil)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handshake not started")
	}
	p.MySQLAdvanced.InitialSQLID = ""
	if _, err := svc.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	s.sessions.Activate(p.ID + 1)
	if err := s.queueInitialSQLCleanup([]string{"captured"}); err != nil {
		t.Fatal(err)
	}
	if !s.initialSQLCleanup.waiting {
		t.Fatal("parked handshake not protected")
	}
	close(release)
	select {
	case err := <-read:
		if err != nil {
			t.Fatal("captured read lost record", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handshake read blocked")
	}
	deadline := time.Now().Add(time.Second)
	for len(s.initialSQLCleanup.candidates) > 0 && time.Now().Before(deadline) {
		s.sessions.Consume()
		_ = s.consumeInitialSQLCleanup()
		time.Sleep(time.Millisecond)
	}
	if _, exists, err := repo.GetRecord(ctx, "initial_sql", "captured"); err != nil || exists {
		t.Fatal("acknowledged handshake did not release candidate", err)
	}
}

func cleanupAdvanced(id string) connections.MySQLAdvanced {
	a := connections.DefaultMySQLAdvanced()
	a.InitialSQLID = id
	return a
}
