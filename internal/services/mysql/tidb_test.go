package mysql

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestTiDBVersionUsesProductRelease(t *testing.T) {
	for _, v := range []string{"8.0.11-TiDB-v8.5.3", "5.7.25-TiDB-v8.5.0", "8.0.11-TiDB-v9.0.0-alpha"} {
		if err := validateTiDBVersion(v); err != nil {
			t.Fatal(v, err)
		}
	}
	for _, v := range []string{"8.0.11", "10.11.15-MariaDB", "8.0.11-TiDB-v8.4.9", "8.0.11-TiDB-v7.5.0", "8.0.11-TiDB-v8.5", "TiDB-v8.5.3garbage"} {
		if err := validateTiDBVersion(v); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal(v, err)
		}
	}
}

func TestTiDBPlannerDiagnosticsDoNotHideValueWarnings(t *testing.T) {
	ctx := context.WithValue(context.Background(), mysqlProductKey{}, connections.TiDB)
	if mysqlValueWarning(ctx, 1105, "skip prepared plan-cache: '1' may be converted to INT") {
		t.Fatal("plan-cache diagnostic rejected exact save")
	}
	for _, v := range []struct {
		code    int
		message string
	}{{1265, "data truncated"}, {1105, "other error"}, {1265, "skip prepared plan-cache: invalid"}} {
		if !mysqlValueWarning(ctx, v.code, v.message) {
			t.Fatal("real warning ignored")
		}
	}
	if !mysqlValueWarning(context.Background(), 1105, "skip prepared plan-cache: reason") {
		t.Fatal("TiDB behavior changed MySQL")
	}
}
