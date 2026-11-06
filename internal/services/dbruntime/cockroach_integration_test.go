package dbruntime_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestCockroachRuntimeTLSAndGenerationRevocationFixture(t *testing.T) {
	if os.Getenv("NYADB_COCKROACH_IT") != "1" {
		t.Skip("requires explicit disposable CockroachDB fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_COCKROACH_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	p, _ := connections.Defaults(connections.CockroachDB)
	p.ID, p.Name, p.Host, p.Port, p.Database, p.Username = 903, "CockroachDB runtime", "127.0.0.1", uint16(port), "nyadb_cockroach_it", "nyadb_it"
	p.PostgreSQLOptions.CAFile = os.Getenv("NYADB_COCKROACH_CA_FILE")
	password := os.Getenv("NYADB_COCKROACH_PASSWORD")
	untrusted := p
	untrusted.ID = 904
	untrusted.PostgreSQLOptions.CAFile = ""
	if err = m.Connect(ctx, untrusted, password); err == nil {
		t.Fatal("untrusted TLS server published")
	}
	if m.IsConnected(untrusted.ID) {
		t.Fatal("failed TLS session remained published")
	}
	if err = m.Connect(ctx, p, password); err != nil {
		t.Fatal(err)
	}
	original, err := m.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := m.Metadata(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := meta.Namespaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range ns {
		if n.Ref == m.Namespace(p.ID, "public") {
			found = true
		}
	}
	if !found {
		t.Fatal("fixed catalog/schema identity missing")
	}
	data := m.Data(p.ID)
	page, err := data.AdvancedLoader(ctx, p.ID, "public", "records", contract.DataOptions{Limit: 10})
	if err != nil || len(page.Rows) != 2 {
		t.Fatal("bound shared grid", err)
	}
	q, err := contract.Require[contract.QueryProvider](original)
	if err != nil {
		t.Fatal(err)
	}
	l, err := q.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err = l.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = l.Execute(short, "public", "SELECT pg_sleep(5)")
	stop()
	if err == nil {
		t.Fatal("SQL cancellation not acknowledged")
	}
	l.Release()
	l, err = q.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, err = l.Execute(ctx, "public", "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if err = m.Connect(ctx, p, password); err != nil {
		t.Fatal(err)
	}
	replacement, err := m.Session(p.ID)
	if err != nil || replacement.Generation() <= original.Generation() {
		t.Fatal("published generation did not increase", err)
	}
	if _, err = l.Execute(ctx, "public", "SELECT 1"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old physical lease survived replacement", err)
	}
	if _, err = data.AdvancedLoader(ctx, p.ID, "public", "records", contract.DataOptions{Limit: 10}); err == nil {
		t.Fatal("old data binding survived replacement")
	}
	caps := replacement.Capabilities()
	if !caps.Transactions || !caps.Metadata || !caps.Explain || caps.Backup || caps.Restore || caps.DesignTables || caps.Maintenance {
		t.Fatal("unsafe PostgreSQL administration offered")
	}
	m.Disconnect(p.ID)
	if m.IsConnected(p.ID) {
		t.Fatal("disconnect retained public session")
	}
}
