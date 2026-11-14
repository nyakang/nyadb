package consul

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConsulNativeCatalogWatchBlockingChangeCancellationAndPureRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	name := "nyadb-catalog-watch-" + rand.Text()
	initial := nativeDo(t, base, CatalogServicesRequest())
	if initial.Metadata.Index == nil {
		t.Fatal("native Catalog index unavailable")
	}
	s, started, _ := nativeHTTPWatchSession(t, cfg, token)
	l, err := s.ReserveKVHTTPWatch(401)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	request := contract.KVHTTPWatchRequest{Catalog: contract.KVHTTPCatalogWatchTarget{Kind: "services"}, StartIndex: *initial.Metadata.Index, Wait: 2 * time.Second, MaxObservations: 1, MaxBytes: 8 << 20}
	run := startHTTPWatch(t, l, request)
	awaitHTTPWatchRead(t, started)
	register, err := AgentServiceRegisterRequest([]byte(`{"ID":"`+name+`","Name":"`+name+`"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	nativeDo(t, base, register)
	t.Cleanup(func() {
		deregister, _ := AgentServiceDeregisterRequest(name)
		_, _ = base.Do(context.Background(), deregister)
	})
	changed := settledHTTPWatch(t, run)
	if len(changed.Observations) != 1 || !changed.Observations[0].ProjectionComplete || changed.Observations[0].HTTP.Catalog == nil || changed.Observations[0].HTTP.Metadata.Index == nil || *changed.Observations[0].HTTP.Metadata.Index <= *initial.Metadata.Index {
		t.Fatal("Catalog change not observed")
	}
	next, err := s.ReserveKVHTTPWatch(402)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	request.StartIndex = *changed.Observations[0].HTTP.Metadata.Index
	blocked := startHTTPWatch(t, next, request)
	awaitHTTPWatchRead(t, started)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	original := settledHTTPWatch(t, blocked)
	if len(original.Observations) != 1 || !original.Observations[0].HTTP.Canceled || !original.Observations[0].HTTP.Ended {
		t.Fatal("Catalog cancellation lost original terminal receipt")
	}
	fresh, _, opens := nativeHTTPWatchSession(t, cfg, token)
	recovered, err := fresh.RecoverKVHTTPWatch(next)
	if err != nil || recovered.Watch == nil || opens.Load() != 0 || recovered.Watch.Request != request {
		t.Fatal("Catalog recovery reopened or changed request", err)
	}
	defer recovered.Lease.Close()
}
