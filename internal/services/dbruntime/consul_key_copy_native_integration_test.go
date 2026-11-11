package dbruntime_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConsulNativePublicDataKeyCopyQualifiedOriginalPlansAndGenerationReplacement(t *testing.T) {
	p, secret := consulPublicProfile(t)
	p.ConsulOptions.Prefix = "native/public-runtime-copy/" + t.Name() + "/"
	m, generation, w := consulMutationRuntime(t, p, secret)
	source, destination := p.ConsulOptions.Prefix+"source", p.ConsulOptions.Prefix+"target"
	consulRuntimeDo(t, w, consulRuntimePut(t, source, []byte{0, 255}))
	before := consulRuntimeDo(t, w, consulRuntimeRead(t, source)).HTTP.Entries
	lease, peer := consulDataReserve(t, m, p.ID), consulDataReserve(t, m, p.ID)
	r := lease.Runtime()
	if r.PreviewKeyCopy == nil {
		t.Fatal("public original copy feature unavailable")
	}
	request := contract.DataKeyCopy{Source: consulMutationRuntimeEdit(t, r, p.ID).Original, Destination: destination}
	original := contract.CloneDataKeyCopy(request)
	ctx := context.Background()
	foreign := contract.CloneDataKeyCopy(request)
	foreign.Source.Ref.Object.ProfileID++
	if _, err := r.PreviewKeyCopy(ctx, p.ID, "kv", "keys", foreign); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign copy source accepted", err)
	}
	plan, err := r.PreviewKeyCopy(ctx, p.ID, "kv", "keys", request)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	request.Source.Ref.Values[0][0] = 99
	inspection := plan.Snapshot()
	inspection.Edits[0].Values["key"] = contract.DataValue{Kind: contract.Text, Text: "foreign"}
	if plan.Snapshot().Edits[0].Values["key"].Text != destination {
		t.Fatal("copy inspection changed original destination")
	}
	if _, err := peer.Runtime().SubmitEdits(ctx, p.ID, "kv", "keys", plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("copy plan crossed document", err)
	}
	if _, err := r.SubmitEdits(ctx, p.ID, "foreign", "keys", plan); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("copy crossed namespace", err)
	}
	out, err := r.SubmitEdits(ctx, p.ID, "kv", "keys", plan)
	if err != nil || out == nil || !out.CommitConfirmed || out.Outcomes[0].Kind != "copy" || out.Outcomes[0].Saved == nil {
		t.Fatal("original public copy not established", err)
	}
	if !reflect.DeepEqual(before, consulRuntimeDo(t, w, consulRuntimeRead(t, source)).HTTP.Entries) || !bytes.Equal(consulRuntimeDo(t, w, consulRuntimeRead(t, destination)).HTTP.Entries[0].Value, []byte{0, 255}) {
		t.Fatal("copy changed native source/target")
	}
	if _, err := r.SubmitEdits(ctx, p.ID, "kv", "keys", plan); err == nil {
		t.Fatal("public copy replayed")
	}
	original.Destination += "-late"
	late, err := r.PreviewKeyCopy(ctx, p.ID, "kv", "keys", original)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	fresh, err := m.ConnectSession(ctx, p, secret)
	if err != nil || fresh.Generation() <= generation {
		t.Fatal(err)
	}
	if _, err := r.SubmitEdits(ctx, p.ID, "kv", "keys", late); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old copy followed replacement", err)
	}
	newLease := consulDataReserve(t, m, p.ID)
	if _, err := newLease.Runtime().SubmitEdits(ctx, p.ID, "kv", "keys", late); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("copy plan crossed public generation", err)
	}
	lease.Close()
	peer.Close()
	if !m.IsConnected(p.ID) {
		t.Fatal("old copy close disconnected replacement")
	}
}
