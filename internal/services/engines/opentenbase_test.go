package engines

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
)

func TestOpenTenBaseRejectsSubstitutesAndIncompleteNativeCatalog(t *testing.T) {
	for _, row := range [][]driver.Value{
		{"PostgreSQL 10.0", int64(100000), "pgxc_class", true},
		{"OpenTenBase_v5.0", int64(100000), nil, true},
		{"OpenTenBase_v5.0", int64(100000), "pgxc_class", false},
		{"OpenTenBase_v5.0", int64(90600), "pgxc_class", true},
	} {
		db := sql.OpenDB(&distributionFixture{rows: [][]driver.Value{row}, columns: []string{"version", "number", "catalog", "definition"}})
		if err := validateOpenTenBaseServer(context.Background(), db); err == nil {
			t.Fatal("non-native or incomplete OpenTenBase accepted")
		}
		_ = db.Close()
	}
	db := sql.OpenDB(&distributionFixture{rows: [][]driver.Value{{"PostgreSQL 10.0 @ OpenTenBase_v5.0", int64(100000), "pgxc_class", true}}, columns: []string{"version", "number", "catalog", "definition"}})
	defer db.Close()
	if err := validateOpenTenBaseServer(context.Background(), db); err != nil {
		t.Fatal("native PG10-derived OpenTenBase rejected", err)
	}
}
