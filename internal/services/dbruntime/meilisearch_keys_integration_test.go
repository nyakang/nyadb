package dbruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
)

func TestMeilisearchNativeKeysPublishedGenerationsScopeAndRecovery(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	m := New()
	RegisterBuiltins(m, nil)
	defer m.Close()
	first, err := m.ConnectSession(context.Background(), parsed.Profile, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := m.KeysDocuments(parsed.Profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	o := m.Namespace(parsed.Profile.ID, "").Object("keys", "keys")
	for _, invalid := range []contract.ObjectRef{
		{ProfileID: parsed.Profile.ID + 1, Kind: "keys", Name: "keys"},
		{ProfileID: parsed.Profile.ID, Catalog: "other", Kind: "keys", Name: "keys"},
		{ProfileID: parsed.Profile.ID, Schema: "other", Kind: "keys", Name: "keys"},
	} {
		if provider.SupportsKeysDocument(invalid) {
			t.Fatal("published key scope expanded")
		}
		if _, err = provider.ReserveKeysDocument(invalid); err == nil {
			t.Fatal("foreign key reservation accepted")
		}
	}
	lease, err := provider.ReserveKeysDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	var record contract.KeyRecord
	if parsed.Password != "" {
		plan, e := lease.Prepare("create", record, contract.KeyDraft{Name: "owned runtime key", Description: "原生🐱", Actions: []string{"search"}, Indexes: []string{"owned*"}})
		if e != nil {
			t.Fatal(e)
		}
		result, e := lease.Apply(context.Background(), plan)
		if e != nil || result.State != "completed" || result.Status != 201 {
			t.Fatal("published original synchronous creation", e)
		}
		record = result.Record
	} else {
		_, e := lease.List(context.Background(), 0, 20)
		var native *meilisearch.HTTPError
		if !errors.As(e, &native) || native.Status != 401 {
			t.Fatal("published anonymous key refusal missing", e)
		}
	}
	second, err := m.ConnectSession(context.Background(), parsed.Profile, parsed.Password)
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal("published key generations", err)
	}
	for _, operation := range []func() error{
		func() error { _, e := lease.List(context.Background(), 0, 20); return e },
		func() error { _, e := lease.Read(context.Background(), record); return e },
		func() error { _, e := lease.Reveal(record); return e },
		func() error { _, e := lease.Prepare("delete", record, contract.KeyDraft{}); return e },
		func() error { _, e := lease.Apply(context.Background(), contract.KeyPlan{}); return e },
	} {
		if err = operation(); !errors.Is(err, contract.ErrRevoked) {
			t.Fatal("old published key request rebound", err)
		}
	}
	next, err := m.KeysDocuments(parsed.Profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := next.RecoverKeysDocument(o, lease)
	if err != nil || recovered.Uncertain {
		t.Fatal("original key evidence recovery", err)
	}
	defer recovered.Lease.Close()
	if parsed.Password != "" {
		if recovered.Last.Record.UID != record.UID || recovered.Last.Record.Token == record.Token {
			t.Fatal("recovered evidence lost or retained old proof")
		}
		if _, err = recovered.Lease.Reveal(record); !errors.Is(err, contract.ErrKeyConflict) {
			t.Fatal("old original key proof followed recovery")
		}
		plan, e := recovered.Lease.Prepare("delete", recovered.Last.Record, contract.KeyDraft{})
		if e != nil {
			t.Fatal(e)
		}
		result, e := recovered.Lease.Apply(context.Background(), plan)
		if e != nil || result.State != "completed" || result.Status != 204 {
			t.Fatal("recovered original synchronous deletion", e)
		}
	}
	_ = lease.Close()
	if !m.IsConnected(parsed.Profile.ID) {
		t.Fatal("original key close damaged replacement")
	}
	// A qualified index is not a global key administration identity.
	raw := second.(*session).raw.(*meilisearch.Session)
	if raw.SupportsKeysDocument(m.Namespace(parsed.Profile.ID, "scoped_index").Object("keys", "keys")) {
		t.Fatal("index-scoped key identity accepted")
	}
	t.Log("optional original key provider, global qualified scope, every operation revoked on published generation replacement, synchronous evidence transfer and explicit recovered deletion; original close preserves replacement")
}
