package dbruntime_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConsulNativePublicDataPrefixSetQualifiedCapturedPlansPartialProgressAndGenerationReplacement(t *testing.T) {
	p, secret := consulPublicProfile(t)
	p.ConsulOptions.Prefix = "native/public-runtime-prefix-set/" + t.Name() + "/"
	m, generation, w := consulMutationRuntime(t, p, secret)
	for i := 0; i < 43; i++ {
		consulRuntimeDo(t, w, consulRuntimePut(t, p.ConsulOptions.Prefix+fmt.Sprintf("%04d", i), []byte{0, 255}))
	}
	lease, peer := consulDataReserve(t, m, p.ID), consulDataReserve(t, m, p.ID)
	r := lease.Runtime()
	if r.CaptureKeyPrefixDelete == nil {
		t.Fatal("complete capture callback unavailable")
	}
	object := contract.ObjectRef{ProfileID: p.ID, Catalog: "kv", Kind: "keyspace", Name: "keys"}
	request := contract.DataKeyPrefixDelete{Object: object, Prefix: p.ConsulOptions.Prefix}
	ctx := context.Background()
	foreign := request
	foreign.Object.ProfileID++
	if _, err := r.CaptureKeyPrefixDelete(ctx, p.ID, "kv", "keys", foreign); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign capture accepted", err)
	}
	set, err := r.CaptureKeyPrefixDelete(ctx, p.ID, "kv", "keys", request)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	snap := set.Snapshot()
	snap.Candidates[0].Ref.TypedValues[0].Text = "foreign"
	if len(set.Snapshot().Candidates) != 43 || set.Snapshot().Candidates[0].Ref.TypedValues[0].Text == "foreign" {
		t.Fatal("snapshot mutation changed original capture")
	}
	if _, err = set.PreviewBatch(ctx, 1); err == nil {
		t.Fatal("unconfirmed earlier batch skipped")
	}
	plan, err := set.PreviewBatch(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	if _, err = peer.Runtime().SubmitEdits(ctx, p.ID, "kv", "keys", plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("batch crossed document lease", err)
	}
	out, err := r.SubmitEdits(ctx, p.ID, "kv", "keys", plan)
	if err != nil || out == nil || !out.CommitConfirmed || out.Applied != 21 || set.Snapshot().NextBatch != 1 || len(set.Snapshot().Receipts) != 1 {
		t.Fatal("first corresponding original receipt missing", err)
	}
	if _, err = r.SubmitEdits(ctx, p.ID, "kv", "keys", plan); err == nil {
		t.Fatal("captured batch replayed")
	}
	late, err := set.PreviewBatch(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	fresh, err := m.ConnectSession(ctx, p, secret)
	if err != nil || fresh.Generation() <= generation {
		t.Fatal("replacement failed", err)
	}
	if _, err = r.SubmitEdits(ctx, p.ID, "kv", "keys", late); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("late batch followed replacement", err)
	}
	if _, err = consulDataReserve(t, m, p.ID).Runtime().SubmitEdits(ctx, p.ID, "kv", "keys", late); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("original batch crossed generation", err)
	}
	lease.Close()
	peer.Close()
	if !m.IsConnected(p.ID) {
		t.Fatal("old set close damaged replacement")
	}
}
