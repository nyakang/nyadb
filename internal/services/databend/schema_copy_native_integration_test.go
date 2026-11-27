package databend

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestDatabendSchemaCopyStructureDataAndOriginalGuards(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 821, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	name := `copy_%猫"source59`
	object := s.Namespace("").Object("table", name)
	providerQuery(t, q, f.Database, "CREATE TABLE "+qualified(object)+" (id UInt64 NOT NULL, payload Binary, label String DEFAULT 'retained', missing Nullable(Int64), exact Decimal(38,20), day Date, instant Timestamp, flag Boolean)")
	values := "(18446744073709551615,unhex('00ff80'),NULL,9007199254740993.12345678901234567890,'2024-02-29','2024-02-29 12:34:56.123456',true),(9007199254740993,unhex('fe00'),-9223372036854775808,-0.00000000000000000001,'1970-01-01','1970-01-01 00:00:00',false),(9007199254740993,unhex('fe00'),-9223372036854775808,-0.00000000000000000001,'1970-01-01','1970-01-01 00:00:00',false)"
	providerQuery(t, q, f.Database, "INSERT INTO "+qualified(object)+" (id,payload,missing,exact,day,instant,flag) VALUES "+values)
	read := func(o contract.ObjectRef) [][]string {
		return localNativeQuery(t, f.localTransportFixture, "SELECT id,hex(payload),label,missing,exact,day,instant,flag FROM "+qualified(o)+" ORDER BY id")
	}
	before := read(object)
	lease, snapshot := nativeSchemaDocument(t, s, "original-copy59", name)
	sibling, _ := nativeSchemaDocument(t, s, "sibling-copy59", name)
	request := contract.SchemaCopyRequest{Original: snapshot, Destination: `copy_%猫"data59`, WithData: true}
	if out := sibling.Operations().Copy(context.Background(), request); out.State != "not_executed" || !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
		t.Fatal("sibling proof admitted", out)
	}
	for _, flag := range []string{"rebind", "rowid", "forged"} {
		r := request.Clone()
		switch flag {
		case "rebind":
			r.RebindSelfReferences = true
		case "rowid":
			r.PreserveRowID = true
		case "forged":
			r.Original.Schema.Databend.Columns[0].Name = "forged"
		}
		if out := lease.Operations().Copy(context.Background(), r); out.State != "not_executed" || out.Error == nil || out.Receipt != nil {
			t.Fatal("unsupported flags/proof admitted", out)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out := lease.Operations().Copy(ctx, request); out.State != "not_executed" || out.Error == nil || out.Receipt != nil {
		t.Fatal("pre-canceled copy dispatched", out)
	}
	providerQuery(t, q, f.Database, "CREATE TABLE copy_occupied59(id UInt64)")
	occupied := request.Clone()
	occupied.Destination = "copy_occupied59"
	if out := lease.Operations().Copy(context.Background(), occupied); out.State != "not_executed" || !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
		t.Fatal("occupied target admitted", out)
	}
	out := lease.Operations().Copy(context.Background(), request)
	if out.State != "committed" || !out.Verified || out.Error != nil || out.Actual == nil || out.Receipt == nil || out.Actual.Schema.Databend.TableID == snapshot.Schema.Databend.TableID || out.Receipt.Sequence != 1 {
		t.Fatal("original data copy not verified", out.State, out.Error)
	}
	copied := object
	copied.Name = request.Destination
	after := read(copied)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(before, read(object)) {
		t.Fatal("copy changed exact values/multiplicity", before, after)
	}
	current, err := lease.Operations().Snapshot(context.Background(), snapshot.Target)
	if err != nil || !reflect.DeepEqual(current, snapshot) {
		t.Fatal("copy retargeted original document", err)
	}
	retained := lease.OriginalOutcome()
	out.Actual.Schema.Databend.Columns[0].Name = "caller mutation"
	if !reflect.DeepEqual(retained, lease.OriginalOutcome()) {
		t.Fatal("caller mutated proof/receipt")
	}
	emptyLease, emptySnapshot := nativeSchemaDocument(t, s, "empty-copy59", name)
	empty := emptyLease.Operations().Copy(context.Background(), contract.SchemaCopyRequest{Original: emptySnapshot, Destination: "copy_empty59"})
	if empty.State != "committed" || !empty.Verified || empty.Error != nil || empty.Receipt == nil {
		t.Fatal("empty copy failed", empty.State, empty.Error)
	}
	emptyRows := localNativeQuery(t, f.localTransportFixture, "SELECT count(*) FROM "+schemaddl.Quote(f.Database)+".copy_empty59")
	if !reflect.DeepEqual(emptyRows, [][]string{{"0"}}) {
		t.Fatal("structure copy retained rows", emptyRows)
	}
	providerQuery(t, q, f.Database, "CREATE TABLE copy_stale59(id UInt64)")
	stale, old := nativeSchemaDocument(t, s, "stale-copy59", "copy_stale59")
	providerQuery(t, q, f.Database, "DROP TABLE copy_stale59; CREATE TABLE copy_stale59(id UInt64)")
	if result := stale.Operations().Copy(context.Background(), contract.SchemaCopyRequest{Original: old, Destination: "copy_bad59"}); result.State != "not_executed" || !errors.Is(result.Error, schemaddl.ErrConflict) {
		t.Fatal("recreated source accepted", result)
	}
	for _, unsupported := range []struct{ name, typ string }{{"copy_float59", "Float64"}, {"copy_array59", "Array(String)"}} {
		providerQuery(t, q, f.Database, "CREATE TABLE "+unsupported.name+"(v "+unsupported.typ+")")
		original, snapshot := nativeSchemaDocument(t, s, unsupported.name, unsupported.name)
		for _, data := range []bool{false, true} {
			out := original.Operations().Copy(context.Background(), contract.SchemaCopyRequest{Original: snapshot, Destination: unsupported.name + "_dest", WithData: data})
			if out.State != "not_executed" || !errors.Is(out.Error, contract.ErrUnsupported) || out.Receipt != nil {
				t.Fatal("unverified type dispatched COPY", out)
			}
		}
		rows := localNativeQuery(t, f.localTransportFixture, "SELECT name FROM system.tables WHERE database="+quoteText(f.Database)+" AND name="+quoteText(unsupported.name+"_dest"))
		if len(rows) != 0 {
			t.Fatal("rejected column created a destination", rows)
		}
	}
	writeDataEvidence(t, f, "schema-copy.json", map[string]any{"original": retained, "empty": empty, "before": before, "after": after, "empty_rows": emptyRows, "sibling_rejected": true, "flags_rejected": true, "recreated_rejected": true, "occupied_rejected": true, "source_document_preserved": true, "unverified_types_rejected": true})
}
