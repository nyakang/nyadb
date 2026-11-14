package connections_test

import (
	"errors"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSelectDBIdentityCatalogAndTemporaryURISecret(t *testing.T) {
	p, err := connections.Defaults(connections.SelectDB)
	p.Name = "SelectDB"
	if err != nil || p.Port != 9030 || p.Username != "root" || p.Kind.IsMySQLFamily() || p.SelectDBOptions.Catalog != "internal" || p.DorisOptions != (connections.DorisOptions{}) {
		t.Fatal("SelectDB inherited another product's settings")
	}
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(t.TempDir(), "CA with spaces.pem")
	r, err := connections.ParseURI("selectdb://reader:temporary@localhost/data?catalog=" + url.QueryEscape("lake`quoted") + "&sslmode=verify-full&ca_file=" + url.QueryEscape(ca) + "&server_name=localhost")
	if err != nil || r.Profile.Kind != p.Kind || r.Profile.Port != 9030 || r.Profile.SelectDBOptions.Catalog != "lake`quoted" || r.Password != "temporary" || r.Profile.SelectDBOptions.CAFile != ca {
		t.Fatal("SelectDB qualified target or temporary credentials lost")
	}
	for _, uri := range []string{
		"selectdb://localhost?catalog=internal&catalog=internal", "selectdb://localhost?password=private",
		"selectdb://localhost?sslmode=require", "selectdb://localhost?ca_file=relative.pem",
		"selectdb://localhost?sslmode=disable&ca_file=" + url.QueryEscape(ca),
	} {
		if _, err = connections.ParseURI(uri); err == nil {
			t.Fatalf("unsupported URI accepted: %s", uri)
		}
	}
	for _, kind := range []connections.Kind{connections.Doris, connections.MySQL, connections.StarRocks, connections.PostgreSQL} {
		other, _ := connections.Defaults(kind)
		other.Name = "other"
		other.SelectDBOptions = p.SelectDBOptions
		if _, err = connections.NormalizeProfile(other); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("SelectDB settings accepted by another product")
		}
	}
	for _, change := range []func(*connections.Profile){
		func(p *connections.Profile) { p.MySQLAdvanced = connections.DefaultMySQLAdvanced() },
		func(p *connections.Profile) {
			p.DorisOptions = connections.DorisOptions{Version: 1, Catalog: "internal"}
		},
		func(p *connections.Profile) { p.SelectDBOptions.Catalog = "bad\nname" },
		func(p *connections.Profile) { p.SelectDBOptions.Version = 2 },
		func(p *connections.Profile) { p.Variants = []connections.ConnectionVariant{{Name: "alternate"}} },
	} {
		bad := p
		change(&bad)
		if _, err = connections.NormalizeProfile(bad); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("foreign or malformed SelectDB profile accepted")
		}
	}
}
