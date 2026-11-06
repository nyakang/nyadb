package connectioneditor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/services/queries"
)

type failingInitialStore struct {
	*initialSQLMemoryStore
	calls, failAt int
}

func (m *failingInitialStore) SaveInitialSQL(ctx context.Context, v queries.InitialSQL) error {
	m.calls++
	if m.calls == m.failAt {
		return errors.New("injected initial SQL failure")
	}
	return m.initialSQLMemoryStore.SaveInitialSQL(ctx, v)
}

type rollbackFailProfiles struct {
	*connections.Service
	updates int
}

func (r *rollbackFailProfiles) Update(ctx context.Context, p connections.Profile) (connections.Profile, error) {
	r.updates++
	if r.updates > 1 {
		return p, errors.New("injected rollback failure")
	}
	return r.Service.Update(ctx, p)
}

func TestInitialSQLSaveFailuresCleanOnlyUnreferencedSuccessfulWrites(t *testing.T) {
	for _, stage := range []string{"partial", "vault-read", "vault-write", "tunnel", "rollback"} {
		t.Run(stage, func(t *testing.T) {
			s, _, svc, _ := profileTestState(t)
			p, err := svc.Create(context.Background(), connections.Profile{Name: "original", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: connections.DefaultMySQLAdvanced(), Variants: []connections.ConnectionVariant{{Name: "Other", Host: "localhost", Port: 3306, MySQLAdvanced: connections.DefaultMySQLAdvanced()}}})
			if err != nil {
				t.Fatal(err)
			}
			s.selectedProfileID = p.ID
			s.startEditProfile()
			m := &failingInitialStore{initialSQLMemoryStore: &initialSQLMemoryStore{records: map[string]queries.InitialSQL{}}}
			s.SetInitialSQLStore(m)
			s.mysqlInitialQuery = "SET @main=1"
			s.connectionOtherConfigs[0].initialSQL = "SET @other=2"
			var candidates []string
			s.SetInitialSQLCleanup(func(ids []string) error {
				candidates = append(candidates, ids...)
				profiles, err := svc.List(context.Background())
				if err != nil {
					return err
				}
				refs := map[string]bool{}
				for _, p := range profiles {
					refs[p.MySQLAdvanced.InitialSQLID] = true
					for _, v := range p.Variants {
						refs[v.MySQLAdvanced.InitialSQLID] = true
					}
				}
				for _, id := range ids {
					if !refs[id] {
						delete(m.records, id)
					}
				}
				return nil
			})
			switch stage {
			case "partial":
				m.failAt = 2
			case "vault-read":
				s.SetCredentialVault(&memoryCredentialVault{getErr: errors.New("locked")})
				s.connectionSavePassword = true
				s.connectionSecret = "unsaved-secret"
			case "vault-write":
				s.SetCredentialVault(&memoryCredentialVault{secrets: map[string]string{}, setErr: errors.New("locked")})
				s.connectionSavePassword = true
				s.connectionSecret = "unsaved-secret"
			case "tunnel":
				s.SetTunnelServices(credentials.NewTunnelStoreWithVault(&tunnelEditorVault{fail: true}), nil, nil)
				s.connectionDraft.MySQLAdvanced.SSH.Enabled = true
				s.connectionDraft.MySQLAdvanced.SSH.Host = "fixture"
				s.connectionDraft.MySQLAdvanced.SSH.Username = "fixture"
				s.mysqlSSHPassword = "unsaved-tunnel"
				s.mysqlSaveTunnelSecrets = true
			case "rollback":
				m.failAt = 2
				s.SetProfileService(connections.NewService(&rollbackFailProfiles{Service: svc}))
			}
			s.Save()
			if !s.active || s.mysqlInitialQuery != "SET @main=1" || s.connectionError == "" || len(candidates) == 0 {
				t.Fatalf("failed save lost draft or cleanup candidates: %s", s.connectionError)
			}
			got, err := svc.Get(context.Background(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "rollback" {
				if len(m.records) != 1 || got.MySQLAdvanced.InitialSQLID == "" || !strings.Contains(s.connectionError, "rollback failure") {
					t.Fatal("rollback failure discarded referenced record or hid error")
				}
			} else if len(m.records) != 0 || got.MySQLAdvanced.InitialSQLID != "" || got.Variants[0].MySQLAdvanced.InitialSQLID != "" {
				t.Fatal("successful writes became orphaned after rollback")
			}
		})
	}
}

func TestInitialSQLSuccessfulReplacementAndVariantRemovalSupplyOldCandidates(t *testing.T) {
	s, _, svc, _ := profileTestState(t)
	ctx := context.Background()
	a := connections.DefaultMySQLAdvanced()
	a.InitialSQLID = "main-old"
	v := connections.DefaultMySQLAdvanced()
	v.InitialSQLID = "variant-old"
	p, err := svc.Create(ctx, connections.Profile{Name: "original", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a, Variants: []connections.ConnectionVariant{{Name: "Other", Host: "localhost", Port: 3306, MySQLAdvanced: v}}})
	if err != nil {
		t.Fatal(err)
	}
	m := &initialSQLMemoryStore{records: map[string]queries.InitialSQL{"main-old": {ID: "main-old", ProfileID: p.ID, Revision: 1, Text: "SET @v=1"}, "variant-old": {ID: "variant-old", ProfileID: p.ID, Revision: 1, Text: "SET @v=2"}}}
	s.SetInitialSQLStore(m)
	s.selectedProfileID = p.ID
	s.startEditProfile()
	deadline := time.Now().Add(time.Second)
	for s.initialSQLResults != nil && time.Now().Before(deadline) {
		s.consumeInitialSQL()
		time.Sleep(time.Millisecond)
	}
	if s.initialSQLLoadFailed {
		t.Fatal("original initial SQL unavailable")
	}
	s.mysqlInitialQuery = "SET @v=3"
	s.connectionOtherConfigs = nil
	var candidates []string
	s.SetInitialSQLCleanup(func(ids []string) error {
		candidates = append(candidates, ids...)
		return errors.New("injected cleanup failure")
	})
	s.Save()
	got, err := svc.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.active || len(got.Variants) != 0 || got.MySQLAdvanced.InitialSQLID == "main-old" || got.MySQLAdvanced.InitialSQLID == "" {
		t.Fatal("cleanup failure rolled back successful configuration", s.connectionError)
	}
	seen := map[string]bool{}
	for _, id := range candidates {
		seen[id] = true
	}
	if !seen["main-old"] || !seen["variant-old"] || !seen[got.MySQLAdvanced.InitialSQLID] {
		t.Fatal("replacement/removal lost lifecycle candidates")
	}
	if !strings.Contains(s.connectionError, "清理失败") {
		t.Fatal("successful save hid cleanup failure")
	}
}
