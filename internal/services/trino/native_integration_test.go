package trino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeConfig(t *testing.T) (connections.TrinoConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_TRINO_IT") != "1" {
		t.Skip("set NYADB_TRINO_IT and a private owned fixture configuration")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_TRINO_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Endpoint, Username, Catalog, Schema string
		CAFile                              string `json:"ca_file"`
		PasswordFile                        string `json:"password_file"`
	}
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	password, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	return connections.TrinoConfig{Endpoint: cfg.Endpoint, Username: cfg.Username, Catalog: cfg.Catalog, Schema: cfg.Schema, CAFile: cfg.CAFile}, string(password)
}

func nativeRun(t *testing.T, c *Client, sql string) [][]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var rows [][]json.RawMessage
	_, err := c.Run(ctx, sql, func(_ []Column, row []json.RawMessage) error { rows = append(rows, row); return nil })
	if err != nil {
		t.Fatalf("native command failed: %s: %v", sql, err)
	}
	return rows
}

func TestTrinoNativeAuthenticationTypesAndSessionState(t *testing.T) {
	cfg, password := nativeConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c, err := Open(ctx, cfg, password)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if password != "" {
		if bad, err := Open(ctx, cfg, password+"wrong"); err == nil {
			bad.Close()
			t.Fatal("wrong password admitted")
		}
		wrong := cfg
		wrong.CAFile = ""
		if bad, err := Open(ctx, wrong, password); err == nil {
			bad.Close()
			t.Fatal("untrusted CA admitted")
		}
		wrong = cfg
		wrong.ServerName = "wrong.invalid"
		if bad, err := Open(ctx, wrong, password); err == nil {
			bad.Close()
			t.Fatal("wrong TLS hostname admitted")
		}
		wrong = cfg
		wrong.Endpoint = strings.Replace(wrong.Endpoint, "https:", "http:", 1)
		wrong.CAFile = ""
		if bad, err := Open(ctx, wrong, password); err == nil {
			bad.Close()
			t.Fatal("plaintext password admitted")
		}
	}
	rows := nativeRun(t, c, "SELECT CAST(9007199254740993 AS BIGINT),DECIMAL '99999999999999999999999999999999999999',DECIMAL '12345678901234567890.123456789012345678',X'00FF',CAST('2026-11-08 01:02:03.123456789' AS TIMESTAMP(9)),ARRAY[9007199254740993,9223372036854775807],MAP(ARRAY['key'],ARRAY[DECIMAL '12345678901234567890.123456789012345678']),CAST(ROW(9007199254740993,'中文') AS ROW(n BIGINT,s VARCHAR)),JSON '{\"n\":9007199254740993}',CAST(NULL AS VARCHAR),'',true,nan(),infinity()")
	if len(rows) != 1 || len(rows[0]) != 14 {
		t.Fatal("native type columns lost")
	}
	expected := []string{"9007199254740993", `"99999999999999999999999999999999999999"`, `"12345678901234567890.123456789012345678"`, `"AP8="`, `"2026-11-08 01:02:03.123456789"`, `[9007199254740993,9223372036854775807]`, `{"key":"12345678901234567890.123456789012345678"}`, `[9007199254740993,"中文"]`, `"{\"n\":9007199254740993}"`, "null", `""`, "true", `"NaN"`, `"Infinity"`}
	for i, v := range expected {
		if string(rows[0][i]) != v {
			t.Fatalf("raw native type %d differs: %s", i, rows[0][i])
		}
	}
	empty, err := c.Run(ctx, "SELECT CAST(NULL AS DECIMAL(38,18)) AS n WHERE FALSE", nil)
	if err != nil || empty.Rows != 0 || len(empty.Columns) != 1 || empty.Columns[0].Type != "decimal(38, 18)" {
		t.Fatal("empty native result lost its typed columns", err, empty.Columns)
	}
	nativeRun(t, c, "SET SESSION query_max_run_time='2h'")
	if c.headers.Get("X-Trino-Session") != "query_max_run_time=2h" {
		t.Fatal("native session set header lost")
	}
	nativeRun(t, c, "RESET SESSION query_max_run_time")
	if c.headers.Get("X-Trino-Session") != "" {
		t.Fatal("native session clear header lost")
	}
	nativeRun(t, c, "PREPARE nyadb_stmt FROM SELECT ? AS n")
	rows = nativeRun(t, c, "EXECUTE nyadb_stmt USING 9007199254740993")
	if string(rows[0][0]) != "9007199254740993" {
		t.Fatal("native prepared state or precision lost")
	}
	nativeRun(t, c, "DEALLOCATE PREPARE nyadb_stmt")
	if c.headers.Get("X-Trino-Prepared-Statement") != "" {
		t.Fatal("deallocated prepare retained")
	}
	nativeRun(t, c, "USE tpch.tiny")
	rows = nativeRun(t, c, "SELECT current_catalog,current_schema,COUNT(*) FROM nation GROUP BY 1,2")
	if string(rows[0][0]) != `"tpch"` || string(rows[0][1]) != `"tiny"` || string(rows[0][2]) != "25" {
		t.Fatal("native catalog/schema switch lost")
	}
	nativeRun(t, c, "START TRANSACTION READ ONLY")
	if c.headers.Get("X-Trino-Transaction-Id") == "NONE" {
		t.Fatal("native transaction token lost")
	}
	nativeRun(t, c, "SELECT COUNT(*) FROM nation")
	nativeRun(t, c, "ROLLBACK")
	if c.headers.Get("X-Trino-Transaction-Id") != "NONE" {
		t.Fatal("native transaction clear lost")
	}
	_, err = c.Run(ctx, "SELECT unknown_column", nil)
	var nativeError *QueryError
	if !errors.As(err, &nativeError) {
		t.Fatal("native terminal error lost", err)
	}
	nativeRun(t, c, "SELECT 1")
	name := "nyadb_" + fmt.Sprint(time.Now().UnixNano())
	nativeRun(t, c, "CREATE TABLE memory.default."+name+"(n BIGINT)")
	defer nativeRun(t, c, "DROP TABLE memory.default."+name)
	nativeRun(t, c, "INSERT INTO memory.default."+name+" VALUES(9007199254740993),(2)")
	rows = nativeRun(t, c, "SELECT n FROM memory.default."+name+" ORDER BY n")
	if len(rows) != 2 || string(rows[1][0]) != "9007199254740993" {
		t.Fatal("native write/read failed")
	}
}

func TestTrinoNativeCancellationAcknowledgement(t *testing.T) {
	cfg, password := nativeConfig(t)
	c, err := Open(context.Background(), cfg, password)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	_, err = c.Run(ctx, "SELECT COUNT(*) FROM tpch.sf100.lineitem a CROSS JOIN tpch.sf100.lineitem b", nil)
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || !interrupted.CancelAcknowledged || !errors.Is(err, context.DeadlineExceeded) || interrupted.QueryID == "" {
		t.Fatal("native cancellation was not acknowledged", err)
	}
	if _, err = c.Run(context.Background(), "SELECT 1", nil); !errors.Is(err, ErrLost) {
		t.Fatal("cancelled session resumed implicitly", err)
	}
	observer, err := Open(context.Background(), cfg, password)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	rows := nativeRun(t, observer, "SELECT state FROM system.runtime.queries WHERE query_id='"+interrupted.QueryID+"'")
	if len(rows) != 1 || string(rows[0][0]) != `"FAILED"` {
		t.Fatal("coordinator did not terminate original query", rows)
	}
}
