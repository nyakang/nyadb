package driverstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func TestManagedH2OfflineRuntimeVersionsReceiptsAndOriginalLease(t *testing.T) {
	if os.Getenv("NYADB_MANAGED_H2_IT") != "1" {
		t.Skip("requires the owned managed H2 fixture")
	}
	var f struct{ Archive, SHA256, Directory, PasswordFile string }
	raw, err := os.ReadFile(os.Getenv("NYADB_MANAGED_H2_FIXTURE_FILE"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("managed fixture unavailable")
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal("fixture credential unavailable")
	}
	s, err := New(filepath.Join(f.Directory, "store"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m, err := s.Install(ctx, input, f.SHA256)
	input.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ profile, version string }{{"h2-v1", "1.4.200"}, {"h2-v2", "2.1.214"}, {"h2-v3", "2.4.240"}} {
		t.Run(v.version, func(t *testing.T) {
			l, err := s.Acquire(ctx, m.Key, m.Version, f.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := l.LaunchSpec(v.profile)
			if err != nil {
				t.Fatal(err)
			}
			r, err := l.Start(ctx, v.profile, agentbridge.Options{CancelWait: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := r.Close(); err != nil {
					t.Error(err)
				}
			})
			// Record exact original process identity for fixture cleanup even if a
			// test is terminated before Go's cleanup callbacks can execute.
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", r.Client.PID()))
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
			if len(fields) < 20 {
				t.Fatal("original process identity unavailable")
			}
			record, _ := json.Marshal(map[string]any{"pid": r.Client.PID(), "start_ticks": fields[19], "java": spec.Executable, "agent": strings.Split(spec.Args[1], string(os.PathListSeparator))[0]})
			journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = journal.Write(append(record, '\n'))
			journal.Close()
			if err != nil {
				t.Fatal(err)
			}
			params := map[string]any{"database": "owned_managed", "connection_string": "jdbc:h2:mem:owned_managed;DB_CLOSE_DELAY=-1", "username": "sa", "password": strings.TrimSpace(string(secret)), "driver_profile": v.profile}
			pool, err := agentbridge.NewQueryPool(r.Client, params, agentbridge.QueryConfig{InitializeSQL: "SET SCHEMA PUBLIC", DefaultSchema: "PUBLIC", MaxLeases: 2, MaxRows: 100, TimeoutSeconds: 5, Split: sqltext.SplitPortable})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			lease, err := pool.ReserveQuery(1)
			if err != nil {
				t.Fatal(err)
			}
			other, err := pool.ReserveQuery(2)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(lease.Release)
			t.Cleanup(other.Release)
			q, err := lease.Execute(ctx, "PUBLIC", "SELECT H2VERSION()")
			if err != nil || len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || q.Sets[0].Rows[0][0].Text != v.version {
				t.Fatalf("actual bundled kernel: %v", err)
			}
			q, err = lease.Execute(ctx, "PUBLIC", "CREATE LOCAL TEMPORARY TABLE OWNED(X BIGINT, D DECIMAL(38,20), B VARBINARY); INSERT INTO OWNED VALUES (9007199254740993,123456789012345678.12345678901234567890,X'00FF'); SELECT * FROM OWNED")
			if err != nil || len(q.Steps) != 3 || len(q.Sets) != 1 || q.NativeJSON == "" {
				t.Fatalf("original bundled query receipts: %v", err)
			}
			row := q.Sets[0].Rows[0]
			if row[0].Kind != contract.Integer || row[0].Text != "9007199254740993" || row[1].Text != "123456789012345678.12345678901234567890" || row[2].Display() != "0x00ff" {
				t.Fatal("managed launch lost typed values")
			}
			if _, err = other.Execute(ctx, "PUBLIC", "SELECT * FROM OWNED"); err == nil {
				t.Fatal("managed leases shared physical temporary state")
			}
			if err = s.Remove(ctx, m.Key, m.Version); !errors.Is(err, ErrBusy) {
				t.Fatal("active managed runtime did not pin original package", err)
			}
			pool.Close()
			if err = r.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = lease.Execute(ctx, "PUBLIC", "SELECT 1"); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("closed original lease recovered implicitly", err)
			}
		})
	}
	if err = s.Remove(ctx, m.Key, m.Version); err != nil {
		t.Fatal(err)
	}
}
