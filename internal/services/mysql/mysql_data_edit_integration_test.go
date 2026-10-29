package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// Opt in only against a disposable loopback MySQL 8.x server. Provision
// data_probe (ALL), data_readonly (SELECT) on the requested database. This test
// creates only uniquely named tables and drops them on exit.
func TestMySQLDataEditingIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_DATA_EDIT_IT") != "1" {
		t.Skip("requires disposable loopback MySQL edit fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_MYSQL_DATA_EDIT_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	database := os.Getenv("NYADB_MYSQL_DATA_EDIT_DATABASE")
	if database == "" {
		database = "table_edit_it"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	m := NewMySQLSessionManager()
	t.Cleanup(func() { _ = m.Close() })
	for i, user := range []string{"data_probe", "data_readonly"} {
		if e = m.Connect(ctx, connections.ActiveSettings{ProfileID: int64(i + 1), Host: "127.0.0.1", Port: uint16(port), Username: user, MySQLAdvanced: connections.DefaultMySQLAdvanced()}, os.Getenv("NYADB_MYSQL_DATA_EDIT_PASSWORD")); e != nil {
			t.Fatal(e)
		}
	}
	db := m.sessions[1].db
	prefix := fmt.Sprintf("edit_%d_", time.Now().UnixNano())
	qualified := func(table string) string {
		v, e := mysqlEditTable(database, table)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	table := prefix + "records"
	noKey := prefix + "nokey"
	huge := prefix + "huge"
	for _, name := range []string{table, noKey, huge, prefix + "composite", prefix + "normalized", prefix + "myisam"} {
		n := name
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			if _, e := db.ExecContext(cleanup, "DROP TABLE IF EXISTS "+qualified(n)); e != nil {
				t.Error(e)
			}
		})
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.ExecContext(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec("CREATE TABLE " + qualified(table) + " (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, note VARCHAR(80) NULL, n DECIMAL(30,9) NOT NULL DEFAULT 0, j JSON, payload BLOB, detail TEXT, generated_n DECIMAL(30,9) GENERATED ALWAYS AS (n * 2) STORED, CHECK(n>=0)) ENGINE=InnoDB AUTO_INCREMENT=41")
	exec("INSERT INTO " + qualified(table) + " (note,n,j,payload,detail) VALUES ('alpha',1.123456789,'{\"a\":1}',X'0001',REPEAT('长',4000)),('beta',2,NULL,NULL,'')")
	page := func(id int64) *MySQLDataPage {
		t.Helper()
		p, e := m.TableDataPageWithOptions(ctx, id, database, table, MySQLDataOptions{Limit: 100, SortColumn: "note", UseKeyset: true})
		if e != nil {
			t.Fatal(e)
		}
		if p.KeysetAvailable {
			t.Fatal("nullable sort enabled keyset")
		}
		return p
	}
	snapshot := func(id int64, row int) *MySQLRowSnapshot { t.Helper(); return page(id).RowSnapshots[row] }
	save := func(edits ...MySQLDataEdit) *MySQLDataEditResult {
		t.Helper()
		r, e := m.ApplyMySQLDataEdits(ctx, 1, database, table, edits)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	scalar := func(id int64, col string) any {
		t.Helper()
		var v any
		q, _ := quotedMySQLIdentifier(col)
		if e := db.QueryRowContext(ctx, "SELECT "+q+" FROM "+qualified(table)+" WHERE id=?", id).Scan(&v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	t.Run("complete long and binary read", func(t *testing.T) {
		p := page(1)
		if !p.Rows[0][5].Truncated {
			t.Fatal("missing preview truncation")
		}
		row, e := m.ReadMySQLDataRow(ctx, 1, database, table, *p.RowRefs[0])
		if e != nil {
			t.Fatal(e)
		}
		if len([]rune(row.Values["detail"].Text)) != 4000 || len(row.Values["payload"].Bytes) != 2 || !row.Values["payload"].Binary {
			t.Fatalf("complete values lost")
		}
	})
	t.Run("null empty precision and defaults", func(t *testing.T) {
		save(MySQLDataEdit{Kind: "update", Original: snapshot(1, 0), Values: map[string]MySQLDataValue{"note": {Null: true}, "n": {Text: "9007199254740993.123456789"}}})
		if scalar(41, "note") != nil || string(mysqlEditRawBytes(scalar(41, "n"))) != "9007199254740993.123456789" {
			t.Fatal("NULL or numeric precision lost")
		}
		// NULL sorts first, so row zero still identifies id 41.
		saved := save(MySQLDataEdit{Kind: "update", Original: snapshot(1, 0), Values: map[string]MySQLDataValue{"note": {Text: ""}, "n": {Default: true}}})
		if len(saved.Outcomes[0].Previews) != 7 || saved.Outcomes[0].Previews[1].Text != "" || saved.Outcomes[0].Previews[1].Null || saved.Outcomes[0].Previews[2].Text != "0.000000000" || saved.Outcomes[0].Previews[6].Text != "0.000000000" || !saved.Outcomes[0].Previews[5].Truncated {
			t.Fatalf("saved default/generated/long previews: %+v", saved.Outcomes[0].Previews)
		}
		if string(mysqlEditRawBytes(scalar(41, "note"))) != "" || string(mysqlEditRawBytes(scalar(41, "n"))) != "0.000000000" {
			t.Fatal("empty/default failed")
		}
	})
	t.Run("autoincrement insert and delete", func(t *testing.T) {
		r := save(MySQLDataEdit{Kind: "insert", Values: map[string]MySQLDataValue{"note": {Text: "inserted"}}})
		if r.Outcomes[0].GeneratedID != 43 || r.Outcomes[0].Saved == nil || len(r.Outcomes[0].Previews) != 7 || r.Outcomes[0].Previews[0].Text != "43" || r.Outcomes[0].Previews[1].Text != "inserted" || !r.Outcomes[0].Previews[4].Null {
			t.Fatalf("generated identity missing %+v", r)
		}
		verification, e := m.VerifyMySQLDataEdits(ctx, 1, database, table, r)
		if e != nil || verification.Status != "applied" {
			t.Fatalf("verification %v %v", verification, e)
		}
		save(MySQLDataEdit{Kind: "delete", Original: r.Outcomes[0].Saved})
		var count int
		db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(table)+" WHERE id=43").Scan(&count)
		if count != 0 {
			t.Fatal("delete not committed")
		}
	})
	t.Run("constraint rejection rolls whole batch back", func(t *testing.T) {
		p := page(1)
		_, e := m.ApplyMySQLDataEdits(ctx, 1, database, table, []MySQLDataEdit{{Kind: "update", Original: p.RowSnapshots[0], Values: map[string]MySQLDataValue{"note": {Text: "rollback"}}}, {Kind: "update", Original: p.RowSnapshots[1], Values: map[string]MySQLDataValue{"n": {Text: "-1"}}}})
		if e == nil || string(mysqlEditRawBytes(scalar(41, "note"))) != "" {
			t.Fatalf("batch was partially applied %v", e)
		}
	})
	t.Run("precision rounding and generated writes rejected", func(t *testing.T) {
		for _, values := range []map[string]MySQLDataValue{{"n": {Text: "1.1234567895"}}, {"generated_n": {Text: "2"}}} {
			if _, e := m.ApplyMySQLDataEdits(ctx, 1, database, table, []MySQLDataEdit{{Kind: "update", Original: snapshot(1, 0), Values: values}}); e == nil {
				t.Fatal("lossy/generated update accepted")
			}
		}
	})
	t.Run("readonly account", func(t *testing.T) {
		if _, e := m.ApplyMySQLDataEdits(ctx, 2, database, table, []MySQLDataEdit{{Kind: "update", Original: snapshot(2, 0), Values: map[string]MySQLDataValue{"note": {Text: "forbidden"}}}}); e == nil {
			t.Fatal("readonly write accepted")
		}
	})
	t.Run("concurrent change in unedited long field", func(t *testing.T) {
		original := snapshot(1, 0)
		exec("UPDATE " + qualified(table) + " SET detail='other writer' WHERE id=41")
		if _, e := m.ApplyMySQLDataEdits(ctx, 1, database, table, []MySQLDataEdit{{Kind: "update", Original: original, Values: map[string]MySQLDataValue{"note": {Text: "stale"}}}}); !errors.Is(e, ErrMySQLDataConflict) {
			t.Fatal(e)
		}
	})
	t.Run("cancellation while record locked", func(t *testing.T) {
		original := snapshot(1, 0)
		lock, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer lock.Rollback()
		if _, e = lock.ExecContext(ctx, "UPDATE "+qualified(table)+" SET note=note WHERE id=41"); e != nil {
			t.Fatal(e)
		}
		short, stop := context.WithTimeout(ctx, 150*time.Millisecond)
		defer stop()
		if _, e = m.ApplyMySQLDataEdits(short, 1, database, table, []MySQLDataEdit{{Kind: "delete", Original: original}}); e == nil {
			t.Fatal("locked delete was not canceled")
		}
		lock.Rollback()
		if _, e = m.ReadMySQLDataRow(ctx, 1, database, table, original.Ref); e != nil {
			t.Fatalf("canceled write removed record: %v", e)
		}
	})
	t.Run("no unique key and oversized explicit reads", func(t *testing.T) {
		exec("CREATE TABLE " + qualified(noKey) + " (note VARCHAR(80)) ENGINE=InnoDB")
		exec("INSERT INTO " + qualified(noKey) + " VALUES('duplicate'),('duplicate')")
		p, e := m.TableDataPage(ctx, 1, database, noKey, 0, 100)
		if e != nil {
			t.Fatal(e)
		}
		if p.RowRefs[0] != nil || p.RowSnapshots[0] != nil {
			t.Fatal("no-key record editable")
		}
		forged := &MySQLRowSnapshot{Ref: MySQLRowRef{Database: database, Table: noKey, Columns: []string{"note"}, Values: [][]byte{[]byte("duplicate")}}}
		if _, e = m.ApplyMySQLDataEdits(ctx, 1, database, noKey, []MySQLDataEdit{{Kind: "delete", Original: forged}}); !errors.Is(e, ErrMySQLDataReadOnly) {
			t.Fatal(e)
		}
		exec("CREATE TABLE " + qualified(huge) + " (id INT PRIMARY KEY, detail LONGTEXT) ENGINE=InnoDB")
		exec("INSERT INTO " + qualified(huge) + " VALUES(1,REPEAT('x',1048577))")
		p, e = m.TableDataPage(ctx, 1, database, huge, 0, 100)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = m.ReadMySQLDataRow(ctx, 1, database, huge, *p.RowRefs[0]); !errors.Is(e, ErrMySQLDataEditLimit) {
			t.Fatal(e)
		}
	})
	t.Run("complete composite unique identity and explicit keys", func(t *testing.T) {
		name := prefix + "composite"
		exec("CREATE TABLE " + qualified(name) + " (tenant INT NOT NULL, code VARCHAR(80) NOT NULL, note VARCHAR(80), UNIQUE KEY whole_key(tenant,code)) ENGINE=InnoDB")
		result, e := m.ApplyMySQLDataEdits(ctx, 1, database, name, []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"tenant": {Text: "7"}, "code": {Text: "A"}, "note": {Text: "before"}}}})
		if e != nil || result.Outcomes[0].Saved == nil || len(result.Outcomes[0].Saved.Ref.Columns) != 2 {
			t.Fatalf("composite insert identity %v %v", result, e)
		}
		p, e := m.TableDataPage(ctx, 1, database, name, 0, 100)
		if e != nil {
			t.Fatal(e)
		}
		result, e = m.ApplyMySQLDataEdits(ctx, 1, database, name, []MySQLDataEdit{{Kind: "update", Original: p.RowSnapshots[0], Values: map[string]MySQLDataValue{"code": {Text: "B"}, "note": {Text: "after"}}}})
		if e != nil || result.Outcomes[0].Saved == nil {
			t.Fatalf("composite key update %v %v", result, e)
		}
		if _, e = m.ApplyMySQLDataEdits(ctx, 1, database, name, []MySQLDataEdit{{Kind: "delete", Original: result.Outcomes[0].Saved}}); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("nontransactional engine cannot partially write", func(t *testing.T) {
		name := prefix + "myisam"
		exec("CREATE TABLE " + qualified(name) + " (id INT PRIMARY KEY, note VARCHAR(80)) ENGINE=MyISAM")
		if _, e := m.ApplyMySQLDataEdits(ctx, 1, database, name, []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "1"}}}}); !errors.Is(e, ErrMySQLDataReadOnly) {
			t.Fatal(e)
		}
	})
	t.Run("saved identity uses server normalized numeric bytes", func(t *testing.T) {
		name := prefix + "normalized"
		exec("CREATE TABLE " + qualified(name) + " (id BIGINT NOT NULL, tenant DECIMAL(8,3) NOT NULL, note VARCHAR(80), UNIQUE KEY whole_key(id,tenant)) ENGINE=InnoDB")
		result, e := m.ApplyMySQLDataEdits(ctx, 1, database, name, []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "00041"}, "tenant": {Text: "007.0"}, "note": {Text: "before"}}}})
		if e != nil || result.Outcomes[0].Saved == nil {
			t.Fatalf("normalized insert %v %v", result, e)
		}
		ref := result.Outcomes[0].Saved.Ref
		if string(ref.Values[0]) != "41" || string(ref.Values[1]) != "7.000" {
			t.Fatalf("insert identity retained user spelling: %+v", ref)
		}
		result, e = m.ApplyMySQLDataEdits(ctx, 1, database, name, []MySQLDataEdit{{Kind: "update", Original: result.Outcomes[0].Saved, Values: map[string]MySQLDataValue{"id": {Text: "00042"}, "tenant": {Text: "008"}}}})
		if e != nil || result.Outcomes[0].Saved == nil {
			t.Fatalf("normalized update %v %v", result, e)
		}
		ref = result.Outcomes[0].Saved.Ref
		if string(ref.Values[0]) != "42" || string(ref.Values[1]) != "8.000" {
			t.Fatalf("update identity retained user spelling: %+v", ref)
		}
		// The SQL clipboard compares identity bytes, so the returned key must
		// match the stored spelling as well as ordinary numeric comparison.
		var count int
		if e := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(name)+" WHERE CAST(id AS BINARY)=? AND CAST(tenant AS BINARY)=?", ref.Values[0], ref.Values[1]).Scan(&count); e != nil || count != 1 {
			t.Fatalf("saved identity does not match server bytes: count=%d error=%v", count, e)
		}
		if _, e = m.ApplyMySQLDataEdits(ctx, 1, database, name, []MySQLDataEdit{{Kind: "delete", Original: result.Outcomes[0].Saved}}); e != nil {
			t.Fatal(e)
		}
	})

	t.Run("SQL tab transaction remains independent", func(t *testing.T) {
		if _, e := m.ExecuteSQLTab(ctx, 1, 778, database, "START TRANSACTION"); e != nil {
			t.Fatal(e)
		}
		defer m.ReleaseSQLTab(1, 778)
		save(MySQLDataEdit{Kind: "update", Original: snapshot(1, 0), Values: map[string]MySQLDataValue{"note": {Text: "independent"}}})
		if _, e := m.ExecuteSQLTab(ctx, 1, 778, database, "ROLLBACK"); e != nil {
			t.Fatal(e)
		}

		if string(mysqlEditRawBytes(scalar(41, "note"))) != "independent" {
			t.Fatal("table write joined SQL tab transaction")
		}
	})
}

func TestMySQLLiteralAndComparisonFiltersIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_DATA_EDIT_IT") != "1" {
		t.Skip("requires disposable loopback MySQL edit fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_MYSQL_DATA_EDIT_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	database := os.Getenv("NYADB_MYSQL_DATA_EDIT_DATABASE")
	if database == "" {
		database = "table_edit_it"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	m := NewMySQLSessionManager()
	t.Cleanup(func() { _ = m.Close() })
	if e = m.Connect(ctx, connections.ActiveSettings{ProfileID: 1, Host: "127.0.0.1", Port: uint16(port), Username: "data_probe", MySQLAdvanced: connections.DefaultMySQLAdvanced()}, os.Getenv("NYADB_MYSQL_DATA_EDIT_PASSWORD")); e != nil {
		t.Fatal(e)
	}
	table := fmt.Sprintf("literal_filter_%d", time.Now().UnixNano())
	target, e := mysqlEditTable(database, table)
	if e != nil {
		t.Fatal(e)
	}
	db := m.sessions[1].db
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if _, e := db.ExecContext(cleanup, "DROP TABLE IF EXISTS "+target); e != nil {
			t.Error(e)
		}
	})
	if _, e = db.ExecContext(ctx, "CREATE TABLE "+target+" (id INT PRIMARY KEY, n DECIMAL(30,0), note VARCHAR(80)) ENGINE=InnoDB"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ExecContext(ctx, "INSERT INTO "+target+" VALUES (1,9007199254740993,'a%_中文'),(2,9007199254740992,'a%中文'),(3,-5,NULL)"); e != nil {
		t.Fatal(e)
	}
	for _, scenario := range []struct {
		mode, column, value string
		ids                 []string
	}{
		{"contains", "note", "%_", []string{"1"}},
		{"not_contains", "note", "%_", []string{"2"}},
		{"like", "note", "中文", []string{"1", "2"}},
		{"not_like", "note", "中文", []string{}},
		{"like", "note", "%_", []string{"1", "2"}},
		{"greater", "n", "9007199254740992", []string{"1"}},
		{"less", "n", "9007199254740993", []string{"2", "3"}},
	} {
		page, e := m.TableDataPageWithOptions(ctx, 1, database, table, MySQLDataOptions{Limit: 100, Filters: []MySQLDataFilterRule{{Column: scenario.column, Mode: scenario.mode, Value: scenario.value}}})
		if e != nil {
			t.Fatal(e)
		}
		if len(page.Rows) != len(scenario.ids) {
			t.Fatalf("%s returned %d rows, expected %v", scenario.mode, len(page.Rows), scenario.ids)
		}
		for i, id := range scenario.ids {
			if page.Rows[i][0].Text != id {
				t.Fatalf("%s row%d id=%s expected%s", scenario.mode, i, page.Rows[i][0].Text, id)
			}
		}
	}
}
