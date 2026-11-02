package engines

import (
	"context"
	"testing"
	"time"
)

// A dependency does not stabilize a domain definition. Some versions take an
// AccessShare object lock, but a second backend can still add CHECK constraints.
// Keep this negative evidence so a probe is never mistaken for a stable guard.
func TestPostgreSQL17DesignDomainDependencyDoesNotStabilizeTypeFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_domain_lock; CREATE DOMAIN nyadb_domain_lock.amount AS integer`)
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defer rollbackDesign(c)
	for _, query := range []string{`BEGIN`, `CREATE TABLE nyadb_domain_lock.guard(value nyadb_domain_lock.amount)`} {
		if _, err := c.ExecContext(context.Background(), query); err != nil {
			t.Fatal(err)
		}
	}
	var locks string
	if err := c.QueryRowContext(context.Background(), `SELECT coalesce(string_agg(mode,','),'') FROM pg_catalog.pg_locks WHERE pid=pg_backend_pid() AND locktype='object' AND classid='pg_catalog.pg_type'::regclass AND objid='nyadb_domain_lock.amount'::regtype`).Scan(&locks); err != nil {
		t.Fatal(err)
	}
	t.Log("actual domain object locks:", locks)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = s.db.ExecContext(ctx, `ALTER DOMAIN nyadb_domain_lock.amount ADD CONSTRAINT positive CHECK(VALUE>0)`)
	if err != nil {
		t.Fatal("domain lock behavior changed; re-evaluate the guard strategy", locks, err)
	}
}
