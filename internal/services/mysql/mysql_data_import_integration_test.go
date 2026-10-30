package mysql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// This opt-in test uses only the dedicated disposable loopback schema. Every
// destination belongs to this test and is dropped even after a partial import.
func TestMySQLDataImportIntegration(t *testing.T) {
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
	settings := connections.ActiveSettings{ProfileID: 2026103091, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_toolbar_tx", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Connect(ctx, settings, os.Getenv("NYADB_MYSQL_IMPORT_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	db := m.sessions[settings.ProfileID].db
	// One connection ensures the executor returns the metadata pool's original
	// sql_mode rather than leaving STRICT_ALL_TABLES behind for later requests.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var originalMode string
	if err := db.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&originalMode); err != nil {
		t.Fatal(err)
	}
	makeTarget := func(t *testing.T, suffix string) (string, string) {
		t.Helper()
		table := fmt.Sprintf("import_probe_%s_%d", suffix, time.Now().UnixNano())
		target, err := mysqlEditTable(database, table)
		if err != nil {
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
	// Cleanup must run before the deferred manager Close releases its pool.
	// Use subtests so t.Cleanup follows each case, while this manager is live.
	t.Run("exact values and existing destination", func(t *testing.T) {
		table, target := makeTarget(t, "exact")
		columns := []MySQLDataImportColumn{{Name: "id", Type: "BIGINT", PrimaryKey: true}, {Name: "amount", Type: "DECIMAL(30,10)", Nullable: true}, {Name: "note", Type: "VARCHAR(80)", Nullable: true}, {Name: "payload", Type: "BLOB", Nullable: true}, {Name: "at", Type: "DATETIME(6)", Nullable: true}}
		edits := []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "1"}, "amount": {Text: "9007199254740993.1234567890"}, "note": {Text: "中文"}, "payload": {Binary: true, Bytes: []byte{0, 0xff, 0x80}}, "at": {Text: "2026-10-30 12:34:56.123456"}}}, {Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "2"}, "amount": {Null: true}, "note": {Text: ""}, "payload": {Binary: true}, "at": {Null: true}}}, {Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "3"}, "amount": {Text: "0.0000000001"}, "note": {Null: true}, "payload": {Null: true}, "at": {Null: true}}}}
		result, err := m.ImportMySQLDataIntoNewTable(ctx, settings.ProfileID, database, table, columns, edits)
		if err != nil || result == nil || !result.Created || result.CreationUncertain || result.Saved == nil || result.Saved.CommitUncertain || len(result.Saved.Outcomes) != len(edits) {
			t.Fatalf("new table import: %+v %v", result, err)
		}
		var amount, note, at string
		var payload []byte
		if err := db.QueryRowContext(ctx, "SELECT CAST(amount AS CHAR),note,payload,CAST(at AS CHAR) FROM "+target+" WHERE id=1").Scan(&amount, &note, &payload, &at); err != nil {
			t.Fatal(err)
		}
		if amount != "9007199254740993.1234567890" || note != "中文" || !bytes.Equal(payload, []byte{0, 0xff, 0x80}) || at != "2026-10-30 12:34:56.123456" {
			t.Fatalf("lost exact values: %q %q %x %q", amount, note, payload, at)
		}
		var amountNull, noteNull, payloadNull, noteLength, payloadLength int
		if err := db.QueryRowContext(ctx, "SELECT amount IS NULL,note IS NULL,payload IS NULL,LENGTH(note),LENGTH(payload) FROM "+target+" WHERE id=2").Scan(&amountNull, &noteNull, &payloadNull, &noteLength, &payloadLength); err != nil || amountNull != 1 || noteNull != 0 || payloadNull != 0 || noteLength != 0 || payloadLength != 0 {
			t.Fatalf("lost NULL/empty distinction: %d %d %d %d %d %v", amountNull, noteNull, payloadNull, noteLength, payloadLength, err)
		}
		if err := db.QueryRowContext(ctx, "SELECT note IS NULL,payload IS NULL FROM "+target+" WHERE id=3").Scan(&noteNull, &payloadNull); err != nil || noteNull != 1 || payloadNull != 1 {
			t.Fatalf("lost explicit NULL: %d %d %v", noteNull, payloadNull, err)
		}
		result, err = m.ImportMySQLDataIntoNewTable(ctx, settings.ProfileID, database, table, columns, edits[:1])
		var serverError *mysqldriver.MySQLError
		if !errors.As(err, &serverError) || serverError.Number != 1050 || result.Created || result.CreationUncertain || result.Saved != nil {
			t.Fatalf("existing table was reused: %+v %v", result, err)
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+target).Scan(&count); err != nil || count != 3 {
			t.Fatalf("existing table changed: %d %v", count, err)
		}
	})
	t.Run("failed rows retain empty created table", func(t *testing.T) {
		table, target := makeTarget(t, "rollback")
		columns := []MySQLDataImportColumn{{Name: "id", Type: "BIGINT", PrimaryKey: true}}
		edits := []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "1"}}}, {Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "1"}}}}
		result, err := m.ImportMySQLDataIntoNewTable(ctx, settings.ProfileID, database, table, columns, edits)
		var serverError *mysqldriver.MySQLError
		if !errors.As(err, &serverError) || serverError.Number != 1062 || result == nil || !result.Created || result.CreationUncertain || result.Saved != nil {
			t.Fatalf("failed row batch contract: %+v %v", result, err)
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+target).Scan(&count); err != nil || count != 0 {
			t.Fatalf("row batch did not roll back: %d %v", count, err)
		}
	})
	t.Run("nullable table without row key", func(t *testing.T) {
		table, target := makeTarget(t, "nokey")
		columns := []MySQLDataImportColumn{{Name: "note", Type: "VARCHAR(80)", Nullable: true}}
		edits := []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"note": {Text: "first"}}}, {Kind: "insert", Values: map[string]MySQLDataValue{"note": {Null: true}}}}
		result, err := m.ImportMySQLDataIntoNewTable(ctx, settings.ProfileID, database, table, columns, edits)
		if err != nil || result == nil || !result.Created || result.Saved == nil || len(result.Saved.Outcomes) != 2 {
			t.Fatalf("keyless import: %+v %v", result, err)
		}
		var count, nulls int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*),SUM(note IS NULL) FROM "+target).Scan(&count, &nulls); err != nil || count != 2 || nulls != 1 {
			t.Fatalf("keyless values: %d %d %v", count, nulls, err)
		}
	})
	t.Run("existing table preserves empty binary and row identity", func(t *testing.T) {
		table, target := makeTarget(t, "binary")
		if _, err := db.ExecContext(ctx, "CREATE TABLE "+target+" (id VARBINARY(20) PRIMARY KEY,payload BLOB NULL) ENGINE=InnoDB"); err != nil {
			t.Fatal(err)
		}
		edits := []MySQLDataEdit{
			{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Binary: true, Bytes: []byte("one")}, "payload": {Binary: true}}},
			{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Binary: true}, "payload": {Binary: true, Bytes: []byte{}}}},
			{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Binary: true, Bytes: []byte("null")}, "payload": {Null: true}}},
		}
		saved, err := m.ApplyMySQLDataEdits(ctx, settings.ProfileID, database, table, edits)
		if err != nil || saved == nil || len(saved.Outcomes) != 3 || saved.Outcomes[1].Saved == nil || saved.Outcomes[1].Saved.Ref.Values[0] == nil {
			t.Fatal("empty binary insert identity", saved, err)
		}
		var empty, nulls int
		if err := db.QueryRowContext(ctx, "SELECT SUM(payload IS NOT NULL AND LENGTH(payload)=0),SUM(payload IS NULL) FROM "+target).Scan(&empty, &nulls); err != nil || empty != 2 || nulls != 1 {
			t.Fatal("empty binary/NULL import", empty, nulls, err)
		}
		if _, err := m.ApplyMySQLDataEdits(ctx, settings.ProfileID, database, table, []MySQLDataEdit{{Kind: "delete", Original: saved.Outcomes[1].Saved}}); err != nil {
			t.Fatal("cannot delete empty binary identity", err)
		}
		changed, err := m.ApplyMySQLDataEdits(ctx, settings.ProfileID, database, table, []MySQLDataEdit{{Kind: "update", Original: saved.Outcomes[0].Saved, Values: map[string]MySQLDataValue{"id": {Binary: true}, "payload": {Binary: true}}}})
		if err != nil || changed == nil || changed.Outcomes[0].Saved == nil || changed.Outcomes[0].Saved.Ref.Values[0] == nil {
			t.Fatal("empty binary update identity", changed, err)
		}
		var emptyKeys int
		if err := db.QueryRowContext(ctx, "SELECT SUM(id=X''),SUM(payload IS NOT NULL AND LENGTH(payload)=0),SUM(payload IS NULL) FROM "+target).Scan(&emptyKeys, &empty, &nulls); err != nil || emptyKeys != 1 || empty != 1 || nulls != 1 {
			t.Fatal("empty binary update not stored", emptyKeys, empty, nulls, err)
		}
	})
	var restoredMode string
	if err := db.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&restoredMode); err != nil || restoredMode != originalMode {
		t.Fatalf("metadata pool leaked import sql_mode: %q -> %q %v", originalMode, restoredMode, err)
	}
}
