package h2

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestH2NativePoolCapacityMetadataReserveAndCanceledAdmission(t *testing.T) {
	provider, options, password, _ := managedH2Fixture(t)
	for _, profile := range []string{"h2-v1", "h2-v2", "h2-v3", "h2-legacy"} {
		t.Run(profile, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			a := options
			a.Mode, a.RequireExisting, a.DriverProfile = "memory", false, profile
			a.MaxConnections, a.MetadataReserve, a.SQLMaxConnections = 4, 1, 3
			a.PoolWaitTimeoutMilliseconds = 1000
			p := connections.Profile{ID: 17, Kind: connections.H2, Name: "owned capacity", Database: "owned_capacity", Username: "sa", H2Options: a}
			raw, err := provider.Open(ctx, p, password)
			if err != nil {
				t.Fatal(err)
			}
			s := raw.(*Session)
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			var leases []contract.QueryLease
			var originalIDs []string
			ids := map[string]bool{}
			for i := uint64(1); i <= 3; i++ {
				l, err := s.ReserveQuery(i)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(l.Release)
				leases = append(leases, l)
				q, err := l.Execute(ctx, "PUBLIC", `SELECT SESSION_ID()`)
				if err != nil || len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 {
					t.Fatal("original SQL affinity unavailable", err)
				}
				id := q.Sets[0].Rows[0][0].Text
				if ids[id] {
					t.Fatal("independent SQL documents shared a physical connection")
				}
				ids[id] = true
				originalIDs = append(originalIDs, id)
			}
			if _, err := s.ReserveQuery(4); !errors.Is(err, contract.ErrSQLSessionLimit) {
				t.Fatal("configured SQL document limit ignored", err)
			}
			if _, err := leases[0].Execute(ctx, "PUBLIC", `CREATE TABLE "capacity_records"("id" INT PRIMARY KEY); INSERT INTO "capacity_records" VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			ns := contract.NamespaceRef{ProfileID: p.ID, Catalog: s.Catalog(), Schema: "PUBLIC"}
			if _, err := s.Namespaces(ctx); err != nil {
				t.Fatal("SQL saturation blocked namespaces", err)
			}
			if _, err := s.Browse(ctx, ns); err != nil {
				t.Fatal("SQL saturation blocked object browsing", err)
			}
			if _, err := s.TableMetadata(ctx, ns.Object("table", "capacity_records")); err != nil {
				t.Fatal("SQL saturation blocked structures", err)
			}
			if !contract.ComposeData(s).NativeReceipts {
				t.Fatal("H2 workbench binding omitted original native receipts")
			}

			// Pin the fourth native connection using the metadata role, then bypass
			// local admission once to prove that the actual pool enforces max=4.
			metadata, _, err := s.runtime.Client.OpenSession(ctx, s.metadataParams)
			if err != nil {
				t.Fatal(err)
			}
			defer metadata.Close()
			if _, err := s.query(ctx, metadata, `SET SCHEMA "PUBLIC"`, 1); err != nil {
				t.Fatal(err)
			}
			q, err := s.query(ctx, metadata, `SELECT COUNT(*) FROM INFORMATION_SCHEMA.SESSIONS`, 1)
			if err != nil || len(q.Sets) != 1 || q.Sets[0].Rows[0][0].Text != "4" {
				t.Fatal("native pool did not share its configured four connections", err)
			}
			extra, r, err := s.runtime.Client.OpenSession(ctx, s.metadataParams)
			if extra != nil {
				defer extra.Close()
			}
			if err == nil || extra != nil || !r.ExecutorAcknowledged || r.ProcessExitConfirmed || !s.Alive() {
				t.Fatal("native physical pool overflow or original runtime retirement", safeError(err))
			}
			if _, err := metadata.Close(); err != nil {
				t.Fatal(err)
			}

			// A data request waits locally behind the original SQL leases. Cancel
			// the actual wait, then release one owned lease and reuse the session.
			wait, stop := context.WithTimeout(ctx, 100*time.Millisecond)
			_, err = contract.ComposeData(s).Loader(wait, p.ID, "PUBLIC", "capacity_records", 0, 1)
			stop()
			if !errors.Is(err, context.DeadlineExceeded) || !s.Alive() {
				t.Fatal("local feature wait canceled an unrelated SQL executor", err)
			}
			_, err = contract.ComposeData(s).Loader(ctx, p.ID, "PUBLIC", "capacity_records", 0, 1)
			if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || !s.Alive() {
				t.Fatal("configured pool wait limit did not bound local admission", err)
			}
			leases[2].Release()
			page, err := contract.ComposeData(s).Loader(ctx, p.ID, "PUBLIC", "capacity_records", 0, 1)
			if err != nil || len(page.Rows) != 1 || page.Rows[0][0].Value.Text != "1" {
				t.Fatal("released SQL capacity did not admit original data work", err)
			}
			for i, l := range leases[:2] {
				q, err := l.Execute(ctx, "PUBLIC", "SELECT SESSION_ID()")
				if err != nil || len(q.Sets) != 1 || q.Sets[0].Rows[0][0].Text != originalIDs[i] {
					t.Fatal("capacity cancellation changed another original SQL connection", err)
				}
			}
		})
	}
}
