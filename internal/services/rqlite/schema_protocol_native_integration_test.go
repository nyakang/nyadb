package rqlite

import (
	"context"
	"testing"
)

// This protocol baseline proves why connection-scoped SQLite PRAGMA changes
// cannot be assumed to protect a RQLite HTTP table rebuild. It is not a
// published schema-design implementation.
func TestNativeRQLiteDDLForeignKeyTransactionProtocol(t *testing.T) {
	p, _ := nativeFixtureConfigured(t, "", "", true)
	s := openNative(t, p, "original-fixture-password")
	request := func(name string, statements []any, transaction bool) receipt {
		t.Helper()
		r, err := s.request(context.Background(), "/db/request", statements, transaction, true)
		if err != nil || r.Uncertain || len(r.Results) != len(statements) {
			t.Fatal("native DDL protocol did not settle every original statement", name, err)
		}
		retainNativeFileReceipt(t, name, r.Raw)
		t.Logf("native DDL protocol case=%s error=%v receipt=%s", name, err, r.Raw)
		return r
	}
	integer := func(r receipt, statement int, value string) {
		t.Helper()
		if len(r.Results[statement].Values) != 1 || len(r.Results[statement].Values[0]) != 1 || string(r.Results[statement].Values[0][0]) != value {
			t.Fatal("native original DDL boundary differs", statement, value, r.Raw)
		}
	}
	request("ddl-fk-setup", []any{
		"CREATE TABLE protocol_parent(id INTEGER PRIMARY KEY)",
		"CREATE TABLE protocol_child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES protocol_parent(id) ON DELETE CASCADE)",
		"INSERT INTO protocol_parent VALUES(1)", "INSERT INTO protocol_child VALUES(1,1)",
	}, true)
	// An entirely read-only request is served by a read connection, whose
	// foreign_keys value does not establish the original writer's FK state.
	readOnly := request("ddl-read-connection-fk", []any{"PRAGMA foreign_keys"}, true)
	integer(readOnly, 0, "1")
	request("ddl-read-connection-fk-toggle", []any{"PRAGMA foreign_keys", "PRAGMA foreign_keys=OFF", "PRAGMA foreign_keys"}, true)
	writer := request("ddl-fk-toggle-inside-transaction", []any{
		"CREATE TEMP TABLE protocol_writer_guard(v)", "PRAGMA foreign_keys", "PRAGMA foreign_keys=OFF", "PRAGMA foreign_keys", "DROP TABLE temp.protocol_writer_guard",
	}, true)
	integer(writer, 1, "1")
	integer(writer, 3, "1")
	rebuild := request("ddl-fk-deferred-rebuild", []any{
		"PRAGMA defer_foreign_keys=ON",
		"CREATE TABLE protocol_shadow(id INTEGER PRIMARY KEY)",
		"INSERT INTO protocol_shadow SELECT * FROM protocol_parent",
		"DROP TABLE protocol_parent", "ALTER TABLE protocol_shadow RENAME TO protocol_parent",
		"SELECT count(*) FROM protocol_child", "PRAGMA foreign_key_check", "PRAGMA foreign_keys",
	}, true)
	integer(rebuild, 5, "0") // Deferred validation does not suppress CASCADE.
	integer(rebuild, 7, "1")
	if len(rebuild.Results[6].Values) != 0 {
		t.Fatal("native cascade fixture left an FK violation")
	}
	explicit := request("ddl-explicit-transaction", []any{"BEGIN", "INSERT INTO protocol_parent VALUES(2)", "COMMIT", "SELECT count(*) FROM protocol_parent"}, false)
	integer(explicit, 3, "2")
	outside := request("ddl-fk-toggle-outside-transaction", []any{
		"CREATE TEMP TABLE protocol_writer_guard(v)", "PRAGMA foreign_keys=OFF", "PRAGMA foreign_keys", "DROP TABLE temp.protocol_writer_guard",
	}, false)
	integer(outside, 2, "0")
	next := request("ddl-next-request-writer-state", []any{
		"CREATE TEMP TABLE protocol_writer_guard(v)", "PRAGMA foreign_keys", "PRAGMA defer_foreign_keys", "SELECT count(*) FROM protocol_child", "DROP TABLE temp.protocol_writer_guard",
	}, true)
	integer(next, 1, "0") // A separate HTTP request observes the leaked setting.
	integer(next, 2, "0")
	integer(next, 3, "0")
	restored := request("ddl-original-writer-state-restored", []any{
		"CREATE TEMP TABLE protocol_writer_guard(v)", "PRAGMA foreign_keys=ON", "PRAGMA foreign_keys", "DROP TABLE temp.protocol_writer_guard",
	}, false)
	integer(restored, 2, "1")
}
