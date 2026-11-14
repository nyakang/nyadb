package consul

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestHTTPCatalogWatchTargetIsolationNativeRoutingAndProjection(t *testing.T) {
	r := contract.KVHTTPWatchRequest{Catalog: contract.KVHTTPCatalogWatchTarget{Kind: "services", IncludeFilter: true}, Wait: time.Second, MaxObservations: 2, MaxBytes: 8 << 20}
	native, err := httpWatchNativeRequest(r)
	if err != nil || native.Path != "/v1/catalog/services" || !native.Query.Has("filter") || native.Query.Has("recurse") {
		t.Fatal("catalog became KV watch", err)
	}
	for _, mutate := range []func(*contract.KVHTTPWatchRequest){func(r *contract.KVHTTPWatchRequest) { r.Target = "foreign" }, func(r *contract.KVHTTPWatchRequest) { r.Health.Kind = "service" }, func(r *contract.KVHTTPWatchRequest) { r.Catalog.Name = "foreign" }, func(r *contract.KVHTTPWatchRequest) { r.Catalog.Kind = "datacenters" }} {
		bad := r
		mutate(&bad)
		if _, err = httpWatchNativeRequest(bad); err == nil {
			t.Fatal("mixed watch accepted")
		}
	}
	s, _ := documentSession(t, func(w http.ResponseWriter, in *http.Request) {
		w.Header().Set("X-Consul-Index", "9")
		w.Write([]byte(`{"original":["tag"]}`))
	})
	l, err := s.ReserveKVHTTPWatch(2)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p, err := l.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	run, err := l.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	deadline := time.After(2 * time.Second)
	for !run.Poll().Ended {
		select {
		case <-deadline:
			t.Fatal("catalog watch did not settle")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	snapshot := run.Snapshot()
	if len(snapshot.Observations) != 2 || !reflect.DeepEqual(snapshot.Request, r) || snapshot.Observations[0].HTTP.Catalog == nil || !snapshot.Observations[0].ProjectionComplete {
		t.Fatalf("catalog observations lost: %#v", snapshot)
	}
	if _, err = l.Start(context.Background(), p); !errors.Is(err, contract.ErrKVBusy) {
		t.Fatal("retained evidence replaced", err)
	}
}
