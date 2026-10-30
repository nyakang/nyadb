package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// This opt-in test creates only import_probe_modes_* targets in the existing
// dedicated loopback fixture. Every case drops its target before pool closure.
func TestMySQLDataImportModesIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_IMPORT_IT") != "1" {
		t.Skip("requires dedicated disposable loopback import fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_IMPORT_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	const database = "nyadb_toolbar_tx_it"
	m := NewMySQLSessionManager()
	defer m.Close()
	settings := connections.ActiveSettings{ProfileID: 2026103095, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_toolbar_tx", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	if err = m.Connect(ctx, settings, os.Getenv("NYADB_MYSQL_IMPORT_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	db := m.sessions[settings.ProfileID].db
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var originalMode string
	if err = db.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&originalMode); err != nil {
		t.Fatal(err)
	}
	makeTarget := func(t *testing.T, suffix string) (string, string) {
		t.Helper()
		table := fmt.Sprintf("import_probe_modes_%s_%d", suffix, time.Now().UnixNano())
		target, err := mysqlEditTable(database, table)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := db.ExecContext(cleanup, "DROP TABLE IF EXISTS "+target); err != nil {
				t.Error(err)
			}
		})
		return table, target
	}
	makeExisting := func(t *testing.T, suffix string) (string, string) {
		t.Helper()
		table, target := makeTarget(t, suffix)
		if _, err := db.ExecContext(ctx, "CREATE TABLE "+target+" (id BIGINT PRIMARY KEY,code VARCHAR(40) NOT NULL UNIQUE,note VARCHAR(80) NULL,payload VARBINARY(40) NULL) ENGINE=InnoDB"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO "+target+" VALUES (1,'a','old1',X''),(2,'b','old2',NULL)"); err != nil {
			t.Fatal(err)
		}
		return table, target
	}
	row := func(id, code, note string) MySQLDataEdit {
		return MySQLDataEdit{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: id}, "code": {Text: code}, "note": {Text: note}, "payload": {Binary: true}}}
	}
	for _, mode := range []string{"append", "update", "upsert", "insert_missing", "delete", "replace"} {
		t.Run(mode, func(t *testing.T) {
			table, target := makeExisting(t, mode)
			input := []MySQLDataEdit{row("1", "a", "updated"), row("3", "c", "new")}
			if mode == "append" {
				input = []MySQLDataEdit{row("3", "c", "new"), row("4", "d", "four")}
			}
			request := MySQLDataImportRequest{Mode: mode, Edits: input}
			if mysqlDataImportNeedsKey(mode) {
				request.MatchColumns = []string{"id"}
			}
			result, err := m.ImportMySQLData(ctx, settings.ProfileID, database, table, request)
			if err != nil || result == nil || !result.Committed || result.Created || result.CommitUncertain || result.CreationUncertain {
				t.Fatal("mode did not commit", mode, result, err)
			}
			var ids, notes string
			if err := db.QueryRowContext(ctx, "SELECT COALESCE(GROUP_CONCAT(id ORDER BY id),''),COALESCE(GROUP_CONCAT(note ORDER BY id),'') FROM "+target).Scan(&ids, &notes); err != nil {
				t.Fatal(err)
			}
			wantIDs, wantNotes, kinds := "", "", []string{}
			switch mode {
			case "append":
				wantIDs, wantNotes, kinds = "1,2,3,4", "old1,old2,new,four", []string{"insert", "insert"}
			case "update":
				wantIDs, wantNotes, kinds = "1,2", "updated,old2", []string{"update", "skip"}
			case "upsert":
				wantIDs, wantNotes, kinds = "1,2,3", "updated,old2,new", []string{"update", "insert"}
			case "insert_missing":
				wantIDs, wantNotes, kinds = "1,2,3", "old1,old2,new", []string{"skip", "insert"}
			case "delete":
				wantIDs, wantNotes, kinds = "2", "old2", []string{"delete", "skip"}
			case "replace":
				wantIDs, wantNotes, kinds = "1,3", "updated,new", []string{"insert", "insert"}
			}
			if ids != wantIDs || notes != wantNotes || len(result.Rows) != 2 {
				t.Fatalf("%s: committed different rows: %q %q %+v", mode, ids, notes, result)
			}
			for i, kind := range kinds {
				wantStatus := "committed"
				if kind == "skip" {
					wantStatus = "skipped"
				}
				if result.Rows[i].Row != i+1 || result.Rows[i].Kind != kind || result.Rows[i].Status != wantStatus {
					t.Fatal("misreported actual row", result.Rows[i])
				}
			}
		})
	}
	t.Run("noop update and complete unique key", func(t *testing.T) {
		table, target := makeExisting(t, "noop")
		request := MySQLDataImportRequest{Mode: "update", MatchColumns: []string{"code"}, Edits: []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"code": {Text: "a"}, "note": {Text: "old1"}}}}}
		result, err := m.ImportMySQLData(ctx, settings.ProfileID, database, table, request)
		if err != nil || !result.Committed || result.Rows[0].Kind != "update" || result.Rows[0].Status != "committed" || len(result.Saved.Outcomes) != 1 {
			t.Fatal("noop incorrectly treated as unmatched", result, err)
		}
		request.MatchColumns = []string{"note"}
		result, err = m.ImportMySQLData(ctx, settings.ProfileID, database, table, request)
		if !errors.Is(err, ErrMySQLDataReadOnly) || result.Committed {
			t.Fatal("non-key matching accepted", result, err)
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+target).Scan(&count); err != nil || count != 2 {
			t.Fatal("invalid key changed target", count, err)
		}
	})
	t.Run("other unique collision cannot update unrelated record", func(t *testing.T) {
		table, target := makeExisting(t, "collision")
		request := MySQLDataImportRequest{Mode: "upsert", MatchColumns: []string{"id"}, ContinueOnError: true, Edits: []MySQLDataEdit{row("3", "a", "wrong record"), row("4", "d", "good")}}
		result, err := m.ImportMySQLData(ctx, settings.ProfileID, database, table, request)
		if err != nil || !result.Committed || result.Rows[0].Status != "failed" || result.Rows[0].Error != "MySQL error 1062" || result.Rows[1].Status != "committed" || len(result.Saved.Outcomes) != 1 {
			t.Fatal("unrelated unique collision continued incorrectly", result, err)
		}
		var notes string
		if err := db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(note ORDER BY id) FROM "+target).Scan(&notes); err != nil || notes != "old1,old2,good" {
			t.Fatal("collision overwrote unrelated row", notes, err)
		}
	})
	t.Run("replace failure rolls back original rows", func(t *testing.T) {
		table, target := makeExisting(t, "replacefail")
		result, err := m.ImportMySQLData(ctx, settings.ProfileID, database, table, MySQLDataImportRequest{Mode: "replace", Edits: []MySQLDataEdit{row("3", "c", "first"), row("3", "d", "duplicate")}})
		if err == nil || result.Committed || result.CommitUncertain || result.Saved != nil || result.Rows[0].Status != "rolled_back" || result.Rows[1].Status != "failed" {
			t.Fatal("replace failure was not atomic", result, err)
		}
		var notes string
		if err := db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(note ORDER BY id) FROM "+target).Scan(&notes); err != nil || notes != "old1,old2" {
			t.Fatal("replace used implicit-commit truncation", notes, err)
		}
	})
	t.Run("new table continues invalid and duplicate rows", func(t *testing.T) {
		table, target := makeTarget(t, "newcontinue")
		request := MySQLDataImportRequest{NewTable: true, ContinueOnError: true, Columns: []MySQLDataImportColumn{{Name: "id", Type: "BIGINT", PrimaryKey: true}, {Name: "note", Type: "VARCHAR(2)", Nullable: true}}, Edits: []MySQLDataEdit{
			{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "1"}, "note": {Text: "中"}}},
			{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "2"}, "note": {Text: "long"}}},
			{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "1"}, "note": {Text: "x"}}},
			{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "3"}, "note": {Null: true}}},
		}}
		result, err := m.ImportMySQLData(ctx, settings.ProfileID, database, table, request)
		if err != nil || !result.Created || !result.Committed || len(result.Saved.Outcomes) != 2 || result.Rows[1].Status != "failed" || result.Rows[2].Status != "failed" {
			t.Fatal("new table row continuation failed", result, err)
		}
		var count, nulls int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*),SUM(note IS NULL) FROM "+target).Scan(&count, &nulls); err != nil || count != 2 || nulls != 1 {
			t.Fatal("new table values changed", count, nulls, err)
		}
	})
	t.Run("large append exceeds editor cap", func(t *testing.T) {
		table, target := makeTarget(t, "large")
		if _, err := db.ExecContext(ctx, "CREATE TABLE "+target+" (note VARCHAR(8) NULL) ENGINE=InnoDB"); err != nil {
			t.Fatal(err)
		}
		request := MySQLDataImportRequest{Edits: make([]MySQLDataEdit, 201)}
		for i := range request.Edits {
			request.Edits[i] = dataImportModesRows(strconv.Itoa(i))[0]
		}
		result, err := m.ImportMySQLData(ctx, settings.ProfileID, database, table, request)
		if err != nil || !result.Committed || len(result.Saved.Outcomes) != 201 {
			t.Fatal("larger import did not finish", result, err)
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+target).Scan(&count); err != nil || count != 201 {
			t.Fatal("larger import lost rows", count, err)
		}
	})
	var restoredMode string
	if err := db.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&restoredMode); err != nil || !strings.EqualFold(restoredMode, originalMode) {
		t.Fatal("import leaked physical connection sql_mode", originalMode, restoredMode, err)
	}
}
