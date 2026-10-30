package mysql

import (
	"context"
	"errors"
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

// Opt in against the dedicated disposable loopback schema only. This test
// creates a uniquely named table and removes it, leaving existing tables alone.
func TestMySQLDataAnalysisIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_ANALYSIS_IT") != "1" {
		t.Skip("requires dedicated disposable loopback analysis fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_ANALYSIS_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	const database = "nyadb_toolbar_tx_it"
	m := NewMySQLSessionManager()
	defer m.Close()
	settings := connections.ActiveSettings{ProfileID: 2026103017, Host: "127.0.0.1", Port: uint16(port),
		Username: "nyadb_toolbar_tx", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Connect(ctx, settings, os.Getenv("NYADB_MYSQL_ANALYSIS_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	db := m.sessions[settings.ProfileID].db
	table := fmt.Sprintf("analysis_probe_%d", time.Now().UnixNano())
	target, err := mysqlEditTable(database, table)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "CREATE TABLE "+target+` (
 id BIGINT PRIMARY KEY, amount DECIMAL(30,10) NULL,
 note VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL,
 detail TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
 doc JSON NULL, payload BLOB NULL) ENGINE=InnoDB`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := db.ExecContext(cleanup, "DROP TABLE "+target); err != nil {
			t.Error(err)
		}
	}()
	long := strings.Repeat("长", 1000)
	if _, err = db.ExecContext(ctx, "INSERT INTO "+target+" VALUES(?,?,?,?,?,?),(?,?,?,?,?,?),(?,?,?,?,?,?),(?,?,?,?,?,?)",
		1, "9007199254740993.2500000000", "Alpha", long+"A", `{"n":1}`, []byte{0xff, 0},
		2, "9007199254740992.7500000000", "alpha", long+"B", `{"n":1}`, []byte{0xff, 0},
		3, nil, "中文", "", `{"n":2}`, []byte{1},
		4, "1.0000000000", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	fields := []string{"amount", "note", "detail", "doc", "payload"}
	result, err := m.AnalyzeMySQLData(ctx, settings.ProfileID, database, table, MySQLDataOptions{}, fields)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows != 4 || len(result.Columns) != 5 {
		t.Fatalf("full result: %+v", result)
	}
	amount, note, detail, doc, payload := result.Columns[0], result.Columns[1], result.Columns[2], result.Columns[3], result.Columns[4]
	maximum, ok := new(big.Rat).SetString(amount.Max)
	if amount.Null != 1 || amount.Distinct != 3 || amount.Singletons != 3 || amount.Min != "1.0000000000" || !ok || maximum.Cmp(big.NewRat(36028797018963973, 4)) != 0 {
		t.Fatalf("exact decimal aggregate: %+v", amount)
	}
	mean, ok := new(big.Rat).SetString(amount.Mean)
	if !ok || new(big.Rat).Abs(new(big.Rat).Sub(mean, big.NewRat(18014398509481987, 3))).Cmp(big.NewRat(1, 1_000_000_000_000)) > 0 {
		t.Fatalf("decimal mean lost server precision: %s", amount.Mean)
	}
	if note.Null != 1 || note.Distinct != 2 || note.Singletons != 1 || note.Collation != "utf8mb4_0900_ai_ci" || len(note.Distribution) != 2 || !strings.EqualFold(note.Distribution[0].Value, "alpha") || note.Distribution[0].Count != 2 {
		t.Fatalf("collation grouping: %+v", note)
	}
	if detail.Null != 1 || detail.Distinct != 3 || detail.Singletons != 3 || len(detail.Distribution) != 3 || !detail.Distribution[1].Truncated || len([]rune(detail.Distribution[1].Value)) != 128 {
		t.Fatalf("full text grouping/bounded preview: %+v", detail)
	}
	if doc.Null != 1 || doc.Distinct != 2 || doc.Singletons != 1 || doc.Collation != "utf8mb4_bin" || doc.Distribution[0].Count != 2 {
		t.Fatalf("JSON grouping: %+v", doc)
	}
	if payload.Null != 1 || payload.Distinct != 2 || payload.Singletons != 1 || payload.Collation != "binary" || payload.Distribution[0].Value != "0xff00" || payload.Distribution[0].Count != 2 {
		t.Fatalf("binary grouping: %+v", payload)
	}
	filtered, err := m.AnalyzeMySQLData(ctx, settings.ProfileID, database, table, MySQLDataOptions{Filters: []MySQLDataFilterRule{{Column: "note", Mode: "equal", Value: "ALPHA"}}}, fields)
	if err != nil || filtered.Rows != 2 || filtered.Columns[1].Distinct != 1 || filtered.Columns[2].Distinct != 2 {
		t.Fatalf("complete filtered result: %+v %v", filtered, err)
	}
	// Close values above 2^53 retain their half-unit median and variance.
	if _, err := db.ExecContext(ctx, "UPDATE "+target+" SET amount=CASE id WHEN 1 THEN ? WHEN 2 THEN ? ELSE NULL END", "9007199254740992.0000000000", "9007199254740993.0000000000"); err != nil {
		t.Fatal(err)
	}
	closeValues, err := m.AnalyzeMySQLData(ctx, settings.ProfileID, database, table, MySQLDataOptions{}, []string{"amount"})
	if err != nil {
		t.Fatal(err)
	}
	closeStats := closeValues.Columns[0]
	median, ok := new(big.Rat).SetString(closeStats.Median)
	deviation, deviationErr := strconv.ParseFloat(closeStats.StdDeviation, 64)
	if !ok || median.Cmp(big.NewRat(18014398509481985, 2)) != 0 || deviationErr != nil || deviation != 0.5 || closeStats.ModeCount != 1 || !closeStats.Modes[0].Null {
		t.Fatalf("centered decimal statistics lost small differences or NULL mode: %+v", closeStats)
	}
	if _, err := db.ExecContext(ctx, "UPDATE "+target+" SET amount=CASE id WHEN 1 THEN 0 WHEN 2 THEN 12.5 WHEN 3 THEN 25 ELSE NULL END"); err != nil {
		t.Fatal(err)
	}
	fixtureResult, err := m.AnalyzeMySQLData(ctx, settings.ProfileID, database, table, MySQLDataOptions{}, []string{"amount"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureStats := fixtureResult.Columns[0]
	median, ok = new(big.Rat).SetString(fixtureStats.Median)
	deviation, deviationErr = strconv.ParseFloat(fixtureStats.StdDeviation, 64)
	if fixtureStats.Zero != 1 || !ok || median.Cmp(big.NewRat(25, 2)) != 0 || deviationErr != nil || math.Abs(deviation-math.Sqrt(625.0/6)) > 1e-8 || fixtureStats.ModeCount != 4 || fixtureStats.MaxFrequency != 1 {
		t.Fatalf("four-row Navicat analysis fixture differs: %+v", fixtureStats)
	}
	if _, err := db.ExecContext(ctx, "UPDATE "+target+" SET amount=50 WHERE id=4"); err != nil {
		t.Fatal(err)
	}
	even, err := m.AnalyzeMySQLData(ctx, settings.ProfileID, database, table, MySQLDataOptions{}, []string{"amount"})
	if err != nil {
		t.Fatal(err)
	}
	median, ok = new(big.Rat).SetString(even.Columns[0].Median)
	if !ok || median.Cmp(big.NewRat(75, 4)) != 0 {
		t.Fatal("even median did not average the central pair")
	}
	empty, err := m.AnalyzeMySQLData(ctx, settings.ProfileID, database, table, MySQLDataOptions{Filters: []MySQLDataFilterRule{{Column: "id", Mode: "greater", Value: "4"}}}, []string{"amount"})
	if err != nil || empty.Rows != 0 || empty.Columns[0].Median != "" || empty.Columns[0].StdDeviation != "" || empty.Columns[0].ModeCount != 0 {
		t.Fatalf("empty analysis invented statistics: %+v %v", empty, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE "+target+" SET amount=NULL"); err != nil {
		t.Fatal(err)
	}
	nulls, err := m.AnalyzeMySQLData(ctx, settings.ProfileID, database, table, MySQLDataOptions{}, []string{"amount"})
	if err != nil || nulls.Columns[0].Zero != 0 || nulls.Columns[0].Median != "" || nulls.Columns[0].StdDeviation != "" || nulls.Columns[0].ModeCount != 1 || !nulls.Columns[0].Modes[0].Null {
		t.Fatalf("all-NULL analysis invented numeric statistics: %+v %v", nulls, err)
	}
	for i := range 25 {
		if _, err := db.ExecContext(ctx, "INSERT INTO "+target+" (id) VALUES(?)", 100+i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "UPDATE "+target+" SET note=CONCAT('unique-',id)"); err != nil {
		t.Fatal(err)
	}
	ties, err := m.AnalyzeMySQLData(ctx, settings.ProfileID, database, table, MySQLDataOptions{}, []string{"note"})
	if err != nil || ties.Columns[0].ModeCount != 29 || len(ties.Columns[0].Modes) != 10 || ties.Columns[0].MaxFrequency != 1 {
		t.Fatalf("complete tie count was replaced by the bounded distribution: %+v %v", ties, err)
	}
	// A canceled aggregate request cannot publish cached or partial statistics.
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := m.AnalyzeMySQLData(canceled, settings.ProfileID, database, table, MySQLDataOptions{}, fields); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled live request: %v", err)
	}
	// Hold this fixture table's write lock on another physical connection so a
	// real aggregate is still in flight when cancellation reaches the driver.
	locker, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	if _, err := locker.ExecContext(ctx, "LOCK TABLES "+target+" WRITE"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := locker.ExecContext(cleanup, "UNLOCK TABLES"); err != nil {
			t.Error(err)
		}
	}()
	reading, stopRead := context.WithCancel(ctx)
	defer stopRead()
	done := make(chan error, 1)
	go func() {
		_, err := m.AnalyzeMySQLData(reading, settings.ProfileID, database, table, MySQLDataOptions{}, fields)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		var waiting int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID<>CONNECTION_ID() AND INFO LIKE ? AND STATE LIKE '%lock%'", "%"+table+"%").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			blocked = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("aggregate did not reach the disposable table's lock")
	}
	stopRead()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight analysis cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("live driver did not acknowledge aggregate cancellation")
	}
}
