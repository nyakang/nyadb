package dameng

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/storage/localdb"
)

func TestNativeDamengURIProfileRestartRetainsOriginalPhysicalSchema(t *testing.T) {
	c, password := nativeFixture(t)
	admin := nativeDB(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := "NYADB_PROFILE_" + nativeSuffix(t) + ` 租户 "'; -- `
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+nativeQuote(schema)+" AUTHORIZATION SYSDBA"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP SCHEMA "+nativeQuote(schema)+" CASCADE") })
	q := url.Values{"library_file": {c.Options.LibraryFile}, "sslmode": {c.TLSMode}, "schema": {schema}, "max_connections": {"2"}, "sql_max_connections": {"3"}}
	for key, value := range map[string]string{"ca_file": c.Options.CAFile, "certificate_file": c.Options.CertificateFile, "key_file": c.Options.KeyFile, "server_name": c.Options.ServerName} {
		if value != "" {
			q.Set(key, value)
		}
	}
	u := url.URL{Scheme: "dameng", Host: net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port))), Path: "/" + c.Database, User: url.UserPassword(c.Username, password), RawQuery: q.Encode()}
	imported, err := connections.ParseURI(u.String())
	if err != nil || imported.Password != password {
		t.Fatal("original native URI import failed", err)
	}
	path := filepath.Join(t.TempDir(), "config.sqlite")
	configDB, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer configDB.Close()
	saved, err := localdb.NewProfileStore(configDB).Create(ctx, imported.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = configDB.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), password) {
		t.Fatal("temporary native password reached local storage", err)
	}
	restarted, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	p, err := localdb.NewProfileStore(restarted).Get(ctx, saved.ID)
	if err != nil || !reflect.DeepEqual(p, saved) {
		t.Fatal("native profile identity/settings changed on restart", err)
	}
	cfg, err := connections.DamengProfileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	db := nativeDB(t, cfg, imported.Password)
	first, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, physical := range []*sql.Conn{first, second} {
		var actualCatalog, actualUser, actualSchema, exact string
		const number = "123456789012345678.12345678901234567891"
		err := physical.QueryRowContext(ctx, `SELECT (SELECT NAME FROM SYS.V$DATABASE),CURRENT_USER,SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID),CAST(? AS DECIMAL(38,20))`, number).Scan(&actualCatalog, &actualUser, &actualSchema, &exact)
		if err != nil || actualCatalog != cfg.Database || actualUser != cfg.Username || actualSchema != schema || exact != number {
			t.Fatalf("restarted profile identity/value differs: catalog=%q user=%q schema=%q number=%q error=%v", actualCatalog, actualUser, actualSchema, exact, err)
		}
	}
	var original string
	if err := admin.QueryRowContext(ctx, "SELECT SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID)").Scan(&original); err != nil || original != c.Username {
		t.Fatal("restarted profile altered its original administrator session", err)
	}
}
