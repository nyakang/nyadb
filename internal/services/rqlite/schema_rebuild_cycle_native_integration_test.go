package rqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// Independent native protocol evidence: suppress only automatic FK UPDATEs
// while every original table is being emptied. All original rows are already
// captured; graph/value verification still precedes clearing deferred counters.
func TestNativeRQLiteRebuildCycleUpdateSuspensionProtocol(t *testing.T) {
	p, _ := nativeFixtureConfigured(t, "", "", true)
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 730)
	var c rebuildProtocolCase
	for _, candidate := range rebuildProtocolCases() {
		if candidate.name == "immediate_cycle_rollback" {
			c = candidate
		}
	}
	nativeSchemaRebuildSetup(t, s, l, c)
	d := reserveSchemaUnit(t, s, "original-cycle-protocol", "target")
	original := nativeSchemaWriteSnapshot(t, d)
	desired := nativeSchemaRebuildDraft(original, c)
	guard, err := s.schemaSnapshotGuardScoped(original, d.scope)
	if err != nil {
		t.Fatal(err)
	}
	b, err := schemaRebuildBatch(original, desired, guard, d.scope, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	var create, drop []string
	for i, e := range original.Schema.SQLite.Entries {
		if e.Type != "table" || e.Name == "sqlite_sequence" {
			continue
		}
		name := schemaddl.Quote(fmt.Sprintf("__nyadb_cycle_probe_%d", i))
		create = append(create, "CREATE TEMP TRIGGER "+name+" BEFORE UPDATE ON main."+schemaddl.Quote(e.Name)+" BEGIN SELECT RAISE(IGNORE); END")
		drop = append(drop, "DROP TRIGGER temp."+name)
	}
	var statements []string
	started := false
	for _, statement := range b.statements {
		if strings.Contains(statement, "_suspend_fk_") && (strings.HasPrefix(statement, "CREATE TEMP TRIGGER ") || strings.HasPrefix(statement, "DROP TRIGGER temp.")) {
			continue // Keep this prototype independent of production suspension.
		}
		if strings.HasPrefix(statement, "DELETE FROM main.") && !started {
			statements = append(statements, create...)
			started = true
		}
		if strings.HasPrefix(statement, "DROP TABLE main.") {
			statements = append(statements, drop...)
		}
		statements = append(statements, statement)
	}
	tables := rebuildProtocolTables(t, s, original)
	before := rebuildProtocolFingerprints(t, l, tables)
	writerBefore := rebuildProtocolWriterState(t, s, "cycle-suspension-before")
	request, _ := json.Marshal(statements)
	retainNativeFileReceipt(t, "cycle-suspension-request", string(request))
	args := make([]any, len(statements))
	for i, sql := range statements {
		args[i] = sql
	}
	native, writeErr := s.request(context.Background(), "/db/execute", args, true, true)
	retainNativeFileReceipt(t, "cycle-suspension-native", native.Raw)
	after := rebuildProtocolFingerprints(t, l, tables)
	writerAfter := rebuildProtocolWriterState(t, s, "cycle-suspension-after")
	actual := nativeSchemaWriteSnapshot(t, d)
	proof, _ := json.Marshal(struct {
		Before, After             any
		WriterBefore, WriterAfter any
		Original, Desired, Actual any
	}{before, after, writerBefore, writerAfter, original, desired, actual})
	retainNativeFileReceipt(t, "cycle-suspension-verification", string(proof))
	if writeErr != nil || len(native.Results) != len(statements) {
		t.Fatal("native UPDATE suspension did not settle the original cycle", writeErr)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(writerBefore, writerAfter) || schemaddl.VerifySQLiteRebuildCatalog(original.Schema, desired, actual.Schema) != nil {
		t.Fatal("native cycle settlement lost original values, catalog or writer settings")
	}
}
