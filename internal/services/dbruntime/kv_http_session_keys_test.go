package dbruntime_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type sessionKeysFakeLease struct {
	*kvFakeLease
	reads   atomic.Int32
	observe func(context.Context, contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error)
}

func (l *sessionKeysFakeLease) ObserveHTTPSessionKeys(ctx context.Context, request contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
	l.reads.Add(1)
	return l.observe(ctx, request)
}

func sessionKeysRuntime(t *testing.T, facet, consul bool) (*dbruntime.Manager, *[]*sessionKeysFakeLease) {
	t.Helper()
	m := dbruntime.New()
	t.Cleanup(func() { m.Close() })
	leases := new([]*sessionKeysFakeLease)
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := kvHandleFor(p.ID)
		if consul {
			h.scope.HTTP.Protocol, h.scope.HTTP.Endpoint = "consul-http", "https://original"
			h.scope.Version = "2.0.2"
		}
		h.reserve = func(uint64) (contract.KVRequestLease, error) {
			base := &kvFakeLease{scope: h.scope.Clone()}
			l := &sessionKeysFakeLease{kvFakeLease: base}
			l.observe = func(_ context.Context, request contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
				return contract.KVHTTPSessionKeysObservation{Scope: h.scope.Clone(), Request: request, Root: contract.KVResult{Attempted: true, Completed: true, HTTP: &contract.KVHTTPResult{Body: []byte("original root"), Entries: []contract.KVHTTPEntry{{Key: "outside", Value: []byte{0, 255}, Session: request.ID}}}}, Matches: []int{0}, Bytes: 100, ProjectionComplete: true, Complete: true}, nil
			}
			*leases = append(*leases, l)
			if !facet {
				return base, nil
			}
			return l, nil
		}
		h.recover = func(old contract.KVRequestLease) (contract.KVRecovery, error) {
			_ = old.Close()
			fresh, _ := h.reserve(1)
			unknown := contract.KVResult{WriteOutcomeUnknown: true, HTTP: &contract.KVHTTPResult{Body: []byte("unknown destroy")}}
			return contract.KVRecovery{Lease: fresh, Last: unknown, Unknown: &unknown}, nil
		}
		return h, nil
	}))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	return m, leases
}

func TestKVHTTPSessionKeysFacetPublicationClonesOriginalEvidenceAndRecoveryDoesNotRead(t *testing.T) {
	m, leases := sessionKeysRuntime(t, true, true)
	p, _ := m.KVRequests(1)
	l, err := p.ReserveKV(71)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	facet, ok := l.(contract.KVHTTPSessionKeysBrowsing)
	if !ok || (*leases)[0].reads.Load() != 0 {
		t.Fatal("reservation lost original optional facet or observed keys")
	}
	request := contract.KVHTTPSessionKeysRequest{ID: "original", MaxRows: 100, MaxBytes: 4096}
	raw, _ := (*leases)[0].observe(context.Background(), request)
	(*leases)[0].observe = func(context.Context, contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
		return raw, nil
	}
	o, err := facet.ObserveHTTPSessionKeys(context.Background(), request)
	if err != nil || o.Scope.Generation != m.Generation(1) || o.Scope.HTTP != raw.Scope.HTTP || o.Request != request || !o.Complete {
		t.Fatal("runtime changed original scope, target or complete receipt", err)
	}
	o.Scope.Prefix[0] = 9
	o.Root.HTTP.Body[0] = 'X'
	o.Root.HTTP.Entries[0].Value[0] = 9
	o.Matches[0] = -1
	if raw.Scope.Prefix[0] != 0 || raw.Root.HTTP.Body[0] != 'o' || raw.Root.HTTP.Entries[0].Value[0] != 0 || raw.Matches[0] != 0 {
		t.Fatal("published observation aliases raw scope, bytes or matches")
	}
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	p, _ = m.KVRequests(1)
	before := (*leases)[0].reads.Load()
	recovered, err := p.RecoverKV(l)
	if err != nil || recovered.Unknown == nil || (*leases)[0].reads.Load() != before || (*leases)[1].reads.Load() != 0 || recovered.Lease.Scope().Generation <= raw.Scope.Generation {
		t.Fatal("recovery did not preserve original unknown receipt without reading", err)
	}
	defer recovered.Lease.Close()
	if _, err := facet.ObserveHTTPSessionKeys(context.Background(), request); !errors.Is(err, contract.ErrRevoked) || (*leases)[0].reads.Load() != before {
		t.Fatal("retired original publication performed root I/O", err)
	}
	fresh := recovered.Lease.(contract.KVHTTPSessionKeysBrowsing)
	observed, err := fresh.ObserveHTTPSessionKeys(context.Background(), request)
	if err != nil || observed.Scope.Generation != m.Generation(1) || !observed.Complete || string(recovered.Unknown.HTTP.Body) != "unknown destroy" {
		t.Fatal("explicit fresh observation changed original recovery evidence", err)
	}
}

func TestKVHTTPSessionKeysRejectsUnsupportedForeignAndRetiredPublicationWithoutRetargeting(t *testing.T) {
	for _, options := range [][2]bool{{false, true}, {true, false}} {
		m, _ := sessionKeysRuntime(t, options[0], options[1])
		p, _ := m.KVRequests(1)
		l, err := p.ReserveKV(1)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := l.(contract.KVHTTPSessionKeysBrowsing); ok {
			t.Fatal("unsupported raw lease or foreign engine advertised Session held keys")
		}
		l.Close()
	}
	m, leases := sessionKeysRuntime(t, true, true)
	p, _ := m.KVRequests(1)
	l, err := p.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	facet := l.(contract.KVHTTPSessionKeysBrowsing)
	request := contract.KVHTTPSessionKeysRequest{ID: "original", MaxRows: 100, MaxBytes: 4096}
	base, _ := (*leases)[0].observe(context.Background(), request)
	for _, change := range []func(*contract.KVHTTPSessionKeysObservation){
		func(o *contract.KVHTTPSessionKeysObservation) { o.Scope.Namespace.ProfileID = 2 },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Scope.Generation++ },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Scope.Prefix = []byte("foreign") },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Scope.HTTP.Endpoint = "https://foreign" },
		func(o *contract.KVHTTPSessionKeysObservation) { o.Request.ID = "foreign" },
	} {
		original := base.Clone()
		change(&original)
		(*leases)[0].observe = func(context.Context, contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
			return original, nil
		}
		o, err := facet.ObserveHTTPSessionKeys(context.Background(), request)
		if !errors.Is(err, contract.ErrRevoked) || o.Complete || o.ProjectionComplete || o.Matches != nil || !reflect.DeepEqual(o.Root, original.Root) || o.Scope.Namespace != original.Scope.Namespace || o.Scope.Generation != original.Scope.Generation {
			t.Fatal("foreign receipt acquired current authority or lost actual captured identity", err)
		}
	}
	started, release := make(chan struct{}), make(chan struct{})
	(*leases)[0].observe = func(context.Context, contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
		close(started)
		<-release
		return base, nil
	}
	type answer struct {
		o   contract.KVHTTPSessionKeysObservation
		err error
	}
	done := make(chan answer, 1)
	go func() { o, err := facet.ObserveHTTPSessionKeys(context.Background(), request); done <- answer{o, err} }()
	kvAwait(t, started)
	oldGeneration := l.Scope().Generation
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-done
	if !errors.Is(result.err, contract.ErrRevoked) || result.o.Scope.Generation != oldGeneration || result.o.Scope.Generation == m.Generation(1) || !reflect.DeepEqual(result.o.Root, base.Root) {
		t.Fatal("late original root observation was relabeled as a new publication", result.err)
	}
}
