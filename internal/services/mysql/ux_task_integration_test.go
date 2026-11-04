package mysql

import (
	"context"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/connections"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMySQLGroupedTaskFixture(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_CORE_IT") != "1" {
		t.Skip("requires explicitly enabled disposable core fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_MYSQL_CORE_PORT"), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m := NewMySQLSessionManager()
	t.Cleanup(func() { m.Close() })
	const id int64 = 918
	err = m.Connect(ctx, connections.ActiveSettings{ProfileID: id, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_core", Database: "nyadb_core_it", MySQLAdvanced: connections.DefaultMySQLAdvanced()}, "")
	if err != nil {
		t.Fatal(err)
	}
	token, err := m.ReserveSQLTabToken(id, 99)
	if err != nil {
		t.Fatal(err)
	}
	defer m.ReleaseSQLTabToken(token)
	_, err = m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "CREATE TABLE ux_grouped(id INT PRIMARY KEY,a INT,b INT,c INT); INSERT INTO ux_grouped VALUES(1,1,0,1),(2,0,1,1),(3,1,1,0),(4,0,0,1)")
	if err != nil {
		t.Fatal(err)
	}
	leaf := func(col string) contract.FilterExpression[contract.DataFilterRule] {
		r := contract.DataFilterRule{Column: col, Mode: "equal", Value: "1"}
		return contract.FilterExpression[contract.DataFilterRule]{Op: "rule", Rule: &r}
	}
	expr := &contract.FilterExpression[contract.DataFilterRule]{Op: "and", Children: []contract.FilterExpression[contract.DataFilterRule]{{Op: "or", Children: []contract.FilterExpression[contract.DataFilterRule]{leaf("a"), leaf("b")}}, leaf("c")}}
	opts := MySQLDataOptions{Condition: expr, Limit: 1, UseKeyset: true}
	first, err := m.TableDataPageWithOptions(ctx, id, "nyadb_core_it", "ux_grouped", opts)
	if err != nil || len(first.Rows) != 1 || first.Rows[0][0].Text != "1" || !first.HasMore {
		t.Fatalf("first: %+v %v", first, err)
	}
	opts.After, opts.Offset = first.NextCursor, 1
	next, err := m.TableDataPageWithOptions(ctx, id, "nyadb_core_it", "ux_grouped", opts)
	if err != nil || len(next.Rows) != 1 || next.Rows[0][0].Text != "2" || next.HasMore {
		t.Fatalf("next: %+v %v", next, err)
	}
	opts.Condition = contract.AndFilters([]contract.DataFilterRule{{Column: "c", Mode: "equal", Value: "0"}})
	if _, err = m.TableDataPageWithOptions(ctx, id, "nyadb_core_it", "ux_grouped", opts); err == nil {
		t.Fatal("old cursor accepted changed expression")
	}
	opts = MySQLDataOptions{Condition: expr}
	stats, err := m.AnalyzeMySQLData(ctx, id, "nyadb_core_it", "ux_grouped", opts, []string{"id"})
	if err != nil || stats.Rows != 2 {
		t.Fatal("grouped analysis", stats, err)
	}
	ids := []string{}
	err = m.ExportMySQLData(ctx, id, "nyadb_core_it", "ux_grouped", opts, []string{"id"}, func(row []MySQLDataValue) error { ids = append(ids, row[0].Text); return nil })
	if err != nil || fmt.Sprint(ids) != "[1 2]" {
		t.Fatal("grouped export", ids, err)
	}
	sql, err := queryplan.Build("mysql", "SELECT * FROM ux_grouped WHERE a=1")
	if err != nil || strings.Contains(sql, "ANALYZE") {
		t.Fatal(sql, err)
	}
	result, err := m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", sql)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := queryplan.Parse("mysql", result)
	if err != nil || len(plan.Nodes) == 0 {
		t.Fatal("real JSON plan", plan, err)
	}
}
