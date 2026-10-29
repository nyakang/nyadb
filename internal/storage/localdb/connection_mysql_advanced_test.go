package localdb

import (
	"context"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLAdvancedRoundTripAndDuplicate(t *testing.T) {
	store, path, _ := openStore(t)
	ctx := context.Background()
	settings := connections.DefaultMySQLAdvanced()
	settings.SettingsLocation = "/tmp/mysql-profile"
	settings.ClientDriver = "旧版"
	settings.Charset = "utf8mb4"
	settings.KeepAlive = true
	settings.KeepAliveSeconds = 120
	settings.ConnectTimeoutSeconds = 40
	settings.ReadTimeout = true
	settings.ReadTimeoutSeconds = 20
	settings.Compression = true
	settings.AutoConnect = true
	settings.NamedPipe = true
	settings.PipeName = "/tmp/mysql.sock"
	settings.LimitSessions = true
	settings.MaxSessions = 8
	p, err := store.Create(ctx, connections.Profile{
		Name: "advanced", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, p.ID)
	if err != nil || !reflect.DeepEqual(got.MySQLAdvanced, settings) {
		t.Fatalf("advanced settings not persisted: %+v (%v)", got.MySQLAdvanced, err)
	}
	got.MySQLAdvanced.WriteTimeoutSeconds = 15
	updated, err := store.Update(ctx, got)
	if err != nil || updated.MySQLAdvanced.WriteTimeoutSeconds != 15 {
		t.Fatalf("advanced update failed: %+v %v", updated.MySQLAdvanced, err)
	}
	duplicate, err := connections.NewService(store).Duplicate(ctx, updated.ID, "advanced copy")
	if err != nil || !reflect.DeepEqual(duplicate.MySQLAdvanced, updated.MySQLAdvanced) {
		t.Fatalf("advanced options not cloned: %+v %v", duplicate, err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reloaded, err := NewProfileStore(db).Get(ctx, updated.ID)
	if err != nil || !reflect.DeepEqual(reloaded.MySQLAdvanced, updated.MySQLAdvanced) {
		t.Fatalf("advanced options not durable after reopen: %+v %v", reloaded.MySQLAdvanced, err)
	}
}
