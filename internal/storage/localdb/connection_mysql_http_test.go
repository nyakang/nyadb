package localdb

import (
	"context"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLHTTPDefaultsPersistenceAndLegacy(t *testing.T) {
	store, _, db := openStore(t)
	if got := connections.DefaultMySQLHTTP(); got != (connections.MySQLHTTP{ProxyPort: 8080}) {
		t.Fatalf("wrong HTTP defaults: %+v", got)
	}
	http := connections.MySQLHTTP{
		Enabled: true, URL: "https://db.example.test/tunnel.php",
		Base64Queries: true, PasswordAuth: true, Username: "user",
		CertificateAuth: true, ClientKey: "/tmp/key.pem",
		ClientCertificate: "/tmp/cert.pem", CACertificate: "/tmp/ca.pem",
		ProxyEnabled: true, ProxyHost: "proxy.example.test", ProxyPort: 3128, ProxyUsername: "proxy-user",
	}
	a := connections.DefaultMySQLAdvanced()
	a.HTTP = http
	profile, err := store.Create(context.Background(), connections.Profile{
		Name: "http", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Get(context.Background(), profile.ID)
	if err != nil || !reflect.DeepEqual(saved.MySQLAdvanced.HTTP, http) {
		t.Fatalf("HTTP metadata not stored: %+v (%v)", saved.MySQLAdvanced.HTTP, err)
	}
	saved.MySQLAdvanced.HTTP.Enabled = false
	updated, err := store.Update(context.Background(), saved)
	if err != nil || updated.MySQLAdvanced.HTTP.ClientKey != http.ClientKey {
		t.Fatalf("disabling HTTP lost configuration: %+v (%v)", updated.MySQLAdvanced.HTTP, err)
	}
	dup, err := connections.NewService(store).Duplicate(context.Background(), profile.ID, "http-copy")
	if err != nil || dup.MySQLAdvanced.HTTP != updated.MySQLAdvanced.HTTP {
		t.Fatalf("duplicating lost HTTP configuration: %+v (%v)", dup.MySQLAdvanced.HTTP, err)
	}
	_, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json = ? WHERE id = ?",
		`{"clientDriver":"默认","charset":"自动","keepAliveSeconds":240,"connectTimeout":true,"connectTimeoutSeconds":30,"readTimeoutSeconds":30,"writeTimeout":true,"writeTimeoutSeconds":30,"maxSessions":10}`, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.Get(context.Background(), profile.ID)
	if err != nil || old.MySQLAdvanced.HTTP != connections.DefaultMySQLHTTP() {
		t.Fatalf("legacy HTTP defaults broken: %+v %v", old.MySQLAdvanced.HTTP, err)
	}
}
