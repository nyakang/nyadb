package dbruntime_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func TestConsulNativeRuntimeHTTPSessionRenewalOriginalGenerationAndEvidenceRecovery(t *testing.T) {
	cfg, token := consulRuntimeConfig(t)
	m := consulRuntimeManager(t, cfg)
	profile := consulRuntimeProfile()
	if e := m.Connect(context.Background(), profile, token); e != nil {
		t.Fatal(e)
	}
	writer := consulRuntimeReserve(t, m, 9001)
	created := consulRuntimeDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/create", Body: []byte(`{"Name":"original-runtime-renewal","TTL":"10s","LockDelay":"0s","NodeChecks":[]}`)})
	id := created.HTTP.SessionID
	provider, e := m.KVHTTPSessionRenewal(profile.ID)
	if e != nil {
		t.Fatal(e)
	}
	l, e := provider.ReserveKVHTTPSessionRenewal(9001)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	r := contract.KVHTTPSessionRenewalRequest{SessionID: id, Interval: time.Second, MaxRenewals: 5, MaxBytes: 16 << 20}
	p, e := l.Prepare(r)
	if e != nil {
		t.Fatal(e)
	}
	w, e := l.Start(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	defer w.Close()
	deadline := time.Now().Add(10 * time.Second)
	for len(w.Snapshot().Observations) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("original renewal missing")
		}
		time.Sleep(time.Millisecond)
	}
	if e = m.Connect(context.Background(), profile, token); e != nil {
		t.Fatal(e)
	}
	if e = w.Stop(context.Background()); e != nil {
		t.Fatal(e)
	}
	v := w.Snapshot()
	if !v.Ended || v.Terminal.Attempts != 1 || v.Terminal.Acknowledged != 1 || v.Terminal.WriteOutcomeUnknown || len(v.Observations) != 2 {
		t.Fatal("generation retirement changed original renewal", v.Terminal)
	}
	if _, e = l.Prepare(r); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("old published generation authorized")
	}
	provider, e = m.KVHTTPSessionRenewal(profile.ID)
	if e != nil {
		t.Fatal(e)
	}
	recovery, e := provider.RecoverKVHTTPSessionRenewal(l)
	if e != nil {
		t.Fatal(e)
	}
	defer recovery.Lease.Close()
	if recovery.Lease.Scope().Generation <= l.Scope().Generation || recovery.Renewal == nil || !bytes.Equal(recovery.Renewal.Observations[1].HTTP.Body, v.Observations[1].HTTP.Body) {
		t.Fatal("original generation/evidence replaced")
	}
	archive := recovery.Lease.(contract.KVHTTPSessionRenewalArchiveLease).RetainedRenewal()
	if archive == nil || !archive.Snapshot().Ended {
		t.Fatal("settled archive unavailable")
	}
	w.Close()
	if len(archive.Snapshot().Observations) != 2 {
		t.Fatal("late close erased recovered evidence")
	}
	writer = consulRuntimeReserve(t, m, 9002)
	consulRuntimeDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
}
func TestConsulNativeRuntimeHTTPSessionRenewalForeignScopeRejectedBeforeRecovery(t *testing.T) {
	cfg, token := consulRuntimeConfig(t)
	m := consulRuntimeManager(t, cfg)
	profile := consulRuntimeProfile()
	if e := m.Connect(context.Background(), profile, token); e != nil {
		t.Fatal(e)
	}
	p, _ := m.KVHTTPSessionRenewal(profile.ID)
	old, e := p.ReserveKVHTTPSessionRenewal(9003)
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	cfg.Consistency = "stale"
	other := consulRuntimeManager(t, cfg)
	if e = other.Connect(context.Background(), profile, token); e != nil {
		t.Fatal(e)
	}
	foreign, _ := other.KVHTTPSessionRenewal(profile.ID)
	if _, e = foreign.RecoverKVHTTPSessionRenewal(old); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("foreign scope recovered original", e)
	}
	if e = old.CheckKVLease(); e != nil {
		t.Fatal("rejected recovery revoked original", e)
	}
}
