package rqlite

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

// An unsafe explicit script can affect a different HTTP client even though
// every client gets its own original receipt. This owned three-node regression
// also proves that a later Raft-replicated ROLLBACK can erase an acknowledged
// intervening write. It is protocol evidence, not a supported SQL workflow.
func TestNativeRQLiteDDLExplicitTimeoutCrossClientReplication(t *testing.T) {
	p, join := nativeFixtureConfigured(t, "", "", true)
	second, _ := nativeFixtureConfigured(t, "", join, true)
	third, _ := nativeFixtureConfigured(t, "", join, true)
	second.ID, third.ID = 82, 83
	leader := openNative(t, p, "original-fixture-password")
	follower := openNative(t, second, "original-fixture-password")
	transport := &originalEndpointTransport{base: follower.transport, host: net.JoinHostPort(second.Host, fmt.Sprint(second.Port)), t: t}
	follower.client.Transport = transport
	setup, err := leader.request(context.Background(), "/db/execute", []any{
		"CREATE TABLE cluster_script_parent(id INTEGER PRIMARY KEY)",
		"CREATE TABLE cluster_script_child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES cluster_script_parent(id) ON DELETE CASCADE)",
		"INSERT INTO cluster_script_parent VALUES(1)", "INSERT INTO cluster_script_child VALUES(1,1)",
	}, true, true)
	if err != nil || setup.Uncertain {
		t.Fatal("original cluster DDL setup did not commit", err, setup.Raw)
	}
	follower.config.Options.DatabaseTimeoutMilliseconds = 50
	r, err := follower.request(context.Background(), "/db/execute", []any{
		"PRAGMA foreign_keys=OFF",
		"BEGIN IMMEDIATE;INSERT INTO cluster_script_parent VALUES(2);WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT SUM(x) FROM n;COMMIT",
		"ROLLBACK", "PRAGMA foreign_keys=ON",
	}, false, true)
	retainNativeFileReceipt(t, "ddl-cluster-original-timeout", r.Raw)
	if !errors.Is(err, context.DeadlineExceeded) || r.Uncertain || len(r.Results) != 4 || !transport.forwarded {
		t.Fatal("original follower timeout was not acknowledged without replay", err, r.Raw)
	}
	version, _ := nativeArtifact(t)
	leaked := version == "10.7.1"
	if r.Results[2].failed() != leaked || r.Results[3].failed() != leaked {
		t.Fatal("native cluster cleanup does not match the exact original version", r.Raw)
	}
	// This is a different session, profile and HTTP endpoint. It must not be
	// mistaken for an owned continuation of the original explicit transaction.
	other := openNative(t, third, "original-fixture-password")
	intervening, err := other.request(context.Background(), "/db/request", []any{
		"INSERT INTO cluster_script_parent VALUES(3)", "SELECT count(*) FROM cluster_script_parent",
	}, false, true)
	retainNativeFileReceipt(t, "ddl-cluster-other-client-write", intervening.Raw)
	if err != nil || intervening.Uncertain || len(intervening.Results) != 2 || intervening.Results[0].Affected != "1" {
		t.Fatal("original intervening client did not receive its affected-row receipt", err, intervening.Raw)
	}
	writerRows, visibleRows := "2", "2"
	if leaked {
		writerRows, visibleRows = "3", "1"
	}
	nativeDDLInteger(t, intervening, 1, writerRows)
	visible, err := leader.request(context.Background(), "/db/query", []any{"SELECT count(*) FROM cluster_script_parent"}, true, false)
	retainNativeFileReceipt(t, "ddl-cluster-other-client-visible-before-cleanup", visible.Raw)
	if err != nil || visible.Uncertain {
		t.Fatal("original committed-row observation failed", err, visible.Raw)
	}
	nativeDDLInteger(t, visible, 0, visibleRows)
	// A new native transaction must also reject the leaked writer transaction.
	atomic, atomicErr := other.request(context.Background(), "/db/request", []any{
		"CREATE TEMP TABLE cluster_atomic_guard(v)", "DROP TABLE temp.cluster_atomic_guard",
	}, true, true)
	retainNativeFileReceipt(t, "ddl-cluster-other-client-atomic-admission", atomic.Raw)
	if leaked {
		if atomicErr == nil || !atomic.Uncertain {
			t.Fatal("nested writer admission was fabricated into an acknowledged rollback", atomic.Raw)
		}
	} else if atomicErr != nil || atomic.Uncertain {
		t.Fatal("restored v8 writer rejected an unrelated native transaction", atomicErr, atomic.Raw)
	}
	cleanup, err := leader.request(context.Background(), "/db/execute", []any{
		"ROLLBACK", "PRAGMA foreign_keys=ON", "CREATE TABLE cluster_script_cleanup(id INTEGER PRIMARY KEY)", "INSERT INTO cluster_script_cleanup VALUES(1)",
	}, false, true)
	retainNativeFileReceipt(t, "ddl-cluster-owned-cleanup", cleanup.Raw)
	if cleanup.Uncertain || len(cleanup.Results) != 4 || cleanup.Results[0].failed() == leaked || cleanup.Results[1].failed() || cleanup.Results[2].failed() || cleanup.Results[3].failed() {
		t.Fatal("owned cluster cleanup did not settle", err, cleanup.Raw)
	}
	// The marker is written after cleanup in the same replicated array. A
	// local reader that observes it has passed both the original script and
	// the intervening write in the log. Poll only reads; never replay a write.
	for i, profile := range []struct {
		name string
		s    *Session
	}{{"leader", leader}, {"follower", follower}, {"third", other}} {
		profile.s.config.Options.Consistency = "none"
		profile.s.config.Options.DatabaseTimeoutMilliseconds = 30000
		deadline := time.Now().Add(10 * time.Second)
		for {
			actual, readErr := profile.s.request(context.Background(), "/db/query", []any{
				"SELECT count(*) FROM cluster_script_cleanup", "SELECT count(*) FROM cluster_script_parent", "SELECT count(*) FROM cluster_script_child",
			}, true, false)
			if readErr == nil && len(actual.Results) == 3 && len(actual.Results[0].Values) == 1 && string(actual.Results[0].Values[0][0]) == "1" {
				retainNativeFileReceipt(t, "ddl-cluster-after-cleanup-"+profile.name, actual.Raw)
				nativeDDLInteger(t, actual, 1, visibleRows)
				nativeDDLInteger(t, actual, 2, "1")
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("original replica did not apply owned cleanup", i, readErr, actual.Raw)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	if intervening.Results[0].Affected != "1" || intervening.Uncertain {
		t.Fatal("later observation rewrote the original intervening receipt")
	}
}

// The native transaction flag owns rollback independently of later SQL array
// statements. Prove the timeout behavior on the writer and keep COMMIT errors
// uncertain when the native receipt itself does not acknowledge a rollback.
func TestNativeRQLiteDDLNativeTransactionFailureProtocol(t *testing.T) {
	for _, endpoint := range []string{"/db/execute", "/db/request"} {
		t.Run(endpoint, func(t *testing.T) {
			p, _ := nativeFixtureConfigured(t, "", "", true)
			s := openNative(t, p, "original-fixture-password")
			setup, err := s.request(context.Background(), "/db/execute", []any{
				"CREATE TABLE atomic_script_parent(id INTEGER PRIMARY KEY)",
				"CREATE TABLE atomic_script_child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES atomic_script_parent(id) DEFERRABLE INITIALLY DEFERRED)",
				"INSERT INTO atomic_script_parent VALUES(1)",
			}, true, true)
			if err != nil || setup.Uncertain {
				t.Fatal("native atomic DDL setup failed", err, setup.Raw)
			}
			for _, name := range []string{"database_timeout", "commit_error"} {
				t.Run(name, func(t *testing.T) {
					statements := []any{
						"ALTER TABLE atomic_script_parent ADD COLUMN transient TEXT",
						"INSERT INTO atomic_script_parent(id) VALUES(2)",
						"WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT SUM(x) FROM n",
						"INSERT INTO atomic_script_parent(id) VALUES(3)",
					}
					if name == "database_timeout" {
						s.config.Options.DatabaseTimeoutMilliseconds = 50
					} else {
						statements = []any{"ALTER TABLE atomic_script_parent ADD COLUMN transient TEXT", "INSERT INTO atomic_script_child VALUES(1,999)"}
					}
					r, requestErr := s.request(context.Background(), endpoint, statements, true, true)
					key := "ddl-native-atomic-" + endpoint[4:] + "-" + name
					retainNativeFileReceipt(t, key, r.Raw)
					t.Logf("native atomic failure endpoint=%s case=%s uncertain=%t err=%v receipt=%s", endpoint, name, r.Uncertain, requestErr, r.Raw)
					if name == "database_timeout" {
						if !errors.Is(requestErr, context.DeadlineExceeded) || r.Uncertain || len(r.Results) != 3 || !r.Results[2].failed() {
							t.Fatal("original native transaction did not acknowledge its timeout", requestErr, r.Raw)
						}
					} else if requestErr == nil || !r.Uncertain || r.Error == "" {
						t.Fatal("native top-level COMMIT error was fabricated into a definite rollback", requestErr, r.Raw)
					}
					s.config.Options.DatabaseTimeoutMilliseconds = 30000
					state, err := s.request(context.Background(), "/db/request", []any{
						"CREATE TEMP TABLE atomic_writer_guard(v)", "PRAGMA foreign_keys",
						"SELECT count(*) FROM atomic_script_parent", "SELECT count(*) FROM atomic_script_child",
						"SELECT count(*) FROM pragma_table_xinfo('atomic_script_parent') WHERE name='transient'", "DROP TABLE temp.atomic_writer_guard",
					}, true, true)
					retainNativeFileReceipt(t, key+"-original-writer-state", state.Raw)
					if err != nil || state.Uncertain {
						t.Fatal("original atomic failure left a leaked writer transaction", err, state.Raw)
					}
					nativeDDLInteger(t, state, 1, "1")
					nativeDDLInteger(t, state, 2, "1")
					nativeDDLInteger(t, state, 3, "0")
					nativeDDLInteger(t, state, 4, "0")
					// Later state is independent evidence. It must not relabel
					// the captured native COMMIT-error receipt as definite.
					if name == "commit_error" && !r.Uncertain {
						t.Fatal("later read relabeled the original COMMIT-error receipt")
					}
				})
			}
		})
	}
}
