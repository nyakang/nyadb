package connections

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestMySQLHTTPDefaultsPersistenceAndLegacy(t *testing.T) {
	store, _ := openStore(t)
	if got := DefaultMySQLHTTP(); got != (MySQLHTTP{ProxyPort: 8080}) {
		t.Fatalf("wrong HTTP defaults: %+v", got)
	}
	http := MySQLHTTP{
		Enabled: true, URL: "https://db.example.test/tunnel.php",
		Base64Queries: true, PasswordAuth: true, Username: "user",
		CertificateAuth: true, ClientKey: "/tmp/key.pem",
		ClientCertificate: "/tmp/cert.pem", CACertificate: "/tmp/ca.pem",
		ProxyEnabled: true, ProxyHost: "proxy.example.test", ProxyPort: 3128, ProxyUsername: "proxy-user",
	}
	a := DefaultMySQLAdvanced()
	a.HTTP = http
	profile, err := store.Create(context.Background(), Profile{
		Name: "http", Kind: MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
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
	dup, err := NewService(store).Duplicate(context.Background(), profile.ID, "http-copy")
	if err != nil || dup.MySQLAdvanced.HTTP != updated.MySQLAdvanced.HTTP {
		t.Fatalf("duplicating lost HTTP configuration: %+v (%v)", dup.MySQLAdvanced.HTTP, err)
	}
	_, err = store.db.Exec("UPDATE connection_profiles SET mysql_advanced_json = ? WHERE id = ?",
		`{"clientDriver":"默认","charset":"自动","keepAliveSeconds":240,"connectTimeout":true,"connectTimeoutSeconds":30,"readTimeoutSeconds":30,"writeTimeout":true,"writeTimeoutSeconds":30,"maxSessions":10}`, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.Get(context.Background(), profile.ID)
	if err != nil || old.MySQLAdvanced.HTTP != DefaultMySQLHTTP() {
		t.Fatalf("legacy HTTP defaults broken: %+v %v", old.MySQLAdvanced.HTTP, err)
	}
}

func TestMySQLHTTPValidationAndTunnelExclusivity(t *testing.T) {
	store, _ := openStore(t)
	for _, change := range []func(*MySQLAdvanced){
		func(a *MySQLAdvanced) { a.HTTP.Enabled = true; a.HTTP.ProxyPort = 0 },
		func(a *MySQLAdvanced) { a.HTTP.ProxyPort = 65536 },
		func(a *MySQLAdvanced) { a.HTTP.URL = "https://name:secret@host/tunnel.php" },
		func(a *MySQLAdvanced) { a.HTTP.URL = "https://host/tunnel?token=secret" },
		func(a *MySQLAdvanced) { a.HTTP.URL = "javascript:alert(1)" },
		func(a *MySQLAdvanced) { a.HTTP.ProxyHost = "line\nbreak" },
		func(a *MySQLAdvanced) { a.HTTP.ClientKey = strings.Repeat("x", 4097) },
		func(a *MySQLAdvanced) { a.SSH.Enabled = true; a.HTTP.Enabled = true },
	} {
		a := DefaultMySQLAdvanced()
		change(&a)
		_, err := store.Create(context.Background(), Profile{
			Name: "invalid", Kind: MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid HTTP configuration: %+v %v", a.HTTP, err)
		}
	}
}
