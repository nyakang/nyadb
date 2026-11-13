package consul

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeDestroyImpactRequest(id string) contract.KVHTTPSessionDestroyImpactRequest {
	return contract.KVHTTPSessionDestroyImpactRequest{ID: id, MaxRows: 10000, MaxBytes: 32 << 20}
}
func nativeDestroyImpactFacet(t *testing.T, l contract.KVRequestLease) contract.KVHTTPSessionDestroyImpactBrowsing {
	t.Helper()
	f, ok := l.(contract.KVHTTPSessionDestroyImpactBrowsing)
	if !ok {
		t.Fatal("original impact facet missing")
	}
	return f
}

func TestConsulNativeSessionDestroyImpactOriginalWholeRootBehaviorAndChangedKeyVersions(t *testing.T) {
	base, cfg, token := nativeClient(t)
	cfg.Prefix = "configured-empty/"
	s, err := OpenNativeSession(context.Background(), 2301, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, behavior := range []string{"release", "delete"} {
		t.Run(behavior, func(t *testing.T) {
			id := nativeSessionToolCreate(t, base, t.Name(), "10m", behavior)
			defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
			key := keyFor(t, "outside-original")
			nativeSessionKeysAcquire(t, base, id, key, []byte{0, 255})
			f := nativeDestroyImpactFacet(t, l)
			request := nativeDestroyImpactRequest(id)
			o, err := f.ObserveHTTPSessionDestroyImpact(context.Background(), request)
			if err != nil || !o.Complete || string(o.Scope.Prefix) != cfg.Prefix || o.Scope.Namespace.ProfileID != 2301 || o.Session.HTTP == nil || o.Keys.Root.HTTP == nil || o.Keys.Root.HTTP.Request.Path != "/v1/kv/" {
				t.Fatal("original whole-root impact changed", err)
			}
			e, err := SessionDestroyExpectation(o)
			if err != nil || e.Behavior != behavior || len(e.HeldKeys) != 1 || e.HeldKeys[0].Key != key || e.HeldKeys[0].ModifyIndex == 0 {
				t.Fatal("original behavior/key expectation missing", err)
			}
			original := o.Clone()
			nativeSessionKeysAcquire(t, base, id, key, []byte{128})
			changed, err := f.ObserveHTTPSessionDestroyImpact(context.Background(), request)
			if err != nil || !errors.Is(CompareSessionDestroyExpectation(e, changed), ErrConflict) {
				t.Fatal("changed held-key version accepted", err)
			}
			if !reflect.DeepEqual(o, original) || !bytes.Equal(nativeRead(t, base, key).Value, []byte{128}) {
				t.Fatal("preflight changed original receipt or native key")
			}
			if got := nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionInfo, id)); len(got.Body) == 0 {
				t.Fatal("read-only preflight destroyed Session")
			}
		})
	}
}

func TestConsulNativeSessionDestroyImpactACLAndOriginalUnknownWriteRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	id := nativeSessionToolCreate(t, base, t.Name(), "10m", "release")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
	nativeSessionKeysAcquire(t, base, id, "allowed/"+keyFor(t, "visible"), []byte{0, 255})
	nativeSessionKeysAcquire(t, base, id, keyFor(t, "hidden"), []byte{128})
	if cfg.Auth != "none" {
		restricted := cfg
		restricted.Prefix = "allowed/"
		secret := os.Getenv("NYADB_CONSUL_WRITE_TOKEN")
		if secret == "" {
			t.Fatal("scoped original token missing")
		}
		s, err := OpenNativeSession(context.Background(), 2302, restricted, secret)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		l, err := s.ReserveKV(1)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		o, err := nativeDestroyImpactFacet(t, l).ObserveHTTPSessionDestroyImpact(context.Background(), nativeDestroyImpactRequest(id))
		if err == nil || o.Complete || o.Session.HTTP == nil {
			t.Fatal("ACL subset produced complete impact", err)
		}
		if _, err := SessionDestroyExpectation(o); err == nil {
			t.Fatal("ACL subset granted expectation")
		}
	}
	relay, calls, processed := nativeFault(t, cfg, token, "lost")
	s, err := OpenNativeSession(context.Background(), 2303, relay, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	unknown, err := l.Execute(context.Background(), documentPlan(t, l, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id)))
	wait(t, processed)
	if err == nil || !unknown.WriteOutcomeUnknown {
		t.Fatal("original lost destroy became acknowledged", err)
	}
	before := calls.Load()
	recovery, err := s.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if calls.Load() != before || recovery.Unknown == nil || !reflect.DeepEqual(*recovery.Unknown, unknown) {
		t.Fatal("recovery changed or replayed original unknown write")
	}
	o, err := nativeDestroyImpactFacet(t, recovery.Lease).ObserveHTTPSessionDestroyImpact(context.Background(), nativeDestroyImpactRequest(id))
	if err == nil || o.Complete || o.Session.HTTP == nil || o.Keys.Root.HTTP != nil {
		t.Fatal("absent Session granted impact expectation", err)
	}
	request, err := DocumentRequest(nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.Lease.Prepare(request); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("later absence cleared original unknown barrier", err)
	}
}

func TestConsulNativeSessionDestroyImpactProcessedRootFaultsAndOriginalAcknowledgement(t *testing.T) {
	base, cfg, token := nativeClient(t)
	cfg.Prefix = "configured-empty/"
	id := nativeSessionToolCreate(t, base, t.Name(), "10m", "release")
	defer nativeDo(t, base, nativeSessionToolID(t, contract.KVToolHTTPSessionDestroy, id))
	nativeSessionKeysAcquire(t, base, id, keyFor(t, "outside"), []byte{0, 255})
	for _, mode := range []string{"lost", "partial", "denied", "malformed", "capture-overflow", "cancel", "retire", "close"} {
		t.Run(mode, func(t *testing.T) {
			relay, calls, processed := nativeSessionKeysRelay(t, cfg, token, mode)
			s, err := OpenNativeSession(context.Background(), 2304, relay, token)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			l, err := s.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				impact contract.KVHTTPSessionDestroyImpact
				err    error
			}
			done := make(chan result, 1)
			facet := nativeDestroyImpactFacet(t, l)
			go func() {
				o, err := facet.ObserveHTTPSessionDestroyImpact(ctx, nativeDestroyImpactRequest(id))
				done <- result{o, err}
			}()
			wait(t, processed)
			switch mode {
			case "cancel":
				cancel()
			case "retire":
				s.Close()
			case "close":
				l.Close()
			}
			var out result
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original impact executor did not acknowledge termination")
			}
			if out.err == nil || out.impact.Complete || out.impact.Session.HTTP == nil || !out.impact.Session.HTTP.Acknowledged || out.impact.Keys.Root.HTTP == nil || !out.impact.Keys.Root.HTTP.Ended || out.impact.Keys.Root.WriteOutcomeUnknown || calls.Load() != 1 {
				t.Fatal("root failure lost original info/ack or became write evidence", out.err)
			}
			if _, err := SessionDestroyExpectation(out.impact); err == nil {
				t.Fatal("failed root granted destroy expectation")
			}
		})
	}
}
