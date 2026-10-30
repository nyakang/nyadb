package mysql

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLDataAnalysisMaximumPrecisionIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_ANALYSIS_IT") != "1" {
		t.Skip("requires explicitly enabled disposable loopback analysis fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_ANALYSIS_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	manager := NewMySQLSessionManager()
	defer manager.Close()
	settings := connections.ActiveSettings{ProfileID: 20261030994, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_toolbar_tx", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	if err := manager.Connect(ctx, settings, os.Getenv("NYADB_MYSQL_ANALYSIS_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	const database = "nyadb_toolbar_tx_it"
	table := fmt.Sprintf("analysis_precision_%d", time.Now().UnixNano())
	target, err := mysqlEditTable(database, table)
	if err != nil {
		t.Fatal(err)
	}
	db := manager.sessions[settings.ProfileID].db
	if _, err = db.ExecContext(ctx, "CREATE TABLE "+target+" (id BIGINT PRIMARY KEY, amount DECIMAL(65,30)) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanup, "DROP TABLE "+target); err != nil {
			t.Error(err)
		}
	}()
	limit := strings.Repeat("9", 35) + "." + strings.Repeat("9", 30)
	if _, err = db.ExecContext(ctx, "INSERT INTO "+target+" VALUES(1,?),(2,?),(3,NULL),(4,NULL)", limit, "-"+limit); err != nil {
		t.Fatal(err)
	}
	result, err := manager.AnalyzeMySQLData(ctx, settings.ProfileID, database, table, MySQLDataOptions{}, []string{"amount"})
	if err != nil {
		t.Fatal(err)
	}
	stats := result.Columns[0]
	mean, meanOK := new(big.Rat).SetString(stats.Mean)
	median, medianOK := new(big.Rat).SetString(stats.Median)
	deviation, deviationErr := strconv.ParseFloat(stats.StdDeviation, 64)
	if stats.Rows != 4 || stats.Null != 2 || stats.Distinct != 2 || stats.Numeric != 2 || stats.Min != "-"+limit || stats.Max != limit || !meanOK || mean.Sign() != 0 || !medianOK || median.Sign() != 0 || deviationErr != nil || math.Abs(deviation/1e35-1) > 1e-14 || stats.ModeCount != 1 || len(stats.Modes) != 1 || !stats.Modes[0].Null {
		t.Fatalf("maximum DECIMAL precision statistics changed exact extrema or center: %+v", stats)
	}
}
