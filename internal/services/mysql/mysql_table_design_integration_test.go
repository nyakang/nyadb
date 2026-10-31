package mysql

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestTableDesignDisposableMySQLRoundTrip(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_CORE_IT") != "1" {
		t.Skip("requires tools/verify_mysql_core_fixture.sh disposable loopback fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_MYSQL_CORE_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("invalid disposable loopback port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m := NewMySQLSessionManager()
	t.Cleanup(func() { m.Close() })
	settings := connections.ActiveSettings{ProfileID: 901, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_core", Database: "nyadb_core_it", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	if e = m.Connect(ctx, settings, ""); e != nil {
		t.Fatal(e)
	}
	db, name := "nyadb_core_it", "design_roundtrip"
	table, e := mysqlddl.Parse(db, name, "CREATE TABLE `design_roundtrip` (`id` bigint NOT NULL AUTO_INCREMENT, `value` varchar(100) DEFAULT 'Case', `amount` decimal(65,30) NOT NULL DEFAULT '9007199254740993.0000000001', `next_id` bigint GENERATED ALWAYS AS ((`id` + 1)) STORED, PRIMARY KEY (`id`), KEY `idx_value` (`value`(12))) ENGINE=InnoDB")
	if e != nil {
		t.Fatal(e)
	}
	// Generated columns cannot depend on auto_increment columns in MySQL.
	table.Clauses[3].SQL = "`next_id` decimal(65,30) GENERATED ALWAYS AS ((`amount` + 1)) STORED"
	original := mysqlddl.Snapshot{Database: db, Table: name, RuntimeGeneration: m.SessionGeneration(901)}
	plan, e := mysqlddl.Build(original, mysqlddl.Draft{Table: table, New: true})
	if e != nil {
		t.Fatal(e)
	}
	out := m.ApplyTableDesign(ctx, 901, plan)
	if out.Error != nil || !out.Verified {
		t.Fatalf("create round trip: %+v", out)
	}
	original, e = m.TableDesign(ctx, 901, db, name)
	if e != nil {
		t.Fatal(e)
	}
	table, e = mysqlddl.Parse(db, name, original.DDL)
	if e != nil {
		t.Fatal(e)
	}
	field, e := mysqlddl.ParseColumn(table.Clauses[1].SQL)
	if e != nil {
		t.Fatal(e)
	}
	field.Type = "varchar(150)"
	field.Comment = "roundtrip 'comment'"
	table.Clauses[1].SQL, e = field.SQL()
	if e != nil {
		t.Fatal(e)
	}
	draft := mysqlddl.Draft{Table: table, Triggers: []mysqlddl.Trigger{{Name: "core_before", SQL: "CREATE TRIGGER `core_before` BEFORE INSERT ON `design_roundtrip` FOR EACH ROW SET NEW.`value`=COALESCE(NEW.`value`,'default')"}}}
	plan, e = mysqlddl.Build(original, draft)
	if e != nil {
		t.Fatal(e)
	}
	out = m.ApplyTableDesign(ctx, 901, plan)
	if out.Error != nil || !out.Verified {
		t.Fatalf("alter/trigger round trip: %+v", out)
	}
	original = *out.Actual
	table, e = mysqlddl.Parse(db, name, original.DDL)
	if e != nil {
		t.Fatal(e)
	}
	table.Options, e = mysqlddl.SetOption(table.Options, "COMMENT", "designer")
	if e != nil {
		t.Fatal(e)
	}
	plan, e = mysqlddl.Build(original, mysqlddl.Draft{Table: table, Triggers: original.Triggers})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.ExecuteSQL(ctx, 901, db, "ALTER TABLE `nyadb_core_it`.`design_roundtrip` COMMENT='external'"); e != nil {
		t.Fatal(e)
	}
	out = m.ApplyTableDesign(ctx, 901, plan)
	if out.State != "not_executed" || out.Error == nil || !strings.Contains(out.Error.Error(), "externally") {
		t.Fatalf("external conflict not blocked: %+v", out)
	}
}
