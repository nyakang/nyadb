package connections_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMongoDBProfileAndURINeverPersistSecrets(t *testing.T) {
	u, err := connections.ParseURI("mongodb://user:temp%40secret@localhost:27018,second:27019/app?authSource=admin&replicaSet=rs&tls=true")
	if err != nil || u.Password != "temp@secret" || u.Profile.MongoDBOptions.Seeds != "second:27019" || u.Profile.MongoDBOptions.AuthSource != "admin" || u.Profile.TLSMode != "verify-full" {
		t.Fatal("URI options not retained", err)
	}
	if strings.Contains(fmt.Sprintf("%+v", u.Profile), "secret") || strings.Contains(fmt.Sprintf("%+v", u), "secret") {
		t.Fatal("password in profile/display")
	}
	u, err = connections.ParseURI("mongodb+srv://user:pass@cluster.example.com/db")
	if err != nil || !u.Profile.MongoDBOptions.SRV || u.Profile.TLSMode != "verify-full" || u.Profile.MongoDBOptions.AuthSource != "db" {
		t.Fatal("SRV defaults", err)
	}
	for _, uri := range []string{"mongodb://h/db?retryWrites=true", "mongodb://h/db?tls=true&ssl=true", "mongodb+srv://h:27017/db", "mongodb://h/db?directConnection=true&replicaSet=rs&maxPoolSize=0", "mongodb://h/db?authMechanism=PLAIN", "mongodb://h/db?password=secret"} {
		if _, err = connections.ParseURI(uri); err == nil {
			t.Fatal("unsupported URI accepted", uri)
		}
	}
	p, _ := connections.Defaults(connections.MongoDB)
	p.Name = "MongoDB"
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal("anonymous local profile", err)
	}
	p.Kind = connections.MySQL
	if _, err = connections.NormalizeProfile(p); err == nil {
		t.Fatal("cross-engine options accepted")
	}
}
