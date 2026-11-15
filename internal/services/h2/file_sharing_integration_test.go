package h2

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestH2NativeSharedFileConnectionsPreserveOriginalSession(t *testing.T) {
	provider, options, password, directory := managedH2Fixture(t)
	for _, profile := range []string{"h2-v1", "h2-v2", "h2-v3", "h2-legacy"} {
		t.Run(profile, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			a := options
			a.DriverProfile, a.RequireExisting, a.AutoServer = profile, false, true
			p := connections.Profile{ID: 51, Kind: connections.H2, Name: "original shared file", Database: filepath.Join(directory, "shared-"+profile), Username: "sa", H2Options: a}
			original, err := provider.Open(ctx, p, password)
			if err != nil {
				t.Fatal("native shared file did not create", err)
			}
			t.Cleanup(func() { _ = original.Close() })
			query, err := contract.Require[contract.QueryProvider](original)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := query.ReserveQuery(1)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(lease.Release)
			if _, err := lease.Execute(ctx, "PUBLIC", `CREATE TABLE "shared_records"("id" BIGINT PRIMARY KEY); INSERT INTO "shared_records" VALUES(9007199254740993)`); err != nil {
				t.Fatal(err)
			}
			sessionID := func() string {
				t.Helper()
				q, err := lease.Execute(ctx, "PUBLIC", `SELECT SESSION_ID()`)
				if err != nil || len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 1 {
					t.Fatal("original physical session unavailable", err)
				}
				return q.Sets[0].Rows[0][0].Text
			}
			before := sessionID()
			secondaryProfile := p
			secondaryProfile.ID, secondaryProfile.H2Options.RequireExisting = 52, true
			secondary, err := provider.Open(ctx, secondaryProfile, password)
			if err != nil {
				t.Fatal("second JVM could not open the original shared file", err)
			}
			t.Cleanup(func() { _ = secondary.Close() })
			data := contract.ComposeData(secondary)
			page, err := data.Loader(ctx, 52, "PUBLIC", "shared_records", 0, 10)
			if err != nil || len(page.Rows) != 1 || page.Rows[0][0].Value.Text != "9007199254740993" || page.RowSnapshots[0].Ref.Object.ProfileID != 52 {
				t.Fatal("secondary connection lost original file values or ownership", err)
			}
			receipt, err := data.Saver(ctx, 52, "PUBLIC", "shared_records", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "9007199254740994"}}}})
			if err != nil || receipt == nil || !receipt.CommitConfirmed || receipt.NativeJSON == "" {
				t.Fatal("shared file save lost original commit receipt", err)
			}
			if err := secondary.Close(); err != nil {
				t.Fatal(err)
			}
			if failed, err := provider.Open(ctx, secondaryProfile, password+"-wrong"); err == nil {
				_ = failed.Close()
				t.Fatal("shared file accepted a wrong native password")
			}
			exclusive := secondaryProfile
			exclusive.H2Options.AutoServer = false
			if failed, err := provider.Open(ctx, exclusive, password); err == nil {
				_ = failed.Close()
				t.Fatal("exclusive second JVM unexpectedly bypassed the original file lock")
			}
			q, err := lease.Execute(ctx, "PUBLIC", `SELECT "id" FROM "shared_records" ORDER BY "id"`)
			if err != nil || len(q.Sets) != 1 || len(q.Sets[0].Rows) != 2 || q.Sets[0].Rows[1][0].Text != "9007199254740994" || sessionID() != before || !original.(*Session).Alive() {
				t.Fatal("shared testing or rejected exclusive open damaged the original physical session", err)
			}
		})
	}
}
