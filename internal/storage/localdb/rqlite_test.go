package localdb

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestRQLiteSavedSettingsRoundTripAndStrictDecode(t *testing.T) {
	s, path, _ := openStore(t)
	p, _ := connections.Defaults(connections.RQLite)
	p.Name, p.Username, p.TLSMode = "native cluster", " reader ", "verify-full"
	p.RQLiteOptions.Auth = "basic"
	p.RQLiteOptions.BasePath = "/gateway"
	p.RQLiteOptions.BatchTransaction = true
	p.RQLiteOptions.CAFile = filepath.Join(t.TempDir(), "native-root.pem")
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
		t.Fatalf("roundtrip changed settings: %v", err)
	}
	raw, err := encodeProfileOptions(created)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"", "{}", strings.Replace(raw, `"rqlite":`, `"RQLite":`, 1), strings.Replace(raw, `"auth":"basic"`, `"auth":"basic","AUTH":"none"`, 1), strings.Replace(raw, `"auth":"basic"`, `"auth":null`, 1), strings.Replace(raw, `"auth":"basic",`, "", 1), strings.Replace(raw, `"base_path":"/gateway"`, `"base_path":"\ud800"`, 1), strings.Replace(raw, `"version":1`, `"version":2`, 1), strings.TrimSuffix(raw, "}") + `,"sqlite":null}`, raw + "{}",
	} {
		q := created
		if err := decodeEngineOptions(&q, bad); err == nil {
			t.Fatal("ambiguous saved RQLite settings admitted")
		}
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []connections.Kind{connections.H2, connections.PostgreSQL, connections.ClickHouse, connections.MySQL} {
		q, _ := connections.Defaults(kind)
		q.Name = "other"
		other, err := encodeProfileOptions(q)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(other), &fields); err != nil {
			t.Fatal(err)
		}
		fields["rqlite"] = json.RawMessage("null")
		b, _ := json.Marshal(fields)
		if err := decodeEngineOptions(&q, string(b)); err == nil {
			t.Fatalf("foreign null RQLite options admitted on %s", kind)
		}
	}
}
