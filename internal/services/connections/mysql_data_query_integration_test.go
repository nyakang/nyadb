package connections

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Optional real MySQL 8.4 test. The DDL fixture is provisioned exclusively
// into the disposable loopback container by the test harness, never by this test.
func TestMySQLAdvancedRealPerformanceFixture(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_PERF_IT") != "1" {
		t.Skip("isolated performance fixture required")
	}
	m := NewMySQLSessionManager()
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	settings := ActiveSettings{ProfileID: 9902, Host: "127.0.0.1", Port: 34306,
		Username: "nyadb_it_reader", MySQLAdvanced: DefaultMySQLAdvanced()}
	if err := m.Connect(ctx, settings, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	fetch := func(table string, o MySQLDataOptions) *MySQLDataPage {
		t.Helper()
		p, err := m.TableDataPageWithOptions(ctx, settings.ProfileID, "nyadb_perf", table, o)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := fetch("blob_entries", MySQLDataOptions{Limit: 1})
	if !first.StableOrder || len(first.Rows) != 1 || len(first.Rows[0]) != 3 ||
		!first.Rows[0][1].Truncated || len(first.Rows[0][1].Text) > mysqlDataCellMaxBytes ||
		!strings.HasPrefix(first.Rows[0][1].Text, "0x") ||
		!first.Rows[0][2].Truncated || len(first.Rows[0][2].Text) > mysqlDataCellMaxBytes {
		t.Fatalf("large payload wasn't bounded: %+v", first)
	}
	second := fetch("blob_entries", MySQLDataOptions{Limit: 1, Offset: 1})
	if len(second.Rows) != 1 || !second.Rows[0][1].Null || second.Rows[0][2].Text != "" {
		t.Fatal("binary NULL and empty strings lost")
	}
	noKey := fetch("no_key_entries", MySQLDataOptions{Limit: 2})
	if noKey.StableOrder || !noKey.HasMore {
		t.Fatal("no-key rows must warn of unstable pagination")
	}
	unique := fetch("unique_no_primary", MySQLDataOptions{Limit: 2})
	if !unique.StableOrder || !unique.HasMore || unique.Rows[0][0].Text != "A" || unique.Rows[1][0].Text != "B" {
		t.Fatalf("unique not-null index wasn't used: %+v", unique)
	}
	filtered := fetch("no_key_entries", MySQLDataOptions{Limit: 10, FilterColumn: "note",
		FilterMode: "equal", FilterValue: "b", SortColumn: "score", SortDescending: true})
	if len(filtered.Rows) != 1 || filtered.Rows[0][0].Text != "b" {
		t.Fatalf("server filter: %+v", filtered)
	}
	start := time.Now()
	for i := 0; i < 15; i++ {
		p := fetch("blob_entries", MySQLDataOptions{Limit: 1})
		if len(p.Rows) != 1 {
			t.Fatal("repeated page failed")
		}
	}
	t.Logf("15 bounded 1MiB-BLOB pages completed in %v (machine-dependent, not an SLA)", time.Since(start))
}
