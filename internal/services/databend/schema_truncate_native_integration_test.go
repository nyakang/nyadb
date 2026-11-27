package databend

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestDatabendSchemaTruncateOriginalAcknowledgementIdentityAndGuards(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 831, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	name := `truncate_%猫"original62`
	object := s.Namespace("").Object("table", name)
	providerQuery(t, q, f.Database, "CREATE TABLE "+qualified(object)+" (id UInt64 NOT NULL, payload Binary, label String DEFAULT 'retained')")
	providerQuery(t, q, f.Database, "INSERT INTO "+qualified(object)+" (id,payload) VALUES(18446744073709551615,unhex('00ff80')),(9007199254740993,unhex('fe00'))")
	providerQuery(t, q, f.Database, "CREATE VIEW truncate_dependent62 AS SELECT * FROM "+qualified(object))
	before := localNativeQuery(t, f.localTransportFixture, "SELECT id,hex(payload),label FROM "+qualified(object)+" ORDER BY id")
	lease, snapshot := nativeSchemaDocument(t, s, "original-truncate62", name)
	sibling, _ := nativeSchemaDocument(t, s, "sibling-truncate62", name)
	request := snapshot.Clone()
	if out := sibling.Operations().Truncate(context.Background(), request); out.State != "not_executed" || !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
		t.Fatal("sibling proof admitted", out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out := lease.Operations().Truncate(ctx, request); out.State != "not_executed" || out.Error == nil || out.Receipt != nil {
		t.Fatal("canceled truncate dispatched", out)
	}
	out := lease.Operations().Truncate(context.Background(), request)
	if out.State != "committed" || !out.Verified || out.Error != nil || out.Rows != 0 || out.Actual == nil || !reflect.DeepEqual(*out.Actual, snapshot) || out.Receipt == nil || out.Receipt.Rows != 0 || out.Receipt.Sequence != 1 || out.Receipt.Operation != "truncate" || !reflect.DeepEqual(out.Receipt.Statements, []string{"TRUNCATE TABLE " + qualified(object)}) {
		t.Fatal("native TRUNCATE ACK/schema not verified", out.State, out.Rows, out.Error)
	}
	remaining := localNativeQuery(t, f.localTransportFixture, "SELECT count(*) FROM "+qualified(object))
	dependent := localNativeQuery(t, f.localTransportFixture, "SELECT count(*) FROM \""+f.Database+"\".truncate_dependent62")
	if !reflect.DeepEqual(remaining, [][]string{{"0"}}) || !reflect.DeepEqual(remaining, dependent) {
		t.Fatal("table or dependent view not empty", remaining, dependent)
	}
	original := lease.OriginalOutcome()
	current, err := lease.Operations().Snapshot(context.Background(), snapshot.Target)
	if err != nil || !reflect.DeepEqual(current, snapshot) || !reflect.DeepEqual(original, lease.OriginalOutcome()) {
		t.Fatal("truncate changed source identity/receipt", err)
	}
	emptyLease, emptySnapshot := nativeSchemaDocument(t, s, "empty-truncate62", name)
	empty := emptyLease.Operations().Truncate(context.Background(), emptySnapshot.Clone())
	if empty.State != "committed" || !empty.Verified || empty.Error != nil || empty.Rows != 0 || empty.Receipt == nil {
		t.Fatal("empty TRUNCATE ACK not acknowledged", empty.State, empty.Error)
	}
	providerQuery(t, q, f.Database, "INSERT INTO "+qualified(object)+" (id,payload) VALUES(18446744073709551615,unhex('00ff80'))")
	restored := localNativeQuery(t, f.localTransportFixture, "SELECT id,hex(payload),label FROM \""+f.Database+"\".truncate_dependent62")
	if !reflect.DeepEqual(restored, [][]string{{"18446744073709551615", "00ff80", "retained"}}) {
		t.Fatal("truncate lost defaults or dependent view", restored)
	}
	providerQuery(t, q, f.Database, "CREATE TABLE truncate_stale62(id UInt64)")
	stale, old := nativeSchemaDocument(t, s, "stale-truncate62", "truncate_stale62")
	providerQuery(t, q, f.Database, "DROP TABLE truncate_stale62; CREATE TABLE truncate_stale62(id UInt64); INSERT INTO truncate_stale62 VALUES(7)")
	if result := stale.Operations().Truncate(context.Background(), old.Clone()); result.State != "not_executed" || !errors.Is(result.Error, schemaddl.ErrConflict) || result.Receipt != nil {
		t.Fatal("stale proof truncated recreated table", result)
	}
	providerQuery(t, q, f.Database, "CREATE TABLE truncate_altered62(id UInt64)")
	altered, old := nativeSchemaDocument(t, s, "altered-truncate62", "truncate_altered62")
	providerQuery(t, q, f.Database, "ALTER TABLE truncate_altered62 ADD COLUMN v String")
	if result := altered.Operations().Truncate(context.Background(), old.Clone()); result.State != "not_executed" || !errors.Is(result.Error, schemaddl.ErrConflict) {
		t.Fatal("altered proof truncated changed table", result)
	}
	writeDataEvidence(t, f, "schema-truncate.json", map[string]any{"original": original, "empty": empty, "before": before, "remaining": remaining, "dependent_remaining": dependent, "restored": restored, "sibling_rejected": true, "stale_rejected": true, "altered_rejected": true})
}
