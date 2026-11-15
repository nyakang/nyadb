package rqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func retainNativeSchemaSnapshot(t *testing.T, name string, original contract.SchemaDesignSnapshot) {
	t.Helper()
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	retainNativeFileReceipt(t, name+"-snapshot", string(raw))
}

func nativeSchemaGuardChange(t *testing.T, s *Session, original contract.SchemaDesignSnapshot, name, change string) (receipt, error) {
	t.Helper()
	query, err := s.schemaSnapshotGuard(original)
	if err != nil {
		t.Fatal("original schema guard could not be built", err)
	}
	r, err := s.request(context.Background(), "/db/request", []any{
		"CREATE TEMP TABLE original_schema_guard(ok INTEGER NOT NULL CHECK(ok=1))",
		"INSERT INTO temp.original_schema_guard " + query,
		change, "PRAGMA foreign_keys", "DROP TABLE temp.original_schema_guard",
	}, true, true)
	retainNativeFileReceipt(t, name, r.Raw)
	return r, err
}

func TestNativeRQLiteSchemaSnapshotOriginalGuardAndTemporaryWriterTriggers(t *testing.T) {
	p, _ := nativeFixtureConfigured(t, "", "", true)
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE original_schema(id INTEGER PRIMARY KEY,v TEXT COLLATE NOCASE UNIQUE DEFAULT 'X''61''',CONSTRAINT original_check CHECK(length(v)>0));CREATE TABLE original_schema_child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES original_schema(id) ON DELETE CASCADE);CREATE TABLE original_schema_audit(v TEXT);CREATE INDEX original_schema_expression ON original_schema(lower(v)) WHERE v IS NOT NULL;CREATE TRIGGER original_schema_trigger AFTER INSERT ON original_schema BEGIN INSERT INTO original_schema_audit VALUES(new.v); END;CREATE VIEW original_schema_view AS SELECT id,v FROM original_schema;INSERT INTO original_schema(id) VALUES(9007199254740993);INSERT INTO original_schema_child VALUES(1,9007199254740993)`)
	target := schemaUnitTarget(s, "original_schema")
	original, err := s.SchemaDesign().Snapshot(context.Background(), target)
	if err != nil || !original.Schema.Exists || len(original.Schema.Table.Clauses) != 3 || len(original.Schema.Table.Definitions) != 2 {
		t.Fatal("original native constraints/defaults/definitions were incomplete", err, original)
	}
	retainNativeSchemaSnapshot(t, "schema-original", original)
	implicit, child, view := false, false, false
	for _, e := range original.Schema.SQLite.Entries {
		implicit = implicit || e.Type == "index" && e.Table == target.Object.Name && e.SQLNull
		child = child || e.Type == "table" && e.Name == "original_schema_child"
		view = view || e.Type == "view" && e.Name == "original_schema_view"
	}
	if !implicit || !child || !view {
		t.Fatal("complete catalog omitted implicit index or inbound/view dependencies")
	}
	r, err := nativeSchemaGuardChange(t, s, original, "schema-guard-original-add", "ALTER TABLE original_schema ADD COLUMN added TEXT DEFAULT 'complete original'")
	if err != nil || r.Uncertain {
		t.Fatal("guarded native direct ALTER failed", err, r.Raw)
	}
	nativeDDLInteger(t, r, 3, "1")
	actual := nativeExecute(t, l, "SELECT id,v,added FROM original_schema;SELECT count(*) FROM original_schema_child;SELECT count(*) FROM original_schema_audit;SELECT id,v FROM original_schema_view")
	if actual.Sets[0].Rows[0][0].Text != "9007199254740993" || actual.Sets[0].Rows[0][1].Text != "X'61'" || actual.Sets[0].Rows[0][2].Text != "complete original" || actual.Sets[1].Rows[0][0].Text != "1" || actual.Sets[2].Rows[0][0].Text != "1" || actual.Sets[3].Rows[0][0].Text != "9007199254740993" {
		t.Fatal("native guarded ALTER lost typed default, original row, child, trigger or view")
	}
	current, err := s.SchemaDesign().Snapshot(context.Background(), target)
	if err != nil || len(current.Schema.Table.Columns()) != 3 || len(current.Schema.Table.Definitions) != 2 {
		t.Fatal("changed native table could not be read losslessly", err)
	}
	retainNativeSchemaSnapshot(t, "schema-after-add", current)
	nativeExecute(t, l, "CREATE INDEX changed_inbound_schema ON original_schema_child(parent_id)")
	r, err = nativeSchemaGuardChange(t, s, current, "schema-guard-inbound-drift", "ALTER TABLE original_schema ADD COLUMN forbidden TEXT")
	if err == nil || r.Uncertain || len(r.Results) != 2 || !r.Results[1].failed() {
		t.Fatal("changed inbound catalog bypassed the original atomic guard", err, r.Raw)
	}
	if actual := nativeExecute(t, l, "SELECT count(*) FROM pragma_table_xinfo('original_schema') WHERE name='forbidden'"); actual.Sets[0].Rows[0][0].Text != "0" {
		t.Fatal("stale original schema was modified")
	}
	current, err = s.SchemaDesign().Snapshot(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	nativeExecute(t, l, "CREATE TEMP TRIGGER original_schema_temporary AFTER UPDATE ON main.original_schema BEGIN SELECT 1; END")
	withTemp, err := s.SchemaDesign().Snapshot(context.Background(), target)
	if err != nil || withTemp.Schema.Revision != current.Schema.Revision {
		t.Fatal("read connection's persistent snapshot changed with writer-local state", err)
	}
	r, err = nativeSchemaGuardChange(t, s, current, "schema-guard-original-temporary-trigger", "ALTER TABLE original_schema ADD COLUMN forbidden TEXT")
	if err == nil || r.Uncertain || len(r.Results) != 2 || !r.Results[1].failed() {
		t.Fatal("original writer temporary trigger was not checked in the transaction", err, r.Raw)
	}
	nativeExecute(t, l, "DROP TRIGGER temp.original_schema_temporary")
	r, err = nativeSchemaGuardChange(t, s, current, "schema-guard-original-restored-writer", "ALTER TABLE original_schema ADD COLUMN restored TEXT")
	if err != nil || r.Uncertain {
		t.Fatal("rolled-back guard leaked temporary state or prevented original recovery", err, r.Raw)
	}
	nativeDDLInteger(t, r, 3, "1")
}

func TestNativeRQLiteSchemaSnapshot2000Columns200IndexesAndReadOnlyAuth(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	empty, err := s.SchemaDesign().Snapshot(context.Background(), schemaUnitTarget(s, "wide_schema"))
	if err != nil || empty.Schema.Exists || empty.Schema.SQLite == nil || len(empty.Schema.SQLite.Entries) != 0 {
		t.Fatal("new-table snapshot lost the complete empty original catalog", err)
	}
	retainNativeSchemaSnapshot(t, "schema-wide-empty-original", empty)
	guard, err := s.schemaSnapshotGuard(empty)
	if err != nil {
		t.Fatal(err)
	}
	columns := make([]string, 2000)
	columns[0] = "c0000 INTEGER PRIMARY KEY"
	for i := 1; i < len(columns); i++ {
		columns[i] = fmt.Sprintf("c%04d TEXT DEFAULT 'seed'", i)
	}
	statements := []any{"CREATE TEMP TABLE wide_original_guard(ok INTEGER NOT NULL CHECK(ok=1))", "INSERT INTO temp.wide_original_guard " + guard, "CREATE TABLE wide_schema(" + strings.Join(columns, ",") + ")"}
	for i := range 200 {
		statements = append(statements, fmt.Sprintf("CREATE INDEX wide_schema_i%03d ON wide_schema(c%04d)", i, i+1))
	}
	statements = append(statements, "DROP TABLE temp.wide_original_guard")
	r, err := s.request(context.Background(), "/db/request", statements, true, true)
	if err != nil || r.Uncertain {
		t.Fatal("official native wide schema setup failed", err, r.Raw)
	}
	retainNativeFileReceipt(t, "schema-wide-native-setup", r.Raw)
	original, err := s.SchemaDesign().Snapshot(context.Background(), schemaUnitTarget(s, "wide_schema"))
	if err != nil || len(original.Schema.Table.Columns()) != 2000 || len(original.Schema.Table.Definitions) != 200 || len(original.Schema.SQLite.Entries) != 201 {
		t.Fatal("original wide catalog was reduced to grid/import limits", err)
	}
	retainNativeSchemaSnapshot(t, "schema-wide-original", original)
	r, err = nativeSchemaGuardChange(t, s, original, "schema-wide-guarded-rename", "ALTER TABLE wide_schema RENAME COLUMN c1999 TO last_complete")
	if err != nil || r.Uncertain {
		t.Fatal("full wide original guard was not one native transaction", err, r.Raw)
	}
	after, err := s.SchemaDesign().Snapshot(context.Background(), schemaUnitTarget(s, "wide_schema"))
	if err != nil || len(after.Schema.Table.Columns()) != 2000 || after.Schema.Table.Columns()[1999].Name != "last_complete" || len(after.Schema.Table.Definitions) != 200 {
		t.Fatal("wide native rename lost a field or complete index definition", err)
	}
	retainNativeSchemaSnapshot(t, "schema-wide-after-rename", after)
	l := lease(t, s, 1)
	nativeExecute(t, l, "INSERT INTO wide_schema(c0000,last_complete) VALUES(9007199254740993,'X''61''')")
	withData, err := s.SchemaDesign().Snapshot(context.Background(), schemaUnitTarget(s, "wide_schema"))
	if err != nil || withData.Schema.Revision != after.Schema.Revision {
		t.Fatal("row edits became schema revisions", err)
	}
	peer := openNative(t, p, "original-fixture-password")
	if _, err := peer.schemaSnapshotGuard(after); !errors.Is(err, schemaddl.ErrConflict) {
		t.Fatal("another session with the same profile/native generation acquired the original schema proof", err)
	}
	p.ID, p.Username, p.RQLiteOptions.ReadOnly = 82, "reader", true
	reader := openNative(t, p, "reader-password")
	readOnly, err := reader.SchemaDesign().Snapshot(context.Background(), schemaUnitTarget(reader, "wide_schema"))
	if err != nil || len(readOnly.Schema.Table.Columns()) != 2000 || len(readOnly.Schema.Table.Definitions) != 200 || readOnly.Target.Object.ProfileID != 82 || reader.SchemaDesign().Apply != nil || reader.Capabilities().DesignTables {
		t.Fatal("query-only credentials lost original wide schema or acquired writes", err)
	}
	retainNativeSchemaSnapshot(t, "schema-wide-read-only-profile", readOnly)
	// Same database/native generation does not transfer a snapshot to another
	// session. Retain original identity even when the other profile is readable.
	transferred := after.Clone()
	transferred.Target = schemaUnitTarget(reader, "wide_schema")
	if _, err := reader.schemaSnapshotGuard(transferred); !errors.Is(err, schemaddl.ErrConflict) {
		t.Fatal("another session acquired the original schema proof", err)
	}
}

func TestNativeRQLiteSchemaSnapshotCancelCloseWaitOriginalAcknowledgement(t *testing.T) {
	p := nativeFixture(t, "")
	admin := openNative(t, p, "original-fixture-password")
	nativeExecute(t, lease(t, admin, 1), "CREATE TABLE held_schema(id INTEGER PRIMARY KEY,v TEXT)")
	s := openNative(t, p, "original-fixture-password")
	h := &heldCompleteRead{base: s.transport, marker: schemaCatalogSQL, started: make(chan struct{}), ack: make(chan struct{}), original: make(chan [2]string, 1)}
	defer h.release.Do(func() { close(h.ack) })
	s.client.Transport = h
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		out contract.SchemaDesignSnapshot
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := s.SchemaDesign().Snapshot(ctx, schemaUnitTarget(s, "held_schema"))
		done <- result{out, err}
	}()
	select {
	case <-h.started:
	case <-time.After(10 * time.Second):
		t.Fatal("original schema read did not reach its native acknowledgement")
	}
	original := <-h.original
	retainNativeFileReceipt(t, "schema-held-original-native-read", original[1])
	cancel()
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	select {
	case <-done:
		t.Fatal("schema cancellation released its unacknowledged executor")
	case <-closed:
		t.Fatal("schema session close released its unacknowledged executor")
	case <-time.After(40 * time.Millisecond):
	}
	h.release.Do(func() { close(h.ack) })
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) && !errors.Is(result.err, contract.ErrRevoked) || result.out.Schema.SQLite != nil {
			t.Fatal("closed original schema read published a baseline", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original schema read did not settle after acknowledgement")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("original schema session close did not finish")
	}
	if h.requests.Load() != 1 || !admin.Alive() {
		t.Fatal("schema read replayed or closed another original session")
	}
}
