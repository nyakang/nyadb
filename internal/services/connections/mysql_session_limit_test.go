package connections

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
)

func TestMySQLSQLTabBudgetUsesExplicitProfileSettings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		chosen  int
		want    int
	}{
		{"disabled-default", false, 1, 8},
		{"enabled-one", true, 1, 1},
		{"enabled-two", true, 2, 2},
		{"enabled-ten-clamped", true, 10, 8},
		{"enabled-huge-clamped", true, 10000, 8},
		{"corrupt-value-fails-closed", true, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			advanced := DefaultMySQLAdvanced()
			advanced.LimitSessions, advanced.MaxSessions = tc.enabled, tc.chosen
			if got := mysqlSQLTabLimit(advanced); got != tc.want {
				t.Fatalf("SQL tab limit: got %d, want %d", got, tc.want)
			}
			if tc.chosen > 0 {
				s := ActiveSettings{Host: "127.0.0.1", Port: 3306, MySQLAdvanced: advanced}
				if _, err := DirectMySQLConfig(s, ""); err != nil {
					t.Fatalf("supported session limit rejected: %v", err)
				}
			}
		})
	}
}

func TestMySQLSQLTabBudgetBlocksPreconnectAndExecutionUntilRelease(t *testing.T) {
	m := NewMySQLSessionManager()
	var db *sql.DB
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		db = sql.OpenDB(sqlTestConnector{log: make(chan string, 64)})
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(0)
		return db, nil
	}
	advanced := DefaultMySQLAdvanced()
	advanced.LimitSessions, advanced.MaxSessions = true, 1
	const profile int64 = 512
	if err := m.Connect(context.Background(), ActiveSettings{ProfileID: profile, MySQLAdvanced: advanced}, ""); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.ReserveSQLTab(profile, 11); err != nil {
		t.Fatal(err)
	}
	if err := m.ReserveSQLTab(profile, 11); err != nil {
		t.Fatalf("reserving an existing tab should be idempotent: %v", err)
	}
	if err := m.PreconnectSQLTab(context.Background(), profile, 11); err != nil {
		t.Fatal(err)
	}
	if err := m.ReserveSQLTab(profile, 22); !errors.Is(err, ErrMySQLSQLSessionLimit) {
		t.Fatalf("second tab must be rejected before network I/O: %v", err)
	}
	if _, err := m.ExecuteSQLTab(context.Background(), profile, 22, "", "SELECT id,name FROM products"); !errors.Is(err, ErrMySQLSQLSessionLimit) {
		t.Fatalf("execution bypassed the tab budget: %v", err)
	}
	if n := db.Stats().InUse; n != 1 {
		t.Fatalf("rejected tab consumed a physical connection: %d", n)
	}
	if _, err := m.ExecuteSQLTab(context.Background(), profile, 11, "", "SELECT id,name FROM products"); err != nil {
		t.Fatalf("existing tab stopped working at quota: %v", err)
	}
	m.ReleaseSQLTab(profile, 11)
	waitSQLTabConnectionCount(t, db, 0)
	if err := m.ReserveSQLTab(profile, 22); err != nil {
		t.Fatalf("closing existing tab did not free quota: %v", err)
	}
	if _, err := m.ExecuteSQLTab(context.Background(), profile, 22, "", "SELECT id,name FROM products"); err != nil {
		t.Fatalf("rejected tab could not retry after quota released: %v", err)
	}
	m.ReleaseSQLTab(profile, 22)
	waitSQLTabConnectionCount(t, db, 0)
}

func TestMySQLSQLTabBudgetConcurrentReservationAndProfiles(t *testing.T) {
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(sqlTestConnector{log: make(chan string, 64)}), nil
	}
	defer m.Close()
	adv := DefaultMySQLAdvanced()
	adv.LimitSessions, adv.MaxSessions = true, 2
	for _, profile := range []int64{31, 32} {
		if err := m.Connect(context.Background(), ActiveSettings{ProfileID: profile, MySQLAdvanced: adv}, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, profile := range []int64{31, 32} {
		var wg sync.WaitGroup
		results := make(chan error, 32)
		for i := 1; i <= 32; i++ {
			wg.Add(1)
			go func(tabID uint64) {
				defer wg.Done()
				results <- m.ReserveSQLTab(profile, tabID)
			}(uint64(i))
		}
		wg.Wait()
		close(results)
		success, denied := 0, 0
		for err := range results {
			switch {
			case err == nil:
				success++
			case errors.Is(err, ErrMySQLSQLSessionLimit):
				denied++
			default:
				t.Fatalf("unexpected concurrent reservation result: %v", err)
			}
		}
		if success != 2 || denied != 30 {
			t.Fatalf("profile %d: admitted %d, denied %d; wanted 2/30", profile, success, denied)
		}
	}
	// These quotas restrict SQL tab leases, not the metadata database pool.
	if got := m.sessions[31].sqlTabLimit; got != 2 {
		t.Fatalf("profile limit was not captured at Connect: %d", got)
	}
}
