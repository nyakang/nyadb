package consul

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativePrepareSessionDestroy(t *testing.T, lease contract.KVRequestLease, id string) contract.KVHTTPSessionDestroyPreparation {
	t.Helper()
	facet, ok := lease.(contract.KVHTTPSessionDestroyPlanning)
	if !ok {
		t.Fatal("original destroy planning facet missing")
	}
	p, err := facet.PrepareHTTPSessionDestroy(context.Background(), nativeDestroyImpactRequest(id))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestConsulNativeSessionDestroyPlanOriginalReleaseDeleteAndCompleteReceipts(t *testing.T) {
	base, cfg, token := nativeClient(t)
	cfg.Prefix, cfg.Datacenter, cfg.Consistency = "configured-empty/", "dc1", "consistent"
	s, err := OpenNativeSession(context.Background(), 2401, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, behavior := range []string{"release", "delete"} {
		t.Run(behavior, func(t *testing.T) {
			lease, err := s.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			id := nativeSessionToolCreate(t, base, t.Name(), "10m", behavior)
			defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
			key, value := keyFor(t, "outside/中文 %?#"), []byte{0, 255, 128}
			nativeSessionKeysAcquire(t, base, id, key, value)
			p := nativePrepareSessionDestroy(t, lease, id)
			preview := p.Impact()
			expected, err := SessionDestroyExpectation(preview)
			if err != nil || expected.Behavior != behavior || len(expected.HeldKeys) != 1 || expected.HeldKeys[0].Key != key || string(preview.Scope.Prefix) != cfg.Prefix {
				t.Fatal("original destroy preview lost scope/key/behavior", err)
			}
			result, err := lease.Execute(context.Background(), p)
			if err != nil || result.WriteOutcomeUnknown || !result.Completed || result.HTTP == nil || !result.HTTP.Acknowledged || !result.HTTP.Succeeded || !bytes.Equal(bytes.TrimSpace(result.HTTP.Body), []byte("true")) || result.HTTP.Request.Path != "/v1/session/destroy/"+id || result.HTTPSessionDestroy == nil || !result.HTTPSessionDestroy.Revalidated || !reflect.DeepEqual(result.HTTPSessionDestroy.Preview, preview) || !reflect.DeepEqual(result, lease.LastResult()) {
				t.Fatal("original destroy did not retain complete native evidence", err)
			}
			if err := CompareSessionDestroyExpectation(expected, result.HTTPSessionDestroy.Recheck); err != nil {
				t.Fatal("original recheck changed captured expectation", err)
			}
			if _, err := lease.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("acknowledged native destroy plan replayed", err)
			}
			if behavior == "delete" {
				if r := nativeDo(t, base, readRequest(key)); r.Status != 404 {
					t.Fatal("delete Behavior did not delete original held key")
				}
			} else if row := nativeRead(t, base, key); row.Session != "" || !bytes.Equal(row.Value, value) {
				t.Fatal("release Behavior changed original bytes or retained Session")
			}
		})
	}
}

func TestConsulNativeSessionDestroyPlanChangedKeysACLAndRecoveryCannotAuthorizePUT(t *testing.T) {
	base, cfg, token := nativeClient(t)
	cfg.Prefix = "configured-empty/"
	id := nativeSessionToolCreate(t, base, t.Name(), "10m", "release")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
	key := keyFor(t, "original-changed-key")
	nativeSessionKeysAcquire(t, base, id, key, []byte{0, 255})
	s, err := OpenNativeSession(context.Background(), 2402, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lease, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	p := nativePrepareSessionDestroy(t, lease, id)
	nativeSessionKeysAcquire(t, base, id, key, []byte{128})
	result, err := lease.Execute(context.Background(), p)
	if !errors.Is(err, ErrConflict) || result.Attempted || result.HTTP != nil || result.WriteOutcomeUnknown || result.HTTPSessionDestroy == nil || result.HTTPSessionDestroy.Revalidated || !result.HTTPSessionDestroy.Recheck.Complete {
		t.Fatal("changed original key version authorized destroy", err)
	}
	if row := nativeRead(t, base, key); row.Session != id || !bytes.Equal(row.Value, []byte{128}) {
		t.Fatal("rejected recheck changed original held key")
	}
	freshPlan := nativePrepareSessionDestroy(t, lease, id)
	recovery, err := s.RecoverKV(lease)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if freshPlan.Impact().Complete {
		t.Fatal("recovery retained destroy authority")
	}
	if _, err := recovery.Lease.Execute(context.Background(), freshPlan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("recovery accepted original plan", err)
	}
	if cfg.Auth != "none" {
		restricted := cfg
		restricted.Prefix = "allowed/"
		secret := os.Getenv("NYADB_CONSUL_READ_TOKEN")
		if secret == "" {
			t.Fatal("original scoped read token missing")
		}
		limited, err := OpenNativeSession(context.Background(), 2403, restricted, secret)
		if err != nil {
			t.Fatal(err)
		}
		defer limited.Close()
		l, err := limited.ReserveKV(1)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		if p, err := l.(contract.KVHTTPSessionDestroyPlanning).PrepareHTTPSessionDestroy(context.Background(), nativeDestroyImpactRequest(id)); err == nil || p != nil || l.LastResult().HTTPSessionDestroy == nil || l.LastResult().HTTPSessionDestroy.Preview.Complete {
			t.Fatal("ACL subset produced destroy authority or lost original receipts", err)
		}
	}
}

func TestConsulNativeSessionDestroyPlanProcessedUnknownCancellationAndOriginalRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	cfg.Prefix = "configured-empty/"
	for _, mode := range []string{"lost", "partial", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			id := nativeSessionToolCreate(t, base, t.Name(), "10m", "release")
			defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
			key := keyFor(t, "original-held-key")
			nativeSessionKeysAcquire(t, base, id, key, []byte{0, 255})
			// A nonempty prefix handshakes with a read-only PUT /v1/txn.
			// This generic write-fault relay must first see the target destroy PUT.
			faultConfig := cfg
			faultConfig.Prefix = ""
			relay, calls, processed := nativeFault(t, faultConfig, token, mode)
			s, err := OpenNativeSession(context.Background(), 2404, relay, token)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			lease, err := s.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			p := nativePrepareSessionDestroy(t, lease, id)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				result contract.KVResult
				err    error
			}
			done := make(chan completion, 1)
			go func() { result, err := lease.Execute(ctx, p); done <- completion{result, err} }()
			wait(t, processed)
			if mode == "cancel" {
				cancel()
			} else if mode == "close" {
				if err := p.Close(); err != nil {
					t.Fatal(err)
				}
			}
			out := <-done
			if out.err == nil || !out.result.WriteOutcomeUnknown || out.result.HTTPSessionDestroy == nil || !out.result.HTTPSessionDestroy.Revalidated || !reflect.DeepEqual(out.result, lease.LastResult()) {
				t.Fatal("processed original destroy became acknowledged or lost original evidence", out.err)
			}
			before := calls.Load()
			recovery, err := s.RecoverKV(lease)
			if err != nil || calls.Load() != before || recovery.Unknown == nil || !reflect.DeepEqual(*recovery.Unknown, out.result) {
				t.Fatal("recovery replayed or rewrote unknown destroy", err)
			}
			defer recovery.Lease.Close()
			if nativeRead(t, base, key).Session != "" {
				t.Fatal("actual original destroy did not release key")
			}
			observation, err := nativeSessionKeysFacet(t, recovery.Lease).ObserveHTTPSessionKeys(context.Background(), nativeSessionKeysRequest(id))
			if err != nil || !observation.Complete || len(observation.Matches) != 0 || !reflect.DeepEqual(*recovery.Lease.(*documentLease).recoveryOwned().Unknown, out.result) {
				t.Fatal("later absence observation changed original unknown receipt", err)
			}
			if _, err := recovery.Lease.Prepare(out.result.Request); !errors.Is(err, contract.ErrKVWriteUnknown) {
				t.Fatal("later absence authorized replay", err)
			}
		})
	}
}
