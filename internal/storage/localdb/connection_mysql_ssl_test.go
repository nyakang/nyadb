package localdb

import (
	"context"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLSSLSavedAndRestored(t *testing.T) {
	store, _, _ := openStore(t)
	ssl := connections.MySQLSSL{
		Enabled: true, UseVerification: true,
		ClientKey:         "/tmp/client-key.pem",
		ClientCertificate: "/tmp/client-cert.pem",
		CACertificate:     "/tmp/ca-cert.pem",
		VerifyServerCA:    true,
	}
	a := connections.DefaultMySQLAdvanced()
	a.SSL = ssl
	created, err := store.Create(context.Background(), connections.Profile{
		Name: "ssl-profile", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), created.ID)
	if err != nil || !reflect.DeepEqual(got.MySQLAdvanced.SSL, ssl) {
		t.Fatalf("SSL config not restored: %+v %v", got.MySQLAdvanced.SSL, err)
	}
	got.MySQLAdvanced.SSL.UseVerification = false
	got.MySQLAdvanced.SSL.Enabled = false
	updated, err := store.Update(context.Background(), got)
	if err != nil || updated.MySQLAdvanced.SSL.ClientKey != ssl.ClientKey ||
		updated.MySQLAdvanced.SSL.UseVerification {
		t.Fatalf("turning off SSL must retain non-secret fields: %+v %v", updated.MySQLAdvanced.SSL, err)
	}
	copyProfile, err := connections.NewService(store).Duplicate(context.Background(), created.ID, "ssl-copy")
	if err != nil || copyProfile.MySQLAdvanced.SSL != updated.MySQLAdvanced.SSL {
		t.Fatalf("duplicate lost SSL fields: %+v %v", copyProfile.MySQLAdvanced.SSL, err)
	}
}
