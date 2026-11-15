package rqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestNativeRQLiteSchemaDocument2000Columns200IndexesOriginalProofIsolation(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	columns := make([]string, 2000)
	columns[0] = "c0000 INTEGER PRIMARY KEY"
	for i := 1; i < len(columns); i++ {
		columns[i] = fmt.Sprintf("c%04d TEXT DEFAULT 'X''61'''", i)
	}
	statements := []any{"CREATE TABLE leased_wide(" + strings.Join(columns, ",") + ")"}
	for i := range 200 {
		statements = append(statements, fmt.Sprintf("CREATE INDEX leased_wide_i%03d ON leased_wide(c%04d)", i, i+1))
	}
	statements = append(statements, "INSERT INTO leased_wide(c0000) VALUES(9007199254740993)")
	setup, err := s.request(context.Background(), "/db/request", statements, true, true)
	if err != nil {
		t.Fatal("native leased wide fixture failed", err)
	}
	retainNativeFileReceipt(t, "schema-document-wide-native-setup", setup.Raw)
	d := reserveSchemaUnit(t, s, "wide-original", "leased_wide")
	peer := reserveSchemaUnit(t, s, "wide-peer", "leased_wide")
	original, err := d.Operations().Snapshot(context.Background(), d.target)
	if err != nil || len(original.Schema.Table.Columns()) != 2000 || len(original.Schema.Table.Definitions) != 200 || len(original.Schema.SQLite.Entries) != 201 {
		t.Fatal("document lease reduced full structural capacity", err)
	}
	retainNativeSchemaSnapshot(t, "schema-document-wide-original", original)
	sibling, err := peer.Operations().Snapshot(context.Background(), peer.target)
	if err != nil || !reflect.DeepEqual(sibling.Schema.SQLite, original.Schema.SQLite) || sibling.Schema.Revision == original.Schema.Revision {
		t.Fatal("sibling lease did not own an independent original proof", err)
	}
	sessionOriginal, err := s.SchemaDesign().Snapshot(context.Background(), d.target)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{peer.scope, s.dataScope} {
		if _, err := s.schemaSnapshotGuardScoped(original, scope); !errors.Is(err, schemaddl.ErrConflict) {
			t.Fatal("full document proof transferred to another owner", err)
		}
	}
	if _, err := s.schemaSnapshotGuardScoped(sessionOriginal, d.scope); !errors.Is(err, schemaddl.ErrConflict) {
		t.Fatal("session-level proof became document authorization", err)
	}
	guard, err := s.schemaSnapshotGuardScoped(original, d.scope)
	if err != nil {
		t.Fatal(err)
	}
	// Protocol evidence, not a published schema-write method: capture the final
	// complete catalog inside this original native transaction/acknowledgement.
	body := []any{"CREATE TEMP TABLE leased_schema_guard(ok INTEGER NOT NULL CHECK(ok=1))", "INSERT INTO temp.leased_schema_guard " + guard, "ALTER TABLE main.leased_wide RENAME COLUMN c1999 TO last_complete", "SELECT schema_version FROM pragma_schema_version", schemaCatalogSQL, "DROP TABLE temp.leased_schema_guard"}
	native, err := s.request(context.Background(), "/db/request", body, true, true)
	if err != nil || native.Uncertain {
		t.Fatal("owning full document guard failed atomically", err)
	}
	retainNativeFileReceipt(t, "schema-document-wide-original-transaction", native.Raw)
	actual, err := s.decodeSchemaSnapshot(d.target, native, 3)
	if err != nil || len(actual.Schema.Table.Columns()) != 2000 || actual.Schema.Table.Columns()[1999].Name != "last_complete" || len(actual.Schema.Table.Definitions) != 200 {
		t.Fatal("complete original transaction receipt lost structural data", err)
	}
	actual.Schema.Revision = schemaSnapshotSealScoped(actual, d.scope)
	retainNativeSchemaSnapshot(t, "schema-document-wide-transaction-catalog", actual)
	if _, err := s.schemaSnapshotGuardScoped(actual, d.scope); err != nil {
		t.Fatal("original transaction baseline lost document proof", err)
	}
	if _, err := s.schemaSnapshotGuardScoped(actual, peer.scope); !errors.Is(err, schemaddl.ErrConflict) {
		t.Fatal("captured original transaction baseline transferred to peer", err)
	}
	// The original native body is preserved verbatim, including Raft/timing data.
	desired := original.Schema.Table.Clone()
	desired.Clauses[len(desired.Clauses)-1].Name = "last_complete"
	full := contract.SchemaDesignWriteReceipt{DocumentID: d.id, Sequence: 1, Operation: "apply", Original: original.Clone(), Desired: &desired, Destination: d.target.Object.Name, NativeJSON: native.Raw, State: "committed", Verified: true, Actual: &actual}
	for _, sql := range body {
		full.Statements = append(full.Statements, sql.(string))
	}
	clone := full.Clone()
	clone.Original.Schema.SQLite.Entries[0].SQL = "changed"
	clone.Actual.Schema.Table.Clauses[1999].Name = "changed"
	clone.Statements[2] = "changed"
	if full.NativeJSON != native.Raw || full.Original.Schema.SQLite.Entries[0].SQL == "changed" || full.Actual.Schema.Table.Clauses[1999].Name != "last_complete" || full.Statements[2] == "changed" {
		t.Fatal("original native evidence was replaced by projection")
	}
	row := nativeExecute(t, lease(t, s, 1), "SELECT c0000,last_complete FROM leased_wide")
	if row.Sets[0].Rows[0][0].Text != "9007199254740993" || row.Sets[0].Rows[0][1].Text != "X'61'" {
		t.Fatal("guarded wide ALTER changed original typed data")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	replacement := reserveSchemaUnit(t, s, "wide-original", "leased_wide")
	if _, err := s.schemaSnapshotGuardScoped(actual, replacement.scope); !errors.Is(err, schemaddl.ErrConflict) {
		t.Fatal("document ID reuse resurrected original transaction proof", err)
	}
	p.ID, p.Username, p.RQLiteOptions.ReadOnly = 82, "reader", true
	reader := openNative(t, p, "reader-password")
	readDocument := reserveSchemaUnit(t, reader, "read-only-wide", "leased_wide")
	readOnly, err := readDocument.Operations().Snapshot(context.Background(), readDocument.target)
	if err != nil || len(readOnly.Schema.Table.Columns()) != 2000 || len(readOnly.Schema.Table.Definitions) != 200 || readDocument.Operations().Apply != nil || reader.Capabilities().DesignTables {
		t.Fatal("read-only document lost wide schema or published writes", err)
	}
	retainNativeSchemaSnapshot(t, "schema-document-wide-query-only", readOnly)
}

func TestNativeRQLiteSchemaDocumentCancelAndCloseWaitOriginalAcknowledgement(t *testing.T) {
	for _, mode := range []string{"document", "session"} {
		t.Run(mode, func(t *testing.T) {
			p := nativeFixture(t, "")
			admin := openNative(t, p, "original-fixture-password")
			nativeExecute(t, lease(t, admin, 1), "CREATE TABLE document_held(id INTEGER PRIMARY KEY,v TEXT)")
			s := openNative(t, p, "original-fixture-password")
			d := reserveSchemaUnit(t, s, "original-held", "document_held")
			h := &heldCompleteRead{base: s.transport, marker: schemaCatalogSQL, started: make(chan struct{}), ack: make(chan struct{}), original: make(chan [2]string, 1)}
			defer h.release.Do(func() { close(h.ack) })
			s.client.Transport = h
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				snapshot contract.SchemaDesignSnapshot
				err      error
			}
			done := make(chan result, 1)
			go func() { out, err := d.Operations().Snapshot(ctx, d.target); done <- result{out, err} }()
			select {
			case <-h.started:
			case <-time.After(10 * time.Second):
				t.Fatal("original leased read did not acknowledge natively")
			}
			original := <-h.original
			retainNativeFileReceipt(t, "schema-document-held-"+mode+"-original-native", original[1])
			cancel()
			closed := make(chan struct{})
			go func() {
				if mode == "session" {
					_ = s.Close()
				} else {
					_ = d.Close()
				}
				close(closed)
			}()
			select {
			case <-done:
				t.Fatal("cancellation abandoned original leased acknowledgement")
			case <-closed:
				t.Fatal("close abandoned original leased acknowledgement")
			case <-time.After(40 * time.Millisecond):
			}
			h.release.Do(func() { close(h.ack) })
			select {
			case r := <-done:
				if !errors.Is(r.err, context.Canceled) && !errors.Is(r.err, contract.ErrRevoked) || r.snapshot.Schema.SQLite != nil {
					t.Fatal("closed original document published baseline", r.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original document read did not settle")
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("close did not finish after original acknowledgement")
			}
			if h.requests.Load() != 1 || !admin.Alive() || d.scope != "" {
				t.Fatal("document close replayed original request or retained original scope")
			}
			if mode == "document" && !s.Alive() {
				t.Fatal("document close revoked another owner")
			}
		})
	}
}
