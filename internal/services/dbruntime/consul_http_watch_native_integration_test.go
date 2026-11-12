package dbruntime_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func consulRuntimeWatchSettled(t *testing.T, w contract.KVHTTPWatchRun) contract.KVHTTPWatchSnapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := w.Snapshot()
		if r.Ended {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatal("original runtime Watch did not ACK")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestConsulNativeRuntimeHTTPWatchOriginalGenerationsCancellationAndEvidenceOnlyRecovery(t *testing.T) {
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
	l, err := provider.ReserveKVHTTPWatch(7001)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	writer := consulRuntimeReserve(t, m, 7001)
	ancestor := "native/runtime-watch/" + t.Name()
	target := ancestor + "/double//猫\n?%"
	flags := uint64(math.MaxUint64)
	value := []byte{0, 255, 128}
	put, err := consul.KeyPutRequest(target, value, &flags, nil)
	if err != nil {
		t.Fatal(err)
	}
	consulRuntimeDo(t, writer, put)
	r := contract.KVHTTPWatchRequest{Target: target, ObservationPrefix: ancestor, Wait: 2 * time.Second, MaxObservations: 2, MaxBytes: 16 << 20}
	p, err := l.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	w, err := l.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	deadline := time.Now().Add(10 * time.Second)
	for len(w.Snapshot().Observations) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("initial original runtime observation missing")
		}
		time.Sleep(time.Millisecond)
	}
	if err = m.Connect(context.Background(), profile, token); err != nil {
		t.Fatal(err)
	}
	original := consulRuntimeWatchSettled(t, w)
	if len(original.Observations) == 0 || original.Request != r || original.Terminal.Reason == "observation-limit" {
		t.Fatal("replacement lost/resumed original Watch", original.Terminal)
	}
	o := original.Observations[0]
	if len(o.Matches) != 1 || !bytes.Equal(o.HTTP.Entries[o.Matches[0]].Value, value) || o.HTTP.Entries[o.Matches[0]].Flags != math.MaxUint64 || o.HTTP.Metadata.Index == nil || l.Scope().Header != (contract.KVHeader{}) || l.Scope().Generation == 0 {
		t.Fatal("runtime reinterpreted original HTTP values/generation")
	}
	if _, err = l.Prepare(r); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("revoked original wrapper authorized new I/O")
	}
	fresh, _ := m.KVHTTPWatch(profile.ID)
	recovery, err := fresh.RecoverKVHTTPWatch(l)
	if err != nil || recovery.Lease == nil || recovery.Watch == nil || recovery.Lease.Scope().Generation <= l.Scope().Generation || len(recovery.Watch.Observations) != len(original.Observations) || recovery.Watch.Request != r {
		t.Fatal("original evidence recovery failed", err)
	}
	defer recovery.Lease.Close()
	if !bytes.Equal(recovery.Watch.Observations[0].HTTP.Body, original.Observations[0].HTTP.Body) {
		t.Fatal("replacement substituted original receipt")
	}
	if err = recovery.Lease.ClearObservations(); err != nil {
		t.Fatal(err)
	}
	r.MaxObservations = 1
	p, err = recovery.Lease.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	newRun, err := recovery.Lease.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer newRun.Close()
	got := consulRuntimeWatchSettled(t, newRun)
	if len(got.Observations) != 1 || len(got.Observations[0].Matches) != 1 || !bytes.Equal(got.Observations[0].HTTP.Entries[got.Observations[0].Matches[0]].Value, value) {
		t.Fatal("explicit new original Watch read failed")
	}
}

func TestConsulNativeRuntimeHTTPWatchRejectsForeignIdentityBeforeRecovery(t *testing.T) {
	cfg, token := consulRuntimeConfig(t)
	m := consulRuntimeManager(t, cfg)
	profile := consulRuntimeProfile()
	if err := m.Connect(context.Background(), profile, token); err != nil {
		t.Fatal(err)
	}
	provider, _ := m.KVHTTPWatch(profile.ID)
	old, err := provider.ReserveKVHTTPWatch(7002)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	cfg.Consistency = "stale"
	foreignManager := consulRuntimeManager(t, cfg)
	if err = foreignManager.Connect(context.Background(), profile, token); err != nil {
		t.Fatal(err)
	}
	foreign, _ := foreignManager.KVHTTPWatch(profile.ID)
	if _, err = foreign.RecoverKVHTTPWatch(old); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("foreign captured configuration recovered original Watch", err)
	}
	if err = old.CheckKVLease(); err != nil {
		t.Fatal("rejected foreign recovery revoked original lease", err)
	}
}
