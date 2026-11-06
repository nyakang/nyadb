package connections_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDuckDBFileOptionsAndURI(t *testing.T) {
	p, err := connections.Defaults(connections.DuckDB)
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "DuckDB"
	if p.Database != ":memory:" || p.Port != 0 || p.Username != "" {
		t.Fatalf("defaults %+v", p)
	}
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal(err)
	}
	for _, uri := range []string{"duckdb:///:memory:", "duckdb:///tmp/fixture.duckdb?read_only=true"} {
		v, e := connections.ParseURI(uri)
		if e != nil || v.Profile.Kind != connections.DuckDB || v.Password != "" {
			t.Fatalf("URI %s %+v %v", uri, v, e)
		}
	}
	for _, uri := range []string{"duckdb://secret:password@localhost/tmp/fixture.duckdb", "duckdb:///tmp/fixture.duckdb?read_only=true&read_only=false", "duckdb:///tmp/fixture.duckdb?encrypt=true", "duckdb:///:memory:?read_only=true"} {
		if _, err = connections.ParseURI(uri); err == nil {
			t.Fatal("unsupported DuckDB URI accepted", uri)
		}
	}
	bad := p
	bad.DuckDBOptions.ReadOnly = true
	if _, err = connections.NormalizeProfile(bad); err == nil {
		t.Fatal("readonly memory accepted")
	}
	bad = p
	bad.DuckDBOptions.SQLMaxConnections = 0
	if _, err = connections.NormalizeProfile(bad); err == nil {
		t.Fatal("invalid SQL pool limit accepted")
	}
	other, _ := connections.Defaults(connections.SQLite)
	other.Name = "other"
	other.Database = "fixture.sqlite"
	other.DuckDBOptions = p.DuckDBOptions
	if _, err = connections.NormalizeProfile(other); err == nil {
		t.Fatal("DuckDB settings accepted on SQLite")
	}
}
