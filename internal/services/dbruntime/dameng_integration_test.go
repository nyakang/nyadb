package dbruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpiworker"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestMain(m *testing.M) {
	if dpiworker.Dispatch() {
		return
	}
	os.Exit(m.Run())
}

func TestDamengRuntimeNativeFeaturesAndGenerationRevocationFixture(t *testing.T) {
	p, password := damengRuntimeFixture(t)
	p.ID = 1203
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	wrong := p
	wrong.ID++
	wrong.Database = "OTHER_CATALOG"
	if err := m.Connect(ctx, wrong, password); err == nil || m.IsConnected(wrong.ID) {
		t.Fatal("foreign native catalog published")
	}
	if p.TLSMode == "verify-full" {
		wrong = p
		wrong.ID++
		wrong.DamengOptions.CAFile = ""
		if err := m.Connect(ctx, wrong, password); err == nil || m.IsConnected(wrong.ID) {
			t.Fatal("untrusted native TLS session published")
		}
	}
	if err := m.Connect(ctx, p, password); err != nil {
		t.Fatal(err)
	}
	original, err := m.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	caps := original.Capabilities()
	if !caps.EditableTables || !caps.Transactions || !caps.Import || !caps.Export || caps.Explain || caps.Analysis || caps.DesignTables || caps.Backup || caps.Accounts || caps.Maintenance {
		t.Fatal("native capability boundary changed")
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
	schema := fmt.Sprintf("NYADB_RUNTIME_DM_%d", time.Now().UnixNano())
	if _, err = l.Execute(ctx, "SYSDBA", "CREATE SCHEMA "+schema+" AUTHORIZATION SYSDBA; CREATE TABLE "+schema+".RECORDS(ID BIGINT PRIMARY KEY,N DECIMAL(38,20),T VARCHAR(128)); INSERT INTO "+schema+".RECORDS VALUES(1,9007199254740993.12345678901234567890,'native')"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		published, err := m.Session(p.ID)
		if err != nil {
			t.Error(err)
			return
		}
		provider, err := contract.Require[contract.QueryProvider](published)
		if err != nil {
			t.Error(err)
			return
		}
		cleanup, err := provider.ReserveQuery(99)
		if err != nil {
			t.Error(err)
			return
		}
		defer cleanup.Release()
		cleanCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if _, err = cleanup.Execute(cleanCtx, "SYSDBA", "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
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
		if n.Ref == m.Namespace(p.ID, schema) {
			found = true
		}
	}
	if !found {
		t.Fatal("qualified native namespace missing")
	}
	data := m.Data(p.ID)
	object := m.Namespace(p.ID, schema).Object("table", "RECORDS")
	options := contract.DataOptions{Object: object, Limit: 10}
	page, err := data.AdvancedLoader(ctx, p.ID, schema, "RECORDS", options)
	if err != nil || page == nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "9007199254740993.1234567890123456789" || page.RowRefs[0] == nil {
		t.Fatal("bound precise native grid", err)
	}
	full, err := data.RowReader(ctx, p.ID, schema, "RECORDS", *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	saved, err := data.Saver(ctx, p.ID, schema, "RECORDS", []contract.DataEdit{{Kind: "update", Original: full.Snapshot, Values: map[string]contract.DataValue{"T": {Text: "runtime saved"}}}})
	if err != nil || saved == nil || !saved.CommitConfirmed || saved.CommitUncertain {
		t.Fatal("bound native saver", err)
	}
	verification, err := data.Verify(ctx, p.ID, schema, "RECORDS", saved)
	if err != nil || verification == nil {
		t.Fatal("bound native verification", err)
	}
	imported, err := data.Importer(ctx, p.ID, schema, "RECORDS", contract.DataImportRequest{Mode: "append", Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"ID": {Text: "2"}, "N": {Text: "2.00000000000000000001"}, "T": {Text: "imported"}}}}})
	if err != nil || imported == nil || !imported.Committed || imported.CommitUncertain {
		t.Fatal("bound native import", err)
	}
	rows := 0
	if err = data.Exporter(ctx, p.ID, schema, "RECORDS", options, []string{"N"}, func(values []contract.DataValue) error {
		rows++
		if values[0].Kind != contract.Decimal {
			return errors.New("lost exact export type")
		}
		return nil
	}); err != nil || rows != 2 {
		t.Fatal("bound native export", err, rows)
	}
	if count, err := data.Count(ctx, p.ID, schema, "RECORDS"); err != nil || count != 2 {
		t.Fatal("bound native count", err, count)
	}
	file := l.(contract.QueryFileLease)
	if out, err := file.ExecuteFile(ctx, schema, strings.NewReader("SELECT 42;")); err != nil || out.Rows[0][0] != "42" {
		t.Fatal("bound native file", err)
	}
	short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	_, err = l.Execute(short, schema, "SELECT COUNT(*) FROM SYS.SYSOBJECTS A CROSS JOIN SYS.SYSOBJECTS B CROSS JOIN SYS.SYSOBJECTS C WHERE MOD(A.ID,17)+MOD(B.ID,17)+MOD(C.ID,17)=0")
	stop()
	if !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("original native cancellation not acknowledged", err)
	}
	if _, err = l.Execute(ctx, schema, "SELECT 1"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("native lease silently recovered", err)
	}
	l.Release()
	l, err = q.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	file = l.(contract.QueryFileLease)
	tx, err := data.Transactions.Begin(ctx, p.ID, schema, "RECORDS")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	if err = m.Connect(ctx, p, password); err != nil {
		t.Fatal(err)
	}
	replacement, err := m.Session(p.ID)
	if err != nil || replacement.Generation() <= original.Generation() {
		t.Fatal("native public generation failed", err)
	}
	if _, err = l.Execute(ctx, schema, "SELECT 1"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old native lease survived replacement", err)
	}
	if _, err = file.ExecuteFile(ctx, schema, strings.NewReader("SELECT 2;")); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old native file binding survived replacement", err)
	}
	if _, err = data.AdvancedLoader(ctx, p.ID, schema, "RECORDS", options); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old native data binding survived replacement", err)
	}
	if err = data.Transactions.Commit(ctx, tx); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old native transaction survived replacement", err)
	}
	foreign := object
	foreign.ProfileID++
	if _, err = m.Data(p.ID).AdvancedLoader(ctx, p.ID, schema, "RECORDS", contract.DataOptions{Object: foreign, Limit: 10}); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign native object accepted", err)
	}
}

// Only the explicit disposable fixture runner supplies this private file.
// The password remains in a local file, never a command-line argument.
func damengRuntimeFixture(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_DAMENG_DPI_IT") != "1" {
		t.Skip("requires owned DM8 fixture")
	}
	data, err := os.ReadFile(os.Getenv("NYADB_DAMENG_DPI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Endpoint     string `json:"endpoint"`
		PasswordFile string `json:"password_file"`
		SSL          string `json:"native_ssl_directory"`
	}
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(f.Endpoint)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("captured loopback fixture required")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		t.Fatal("explicit native fixture port required")
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.Dameng)
	p.Name, p.Host, p.Port, p.TLSMode = "Dameng fixture", host, uint16(n), "disable"
	a := &p.DamengOptions
	a.LibraryFile = os.Getenv("NYADB_DAMENG_DPI_LIBRARY")
	a.MaxConnections, a.SQLMaxConnections = 2, 3
	if f.SSL != "" {
		p.TLSMode = "verify-full"
		a.CAFile = filepath.Join(f.SSL, "ca-cert.pem")
		a.CertificateFile = filepath.Join(f.SSL, "client-cert.pem")
		a.KeyFile = filepath.Join(f.SSL, "client-key.pem")
	}
	p, err = connections.NormalizeProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, string(secret)
}
