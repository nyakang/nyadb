package engines

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestPostgreSQL17OptionsFixture(t *testing.T) {
	if os.Getenv("NYADB_POSTGRES_IT") != "1" {
		t.Skip("requires dedicated disposable PostgreSQL fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_POSTGRES_PORT"), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p := connections.Profile{ID: 91, Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: uint16(port), Database: "nyadb_it", Username: "nyadb_it", TLSMode: "disable"}
	seed := NewManager()
	defer seed.Close()
	if err = seed.Connect(ctx, p, ""); err != nil {
		t.Fatal(err)
	}
	s, _ := seed.Session(p.ID)
	run(t, s, "CREATE SCHEMA engine_options; CREATE TABLE engine_options.locked(id integer PRIMARY KEY,value text); INSERT INTO engine_options.locked VALUES(1,'original');")
	seed.Close()
	a := connections.DefaultPostgreSQLOptions()
	a.DefaultSchema, a.ApplicationName = "engine_options", "native options fixture"
	a.ConnectTimeoutSeconds, a.MaxConnections = 3, 3
	a.SQLMaxConnections = 3
	a.StatementTimeoutMS, a.LockTimeoutMS = 70, 35
	p.PostgreSQLOptions = a
	m := NewManager()
	defer m.Close()
	if err = m.Connect(ctx, p, ""); err != nil {
		t.Fatal(err)
	}
	s, _ = m.Session(p.ID)
	leases := make([]*Lease, 0, 3)
	defer func() {
		for _, l := range leases {
			l.Close()
		}
	}()
	for range 3 {
		l, err := s.Reserve(ctx)
		if err != nil {
			t.Fatal(err)
		}
		concrete := l.(*Lease)
		leases = append(leases, concrete)
		r, err := l.Execute(ctx, "SELECT current_schema(),current_setting('application_name'),current_setting('statement_timeout'),current_setting('lock_timeout'),value FROM locked")
		if err != nil || len(r.Sets) != 1 || len(r.Sets[0].Rows) != 1 {
			t.Fatalf("connection initialization: %+v %v", r, err)
		}
		row := r.Sets[0].Rows[0]
		if row[0].Text != "engine_options" || row[1].Text != "native options fixture" || row[2].Text != "70ms" || row[3].Text != "35ms" || row[4].Text != "original" {
			t.Fatalf("server settings: %+v", row)
		}
	}
	bounded, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	if l, err := s.Reserve(bounded); err == nil {
		l.Close()
		t.Fatal("pool limit was ignored")
	}
	cancel()
	start := time.Now()
	if _, err := leases[2].Execute(ctx, "SELECT pg_sleep(2)"); err == nil || time.Since(start) > time.Second {
		t.Fatalf("server statement timeout not applied: %v", err)
	}
	if _, err := leases[0].Execute(ctx, "BEGIN; SELECT * FROM locked WHERE id=1 FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if _, err := leases[1].Execute(ctx, "UPDATE locked SET value='forbidden' WHERE id=1"); err == nil || time.Since(start) > time.Second {
		t.Fatalf("server lock timeout not applied: %v", err)
	}
	if _, err := leases[0].Execute(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if r, err := leases[1].Execute(ctx, "SELECT value FROM locked"); err != nil || r.Sets[0].Rows[0][0].Text != "original" {
		t.Fatalf("failed write changed data: %+v %v", r, err)
	}
}

func TestPostgreSQLConfigUsesProfileOptionsAndRejectsUnsupportedTunnels(t *testing.T) {
	t.Setenv("PGPASSWORD", "unexpected-environment-secret")
	t.Setenv("PGSSLCERT", "/missing/environment.crt")
	t.Setenv("PGSSLKEY", "/missing/environment.key")
	t.Setenv("PGSSLROOTCERT", "/missing/environment-ca.crt")
	a := connections.DefaultPostgreSQLOptions()
	a.ConnectTimeoutSeconds, a.MaxConnections = 7, 3
	a.StatementTimeoutMS, a.LockTimeoutMS = 1200, 300
	a.ApplicationName, a.DefaultSchema = "isolated application", `a"b`
	p := connections.Profile{Kind: connections.PostgreSQL, Host: "localhost", Port: 5432, Database: "options", Username: "reader", TLSMode: "require", PostgreSQLOptions: a}
	cfg, err := postgresConfig(p, "explicit-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != "explicit-secret" || cfg.Database != "options" || cfg.User != "reader" || cfg.ConnectTimeout.Seconds() != 7 || cfg.TLSConfig == nil || cfg.RuntimeParams["search_path"] != `"a""b"` || cfg.RuntimeParams["statement_timeout"] != "1200" || cfg.RuntimeParams["lock_timeout"] != "300" || cfg.RuntimeParams["application_name"] != "isolated application" {
		t.Fatal("PostgreSQL config did not preserve explicit profile settings")
	}
	for _, tunnel := range []string{"ssh", "http"} {
		invalid := p
		if tunnel == "ssh" {
			invalid.PostgreSQLOptions.SSH.Enabled = true
			invalid.PostgreSQLOptions.SSH.Host = "localhost"
			invalid.PostgreSQLOptions.SSH.Username = "reader"
		} else {
			invalid.PostgreSQLOptions.HTTP.Enabled = true
			invalid.PostgreSQLOptions.HTTP.URL = "https://localhost/tunnel"
		}
		if _, err := postgresConfig(invalid, "", nil); err == nil {
			t.Fatalf("silently ignored %s", tunnel)
		}
	}
}
