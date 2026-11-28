package manticore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func schemaTableFixture(t *testing.T, q contract.QueryLease) string {
	t.Helper()
	name := fmt.Sprintf("nyadb_schema_table81_%d", time.Now().UnixNano())
	runNative(t, q, "CREATE TABLE "+name+" (title text, qty integer) min_word_len='2'")
	runNative(t, q, "INSERT INTO "+name+" (id,title,qty) VALUES (1,'hello',7),(2,'world',9)")
	t.Cleanup(func() {
		_, err := q.Execute(context.Background(), "Manticore", "DROP TABLE IF EXISTS "+name)
		if err != nil && !errors.Is(err, contract.ErrRevoked) {
			t.Error(err)
		}
	})
	return name
}
func schemaTableExecutor(d contract.SchemaDesignDocumentLease, op string) func(context.Context, contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
	if op == "drop" {
		return d.Operations().Drop
	}
	return d.Operations().Truncate
}
func TestNativeSchemaTableOriginalTruncateDropAndConsumedProof(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 8101)
	unaffected := schemaTableFixture(t, q)
	for _, op := range []string{"truncate", "drop"} {
		t.Run(op, func(t *testing.T) {
			table := schemaTableFixture(t, q)
			d, target := nativeSchema(t, s, table, "table-"+op)
			original := schemaOriginal(t, d, target)
			run := schemaTableExecutor(d, op)
			other, _ := nativeSchema(t, s, table, "foreign-"+op)
			if out := schemaTableExecutor(other, op)(context.Background(), original); !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
				t.Fatal(out)
			}
			bad := original.Clone()
			bad.Schema.DDL += " "
			if out := run(context.Background(), bad); !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
				t.Fatal(out)
			}
			out := run(context.Background(), original)
			if out.Error != nil || out.State != "committed" || !out.Verified || out.Rows != 0 || out.Actual == nil || out.Receipt == nil || out.Receipt.Operation != op || out.Receipt.Original.Schema.DDL != original.Schema.DDL {
				t.Fatal(out)
			}
			var native nativeWriteReceipt
			if err := json.Unmarshal([]byte(out.Receipt.NativeJSON), &native); err != nil || !native.Acknowledged || native.AffectedRows != 0 || native.ErrorCode != 0 {
				t.Fatal(native, err)
			}
			if op == "drop" {
				if out.Actual.Schema.Exists || out.Actual.Schema.DDL != "" {
					t.Fatal(out)
				}
				if err := s.schemaNameAbsent(context.Background(), target); err != nil {
					t.Fatal(err)
				}
				runNative(t, q, "CREATE TABLE "+table+" (title text, qty integer) min_word_len='2'")
			} else {
				if !out.Actual.Schema.Exists || out.Actual.Schema.DDL != original.Schema.DDL || out.Actual.Schema.Revision == original.Schema.Revision {
					t.Fatal(out)
				}
				if rows := runNative(t, q, "SELECT id FROM "+table); len(rows.Rows) != 0 {
					t.Fatal(rows)
				}
			}
			runNative(t, q, "INSERT INTO "+table+" (id,title,qty) VALUES (3,'new',12)")
			retained := d.OriginalOutcome()
			if again := run(context.Background(), original); !errors.Is(again.Error, schemaddl.ErrConflict) || again.Receipt != nil || !reflect.DeepEqual(retained, d.OriginalOutcome()) {
				t.Fatal("consumed request replayed", again)
			}
			if rows := runNative(t, q, "SELECT id,title,qty FROM "+table); fmt.Sprint(rows.Rows) != "[[3 new 12]]" {
				t.Fatal(rows)
			}
			if rows := runNative(t, q, "SELECT id,title,qty FROM "+unaffected+" ORDER BY id ASC"); fmt.Sprint(rows.Rows) != "[[1 hello 7] [2 world 9]]" {
				t.Fatal(rows)
			}
			objectEvidence(t, "schema-table-"+op+"-receipt.json", out.Receipt)
		})
	}
}
func TestNativeSchemaTableConcurrentConflictNativeFailureAndPostflight(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 8102)
	for _, op := range []string{"truncate", "drop"} {
		for _, mode := range []string{"before-preflight", "native-error", "after-ack"} {
			t.Run(op+"/"+mode, func(t *testing.T) {
				table := schemaTableFixture(t, q)
				proxied, gate := gateConfig(t, cfg)
				s := openNative(t, proxied)
				d, target := nativeSchema(t, s, table, op+"-"+mode)
				original := schemaOriginal(t, d, target)
				run := schemaTableExecutor(d, op)
				if mode == "before-preflight" {
					runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN external integer")
					out := run(context.Background(), original)
					if !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
						t.Fatal(out)
					}
					return
				}
				sql := "SHOW CREATE TABLE " + quote(table)
				if mode == "after-ack" {
					p, e := schemaddl.PlanManticoreRTTableOperation(original.Schema, op)
					if e != nil {
						t.Fatal(e)
					}
					sql = p.SQL[0]
				}
				observed := gate.arm(sql, "hold")
				done := make(chan contract.SchemaDesignOutcome, 1)
				go func() { done <- run(context.Background(), original) }()
				observedPacket(t, gate, observed)
				if mode == "native-error" {
					runNative(t, q, "DROP TABLE "+table)
				} else if op == "drop" {
					runNative(t, q, "CREATE TABLE "+table+" (title text, qty integer) min_word_len='2'")
				} else {
					runNative(t, q, "INSERT INTO "+table+" (id,title,qty) VALUES (3,'concurrent',12)")
				}
				close(gate.resume)
				var out contract.SchemaDesignOutcome
				select {
				case out = <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("native table operation did not settle")
				}
				if out.Receipt == nil || out.Error == nil || out.Verified {
					t.Fatal(out)
				}
				var native nativeWriteReceipt
				if e := json.Unmarshal([]byte(out.Receipt.NativeJSON), &native); e != nil || !native.Acknowledged {
					t.Fatal(native, e)
				}
				if mode == "native-error" {
					if out.State != "not_executed" || native.ErrorCode == 0 {
						t.Fatal(out)
					}
				} else {
					if out.State != "committed" || native.ErrorCode != 0 || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) {
						t.Fatal(out)
					}
					if again := run(context.Background(), original); !errors.Is(again.Error, contract.ErrSchemaCommitUncertain) {
						t.Fatal(again)
					}
				}
				objectEvidence(t, "schema-table-"+op+"-"+mode+".json", out.Receipt)
			})
		}
	}
}
func TestNativeSchemaTableOriginalLostCanceledClosedReceipt(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 8103)
	for _, op := range []string{"truncate", "drop"} {
		for _, mode := range []string{"lost", "cancel", "document-close", "session-close"} {
			t.Run(op+"/"+mode, func(t *testing.T) {
				table := schemaTableFixture(t, q)
				proxied, gate := gateConfig(t, cfg)
				s := openNative(t, proxied)
				d, target := nativeSchema(t, s, table, op+"-"+mode)
				original := schemaOriginal(t, d, target)
				run := schemaTableExecutor(d, op)
				p, e := schemaddl.PlanManticoreRTTableOperation(original.Schema, op)
				if e != nil {
					t.Fatal(e)
				}
				gateMode := "hold"
				if mode == "lost" {
					gateMode = "drop"
				}
				observed := gate.arm(p.SQL[0], gateMode)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan contract.SchemaDesignOutcome, 1)
				go func() { done <- run(ctx, original) }()
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
					t.Fatal("original table executor did not settle")
				}
				if out.State != "uncertain" || out.Verified || out.Receipt == nil || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) || gate.calls.Load() != 1 {
					t.Fatal(out)
				}
				captured := out.Receipt.Clone()
				disarm(gate)
				if op == "drop" {
					if e = control.schemaNameAbsent(context.Background(), contract.SchemaDesignTarget{Object: control.Namespace("Manticore").Object("table", table), Generation: 1}); e != nil {
						t.Fatal(e)
					}
				} else {
					if rows := runNative(t, q, "SELECT id FROM "+table); len(rows.Rows) != 0 {
						t.Fatal(rows)
					}
				}
				if mode == "lost" || mode == "cancel" {
					if op == "drop" {
						runNative(t, q, "CREATE TABLE "+table+" (title text, qty integer) min_word_len='2'")
					}
					fresh := schemaOriginal(t, d, target)
					if again := run(context.Background(), fresh); !errors.Is(again.Error, contract.ErrSchemaCommitUncertain) || gate.calls.Load() != 1 || !reflect.DeepEqual(d.OriginalOutcome().Receipt, &captured) {
						t.Fatal(again)
					}
				}
				objectEvidence(t, "schema-table-"+op+"-"+mode+".json", out.Receipt)
			})
		}
	}
}
