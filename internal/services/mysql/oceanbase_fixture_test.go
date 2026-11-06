package mysql

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func oceanBaseFixtureProfile(t *testing.T) (context.Context, connections.Profile) {
	t.Helper()
	if os.Getenv("NYADB_OCEANBASE_IT") != "1" {
		t.Skip("requires an explicitly enabled disposable OceanBase MySQL tenant")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_OCEANBASE_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	p, _ := connections.Defaults(connections.OceanBase)
	p.ID, p.Host, p.Port, p.Username, p.Database = 703, "127.0.0.1", uint16(port), "nyadb_ob@test", "nyadb_oceanbase_it"
	p.Name, p.TLSMode, p.MySQLAdvanced.SSL.Enabled = "OceanBase fixture", "verify-full", true
	p.MySQLAdvanced.SSL.CACertificate = os.Getenv("NYADB_OCEANBASE_CA_FILE")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx, p
}

func TestOceanBaseVerifiedTLSAndProductCheckDisposableFixture(t *testing.T) {
	ctx, p := oceanBaseFixtureProfile(t)
	settings, err := connections.ResolveActiveSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = TestDirectMySQLConnection(ctx, settings, os.Getenv("NYADB_OCEANBASE_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	// OceanBase's Ssl_cipher status is empty even for encrypted sessions.
	// Verify the actual handshake: the fixture certificate is not system trusted.
	settings.MySQLAdvanced.SSL.CACertificate = ""
	err = TestDirectMySQLConnection(ctx, settings, os.Getenv("NYADB_OCEANBASE_PASSWORD"))
	var trustError x509.UnknownAuthorityError
	if !errors.As(err, &trustError) {
		t.Fatal("untrusted fixture certificate accepted", err)
	}
	settings.MySQLAdvanced.SSL.CACertificate = p.MySQLAdvanced.SSL.CACertificate
	settings.Kind = connections.TiDB
	if err = TestDirectMySQLConnection(ctx, settings, os.Getenv("NYADB_OCEANBASE_PASSWORD")); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("OceanBase accepted as TiDB", err)
	}
}

func TestOceanBaseTableOnlyAccountCanBrowseDisposableFixture(t *testing.T) {
	ctx, p := oceanBaseFixtureProfile(t)
	password := os.Getenv("NYADB_OCEANBASE_PASSWORD")
	session, err := OpenSession(ctx, p, password, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	l, err := session.(contract.QueryProvider).ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	user := "nyadb_reader_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	account, _ := quoteAccount(MySQLAccount{User: user, Host: "%"})
	if _, err = l.Execute(ctx, p.Database, "CREATE USER "+account+" IDENTIFIED BY '"+password+"' REQUIRE SSL"); err != nil {
		t.Fatal("reader initialization failed")
	}
	t.Cleanup(func() { _, _ = l.Execute(context.Background(), p.Database, "DROP USER "+account) })
	if _, err = l.Execute(ctx, p.Database, "GRANT SELECT ON nyadb_oceanbase_it.* TO "+account); err != nil {
		t.Fatal(err)
	}
	p.ID, p.Username = 704, user+"@test"
	reader, err := OpenSession(ctx, p, password, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	objects, err := reader.(contract.MetadataProvider).Browse(ctx, contract.NamespaceRef{ProfileID: p.ID, Catalog: p.Database})
	if err != nil {
		t.Fatal("table-only grant cannot browse tables", err)
	}
	found := false
	for _, object := range objects {
		found = found || object.Name == "records"
		if object.Kind == "序列" {
			t.Fatal("sequence metadata exposed without tenant DBA privileges")
		}
	}
	if !found {
		t.Fatal("readable table omitted")
	}
}
