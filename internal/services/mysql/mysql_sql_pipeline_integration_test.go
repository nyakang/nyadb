package mysql

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSQLPipelineDisposableTypedMultiResultsAndFullStream(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_CORE_IT") != "1" {
		t.Skip("requires disposable core fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_MYSQL_CORE_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("invalid loopback fixture port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m := NewMySQLSessionManager()
	t.Cleanup(func() { m.Close() })
	if e = m.Connect(ctx, connections.ActiveSettings{ProfileID: 902, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_core", Database: "nyadb_core_it", MySQLAdvanced: connections.DefaultMySQLAdvanced()}, ""); e != nil {
		t.Fatal(e)
	}
	token, e := m.ReserveSQLTabToken(902, 44)
	if e != nil {
		t.Fatal(e)
	}
	defer m.ReleaseSQLTabToken(token)
	r, e := m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "SELECT NULL AS n, 'NULL' AS literal, '' AS `empty`, CAST('' AS BINARY) AS empty_binary, X'00ff' AS bytes, CAST('9007199254740993.0001' AS DECIMAL(30,4)) AS exact; SELECT 2;")
	if e != nil || len(r.Sets) != 2 {
		t.Fatalf("multi results: %+v %v", r, e)
	}
	row := r.Sets[0].Rows[0]
	if row[0].Kind != contract.Null || row[1].Kind != contract.Text || row[1].Text != "NULL" || row[2].Text != "" || row[3].Kind != contract.Binary || len(row[3].Bytes) != 0 || row[4].Display() != "0x00ff" || row[5].Text != "9007199254740993.0001" {
		t.Fatalf("typed values lost: %+v", row)
	}
	full := 0
	r, e = m.ExecuteSQLTabTokenStream(ctx, token, "nyadb_core_it", "SET SESSION cte_max_recursion_depth=3000; WITH RECURSIVE seq AS (SELECT 1 AS n UNION ALL SELECT n+1 FROM seq WHERE n<2000) SELECT n FROM seq; SELECT NULL;", func(_ int, _ []contract.Column, row []contract.Cell) error {
		if row != nil {
			full++
		}
		return nil
	})
	if e != nil || full != 2001 || !r.Truncated || len(r.Sets) != 3 || len(r.Sets[1].Rows) != 1000 {
		t.Fatalf("full stream/preview cap: rows=%d %+v %v", full, r, e)
	}
	r, e = m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "DELIMITER $$\nCREATE PROCEDURE `core_multi`() BEGIN SELECT 'a;$$' AS a; SELECT 2 AS b; END$$\nDELIMITER ;\nCALL core_multi(); SELECT 3;")
	if e != nil || len(r.Sets) < 4 {
		t.Fatalf("delimiter/multi CALL: %+v %v", r, e)
	}
	r, e = m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "CREATE TABLE `script_stop` (`id` int); INSERT INTO script_stop VALUES (1); SELECT absent FROM script_stop; INSERT INTO script_stop VALUES (2);")
	if e == nil || len(r.Steps) != 3 || r.Steps[1].State != "executed" || r.Steps[2].State != "not_executed" {
		t.Fatalf("script stop/reports: %+v %v", r, e)
	}
	check, e := m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "SELECT COUNT(*) FROM script_stop")
	if e != nil || check.Rows[0][0] != "1" {
		t.Fatal("failed script continued or replayed")
	}
	check, e = m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "WITH ids AS (SELECT 1 AS id) UPDATE script_stop JOIN ids USING(id) SET script_stop.id=2")
	if e != nil || check.AffectedRows != 1 {
		t.Fatalf("CTE UPDATE lost affected rows: %+v %v", check, e)
	}
	check, e = m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "WITH ids AS (SELECT 2 AS id) DELETE FROM script_stop WHERE id IN (SELECT id FROM ids)")
	if e != nil || check.AffectedRows != 1 {
		t.Fatalf("CTE DELETE lost affected rows: %+v %v", check, e)
	}
	if strings.Contains(strings.Join([]string{r.Steps[0].State, r.Steps[1].State}, ""), "rollback") {
		t.Fatal("DDL rollback falsely claimed")
	}
}
