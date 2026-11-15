package h2

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestH2ProviderFileCreateReopenRequireExistingAndReadOnly(t *testing.T) {
	provider, options, password, directory := managedH2Fixture(t)
	for _, profile := range []string{"h2-v1", "h2-v2", "h2-v3", "h2-legacy"} {
		t.Run(profile, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			a := options
			a.DriverProfile = profile
			p := connections.Profile{ID: 42, Kind: connections.H2, Name: "owned file", Database: filepath.Join(directory, "database files", profile), Username: "sa", H2Options: a}
			if err := os.MkdirAll(filepath.Dir(p.Database), 0700); err != nil {
				t.Fatal(err)
			}
			if raw, err := provider.Open(ctx, p, password); err == nil {
				raw.Close()
				t.Fatal("require-existing created a missing original database")
			}
			for _, suffix := range []string{".mv.db", ".h2.db"} {
				if _, err := os.Stat(p.Database + suffix); !os.IsNotExist(err) {
					t.Fatal("missing database acquired native storage", err)
				}
			}
			p.H2Options.RequireExisting = false
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
			lease, err := s.ReserveQuery(1)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(lease.Release)
			if _, err := lease.Execute(ctx, "PUBLIC", `CREATE TABLE "file_records"("id" BIGINT PRIMARY KEY,"note" VARCHAR); INSERT INTO "file_records" VALUES(9007199254740993,'original file')`); err != nil {
				t.Fatal(err)
			}
			catalog := s.Catalog()
			lease.Release()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			p.H2Options.RequireExisting = true
			raw, err = provider.Open(ctx, p, password)
			if err != nil {
				t.Fatal("existing native file did not reopen", err)
			}
			reopened := raw.(*Session)
			t.Cleanup(func() {
				if err := reopened.Close(); err != nil {
					t.Error(err)
				}
			})
			if reopened.Catalog() != catalog {
				t.Fatal("file reopen changed its qualified native catalog")
			}
			r := contract.ComposeData(reopened)
			page, err := r.Loader(ctx, p.ID, "PUBLIC", "file_records", 0, 1)
			if err != nil || len(page.Rows) != 1 || page.Rows[0][0].Value.Text != "9007199254740993" || page.Rows[0][1].Value.Text != "original file" {
				t.Fatal("file restart lost committed native values", err)
			}
			if len(page.RowSnapshots) != 1 || page.RowSnapshots[0] == nil || page.RowSnapshots[0].Ref.Object.ProfileID != p.ID || page.RowSnapshots[0].Ref.Object.Catalog != catalog {
				t.Fatal("file row did not retain original qualified identity")
			}
			saved, err := r.Saver(ctx, p.ID, "PUBLIC", "file_records", []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "confirmed file save"}}}})
			if err != nil || saved == nil || !saved.CommitConfirmed || saved.NativeJSON == "" || !r.NativeReceipts {
				t.Fatal("native file save did not retain original commit evidence", err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			p.H2Options.ReadOnly = true
			p.H2Options.AutoServer = false
			raw, err = provider.Open(ctx, p, password)
			if err != nil {
				t.Fatal("read-only original file did not open", err)
			}
			readonly := raw.(*Session)
			t.Cleanup(func() {
				if err := readonly.Close(); err != nil {
					t.Error(err)
				}
			})
			r = contract.ComposeData(readonly)
			if readonly.Capabilities().EditableTables || r.Saver != nil || r.NativeReceipts {
				t.Fatal("read-only binding admitted data writes")
			}
			page, err = r.Loader(ctx, p.ID, "PUBLIC", "file_records", 0, 1)
			if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Value.Text != "confirmed file save" || page.Editable == nil || *page.Editable {
				t.Fatal("read-only file lost original values or edit restrictions", err)
			}
			reader, err := readonly.ReserveQuery(2)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reader.Release)
			if _, err := reader.Execute(ctx, "PUBLIC", `UPDATE "file_records" SET "note"='must reject'`); err == nil {
				t.Fatal("native read-only file accepted a SQL write")
			}
			q, err := reader.Execute(ctx, "PUBLIC", `SELECT "note" FROM "file_records"`)
			if err != nil || len(q.Sets) != 1 || q.Sets[0].Rows[0][0].Text != "confirmed file save" {
				t.Fatal("rejected SQL write changed original readonly data", err)
			}
		})
	}
}
