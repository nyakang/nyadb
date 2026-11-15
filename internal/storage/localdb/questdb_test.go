package localdb

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestQuestDBSavedOptionsRoundTripAndRejectAmbiguousEnvelope(t *testing.T) {
	s, path, _ := openStore(t)
	p, _ := connections.Defaults(connections.QuestDB)
	p.Name = "native QuestDB"
	p.QuestDBOptions.ReadOnly = true
	p.QuestDBOptions.SQLMaxConnections = 3
	created, err := s.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := NewProfileStore(db).Get(context.Background(), created.ID)
	if err != nil || !reflect.DeepEqual(got, created) {
		t.Fatal("saved settings changed", err)
	}
	raw, err := encodeProfileOptions(created)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "{}", raw + "{}", strings.Replace(raw, `"questdb":`, `"QuestDB":`, 1), strings.Replace(raw, `"query_timeout_seconds":60`, `"query_timeout_seconds":60,"Query_Timeout_Seconds":1`, 1), strings.Replace(raw, `"query_timeout_seconds":60`, `"query_timeout_seconds":60,"password":"secret"`, 1), strings.Replace(raw, `"version":1`, `"version":2`, 1), strings.TrimSuffix(raw, "}") + `,"postgresql":null}`} {
		q := created
		if decodeEngineOptions(&q, invalid) == nil {
			t.Fatal("ambiguous stored options admitted")
		}
	}
	legacy, _ := connections.Defaults(connections.PostgreSQL)
	legacy.Name = "legacy"
	if err = decodeEngineOptions(&legacy, ""); err != nil || legacy.PostgreSQLOptions != connections.DefaultPostgreSQLOptions() {
		t.Fatal("legacy decoding changed", err)
	}
	if err = decodeEngineOptions(&legacy, `{"kind":"postgresql","version":1,"questdb":null}`); err == nil {
		t.Fatal("foreign null product options admitted")
	}
}
