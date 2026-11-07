package connections_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestStarRocksNativeProfileAndIndependentCapabilities(t *testing.T) {
	p, err := connections.Defaults(connections.StarRocks)
	p.Name = "StarRocks"
	if err != nil || p.Port != 9030 || p.Username != "root" || p.TLSMode != "verify-full" || p.StarRocksOptions.Catalog != "default_catalog" || p.Kind.IsMySQLFamily() {
		t.Fatal("StarRocks identity inherited MySQL OLTP settings")
	}
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal(err)
	}
	d, ok := connections.Describe(p.Kind)
	if !ok || d.ClientFamily != "starrocks" || !d.Capabilities.Query || !d.Capabilities.Metadata || !d.Capabilities.Export || d.Capabilities.EditableTables || d.Capabilities.Transactions || d.Capabilities.Import || d.Capabilities.Accounts || d.Capabilities.Backup || !d.Capabilities.Explain {
		t.Fatal("StarRocks inherited unverified capabilities")
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
	bad.StarRocksOptions.Catalog = "hive"
	if _, err = connections.NormalizeProfile(bad); err == nil {
		t.Fatal("unverified external catalog accepted")
	}
	bad = p
	bad.TLSMode = "disable"
	bad.StarRocksOptions.CAFile = "/tmp/ca.pem"
	if _, err = connections.NormalizeProfile(bad); err == nil {
		t.Fatal("certificate silently ignored")
	}
	for _, kind := range []connections.Kind{connections.MySQL, connections.PostgreSQL, connections.ClickHouse} {
		other, _ := connections.Defaults(kind)
		other.Name = "other"
		other.StarRocksOptions = p.StarRocksOptions
		if _, err = connections.NormalizeProfile(other); err == nil {
			t.Fatal("StarRocks options accepted by another product")
		}
	}
}
func TestStarRocksURIAndTemporarySecret(t *testing.T) {
	r, err := connections.ParseURI("starrocks://reader:temporary@localhost/data?catalog=default_catalog&sslmode=verify-full&ca_file=%2Ftmp%2Fca.pem&server_name=localhost")
	if err != nil || r.Profile.Kind != connections.StarRocks || r.Profile.Port != 9030 || r.Password != "temporary" || r.Profile.StarRocksOptions.CAFile != "/tmp/ca.pem" {
		t.Fatal("native URI did not preserve settings and temporary secret")
	}
	for _, uri := range []string{"starrocks://localhost?catalog=hive", "starrocks://localhost?sslmode=require", "starrocks://localhost?sslmode=disable&ca_file=%2Ftmp%2Fca.pem", "starrocks://localhost?catalog=default_catalog&catalog=default_catalog", "starrocks://localhost?password=private", "starrocks://localhost?ca_file=relative.pem"} {
		if _, err = connections.ParseURI(uri); err == nil {
			t.Fatal("unsupported URI accepted")
		}
	}
}
