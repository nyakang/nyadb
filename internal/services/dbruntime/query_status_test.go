package dbruntime_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestQueryLeaseStatusOriginalReleaseReplacementAndPublication(t *testing.T) {
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	p := profile(17)
	p.Database = filepath.Join(t.TempDir(), "status.sqlite")
	first, err := m.ConnectSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := contract.Require[contract.QueryProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	status := lease.(contract.QueryLeaseStatus)
	if err = status.CheckQueryLease(); err != nil {
		t.Fatal(err)
	}
	replacement, err := lease.Replace()
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Release()
	if !errors.Is(status.CheckQueryLease(), contract.ErrRevoked) || replacement.(contract.QueryLeaseStatus).CheckQueryLease() != nil {
		t.Fatal("status admitted an explicitly replaced original lease")
	}
	fresh, err := m.ConnectSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(replacement.(contract.QueryLeaseStatus).CheckQueryLease(), contract.ErrRevoked) || m.Generation(p.ID) != fresh.Generation() {
		t.Fatal("parked original publication remained current")
	}
	next, err := contract.Require[contract.QueryProvider](fresh)
	if err != nil {
		t.Fatal(err)
	}
	active, err := next.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	active.Release()
	if !errors.Is(active.(contract.QueryLeaseStatus).CheckQueryLease(), contract.ErrRevoked) {
		t.Fatal("released lease remained current")
	}
}
