package dbruntime_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestOpenGaussRuntimeTLSAndGenerationRevocationFixture(t *testing.T) {
	if os.Getenv("NYADB_OPENGAUSS_IT") != "1" {
		t.Skip("requires explicit disposable openGauss fixture")
	}
	if os.Getenv("NYADB_OPENGAUSS_CA_FILE") == "" {
		t.Skip("requires the verified TLS fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_OPENGAUSS_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	p, _ := connections.Defaults(connections.OpenGauss)
	p.ID, p.Name, p.Host, p.Port, p.Database, p.Username = 903, "openGauss runtime", "127.0.0.1", uint16(port), "nyadb_opengauss_it", "nyadb_it"
	p.PostgreSQLOptions.CAFile = os.Getenv("NYADB_OPENGAUSS_CA_FILE")
	password := os.Getenv("NYADB_OPENGAUSS_PASSWORD")
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
	if !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("SQL cancellation not acknowledged")
	}
	if _, err = l.Execute(ctx, "public", "SELECT 1"); !errors.Is(err, contract.ErrSQLSessionLost) && !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("cancelled physical session was silently replaced", err)
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
	file, ok := l.(contract.QueryFileLease)
	if !ok {
		t.Fatal("runtime SQL file entry is absent")
	}
	if out, e := file.ExecuteFile(ctx, "public", strings.NewReader("SELECT 2;")); e != nil || out.Rows[0][0] != "2" {
		t.Fatal("runtime SQL file did not use original session", e)
	}
	table := fmt.Sprintf("nyadb_gauss_drop_%d", time.Now().UnixNano())
	if _, err = l.Execute(ctx, "public", "CREATE SCHEMA "+table+"; CREATE TABLE "+table+".original(id bigint)"); err != nil {
		t.Fatal(err)
	}
	target := contract.SchemaDesignTarget{Object: m.Namespace(p.ID, table).Object("table", "original"), Generation: original.Generation()}
	ops := m.SchemaDesign(p.ID)
	if !m.Availability(p.ID, "drop_table", target).Enabled || !m.Availability(p.ID, "copy_table", target).Enabled || m.Availability(p.ID, "design_table", target).Enabled || ops.Apply != nil || ops.Copy == nil {
		t.Fatal("native table operation capability boundary")
	}
	snapshot, err := ops.Snapshot(ctx, target)
	if err != nil || snapshot.Target != target || snapshot.Schema.OpenGauss == nil {
		t.Fatal("public/raw native table snapshot identity", err)
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
	if _, err = file.ExecuteFile(ctx, "public", strings.NewReader("SELECT 2;")); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old SQL file binding survived replacement", err)
	}
	if _, err = data.AdvancedLoader(ctx, p.ID, "public", "records", contract.DataOptions{Limit: 10}); err == nil {
		t.Fatal("old data binding survived replacement")
	}
	if out := ops.Drop(ctx, snapshot); out.State != "not_executed" || !errors.Is(out.Error, contract.ErrRevoked) || ops.CanDrop(target) {
		t.Fatal("native table drop binding survived replacement", out)
	}
	if out := ops.Copy(ctx, contract.SchemaCopyRequest{Original: snapshot, Destination: "obsolete_copy", WithData: true}); out.State != "not_executed" || !errors.Is(out.Error, contract.ErrRevoked) || ops.CanCopy(target) {
		t.Fatal("native table copy binding survived replacement", out)
	}
	target.Generation = replacement.Generation()
	next := m.SchemaDesign(p.ID)
	snapshot, err = next.Snapshot(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if out := next.Drop(ctx, snapshot); out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil || out.Actual.Target != target || out.Actual.Schema.Exists {
		t.Fatal("native drop receipt lost published scope", out)
	}
	replacementQuery, err := contract.Require[contract.QueryProvider](replacement)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := replacementQuery.ReserveQuery(3)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup.Release()
	if _, err = cleanup.Execute(ctx, table, "DROP SCHEMA "+table); err != nil {
		t.Fatal(err)
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
