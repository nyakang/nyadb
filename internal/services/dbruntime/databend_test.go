package dbruntime_test

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/databend"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"testing"
)

func TestDatabendBuiltinRequiresExplicitInstalledPackage(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	p, _ := connections.Defaults(connections.Databend)
	p.ID, p.Name = 7, "original"
	if err := m.Connect(context.Background(), p, ""); !errors.Is(err, databend.ErrManagedDriver) {
		t.Fatal("builtin missing or selected implicit driver", err)
	}
	if _, err := m.Session(p.ID); err == nil {
		t.Fatal("failed open published session")
	}
}
