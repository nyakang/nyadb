package dbruntime_test

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"os"
	"strconv"
	"testing"
)

func TestOracleRuntimeDisposableFixture(t *testing.T) {
	if os.Getenv("NYADB_ORACLE_IT") != "1" {
		t.Skip("requires disposable Oracle fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_ORACLE_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit loopback fixture port required")
	}
	p, _ := connections.Defaults(connections.Oracle)
	p.ID = 301
	p.Name = "Oracle runtime"
	p.Host = "127.0.0.1"
	p.Port = uint16(port)
	p.Username = "nyadb_it"
	p.OracleOptions.SQLMaxConnections = 1
	ctx := context.Background()
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	if err = m.Connect(ctx, p, os.Getenv("NYADB_ORACLE_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	s, err := m.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := s.Generation()
	caps := s.Capabilities()
	if !caps.Query || !caps.Metadata || !caps.EditableTables || !caps.Transactions || !caps.Import || caps.Backup || caps.DesignTables || caps.Accounts {
		t.Fatal(caps)
	}
	q, err := contract.Require[contract.QueryProvider](s)
	if err != nil {
		t.Fatal(err)
	}
	l, err := q.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, err = q.ReserveQuery(2); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal("SQL quota", err)
	}
	result, err := l.Execute(ctx, "NYADB_IT", `SELECT 1 FROM dual`)
	if err != nil || len(result.Sets) != 1 {
		t.Fatal(err)
	}
	object := m.Namespace(p.ID, "NYADB_IT").Object("table", "records")
	if object.Catalog != "XEPDB1" {
		t.Fatal(object)
	}
	data := m.Data(p.ID)
	page, err := data.AdvancedLoader(ctx, p.ID, "NYADB_IT", "records", contract.DataOptions{Object: object, Limit: 10})
	if err != nil || len(page.Rows) < 1 {
		t.Fatal(page, err)
	}
	if err = m.Disconnect(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Execute(ctx, "NYADB_IT", `SELECT 1 FROM dual`); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old SQL lease survived", err)
	}
	if _, err = data.AdvancedLoader(ctx, p.ID, "NYADB_IT", "records", contract.DataOptions{Object: object, Limit: 10}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old data runtime survived", err)
	}
	if err = m.Connect(ctx, p, os.Getenv("NYADB_ORACLE_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	replacement, err := m.Session(p.ID)
	if err != nil || replacement.Generation() <= first {
		t.Fatal("public session generation", err)
	}
}
