package mysql

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// This opt-in test uses only newly named targets in the dedicated loopback
// fixture. Sources are regenerated deterministically and never retain all rows.
func TestMySQLDataImportStreamBatchIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_IMPORT_IT") != "1" {
		t.Skip("requires dedicated disposable loopback import fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_IMPORT_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	const database = "nyadb_toolbar_tx_it"
	m := NewMySQLSessionManager()
	t.Cleanup(func() { m.Close() })
	settings := connections.ActiveSettings{ProfileID: 2026103096, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_toolbar_tx", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if err := m.Connect(ctx, settings, os.Getenv("NYADB_MYSQL_IMPORT_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	db := m.sessions[settings.ProfileID].db
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var originalMode string
	var originalFK int
	if err := db.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode,@@SESSION.foreign_key_checks").Scan(&originalMode, &originalFK); err != nil {
		t.Fatal(err)
	}
	makeTarget := func(t *testing.T, suffix, definition string) (string, string) {
		t.Helper()
		table := fmt.Sprintf("import_probe_stream_%s_%d", suffix, time.Now().UnixNano())
		target, err := mysqlEditTable(database, table)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "CREATE TABLE "+target+" ("+definition+") ENGINE=InnoDB"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			if _, err := db.ExecContext(cleanup, "DROP TABLE IF EXISTS "+target); err != nil {
				t.Error(err)
			}
		})
		return table, target
	}
	row := func(id, note string) MySQLDataEdit {
		return MySQLDataEdit{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: id}, "note": {Text: note}}}
	}
	for _, mode := range []string{"append", "update", "upsert", "insert_missing", "delete", "replace"} {
		t.Run(mode, func(t *testing.T) {
			table, target := makeTarget(t, mode, "id BIGINT PRIMARY KEY,note VARCHAR(80) NULL")
			if _, err := db.ExecContext(ctx, "INSERT INTO "+target+" VALUES (1,'old1'),(2,'old2')"); err != nil {
				t.Fatal(err)
			}
			input := []MySQLDataEdit{row("1", "updated"), row("3", "new")}
			if mode == "append" {
				input = []MySQLDataEdit{row("3", "new"), row("4", "four")}
			}
			request := streamRequest(len(input), func(i int) MySQLDataEdit { return input[i-1] })
			request.ExtendedInsert = true
			request.Request.Mode = mode
			if mysqlDataImportNeedsKey(mode) {
				request.Request.MatchColumns = []string{"id"}
			}
			result, err := m.ImportMySQLDataStream(ctx, settings.ProfileID, database, table, request)
			if err != nil || !result.Committed || result.Progress.Processed != 2 {
				t.Fatal(result, err)
			}
			want := map[string]string{"append": "1:old1,2:old2,3:new,4:four", "update": "1:updated,2:old2", "upsert": "1:updated,2:old2,3:new", "insert_missing": "1:old1,2:old2,3:new", "delete": "2:old2", "replace": "1:updated,3:new"}[mode]
			var actual string
			if err := db.QueryRowContext(ctx, "SELECT COALESCE(GROUP_CONCAT(CONCAT(id,':',note) ORDER BY id),'') FROM "+target).Scan(&actual); err != nil || actual != want {
				t.Fatal("external SQL disagrees", actual, want, err)
			}
		})
	}
	t.Run("continue duplicate group and exact values", func(t *testing.T) {
		table, target := makeTarget(t, "exact", "id BIGINT UNSIGNED PRIMARY KEY,amount DECIMAL(65,30) NULL,note VARCHAR(80) NULL,payload VARBINARY(40) NULL")
		amount := "12345678901234567890123456789012345.123456789012345678901234567890"
		input := []MySQLDataEdit{
			{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "18446744073709551615"}, "amount": {Text: amount}, "note": {Text: "中文\nline"}, "payload": {Binary: true, Bytes: []byte{0, 255}}}},
			{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "18446744073709551615"}, "amount": {Null: true}, "note": {Text: "duplicate"}, "payload": {Null: true}}},
			{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "1"}, "amount": {Null: true}, "note": {Text: ""}, "payload": {Binary: true}}},
			{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "2"}, "amount": {Null: true}, "note": {Null: true}, "payload": {Null: true}}},
		}
		request := streamRequest(len(input), func(i int) MySQLDataEdit { return input[i-1] })
		request.ExtendedInsert, request.Request.ContinueOnError = true, true
		records := 0
		request.Record = func(r MySQLDataImportRowResult) error {
			records++
			if r.Row != records {
				return ErrMySQLDataPage
			}
			return nil
		}
		result, err := m.ImportMySQLDataStream(ctx, settings.ProfileID, database, table, request)
		if err != nil || !result.Committed || result.Progress.Prepared != 3 || result.Progress.Failed != 1 || records != 4 {
			t.Fatal(result, err, records)
		}
		var count, emptyText, nullText, emptyBlob, nullBlob int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*),SUM(note=''),SUM(note IS NULL),SUM(payload=X''),SUM(payload IS NULL) FROM "+target).Scan(&count, &emptyText, &nullText, &emptyBlob, &nullBlob); err != nil || count != 3 || emptyText != 1 || nullText != 1 || emptyBlob != 1 || nullBlob != 1 {
			t.Fatal("NULL/empty semantics changed", count, emptyText, nullText, emptyBlob, nullBlob, err)
		}
		var id, value, note, payload string
		if err := db.QueryRowContext(ctx, "SELECT CAST(id AS CHAR),CAST(amount AS CHAR),note,HEX(payload) FROM "+target+" WHERE id=18446744073709551615").Scan(&id, &value, &note, &payload); err != nil || id != "18446744073709551615" || value != amount || note != "中文\nline" || payload != "00FF" {
			t.Fatal("exact values changed", id, value, note, payload, err)
		}
	})
	t.Run("large source and cross batch replace rollback", func(t *testing.T) {
		table, target := makeTarget(t, "large", "id BIGINT PRIMARY KEY,note TEXT NULL")
		const total = 10001
		text := strings.Repeat("x", 2048)
		request := streamRequest(total, func(i int) MySQLDataEdit { return row(strconv.Itoa(i), text) })
		request.ExtendedInsert, request.StatementMaxBytes = true, 128*1024
		records := 0
		request.Record = func(r MySQLDataImportRowResult) error {
			records++
			if r.Row != records {
				return ErrMySQLDataPage
			}
			return nil
		}
		result, err := m.ImportMySQLDataStream(ctx, settings.ProfileID, database, table, request)
		if err != nil || !result.Committed || result.Progress.Prepared != total || records != total || len(result.Rows) != 100 {
			t.Fatal("legacy total cap or unbounded receipt", result, err, records)
		}
		var count, bytes int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*),SUM(OCTET_LENGTH(note)) FROM "+target).Scan(&count, &bytes); err != nil || count != total || bytes != total*2048 {
			t.Fatal("large source incomplete", count, bytes, err)
		}
		request.Request.Mode = "replace"
		request.Record = nil
		request.Open = streamRequest(total, func(i int) MySQLDataEdit {
			if i == total {
				return row("1", "duplicate")
			}
			return row(strconv.Itoa(i), "replacement")
		}).Open
		result, err = m.ImportMySQLDataStream(ctx, settings.ProfileID, database, table, request)
		if err == nil || result.Committed || result.CommitUncertain || result.Progress.Failed == 0 {
			t.Fatal("cross batch stop did not rollback", result, err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*),SUM(OCTET_LENGTH(note)) FROM "+target).Scan(&count, &bytes); err != nil || count != total || bytes != total*2048 {
			t.Fatal("replace rollback lost original rows", count, bytes, err)
		}
	})
	t.Run("foreign keys ignored only on owned connection", func(t *testing.T) {
		_, parent := makeTarget(t, "fkparent", "id BIGINT PRIMARY KEY")
		table, target := makeTarget(t, "fkchild", "id BIGINT PRIMARY KEY,note VARCHAR(80),FOREIGN KEY(id) REFERENCES "+parent+"(id)")
		request := streamRequest(2, func(i int) MySQLDataEdit { return row(strconv.Itoa(i), "orphan") })
		request.ExtendedInsert, request.IgnoreForeignKeys = true, true
		result, err := m.ImportMySQLDataStream(ctx, settings.ProfileID, database, table, request)
		if err != nil || !result.Committed || result.Progress.Prepared != 2 {
			t.Fatal(result, err)
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+target).Scan(&count); err != nil || count != 2 {
			t.Fatal(count, err)
		}
		var mode string
		var fk int
		if err := db.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode,@@SESSION.foreign_key_checks").Scan(&mode, &fk); err != nil || !strings.EqualFold(mode, originalMode) || fk != originalFK {
			t.Fatal("session settings leaked", mode, fk, err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO "+target+" VALUES (3,'must fail')"); err == nil {
			t.Fatal("foreign keys still disabled")
		}
	})
}
