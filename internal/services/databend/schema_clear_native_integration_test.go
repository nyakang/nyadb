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

func TestDatabendSchemaClearOriginalCountIdentityAndGuards(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 831, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	name := `clear_%猫"original60`
	object := s.Namespace("").Object("table", name)
	providerQuery(t, q, f.Database, "CREATE TABLE "+qualified(object)+" (id UInt64 NOT NULL, payload Binary, label String DEFAULT 'retained')")
	providerQuery(t, q, f.Database, "INSERT INTO "+qualified(object)+" (id,payload) VALUES(18446744073709551615,unhex('00ff80')),(9007199254740993,unhex('fe00'))")
	providerQuery(t, q, f.Database, "CREATE VIEW clear_dependent60 AS SELECT * FROM "+qualified(object))
	before := localNativeQuery(t, f.localTransportFixture, "SELECT id,hex(payload),label FROM "+qualified(object)+" ORDER BY id")
	lease, snapshot := nativeSchemaDocument(t, s, "original-clear60", name)
	sibling, _ := nativeSchemaDocument(t, s, "sibling-clear60", name)
	request := contract.SchemaClearRequest{Original: snapshot}
	if out := sibling.Operations().Clear(context.Background(), request); out.State != "not_executed" || !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
		t.Fatal("sibling proof admitted", out)
	}
	unsupported := request.Clone()
	unsupported.ResetIdentity = true
	if out := lease.Operations().Clear(context.Background(), unsupported); out.State != "not_executed" || !errors.Is(out.Error, contract.ErrUnsupported) || out.Receipt != nil {
		t.Fatal("unsupported identity reset admitted", out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out := lease.Operations().Clear(ctx, request); out.State != "not_executed" || out.Error == nil || out.Receipt != nil {
		t.Fatal("canceled clear dispatched", out)
	}
	out := lease.Operations().Clear(context.Background(), request)
	if out.State != "committed" || !out.Verified || out.Error != nil || out.Rows != 2 || out.Actual == nil || !reflect.DeepEqual(*out.Actual, snapshot) || out.Receipt == nil || out.Receipt.Rows != 2 || out.Receipt.Sequence != 1 {
		t.Fatal("native DELETE count/schema not verified", out.State, out.Rows, out.Error)
	}
	remaining := localNativeQuery(t, f.localTransportFixture, "SELECT count(*) FROM "+qualified(object))
	dependent := localNativeQuery(t, f.localTransportFixture, "SELECT count(*) FROM \""+f.Database+"\".clear_dependent60")
	if !reflect.DeepEqual(remaining, [][]string{{"0"}}) || !reflect.DeepEqual(remaining, dependent) {
		t.Fatal("table or dependent view not empty", remaining, dependent)
	}
	original := lease.OriginalOutcome()
	current, err := lease.Operations().Snapshot(context.Background(), snapshot.Target)
	if err != nil || !reflect.DeepEqual(current, snapshot) || !reflect.DeepEqual(original, lease.OriginalOutcome()) {
		t.Fatal("clear changed source identity/receipt", err)
	}
	emptyLease, emptySnapshot := nativeSchemaDocument(t, s, "empty-clear60", name)
	empty := emptyLease.Operations().Clear(context.Background(), contract.SchemaClearRequest{Original: emptySnapshot})
	if empty.State != "committed" || !empty.Verified || empty.Error != nil || empty.Rows != 0 || empty.Receipt == nil {
		t.Fatal("empty DELETE count not acknowledged", empty.State, empty.Error)
	}
	providerQuery(t, q, f.Database, "INSERT INTO "+qualified(object)+" (id,payload) VALUES(18446744073709551615,unhex('00ff80'))")
	restored := localNativeQuery(t, f.localTransportFixture, "SELECT id,hex(payload),label FROM \""+f.Database+"\".clear_dependent60")
	if !reflect.DeepEqual(restored, [][]string{{"18446744073709551615", "00ff80", "retained"}}) {
		t.Fatal("clear lost defaults or dependent view", restored)
	}
	providerQuery(t, q, f.Database, "CREATE TABLE clear_stale60(id UInt64)")
	stale, old := nativeSchemaDocument(t, s, "stale-clear60", "clear_stale60")
	providerQuery(t, q, f.Database, "DROP TABLE clear_stale60; CREATE TABLE clear_stale60(id UInt64); INSERT INTO clear_stale60 VALUES(7)")
	if result := stale.Operations().Clear(context.Background(), contract.SchemaClearRequest{Original: old}); result.State != "not_executed" || !errors.Is(result.Error, schemaddl.ErrConflict) || result.Receipt != nil {
		t.Fatal("stale proof cleared recreated table", result)
	}
	providerQuery(t, q, f.Database, "CREATE TABLE clear_altered60(id UInt64)")
	altered, old := nativeSchemaDocument(t, s, "altered-clear60", "clear_altered60")
	providerQuery(t, q, f.Database, "ALTER TABLE clear_altered60 ADD COLUMN v String")
	if result := altered.Operations().Clear(context.Background(), contract.SchemaClearRequest{Original: old}); result.State != "not_executed" || !errors.Is(result.Error, schemaddl.ErrConflict) {
		t.Fatal("altered proof cleared changed table", result)
	}
	writeDataEvidence(t, f, "schema-clear.json", map[string]any{"original": original, "empty": empty, "before": before, "remaining": remaining, "dependent_remaining": dependent, "restored": restored, "sibling_rejected": true, "reset_rejected": true, "stale_rejected": true, "altered_rejected": true})
}
