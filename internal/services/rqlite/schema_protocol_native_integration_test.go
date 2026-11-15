package rqlite

import (
	"context"
	"errors"
	"testing"
)

func nativeDDLInteger(t *testing.T, r receipt, statement int, expected string) {
	t.Helper()
	if statement >= len(r.Results) || len(r.Results[statement].Values) != 1 || len(r.Results[statement].Values[0]) != 1 || string(r.Results[statement].Values[0][0]) != expected {
		t.Fatal("native original DDL boundary differs", statement, expected, r.Raw)
	}
}

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
	request("ddl-fk-setup", []any{
		"CREATE TABLE protocol_parent(id INTEGER PRIMARY KEY)",
		"CREATE TABLE protocol_child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES protocol_parent(id) ON DELETE CASCADE)",
		"INSERT INTO protocol_parent VALUES(1)", "INSERT INTO protocol_child VALUES(1,1)",
	}, true)
	// An entirely read-only request is served by a read connection, whose
	// foreign_keys value does not establish the original writer's FK state.
	readOnly := request("ddl-read-connection-fk", []any{"PRAGMA foreign_keys"}, true)
	nativeDDLInteger(t, readOnly, 0, "1")
	request("ddl-read-connection-fk-toggle", []any{"PRAGMA foreign_keys", "PRAGMA foreign_keys=OFF", "PRAGMA foreign_keys"}, true)
	writer := request("ddl-fk-toggle-inside-transaction", []any{
		"CREATE TEMP TABLE protocol_writer_guard(v)", "PRAGMA foreign_keys", "PRAGMA foreign_keys=OFF", "PRAGMA foreign_keys", "DROP TABLE temp.protocol_writer_guard",
	}, true)
	nativeDDLInteger(t, writer, 1, "1")
	nativeDDLInteger(t, writer, 3, "1")
	rebuild := request("ddl-fk-deferred-rebuild", []any{
		"PRAGMA defer_foreign_keys=ON",
		"CREATE TABLE protocol_shadow(id INTEGER PRIMARY KEY)",
		"INSERT INTO protocol_shadow SELECT * FROM protocol_parent",
		"DROP TABLE protocol_parent", "ALTER TABLE protocol_shadow RENAME TO protocol_parent",
		"SELECT count(*) FROM protocol_child", "PRAGMA foreign_key_check", "PRAGMA foreign_keys",
	}, true)
	nativeDDLInteger(t, rebuild, 5, "0") // Deferred validation does not suppress CASCADE.
	nativeDDLInteger(t, rebuild, 7, "1")
	if len(rebuild.Results[6].Values) != 0 {
		t.Fatal("native cascade fixture left an FK violation")
	}
	explicit := request("ddl-explicit-transaction", []any{"BEGIN", "INSERT INTO protocol_parent VALUES(2)", "COMMIT", "SELECT count(*) FROM protocol_parent"}, false)
	nativeDDLInteger(t, explicit, 3, "2")
	outside := request("ddl-fk-toggle-outside-transaction", []any{
		"CREATE TEMP TABLE protocol_writer_guard(v)", "PRAGMA foreign_keys=OFF", "PRAGMA foreign_keys", "DROP TABLE temp.protocol_writer_guard",
	}, false)
	nativeDDLInteger(t, outside, 2, "0")
	next := request("ddl-next-request-writer-state", []any{
		"CREATE TEMP TABLE protocol_writer_guard(v)", "PRAGMA foreign_keys", "PRAGMA defer_foreign_keys", "SELECT count(*) FROM protocol_child", "DROP TABLE temp.protocol_writer_guard",
	}, true)
	nativeDDLInteger(t, next, 1, "0") // A separate HTTP request observes the leaked setting.
	nativeDDLInteger(t, next, 2, "0")
	nativeDDLInteger(t, next, 3, "0")
	restored := request("ddl-original-writer-state-restored", []any{
		"CREATE TEMP TABLE protocol_writer_guard(v)", "PRAGMA foreign_keys=ON", "PRAGMA foreign_keys", "DROP TABLE temp.protocol_writer_guard",
	}, false)
	nativeDDLInteger(t, restored, 2, "1")
}

// The execute endpoint holds the writer for its whole statement array. That
// alone does not prove that an explicit transaction script can restore writer
// PRAGMAs after every failure. Keep these native receipts before choosing a
// schema-rebuild strategy; none of these probes publishes schema capabilities.
func TestNativeRQLiteDDLExplicitScriptCleanupProtocol(t *testing.T) {
	for _, name := range []string{"committed", "statement_error", "commit_error", "database_timeout"} {
		t.Run(name, func(t *testing.T) {
			p, _ := nativeFixtureConfigured(t, "", "", true)
			s := openNative(t, p, "original-fixture-password")
			setup, err := s.request(context.Background(), "/db/execute", []any{
				"CREATE TABLE script_parent(id INTEGER PRIMARY KEY)",
				"CREATE TABLE script_child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES script_parent(id) ON DELETE CASCADE)",
				"CREATE TABLE script_deferred(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES script_parent(id) DEFERRABLE INITIALLY DEFERRED)",
				"INSERT INTO script_parent VALUES(1)", "INSERT INTO script_child VALUES(1,1)",
			}, true, true)
			if err != nil || setup.Uncertain {
				t.Fatal("native explicit-script setup did not commit", err)
			}
			body := "BEGIN IMMEDIATE;CREATE TABLE script_shadow(id INTEGER PRIMARY KEY,note TEXT);INSERT INTO script_shadow(id) SELECT id FROM script_parent;DROP TABLE script_parent;ALTER TABLE script_shadow RENAME TO script_parent;COMMIT"
			if name == "statement_error" {
				body = "BEGIN IMMEDIATE;INSERT INTO script_parent VALUES(2);INSERT INTO script_parent VALUES(1);INSERT INTO script_parent VALUES(3);COMMIT"
			}
			if name == "commit_error" {
				// This case must keep FK enforcement enabled so COMMIT itself
				// fails after SQLite accepted the deferred INSERT.
				body = "BEGIN IMMEDIATE;INSERT INTO script_deferred VALUES(1,999);COMMIT"
			}
			if name == "database_timeout" {
				s.config.Options.DatabaseTimeoutMilliseconds = 50
				// A timed-out SELECT need not roll back an earlier INSERT in
				// an explicit transaction. No schema/data write after it may
				// be assumed to have run, including cleanup.
				body = "BEGIN IMMEDIATE;INSERT INTO script_parent VALUES(2);WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT SUM(x) FROM n;COMMIT"
			}
			fk := "PRAGMA foreign_keys=OFF"
			if name == "commit_error" {
				fk = "PRAGMA foreign_keys=ON"
			}
			r, executeErr := s.request(context.Background(), "/db/execute", []any{fk, body, "ROLLBACK", "PRAGMA foreign_keys=ON"}, false, true)
			retainNativeFileReceipt(t, "ddl-script-"+name, r.Raw)
			t.Logf("native explicit script case=%s uncertain=%t err=%v receipt=%s", name, r.Uncertain, executeErr, r.Raw)
			if executeErr == nil || r.Uncertain || len(r.Results) != 4 || !r.Results[1].failed() && name != "committed" {
				t.Fatal("original explicit script failure/cleanup receipt differs", executeErr, r.Raw)
			}
			if name == "committed" && (r.Results[1].failed() || !r.Results[2].failed() || r.Results[3].failed()) {
				t.Fatal("unconditional ROLLBACK lost the original successful commit", r.Raw)
			}
			if name == "database_timeout" && !errors.Is(executeErr, context.DeadlineExceeded) {
				t.Fatal("original execute timeout was not retained", executeErr, r.Raw)
			}
			version, _ := nativeArtifact(t)
			leaked := name == "database_timeout" && version == "10.7.1"
			if name != "committed" && (r.Results[2].failed() != leaked || r.Results[3].failed() != leaked) {
				t.Fatal("native version-specific same-request cleanup differs", version, r.Raw)
			}
			s.config.Options.DatabaseTimeoutMilliseconds = 30000
			// Include a real write to prevent routing this observation to a
			// reader connection with unrelated FK/transaction state.
			state, err := s.request(context.Background(), "/db/request", []any{
				"CREATE TEMP TABLE script_writer_state(v)", "PRAGMA foreign_keys",
				"SELECT count(*) FROM script_parent", "SELECT count(*) FROM script_child",
				"SELECT count(*) FROM script_deferred", "DROP TABLE temp.script_writer_state",
			}, false, true)
			retainNativeFileReceipt(t, "ddl-script-"+name+"-writer-state", state.Raw)
			t.Logf("native explicit writer state case=%s err=%v receipt=%s", name, err, state.Raw)
			if err != nil || state.Uncertain || len(state.Results) != 6 {
				t.Fatal("original writer observation did not settle", err, state.Raw)
			}
			fkState, writerRows := "1", "1"
			if leaked {
				fkState, writerRows = "0", "2"
			}
			nativeDDLInteger(t, state, 1, fkState)
			nativeDDLInteger(t, state, 2, writerRows)
			nativeDDLInteger(t, state, 3, "1")
			nativeDDLInteger(t, state, 4, "0")
			reader, err := s.request(context.Background(), "/db/query", []any{"SELECT count(*) FROM script_parent", "SELECT count(*) FROM script_child", "SELECT count(*) FROM script_deferred"}, true, false)
			retainNativeFileReceipt(t, "ddl-script-"+name+"-reader-state", reader.Raw)
			t.Logf("native explicit reader state case=%s err=%v receipt=%s", name, err, reader.Raw)
			if err != nil || reader.Uncertain {
				t.Fatal("original reader observation did not settle", err, reader.Raw)
			}
			nativeDDLInteger(t, reader, 0, "1")
			nativeDDLInteger(t, reader, 1, "1")
			nativeDDLInteger(t, reader, 2, "0")
			// Fixture cleanup is deliberately a later request. Its success
			// does not establish unconditional same-request restoration.
			cleanup, err := s.request(context.Background(), "/db/execute", []any{"ROLLBACK", "PRAGMA foreign_keys=ON"}, false, true)
			retainNativeFileReceipt(t, "ddl-script-"+name+"-fixture-cleanup", cleanup.Raw)
			if cleanup.Uncertain || len(cleanup.Results) != 2 || cleanup.Results[1].failed() {
				t.Fatal("owned native fixture writer restoration failed", err, cleanup.Raw)
			}
			if cleanup.Results[0].failed() == leaked {
				t.Fatal("original transaction survived differently from its writer/reader observations", cleanup.Raw)
			}
			restored, err := s.request(context.Background(), "/db/request", []any{
				"CREATE TEMP TABLE script_restored(v)", "PRAGMA foreign_keys", "SELECT count(*) FROM script_parent", "DROP TABLE temp.script_restored",
			}, true, true)
			retainNativeFileReceipt(t, "ddl-script-"+name+"-restored-state", restored.Raw)
			if err != nil || restored.Uncertain {
				t.Fatal("owned original writer could not start a fresh transaction", err, restored.Raw)
			}
			nativeDDLInteger(t, restored, 1, "1")
			nativeDDLInteger(t, restored, 2, "1")
		})
	}
}
