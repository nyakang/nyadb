package manticore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func nativeSchema(t *testing.T, s *Session, table, id string) (contract.SchemaDesignDocumentLease, contract.SchemaDesignTarget) {
	t.Helper()
	target := contract.SchemaDesignTarget{Generation: 1, Object: s.Namespace("Manticore").Object("table", table)}
	d, e := s.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: id, Target: target})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := d.Close(); e != nil {
			t.Error(e)
		}
	})
	return d, target
}
func schemaOriginal(t *testing.T, d contract.SchemaDesignDocumentLease, target contract.SchemaDesignTarget) contract.SchemaDesignSnapshot {
	t.Helper()
	v, e := d.Operations().Snapshot(context.Background(), target)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func schemaAddRequest(original contract.SchemaDesignSnapshot, name, typ string) contract.SchemaDesignRequest {
	desired := original.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:" + name, Kind: "column", Name: name, SQL: typ})
	return contract.SchemaDesignRequest{Original: original.Clone(), Desired: desired, AllowColumnDrop: true}
}
func TestNativeSchemaOriginalColumnTypesPlansAndReceipts(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 8001)
	table := createNativeTable(t, q)
	runNative(t, q, "INSERT INTO "+table+" (id,title,qty,tag) VALUES (1,'hello',7,'original')")
	d, target := nativeSchema(t, s, table, "original-types")
	ops := d.Operations()
	ctx := context.Background()
	for _, typ := range []string{"integer", "bigint", "float", "bool", "timestamp", "string", "multi", "multi64", "json", "text"} {
		t.Run(typ, func(t *testing.T) {
			original := schemaOriginal(t, d, target)
			r := schemaAddRequest(original, "added_"+typ, typ)
			p, e := ops.Plan(r)
			if e != nil || len(p.SQL) != 1 || !strings.Contains(p.ScopeHint, "不能通过回滚撤销") {
				t.Fatal(p, e)
			}
			out := ops.Apply(ctx, r)
			if out.Error != nil || out.State != "committed" || !out.Verified || out.Rows != 0 || out.Actual == nil || out.Receipt == nil || out.Receipt.Original.Schema.DDL != original.Schema.DDL {
				t.Fatal(out)
			}
			var native nativeWriteReceipt
			if e = json.Unmarshal([]byte(out.Receipt.NativeJSON), &native); e != nil || !native.Acknowledged || native.AffectedRows != 0 || native.ErrorCode != 0 {
				t.Fatal(native, e)
			}
			observed := runNative(t, q, "SELECT id,title,qty,tag FROM "+table)
			if fmt.Sprint(observed.Rows) != "[[1 hello 7 original]]" {
				t.Fatal(observed)
			}
			actual := out.Actual.Clone()
			first := d.OriginalOutcome()
			out.Receipt.Statements[0] = "forged"
			out.Actual.Schema.Table.Clauses[0].Name = "forged"
			if reflect.DeepEqual(out, d.OriginalOutcome()) || !reflect.DeepEqual(first, d.OriginalOutcome()) {
				t.Fatal("original receipt aliased caller")
			}
			drop := contract.SchemaDesignRequest{Original: actual, Desired: actual.Schema.Table.Clone(), AllowColumnDrop: true}
			for i, c := range drop.Desired.Clauses {
				if c.Name == "added_"+typ {
					drop.Desired.Clauses = append(drop.Desired.Clauses[:i], drop.Desired.Clauses[i+1:]...)
					break
				}
			}
			out = ops.Apply(ctx, drop)
			if out.Error != nil || !out.Verified || out.State != "committed" || out.Actual.Schema.DDL != original.Schema.DDL {
				t.Fatal(out)
			}
			objectEvidence(t, "schema-"+typ+"-drop-receipt.json", out.Receipt)
			// The exact native definition is back to its original form, but
			// its previously consumed source proof must never authorize replay.
			last := d.OriginalOutcome()
			again := ops.Apply(ctx, r)
			if !errors.Is(again.Error, schemaddl.ErrConflict) || again.State != "not_executed" || again.Receipt != nil || !reflect.DeepEqual(last, d.OriginalOutcome()) {
				t.Fatal("consumed schema request replayed", again)
			}
			if got := schemaOriginal(t, d, target); got.Schema.DDL != original.Schema.DDL || got.Schema.Revision == original.Schema.Revision {
				t.Fatal("native schema changed or source proof reused", got)
			}
		})
	}
	// Original proof is scoped to one document, target and exact snapshot bytes.
	original := schemaOriginal(t, d, target)
	r := schemaAddRequest(original, "candidate", "integer")
	other, _ := nativeSchema(t, s, table, "foreign-document")
	if _, e := other.Operations().Plan(r); !errors.Is(e, schemaddl.ErrConflict) {
		t.Fatal(e)
	}
	for _, mode := range []string{"schema", "identity", "generation", "ddl", "revision"} {
		changed := r.Clone()
		switch mode {
		case "schema":
			changed.Original.Schema.Table.Clauses[1].SQL = "integer"
		case "identity":
			changed.Original.Target.Object.ProfileID++
		case "generation":
			changed.Original.Target.Generation++
		case "ddl":
			changed.Original.Schema.DDL += " "
		case "revision":
			changed.Original.Schema.Revision = "forged"
		}
		out := ops.Apply(ctx, changed)
		if out.Error == nil || out.State != "not_executed" || out.Receipt != nil {
			t.Fatal(mode, out)
		}
	}
	pq := createNativePercolate(t, q)
	pqDoc, pqTarget := nativeSchema(t, s, pq, "percolate-source")
	if _, e := pqDoc.Operations().Snapshot(ctx, pqTarget); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal("percolate match schema admitted", e)
	}
}
func TestNativeSchemaOriginalFailuresAndPostflightMismatch(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 8002)
	for _, mode := range []string{"before-preflight", "native-error", "after-preflight"} {
		t.Run(mode, func(t *testing.T) {
			table := createNativeTable(t, q)
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			d, target := nativeSchema(t, s, table, "schema-"+mode)
			ops := d.Operations()
			r := schemaAddRequest(schemaOriginal(t, d, target), "candidate", "integer")
			if mode == "before-preflight" {
				runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN external integer")
				out := ops.Apply(context.Background(), r)
				if !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil || out.State != "not_executed" {
					t.Fatal(out)
				}
				objectEvidence(t, "schema-conflict.json", out)
				return
			}
			observed := gate.arm("SHOW CREATE TABLE "+quote(table), "hold")
			done := make(chan contract.SchemaDesignOutcome, 1)
			go func() { done <- ops.Apply(context.Background(), r) }()
			observedPacket(t, gate, observed)
			name := "external"
			if mode == "native-error" {
				name = "candidate"
			}
			runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN "+name+" integer")
			close(gate.resume)
			var out contract.SchemaDesignOutcome
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original schema executor did not settle")
			}
			if out.Receipt == nil || out.Error == nil || out.Verified {
				t.Fatal(out)
			}
			var native nativeWriteReceipt
			if e := json.Unmarshal([]byte(out.Receipt.NativeJSON), &native); e != nil || !native.Acknowledged {
				t.Fatal(native, e)
			}
			if mode == "native-error" {
				if native.ErrorCode != 1064 || native.SQLState != "42000" || out.State != "not_executed" {
					t.Fatal(out, native)
				}
			} else {
				if native.ErrorCode != 0 || out.State != "committed" || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) {
					t.Fatal(out, native)
				}
				next := schemaAddRequest(schemaOriginal(t, d, target), "later", "integer")
				if _, e := ops.Plan(next); !errors.Is(e, contract.ErrSchemaCommitUncertain) {
					t.Fatal("postflight read unlocked original document", e)
				}
			}
			objectEvidence(t, "schema-"+mode+"-receipt.json", out.Receipt)
		})
	}
}
func TestNativeSchemaOriginalLostCanceledClosedReceiptAndNoReplay(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 8003)
	for _, mode := range []string{"drop", "cancel", "document-close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			table := createNativeTable(t, q)
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			d, target := nativeSchema(t, s, table, "schema-"+mode)
			ops := d.Operations()
			r := schemaAddRequest(schemaOriginal(t, d, target), "candidate", "integer")
			p, e := ops.Plan(r)
			if e != nil {
				t.Fatal(e)
			}
			gateMode := "hold"
			if mode == "drop" {
				gateMode = "drop"
			}
			observed := gate.arm(p.SQL[0], gateMode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan contract.SchemaDesignOutcome, 1)
			go func() { done <- ops.Apply(ctx, r) }()
			observedPacket(t, gate, observed)
			switch mode {
			case "cancel":
				cancel()
			case "document-close":
				if e = d.Close(); e != nil {
					t.Fatal(e)
				}
			case "session-close":
				if e = s.Close(); e != nil {
					t.Fatal(e)
				}
			}
			var out contract.SchemaDesignOutcome
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original schema executor did not settle")
			}
			if !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) || out.State != "uncertain" || out.Verified || out.Receipt == nil || gate.calls.Load() != 1 {
				t.Fatal(out)
			}
			original := out.Receipt.Clone()
			disarm(gate)
			rows := runNative(t, q, "DESCRIBE "+table)
			found := false
			for _, row := range rows.Rows {
				found = found || row[0] == "candidate"
			}
			if !found {
				t.Fatal("withheld original native OK did not apply")
			}
			if mode == "drop" || mode == "cancel" {
				current := schemaOriginal(t, d, target)
				next := schemaAddRequest(current, "later", "integer")
				if _, e = ops.Plan(next); !errors.Is(e, contract.ErrSchemaCommitUncertain) {
					t.Fatal(e)
				}
				again := ops.Apply(context.Background(), next)
				if !errors.Is(again.Error, contract.ErrSchemaCommitUncertain) || gate.calls.Load() != 1 || !reflect.DeepEqual(d.OriginalOutcome().Receipt, &original) {
					t.Fatal("original schema request replayed or receipt repaired", again)
				}
			}
			objectEvidence(t, "schema-"+mode+"-receipt.json", out.Receipt)
		})
	}
}

func TestNativeSchemaSnapshotCancellationAndClose(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 8004)
	table := createNativeTable(t, q)
	for _, mode := range []string{"cancel", "document-close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			d, target := nativeSchema(t, s, table, "schema-read-"+mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observed := gate.arm("SHOW CREATE TABLE "+quote(table), "hold")
			type result struct {
				snapshot contract.SchemaDesignSnapshot
				err      error
			}
			done := make(chan result, 1)
			go func() { v, e := d.Operations().Snapshot(ctx, target); done <- result{v, e} }()
			observedPacket(t, gate, observed)
			switch mode {
			case "cancel":
				cancel()
			case "document-close":
				if e := d.Close(); e != nil {
					t.Fatal(e)
				}
			case "session-close":
				if e := s.Close(); e != nil {
					t.Fatal(e)
				}
			}
			select {
			case v := <-done:
				if v.err == nil || v.snapshot.Schema.Revision != "" || d.OriginalOutcome() != nil {
					t.Fatal("canceled schema read published a source or receipt", v)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("original schema read did not acknowledge cancellation")
			}
			disarm(gate)
			if mode == "cancel" {
				fresh := schemaOriginal(t, d, target)
				if fresh.Schema.Revision == "" || fresh.Schema.Table.Name != table {
					t.Fatal(fresh)
				}
			}
		})
	}
}
