package localdb

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestEngineOptionsPersistDuplicateAndLegacyWithoutMigration(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	pg, _ := connections.Defaults(connections.PostgreSQL)
	pg.Name = "PostgreSQL options"
	pg.PostgreSQLOptions.ApplicationName = "restored application"
	pg.PostgreSQLOptions.DefaultSchema = "tenant"
	pg.PostgreSQLOptions.StatementTimeoutMS = 431
	pg.PostgreSQLOptions.CertificateFile, pg.PostgreSQLOptions.KeyFile = "client.crt", "client.key"
	sq, _ := connections.Defaults(connections.SQLite)
	sq.Name, sq.Database = "SQLite options", filepath.Join(t.TempDir(), "main.sqlite")
	sq.SQLiteOptions.Attachments = []connections.SQLiteAttachment{{Name: "aux", File: filepath.Join(t.TempDir(), "aux.sqlite"), ReadOnly: true}}
	pgSaved, err := store.Create(ctx, pg)
	if err != nil {
		t.Fatal(err)
	}
	sqSaved, err := store.Create(ctx, sq)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := connections.NewService(store).Duplicate(ctx, sqSaved.ID, "Duplicated options")
	if err != nil || !reflect.DeepEqual(duplicate.SQLiteOptions, sqSaved.SQLiteOptions) {
		t.Fatalf("duplicate: %+v %v", duplicate, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store = NewProfileStore(reopened)
	gotPG, err := store.Get(ctx, pgSaved.ID)
	if err != nil || !reflect.DeepEqual(gotPG.PostgreSQLOptions, pgSaved.PostgreSQLOptions) {
		t.Fatalf("PG restart: %+v %v", gotPG, err)
	}
	gotSQ, err := store.Get(ctx, sqSaved.ID)
	if err != nil || !reflect.DeepEqual(gotSQ.SQLiteOptions, sqSaved.SQLiteOptions) {
		t.Fatalf("SQLite restart: %+v %v", gotSQ, err)
	}
	if _, err := reopened.Exec("UPDATE connection_profiles SET mysql_advanced_json='' WHERE id IN (?,?)", pgSaved.ID, sqSaved.ID); err != nil {
		t.Fatal(err)
	}
	legacyPG, err := store.Get(ctx, pgSaved.ID)
	if err != nil || legacyPG.PostgreSQLOptions != connections.DefaultPostgreSQLOptions() {
		t.Fatalf("legacy PG defaults: %+v %v", legacyPG, err)
	}
	legacySQ, err := store.Get(ctx, sqSaved.ID)
	if err != nil || legacySQ.SQLiteOptions.RequireExisting || !legacySQ.SQLiteOptions.ForeignKeys || legacySQ.SQLiteOptions.BusyTimeoutMS != 1000 || len(legacySQ.SQLiteOptions.Attachments) != 0 {
		t.Fatalf("legacy SQLite defaults: %+v %v", legacySQ, err)
	}
	var after int
	if err := reopened.QueryRow("PRAGMA user_version").Scan(&after); err != nil || after != version {
		t.Fatalf("unexpected database migration: %d -> %d %v", version, after, err)
	}
}

func TestEngineOptionsRejectCorruptEnvelopeWithoutReplacingRecord(t *testing.T) {
	store, _, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.SQLite)
	p.Name, p.Database = "corrupt", filepath.Join(t.TempDir(), "main.sqlite")
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"version":1,"kind":"postgresql","postgresql":{}}`,
		`{"version":1,"kind":"sqlite","sqlite":{"version":999}}`,
		`{"version":1,"kind":"sqlite","sqlite":{"version":1,"password":"not-allowed"}}`,
		`{"version":1,"kind":"sqlite","sqlite":{"version":1}} {}`,
	} {
		if _, err := db.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, saved.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, saved.ID); err == nil {
			t.Fatalf("accepted corrupt envelope %s", raw)
		}
		var retained string
		if err := db.QueryRow("SELECT mysql_advanced_json FROM connection_profiles WHERE id=?", saved.ID).Scan(&retained); err != nil || retained != raw {
			t.Fatal("reading corruption replaced original settings")
		}
	}
}
