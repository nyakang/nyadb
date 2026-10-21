package connections

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestMySQLSSLSavedAndRestored(t *testing.T) {
	store, _ := openStore(t)
	ssl := MySQLSSL{
		Enabled: true, UseVerification: true,
		ClientKey:         "/tmp/client-key.pem",
		ClientCertificate: "/tmp/client-cert.pem",
		CACertificate:     "/tmp/ca-cert.pem",
		VerifyServerCA:    true,
	}
	a := DefaultMySQLAdvanced()
	a.SSL = ssl
	created, err := store.Create(context.Background(), Profile{
		Name: "ssl-profile", Kind: MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
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
	copyProfile, err := NewService(store).Duplicate(context.Background(), created.ID, "ssl-copy")
	if err != nil || copyProfile.MySQLAdvanced.SSL != updated.MySQLAdvanced.SSL {
		t.Fatalf("duplicate lost SSL fields: %+v %v", copyProfile.MySQLAdvanced.SSL, err)
	}
}

func TestMySQLSSLInvalidPathsRejected(t *testing.T) {
	store, _ := openStore(t)
	for _, modify := range []func(*MySQLSSL){
		func(s *MySQLSSL) { s.ClientKey = "/tmp/key\x00.pem" },
		func(s *MySQLSSL) { s.ClientCertificate = strings.Repeat("x", 4097) },
		func(s *MySQLSSL) { s.CACertificate = "bad\npath" },
	} {
		a := DefaultMySQLAdvanced()
		modify(&a.SSL)
		_, err := store.Create(context.Background(), Profile{
			Name: "bad-ssl", Kind: MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid SSL path accepted: %v", err)
		}
	}
}
