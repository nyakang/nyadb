package dbruntime_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConsulNativeRuntimeHealthWatchOriginalGenerationReplacementAndEvidenceRecovery(t *testing.T) {
	cfg, token := consulRuntimeConfig(t)
	m := consulRuntimeManager(t, cfg)
	profile := consulRuntimeProfile()
	if err := m.Connect(context.Background(), profile, token); err != nil {
		t.Fatal(err)
	}
	provider, err := m.KVHTTPWatch(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	l, err := provider.ReserveKVHTTPWatch(7201)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r := contract.KVHTTPWatchRequest{Health: contract.KVHTTPHealthWatchTarget{Kind: "state", Name: "any", IncludeNear: true, Near: "_agent"}, Wait: 2 * time.Second, MaxObservations: 3, MaxBytes: 32 << 20}
	prepared, err := l.Prepare(r)
	if err != nil || prepared.Snapshot() != r {
		t.Fatal("public preparation changed original health request", err)
	}
	run, err := l.Start(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	deadline := time.Now().Add(10 * time.Second)
	for len(run.Snapshot().Observations) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("original health runtime observation absent")
		}
		time.Sleep(time.Millisecond)
	}
	if err := m.Connect(context.Background(), profile, token); err != nil {
		t.Fatal(err)
	}
	original := consulRuntimeWatchSettled(t, run)
	if original.Request != r || len(original.Observations) == 0 || original.Observations[0].HTTP.Health == nil || !original.Observations[0].ProjectionComplete || l.Scope().Header != (contract.KVHeader{}) {
		t.Fatal("replacement lost or reinterpreted original health evidence")
	}
	if _, err := l.Prepare(r); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old generation authorized another health read", err)
	}
	fresh, err := m.KVHTTPWatch(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := fresh.RecoverKVHTTPWatch(l)
	if err != nil || recovered.Watch == nil || recovered.Lease == nil {
		t.Fatal("health runtime recovery failed", err)
	}
	defer recovered.Lease.Close()
	if recovered.Lease.Scope().Generation <= l.Scope().Generation || !reflect.DeepEqual(*recovered.Watch, original) {
		t.Fatal("published generation translation replaced original archive")
	}
	clone := recovered.Watch.Clone()
	clone.Observations[0].HTTP.Body[0] = 'x'
	clone.Observations[0].HTTP.Health.Checks[0].Status = "changed"
	if !bytes.Equal(recovered.Lease.LastWatch().Observations[0].HTTP.Body, original.Observations[0].HTTP.Body) || recovered.Lease.LastWatch().Observations[0].HTTP.Health.Checks[0].Status == "changed" {
		t.Fatal("health archive aliases recovered snapshot")
	}
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*recovered.Lease.LastWatch(), original) {
		t.Fatal("late original Close erased transferred health archive")
	}
	if err := recovered.Lease.ClearObservations(); err != nil {
		t.Fatal(err)
	}
	r.MaxObservations = 1
	nextPreparation, err := recovered.Lease.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	next, err := recovered.Lease.Start(context.Background(), nextPreparation)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	got := consulRuntimeWatchSettled(t, next)
	if len(got.Observations) != 1 || !got.Observations[0].ProjectionComplete || got.Observations[0].HTTP.Health == nil || next.Request() != r {
		t.Fatal("explicit original new health run failed")
	}
}

func TestConsulNativeRuntimeHealthWatchRejectsForeignOriginalConfiguration(t *testing.T) {
	cfg, token := consulRuntimeConfig(t)
	m := consulRuntimeManager(t, cfg)
	profile := consulRuntimeProfile()
	if err := m.Connect(context.Background(), profile, token); err != nil {
		t.Fatal(err)
	}
	provider, err := m.KVHTTPWatch(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	l, err := provider.ReserveKVHTTPWatch(7202)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r := contract.KVHTTPWatchRequest{Health: contract.KVHTTPHealthWatchTarget{Kind: "state", Name: "any"}, Wait: 100 * time.Millisecond, MaxObservations: 1, MaxBytes: 8 << 20}
	prepared, err := l.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	run, err := l.Start(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	original := consulRuntimeWatchSettled(t, run)
	cfg.Consistency = "stale"
	cfg.Prefix = "foreign/"
	foreignManager := consulRuntimeManager(t, cfg)
	if err := foreignManager.Connect(context.Background(), profile, token); err != nil {
		t.Fatal(err)
	}
	foreign, err := foreignManager.KVHTTPWatch(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.RecoverKVHTTPWatch(l); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("health read scope bypassed original configuration identity", err)
	}
	if err := l.CheckKVLease(); err != nil || !reflect.DeepEqual(*l.LastWatch(), original) {
		t.Fatal("rejected foreign recovery revoked or replaced original health evidence", err)
	}
}
