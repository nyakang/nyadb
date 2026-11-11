package dbruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConsulNativePublicDataKeyPrefixDeleteQualifiedOriginalPlansAndGenerationReplacement(t *testing.T) {
	p, secret := consulPublicProfile(t)
	p.ConsulOptions.Prefix = "native/public-runtime-prefix/" + t.Name() + "/"
	m, generation, w := consulMutationRuntime(t, p, secret)
	key := p.ConsulOptions.Prefix + "source"
	consulRuntimeDo(t, w, consulRuntimePut(t, key, []byte{0, 255}))
	lease, peer := consulDataReserve(t, m, p.ID), consulDataReserve(t, m, p.ID)
	r := lease.Runtime()
	if r.PreviewKeyPrefixDelete == nil {
		t.Fatal("public prefix preparation unavailable")
	}
	object := contract.ObjectRef{ProfileID: p.ID, Catalog: "kv", Kind: "keyspace", Name: "keys"}
	request := contract.DataKeyPrefixDelete{Object: object, Prefix: p.ConsulOptions.Prefix}
	ctx := context.Background()
	foreign := request
	foreign.Object.ProfileID++
	if _, err := r.PreviewKeyPrefixDelete(ctx, p.ID, "kv", "keys", foreign); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign original prefix accepted", err)
	}
	plan, err := r.PreviewKeyPrefixDelete(ctx, p.ID, "kv", "keys", request)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	inspection := plan.Snapshot()
	inspection.KeyPrefixDelete.Prefix = "foreign/"
	inspection.Edits[0].Original.Ref.TypedValues[0].Text = "foreign"
	if plan.Snapshot().KeyPrefixDelete.Prefix != request.Prefix || plan.Snapshot().Edits[0].Original.Ref.TypedValues[0].Text != key {
		t.Fatal("inspection changed original candidate set")
	}
	if _, err = r.SubmitEdits(ctx, p.ID, "foreign", "keys", plan); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("prefix plan crossed namespace", err)
	}
	if _, err = peer.Runtime().SubmitEdits(ctx, p.ID, "kv", "keys", plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("prefix plan crossed document", err)
	}
	later := request.Prefix + "later"
	consulRuntimeDo(t, w, consulRuntimePut(t, later, []byte("later")))
	out, err := r.SubmitEdits(ctx, p.ID, "kv", "keys", plan)
	if err != nil || out == nil || !out.CommitConfirmed || out.Applied != 1 || len(out.Outcomes) != 1 || out.Outcomes[0].Kind != "delete" {
		t.Fatal("original conditional prefix not established", err)
	}
	if len(consulRuntimeDo(t, w, consulRuntimeRead(t, later)).HTTP.Entries) != 1 {
		t.Fatal("later key was included in original candidate set")
	}
	if _, err = r.SubmitEdits(ctx, p.ID, "kv", "keys", plan); err == nil {
		t.Fatal("prefix plan replayed")
	}
	late, err := r.PreviewKeyPrefixDelete(ctx, p.ID, "kv", "keys", request)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	fresh, err := m.ConnectSession(ctx, p, secret)
	if err != nil || fresh.Generation() <= generation {
		t.Fatal(err)
	}
	if _, err = r.SubmitEdits(ctx, p.ID, "kv", "keys", late); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old prefix followed replacement", err)
	}
	newLease := consulDataReserve(t, m, p.ID)
	if _, err = newLease.Runtime().SubmitEdits(ctx, p.ID, "kv", "keys", late); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("prefix plan crossed generation", err)
	}
	lease.Close()
	peer.Close()
	if !m.IsConnected(p.ID) {
		t.Fatal("old prefix close disconnected replacement")
	}
}
