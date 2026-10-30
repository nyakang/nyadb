package mysql

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// A repeatable large-table measurement is opt-in, uses only its own temporary
// InnoDB table in the existing disposable schema, and records actual resource
// measurements instead of setting machine-dependent timing gates.
func TestMySQLDataAnalysisLargeTableIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_ANALYSIS_LARGE_IT") != "1" {
		t.Skip("requires dedicated disposable loopback large-table fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_ANALYSIS_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	const database = "nyadb_toolbar_tx_it"
	m := NewMySQLSessionManager()
	defer m.Close()
	settings := connections.ActiveSettings{ProfileID: 2026103030, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_toolbar_tx", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := m.Connect(ctx, settings, os.Getenv("NYADB_MYSQL_ANALYSIS_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	db := m.sessions[settings.ProfileID].db
	table := fmt.Sprintf("analysis_large_%d", time.Now().UnixNano())
	target, err := mysqlEditTable(database, table)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+target+" (id BIGINT PRIMARY KEY, amount DECIMAL(30,10) NOT NULL, note VARCHAR(32) NOT NULL) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := db.ExecContext(cleanup, "DROP TABLE "+target); err != nil {
			t.Error(err)
		}
	}()
	started := time.Now()
	// Five independent digit sets generate exactly 100000 rows in one bounded
	// server-side insertion; this changes no global server/session settings.
	seed := "INSERT INTO " + target + " (id,amount,note) WITH digits AS (SELECT 0 d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9), numbers AS (SELECT 1+a.d+10*b.d+100*c.d+1000*d.d+10000*e.d AS n FROM digits a CROSS JOIN digits b CROSS JOIN digits c CROSS JOIN digits d CROSS JOIN digits e) SELECT n,n-50000,CONCAT('group-',MOD(n,100)) FROM numbers"
	if _, err := db.ExecContext(ctx, seed); err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture_rows=100000 fixture_build=%s", time.Since(started))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started = time.Now()
	result, err := m.AnalyzeMySQLData(ctx, settings.ProfileID, database, table, MySQLDataOptions{}, []string{"id", "amount", "note"})
	wall := time.Since(started)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	amount, note := result.Columns[1], result.Columns[2]
	median, ok := new(big.Rat).SetString(amount.Median)
	deviation, deviationErr := strconv.ParseFloat(amount.StdDeviation, 64)
	if result.Rows != 100000 || amount.Zero != 1 || !ok || median.Cmp(big.NewRat(1, 2)) != 0 || deviationErr != nil || math.Abs(deviation-math.Sqrt((100000.0*100000-1)/12)) > 1e-6 || amount.ModeCount != 100000 || len(amount.Modes) != 10 || note.Distinct != 100 || note.MaxFrequency != 1000 || note.ModeCount != 100 || len(note.Modes) != 10 {
		t.Fatalf("large-table statistics incomplete: rows=%d amount=%+v note=%+v", result.Rows, amount, note)
	}
	t.Logf("analysis_rows=%d columns=%d wall=%s service=%s client_total_alloc_delta=%d client_heap_alloc_delta=%d", result.Rows, len(result.Columns), wall, result.Duration, after.TotalAlloc-before.TotalAlloc, int64(after.HeapAlloc)-int64(before.HeapAlloc))
	started = time.Now()
	page, err := m.TableDataPageWithOptions(ctx, settings.ProfileID, database, table, MySQLDataOptions{Limit: 100})
	if err != nil || len(page.Rows) != 100 || !page.HasMore || !page.StableOrder || page.Rows[0][0].Text != "1" || page.Rows[99][0].Text != "100" {
		t.Fatalf("large table ignored bounded page or stable identity: %+v %v", page, err)
	}
	t.Logf("loaded_page_rows=%d page_wall=%s has_more=%v", len(page.Rows), time.Since(started), page.HasMore)

}
