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

func schemaCreateName(t *testing.T, q contract.QueryLease) string {
	t.Helper()
	name := fmt.Sprintf("nyadb_schema_create82_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, err := q.Execute(context.Background(), "Manticore", "DROP TABLE IF EXISTS "+name)
		if err != nil && !errors.Is(err, contract.ErrRevoked) {
			t.Error(err)
		}
	})
	return name
}
func schemaCreateRequest(original contract.SchemaDesignSnapshot, allTypes bool) contract.SchemaDesignRequest {
	desired := schemaddl.Table{Name: original.Target.Object.Name, Clauses: []schemaddl.Clause{{ID: "native:id", Kind: "column", Name: "id", SQL: "bigint"}}}
	types := []string{"text"}
	if allTypes {
		types = []string{"integer", "bigint", "float", "bool", "timestamp", "string", "multi", "multi64", "json", "text"}
	}
	for _, typ := range types {
		desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:" + typ, Kind: "column", Name: "value_" + typ, SQL: typ})
	}
	return contract.SchemaDesignRequest{Original: original.Clone(), Desired: desired}
}
func TestNativeSchemaCreateAbsentTypesOriginalReceiptAndConsumedProof(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 8201)
	unaffected := schemaTableFixture(t, q)
	table := schemaCreateName(t, q)
	d, target := nativeSchema(t, s, table, "create-original")
	original := schemaOriginal(t, d, target)
	if original.Schema.Exists || original.Schema.DDL != "" || original.Schema.Revision == "" || !reflect.DeepEqual(original.Schema.Table, schemaddl.Table{Name: table}) {
		t.Fatal("complete absence not retained", original)
	}
	r := schemaCreateRequest(original, true)
	other, _ := nativeSchema(t, s, table, "create-foreign")
	if out := other.Operations().Apply(context.Background(), r); !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
		t.Fatal(out)
	}
	for _, mode := range []string{"exists", "ddl", "revision", "table", "target", "generation"} {
		bad := r.Clone()
		switch mode {
		case "exists":
			bad.Original.Schema.Exists = true
		case "ddl":
			bad.Original.Schema.DDL = "CREATE TABLE forged (id bigint)"
		case "revision":
			bad.Original.Schema.Revision = "forged"
		case "table":
			bad.Original.Schema.Table.Clauses = r.Desired.Clone().Clauses
		case "target":
			bad.Original.Target.Object.ProfileID++
		case "generation":
			bad.Original.Target.Generation++
		}
		if out := d.Operations().Apply(context.Background(), bad); out.Error == nil || out.Receipt != nil {
			t.Fatal(mode, out)
		}
	}
	p, err := d.Operations().Plan(r)
	if err != nil || p.Mode != "native_create" || len(p.SQL) != 1 || strings.Contains(p.SQL[0], "IF NOT EXISTS") {
		t.Fatal(p, err)
	}
	out := d.Operations().Apply(context.Background(), r)
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Rows != 0 || out.Actual == nil || out.Actual.Target != target || !out.Actual.Schema.Exists || out.Receipt == nil || out.Receipt.Operation != "apply" || out.Receipt.Sequence != 1 || out.Receipt.Original.Schema.Exists || !reflect.DeepEqual(out.Receipt.Desired, &r.Desired) {
		t.Fatal(out)
	}
	if err = schemaddl.VerifyManticoreRT(r.Desired, out.Actual.Schema.Table); err != nil {
		t.Fatal(err)
	}
	var native nativeWriteReceipt
	if err = json.Unmarshal([]byte(out.Receipt.NativeJSON), &native); err != nil || !native.Dispatched || !native.Acknowledged || native.AffectedRows != 0 || native.ErrorCode != 0 {
		t.Fatal(native, err)
	}
	runNative(t, q, "INSERT INTO "+table+" (id,value_integer,value_bigint,value_float,value_bool,value_timestamp,value_string,value_multi,value_multi64,value_json,value_text) VALUES (1,7,-9,1.25,1,1700000000,'original',(1,2),(4294967296),'{\"key\":7}','hello')")
	if got := runNative(t, q, "SELECT id,value_integer,value_string,value_text FROM "+table); fmt.Sprint(got.Rows) != "[[1 7 original hello]]" {
		t.Fatal(got)
	}
	if got := runNative(t, q, "SELECT id,title,qty FROM "+unaffected+" ORDER BY id ASC"); fmt.Sprint(got.Rows) != "[[1 hello 7] [2 world 9]]" {
		t.Fatal(got)
	}
	objectEvidence(t, "schema-create-types-receipt.json", out.Receipt)
	retained := d.OriginalOutcome()
	out.Receipt.Desired.Clauses[0].Name = "forged"
	out.Actual.Schema.Table.Clauses[0].Name = "forged"
	if !reflect.DeepEqual(retained, d.OriginalOutcome()) {
		t.Fatal("caller changed original receipt")
	}
	runNative(t, q, "DROP TABLE "+table)
	if again := d.Operations().Apply(context.Background(), r); !errors.Is(again.Error, schemaddl.ErrConflict) || again.Receipt != nil || !reflect.DeepEqual(retained, d.OriginalOutcome()) {
		t.Fatal("consumed absent proof replayed", again)
	}
	fresh := schemaOriginal(t, d, target)
	if fresh.Schema.Exists || fresh.Schema.Revision == original.Schema.Revision {
		t.Fatal(fresh)
	}
	idOnly := schemaCreateRequest(fresh, false)
	idOnly.Desired.Clauses = idOnly.Desired.Clauses[:1]
	out = d.Operations().Apply(context.Background(), idOnly)
	if out.Error != nil || !out.Verified || out.Receipt.Sequence != 2 || len(out.Actual.Schema.Table.Clauses) != 1 {
		t.Fatal(out)
	}
	objectEvidence(t, "schema-create-id-only-receipt.json", out.Receipt)
}

func TestNativeSchemaCreateOriginalConflictNativeErrorAndPostflight(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 8202)
	for _, mode := range []string{"before-preflight", "native-error", "after-ack-change", "after-ack-drop"} {
		t.Run(mode, func(t *testing.T) {
			table := schemaCreateName(t, q)
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			d, target := nativeSchema(t, s, table, "create-"+mode)
			r := schemaCreateRequest(schemaOriginal(t, d, target), false)
			if mode == "before-preflight" {
				runNative(t, q, "CREATE TABLE "+table+" (external text)")
				out := d.Operations().Apply(context.Background(), r)
				if !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
					t.Fatal(out)
				}
				return
			}
			p, err := d.Operations().Plan(r)
			if err != nil {
				t.Fatal(err)
			}
			command := p.SQL[0]
			if mode == "native-error" {
				command = "SHOW TABLES"
			}
			observed := gate.arm(command, "hold")
			done := make(chan contract.SchemaDesignOutcome, 1)
			go func() { done <- d.Operations().Apply(context.Background(), r) }()
			observedPacket(t, gate, observed)
			switch mode {
			case "native-error":
				runNative(t, q, "CREATE TABLE "+table+" (external text)")
			case "after-ack-change":
				runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN external integer")
			case "after-ack-drop":
				runNative(t, q, "DROP TABLE "+table)
			}
			close(gate.resume)
			var out contract.SchemaDesignOutcome
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original CREATE did not settle")
			}
			if out.Receipt == nil || out.Error == nil || out.Verified || out.Actual != nil {
				t.Fatal(out)
			}
			var native nativeWriteReceipt
			if err = json.Unmarshal([]byte(out.Receipt.NativeJSON), &native); err != nil || !native.Dispatched || !native.Acknowledged {
				t.Fatal(native, err)
			}
			if mode == "native-error" {
				if out.State != "not_executed" || native.ErrorCode != 1064 {
					t.Fatal(out)
				}
			} else if out.State != "committed" || native.ErrorCode != 0 || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) {
				t.Fatal(out)
			}
			objectEvidence(t, "schema-create-"+mode+".json", out.Receipt)
		})
	}
}

func TestNativeSchemaCreateLostCanceledClosedReceiptAndNoReplay(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 8203)
	for _, mode := range []string{"lost", "cancel", "document-close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			table := schemaCreateName(t, q)
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			d, target := nativeSchema(t, s, table, "create-"+mode)
			r := schemaCreateRequest(schemaOriginal(t, d, target), false)
			p, err := d.Operations().Plan(r)
			if err != nil {
				t.Fatal(err)
			}
			gateMode := "hold"
			if mode == "lost" {
				gateMode = "drop"
			}
			observed := gate.arm(p.SQL[0], gateMode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			apply := d.Operations().Apply
			done := make(chan contract.SchemaDesignOutcome, 1)
			go func() { done <- apply(ctx, r) }()
			observedPacket(t, gate, observed)
			switch mode {
			case "cancel":
				cancel()
			case "document-close":
				if err = d.Close(); err != nil {
					t.Fatal(err)
				}
			case "session-close":
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var out contract.SchemaDesignOutcome
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original CREATE executor did not settle")
			}
			if out.State != "uncertain" || out.Verified || out.Actual != nil || out.Receipt == nil || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) || gate.calls.Load() != 1 {
				t.Fatal(out)
			}
			captured := out.Receipt.Clone()
			disarm(gate)
			if rows := runNative(t, q, "DESCRIBE "+table); len(rows.Rows) != 2 {
				t.Fatal(rows)
			}
			if mode == "lost" || mode == "cancel" {
				observed := schemaOriginal(t, d, target)
				if !observed.Schema.Exists || !reflect.DeepEqual(&captured, d.OriginalOutcome().Receipt) {
					t.Fatal("observation repaired original receipt")
				}
				runNative(t, q, "DROP TABLE "+table)
				fresh := schemaOriginal(t, d, target)
				if again := apply(context.Background(), schemaCreateRequest(fresh, false)); !errors.Is(again.Error, contract.ErrSchemaCommitUncertain) || gate.calls.Load() != 1 || !reflect.DeepEqual(&captured, d.OriginalOutcome().Receipt) {
					t.Fatal("fresh absence replayed unknown CREATE", again)
				}
			}
			objectEvidence(t, "schema-create-"+mode+".json", out.Receipt)
		})
	}
}

func TestNativeSchemaCreateAbsenceReadFailureNeverAuthorizesWrite(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 8204)
	for _, mode := range []string{"lost", "cancel", "document-close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			table := schemaCreateName(t, q)
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			d, target := nativeSchema(t, s, table, "absence-"+mode)
			gateMode := "hold"
			if mode == "lost" {
				gateMode = "drop"
			}
			observed := gate.arm("SHOW TABLES", gateMode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
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
					t.Fatal("failed enumeration fabricated signed absence", v)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("absence reader did not acknowledge cancellation")
			}
			objectEvidence(t, "schema-create-absence-"+mode+".json", map[string]any{"signed_absence": false, "original_receipt": d.OriginalOutcome(), "calls": gate.calls.Load()})
		})
	}
}
