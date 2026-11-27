package databend

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeDatabaseCreateDocument(t *testing.T, s *Session, id, name string) contract.DatabaseCreateDocumentLease {
	t.Helper()
	r := contract.DatabaseCreateDocumentRequest{DocumentID: id, Target: contract.DatabaseCreateTarget{Namespace: s.Namespace(name), Generation: s.Generation()}}
	l, err := s.ReserveDatabaseCreateDocument(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestDatabendDatabaseCreateOriginalAcknowledgementAndGuards(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 901, filepath.Join(f.Directory, t.Name(), "store")))
	ctx := context.Background()
	name := "create_%猫\"database61"
	d := nativeDatabaseCreateDocument(t, s, "create-original", name)
	before, err := d.Snapshot(ctx)
	if err != nil || before.Exists || before.Revision == "" || before.DDL != "" || before.NativeID != "" {
		t.Fatal("absence", before, err)
	}
	sibling := nativeDatabaseCreateDocument(t, s, "create-sibling", name)
	if out := sibling.Create(ctx, before); out.State != "not_executed" || !errors.Is(out.Error, contract.ErrNamespaceChanged) || out.Receipt != nil {
		t.Fatal("transferred proof", out)
	}
	forged := before
	forged.Target.Namespace.Schema += "_other"
	if out := d.Create(ctx, forged); out.State != "not_executed" || out.Receipt != nil {
		t.Fatal("changed target", out)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if out := d.Create(cancelled, before); out.State != "not_executed" || out.Receipt != nil {
		t.Fatal("cancelled admission", out)
	}
	out := d.Create(ctx, before)
	if out.State != "committed" || !out.Verified || out.Error != nil || out.Actual == nil || !out.Actual.Exists || out.Actual.NativeID == "" || out.Receipt == nil {
		t.Fatal("native create", out)
	}
	var native struct {
		Receipt  struct{ Result json.RawMessage }
		Original struct{ Result json.RawMessage }
	}
	if err := json.Unmarshal([]byte(out.Receipt.NativeJSON), &native); err != nil || string(native.Receipt.Result) != string(native.Original.Result) {
		t.Fatal("complete original receipt", err)
	}
	retained := d.OriginalOutcome()
	out.Actual.DDL = "changed"
	out.Receipt.Actual.NativeID = "changed"
	if !reflect.DeepEqual(retained, d.OriginalOutcome()) {
		t.Fatal("aliased original")
	}
	q := reserveNativeQuery(t, s, 1)
	providerQuery(t, q, f.Database, "CREATE TABLE "+quoteName(name)+".probe (id UInt64)")
	providerQuery(t, q, f.Database, "INSERT INTO "+quoteName(name)+".probe VALUES (18446744073709551615)")
	rows := localNativeQuery(t, f.localTransportFixture, "SELECT to_string(id) FROM "+quoteName(name)+".probe")
	if !reflect.DeepEqual(rows, [][]string{{"18446744073709551615"}}) {
		t.Fatal("new namespace use", rows)
	}
	providerQuery(t, q, f.Database, "DROP DATABASE "+quoteName(name))
	absent, err := d.Snapshot(ctx)
	if err != nil || absent.Exists {
		t.Fatal("later explicit read", absent, err)
	}
	if !reflect.DeepEqual(retained, d.OriginalOutcome()) {
		t.Fatal("read changed original evidence")
	}
	if again := d.Create(ctx, absent); again.State != "not_executed" || !errors.Is(again.Error, contract.ErrDatabaseCreateConsumed) || again.Receipt != nil {
		t.Fatal("replayed successful creation", again)
	}
	// Independent same-name recreation has a different native incarnation.
	providerQuery(t, q, f.Database, "CREATE DATABASE "+quoteName(name))
	recreated, err := sibling.Snapshot(ctx)
	if err != nil || !recreated.Exists || recreated.NativeID == retained.Actual.NativeID {
		t.Fatal("same-name replacement", recreated, err)
	}
	if again := sibling.Create(ctx, before); again.State != "not_executed" || again.Receipt != nil {
		t.Fatal("occupied name", again)
	}
	if again := sibling.Create(ctx, recreated); again.State != "not_executed" || again.Receipt != nil {
		t.Fatal("existing target", again)
	}
	// Embedded native quoting is verified against the real SHOW CREATE output.
	quoted := nativeDatabaseCreateDocument(t, s, "backtick-database", "create_back`tick61")
	quotedBefore, err := quoted.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	quotedOut := quoted.Create(ctx, quotedBefore)
	if quotedOut.State != "committed" || !quotedOut.Verified || quotedOut.Error != nil {
		t.Fatal("backtick create", quotedOut)
	}
	for _, target := range []contract.DatabaseCreateTarget{
		{Namespace: contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: "other", Schema: "name"}, Generation: s.Generation()},
		{Namespace: s.Namespace("system"), Generation: s.Generation()},
		{Namespace: s.Namespace("name"), Generation: s.Generation() + 1},
	} {
		if s.CanCreateDatabase(target) {
			t.Fatal("unsupported scope", target)
		}
	}
	if err := d.Close(); err != nil || d.OriginalOutcome() != nil {
		t.Fatal("close retained cache", err)
	}
	if _, err := d.Snapshot(ctx); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed read", err)
	}
	encoded, err := json.MarshalIndent(map[string]any{"original": retained, "independent_rows": rows, "replacement": recreated, "quoted": quotedOut, "sibling_rejected": true, "replay_rejected": true, "close_released": true}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Directory, "database-create.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
}
