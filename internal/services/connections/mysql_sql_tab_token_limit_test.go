package connections

import (
	"context"
	"errors"
	"testing"
)

func TestMySQLSQLTabTokenRespectsSessionLimitAndReleasesQuota(t *testing.T) {
	m := newSQLTokenTestManager(t)
	advanced := DefaultMySQLAdvanced()
	advanced.LimitSessions = true
	advanced.MaxSessions = 1
	const profile int64 = 621
	if err := m.Connect(context.Background(), ActiveSettings{ProfileID: profile, MySQLAdvanced: advanced}, ""); err != nil {
		t.Fatal(err)
	}
	first, err := m.ReserveSQLTabToken(profile, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReserveSQLTabToken(profile, 2); !errors.Is(err, ErrMySQLSQLSessionLimit) {
		t.Fatalf("token reservation bypassed SQL session quota: %v", err)
	}
	if _, err := m.ExecuteSQLTabToken(context.Background(), first, "", "SELECT id,name FROM products"); err != nil {
		t.Fatalf("quota prevented existing token from executing: %v", err)
	}
	m.ReleaseSQLTabToken(first)
	second, err := m.ReserveSQLTabToken(profile, 2)
	if err != nil {
		t.Fatalf("closing first tab did not release quota: %v", err)
	}
	m.ReleaseSQLTabToken(first) // late close cannot release the new tab
	if err := m.PreconnectSQLTabToken(context.Background(), second); err != nil {
		t.Fatalf("late close damaged next token: %v", err)
	}
}
