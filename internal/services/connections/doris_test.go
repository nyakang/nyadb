package connections_test

import (
	"net/url"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDorisNativeProfileAndIndependentCapabilities(t *testing.T) {
	p, err := connections.Defaults(connections.Doris)
	p.Name = "Doris"
	if err != nil || p.Port != 9030 || p.Username != "root" || p.TLSMode != "verify-full" || p.DorisOptions.Catalog != "internal" || p.Kind.IsMySQLFamily() {
		t.Fatal("Doris identity inherited MySQL OLTP settings")
	}
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal(err)
	}
	d, _ := connections.Describe(p.Kind)
	if d.Capabilities != (contract.Capabilities{Query: true, Metadata: true, Schemas: true, Count: true, Export: true, JSONL: true, Explain: true}) {
		t.Fatal("unsupported Doris operation advertised")
	}
	for _, mode := range []string{"require", "prefer", "verify-ca"} {
		bad := p
		bad.TLSMode = mode
		if _, err = connections.NormalizeProfile(bad); err == nil {
			t.Fatal("TLS downgrade accepted")
		}
	}
	bad := p
	bad.MySQLAdvanced = connections.DefaultMySQLAdvanced()
	if _, err = connections.NormalizeProfile(bad); err == nil {
		t.Fatal("MySQL administration/options accepted")
	}
	bad = p
	bad.DorisOptions.Catalog = "hive"
	if _, err = connections.NormalizeProfile(bad); err == nil {
		t.Fatal("unverified external catalog accepted")
	}
	bad = p
	bad.TLSMode = "disable"
	bad.DorisOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	if _, err = connections.NormalizeProfile(bad); err == nil {
		t.Fatal("certificate silently ignored")
	}
	for _, kind := range []connections.Kind{connections.MySQL, connections.PostgreSQL, connections.ClickHouse} {
		other, _ := connections.Defaults(kind)
		other.Name = "other"
		other.DorisOptions = p.DorisOptions
		if _, err = connections.NormalizeProfile(other); err == nil {
			t.Fatal("Doris options accepted by another product")
		}
	}
}
func TestDorisURIAndTemporarySecret(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca with spaces.pem")
	r, err := connections.ParseURI("doris://reader:temporary@localhost/data?catalog=internal&sslmode=verify-full&ca_file=" + url.QueryEscape(caFile) + "&server_name=localhost")
	if err != nil || r.Profile.Kind != connections.Doris || r.Profile.Port != 9030 || r.Password != "temporary" || r.Profile.DorisOptions.CAFile != caFile {
		t.Fatal("native URI did not preserve settings and temporary secret")
	}
	for _, uri := range []string{"doris://localhost?catalog=hive", "doris://localhost?sslmode=require", "doris://localhost?sslmode=disable&ca_file=" + url.QueryEscape(caFile), "doris://localhost?catalog=internal&catalog=internal", "doris://localhost?password=private", "doris://localhost?ca_file=relative.pem"} {
		if _, err = connections.ParseURI(uri); err == nil {
			t.Fatal("unsupported URI accepted")
		}
	}
}
