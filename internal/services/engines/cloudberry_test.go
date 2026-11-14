package engines

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
)

func TestCloudberryOptionsPreserveNativeValueBoundaries(t *testing.T) {
	raw := `["location_uris=file://cdw/tmp/a,b.csv","delimiter=,","null=","quoted=tenant's \"value\"\nnext"]`
	options, err := cloudberryOptions(raw)
	if err != nil || !reflect.DeepEqual(options, []string{"location_uris=file://cdw/tmp/a,b.csv", "delimiter=,", "null=", "quoted=tenant's \"value\"\nnext"}) {
		t.Fatal("native option boundaries changed", err)
	}
	clauses, err := cloudberryOptionClauses(options, false)
	if err != nil || !strings.Contains(clauses, `"null" ''`) || !strings.Contains(clauses, `"delimiter" ','`) || !strings.Contains(clauses, "tenant''s") {
		t.Fatal("native option quoting changed", err)
	}
	for _, raw := range []string{"null", `[null]`, `["missing"]`, `["=empty"]`, `["bad=\u0000"]`, `{"key":"value"}`, `["bad=`, strings.Repeat(" ", (256<<10)+1)} {
		if _, err := cloudberryOptions(raw); err == nil {
			t.Fatal("incomplete native options accepted")
		}
	}
}

func TestCloudberryRejectsOrdinaryPostgresAndAbsentNativeCatalog(t *testing.T) {
	for _, rows := range [][][]driver.Value{
		{{"PostgreSQL 14.4", int64(140000), "gp_distribution_policy"}},
		{{"Apache Cloudberry", int64(140000), nil}},
		{{"Apache Cloudberry", int64(130000), "gp_distribution_policy"}},
	} {
		fixture := &distributionFixture{rows: rows}
		db := sql.OpenDB(fixture)
		if err := validateCloudberryServer(context.Background(), db); err == nil {
			t.Fatal("protocol substitute accepted as native Cloudberry")
		}
		_ = db.Close()
	}
}
