package localdb

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLSSHDefaultsAndRoundTrip(t *testing.T) {
	store, _, _ := openStore(t)
	d := connections.DefaultMySQLSSH()
	if d.Enabled || d.Host != "" || d.Port != 22 || d.Username != "" ||
		d.AuthMethod != connections.MySQLSSHPassword || d.PrivateKey != "" {
		t.Fatalf("unexpected SSH defaults: %+v", d)
	}
	ssh := connections.MySQLSSH{
		Enabled: true, Host: "ssh.example.test", Port: 2222,
		Username: "alice", AuthMethod: connections.MySQLSSHPasswordAndKey,
		PrivateKey: "/home/alice/.ssh/id_ed25519",
	}
	a := connections.DefaultMySQLAdvanced()
	a.SSH = ssh
	saved, err := store.Create(context.Background(), connections.Profile{
		Name: "ssh-profile", Kind: connections.MySQL, Host: "localhost", Port: 3306,
		MySQLAdvanced: a,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), saved.ID)
	if err != nil || !reflect.DeepEqual(got.MySQLAdvanced.SSH, ssh) {
		t.Fatalf("SSH metadata not stored: %+v (%v)", got.MySQLAdvanced.SSH, err)
	}
	got.MySQLAdvanced.SSH.Enabled = false
	got.MySQLAdvanced.SSH.AuthMethod = connections.MySQLSSHPublicKey
	updated, err := store.Update(context.Background(), got)
	if err != nil || updated.MySQLAdvanced.SSH.Host != ssh.Host ||
		updated.MySQLAdvanced.SSH.PrivateKey != ssh.PrivateKey {
		t.Fatalf("disabled SSH lost selected values: %+v (%v)", updated.MySQLAdvanced.SSH, err)
	}
	dup, err := connections.NewService(store).Duplicate(context.Background(), saved.ID, "ssh-profile-copy")
	if err != nil || dup.MySQLAdvanced.SSH != updated.MySQLAdvanced.SSH {
		t.Fatalf("SSH metadata not duplicated: %+v (%v)", dup.MySQLAdvanced.SSH, err)
	}
}

func TestMySQLSSHValidationAndLegacyDefaults(t *testing.T) {
	store, _, db := openStore(t)
	for _, change := range []func(*connections.MySQLSSH){
		func(ssh *connections.MySQLSSH) { ssh.Port = 0 },
		func(ssh *connections.MySQLSSH) { ssh.Port = 65536 },
		func(ssh *connections.MySQLSSH) { ssh.AuthMethod = "unsupported" },
		func(ssh *connections.MySQLSSH) { ssh.Host = "bad\nname" },
		func(ssh *connections.MySQLSSH) { ssh.Username = "bad\x00name" },
		func(ssh *connections.MySQLSSH) { ssh.PrivateKey = strings.Repeat("x", 4097) },
	} {
		a := connections.DefaultMySQLAdvanced()
		change(&a.SSH)
		_, err := store.Create(context.Background(), connections.Profile{
			Name: "bad", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
		})
		if !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("invalid SSH option accepted: %+v (%v)", a.SSH, err)
		}
	}
	p, err := store.Create(context.Background(), connections.Profile{
		Name: "legacy", Kind: connections.MySQL, Host: "localhost", Port: 3306,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE connection_profiles SET mysql_advanced_json = ? WHERE id = ?",
		`{"clientDriver":"默认","charset":"自动","keepAliveSeconds":240,"connectTimeout":true,"connectTimeoutSeconds":30,"readTimeoutSeconds":30,"writeTimeout":true,"writeTimeoutSeconds":30,"maxSessions":10}`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Get(context.Background(), p.ID)
	if err != nil || legacy.MySQLAdvanced.SSH != connections.DefaultMySQLSSH() {
		t.Fatalf("legacy MySQL SSH defaults incorrect: %+v (%v)", legacy.MySQLAdvanced.SSH, err)
	}
}
