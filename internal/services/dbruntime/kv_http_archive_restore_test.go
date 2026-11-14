package dbruntime_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestHTTPArchiveRestoreRuntimeRevokesAllConsulAndKeepsPublishedOriginalRecovery(t *testing.T) {
	var restores atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Consul-Index", "9")
		switch r.URL.Path {
		case "/v1/agent/self":
			fmt.Fprint(w, `{"Config":{"Version":"1.22.3","NodeID":"`+runtimeDestroyUUID+`","NodeName":"original","Datacenter":"dc1"}}`)
		case "/v1/status/leader":
			fmt.Fprint(w, `"127.0.0.1:8300"`)
		case "/v1/kv/":
			fmt.Fprint(w, `[]`)
		case "/v1/snapshot":
			restores.Add(1)
			close(entered)
			<-release
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	m := dbruntime.New()
	defer m.Close()
	m.Register(connections.Consul, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, _ string) (contract.Session, error) {
		return consul.OpenNativeSession(ctx, p.ID, consul.Config{Endpoint: server.URL, RestoreWrite: true}, "")
	}))
	profiles := make([]connections.Profile, 2)
	for i := range profiles {
		p, _ := connections.Defaults(connections.Consul)
		p.ID = int64(i + 1)
		p.Name = "fixture"
		profiles[i] = p
		if err := m.Connect(context.Background(), p, ""); err != nil {
			t.Fatal(err)
		}
	}
	provider, _ := m.KVRequests(1)
	l, _ := provider.ReserveKV(1)
	defer l.Close()
	otherProvider, _ := m.KVRequests(2)
	other, _ := otherProvider.ReserveKV(2)
	defer other.Close()
	restore, ok := l.(contract.KVHTTPArchiveRestoring)
	if !ok {
		t.Fatal("restore binding missing")
	}
	p, err := restore.PrepareHTTPArchiveRestore(contract.KVHTTPArchiveRestoreRequest{Archive: runtimeArchiveBytes(t), ExpandedBytes: 2 << 20})
	if err != nil {
		t.Fatal(err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, e := restore.ExecuteHTTPArchiveRestore(canceled, p); !errors.Is(e, context.Canceled) || !m.IsConnected(1) || !m.IsConnected(2) || restores.Load() != 0 || len(m.ConsumeHTTPArchiveRevocations()) != 0 {
		t.Fatal("pre-dispatch cancellation revoked original sessions or sent restore", e)
	}
	expected := p.Scope()
	done := make(chan contract.KVHTTPArchiveRestoreResult, 1)
	failed := make(chan error, 1)
	go func() { r, e := restore.ExecuteHTTPArchiveRestore(context.Background(), p); done <- r; failed <- e }()
	<-entered
	if other.CheckKVLease() == nil || m.IsConnected(2) {
		t.Fatal("affected session remained connected")
	}
	if err = m.Connect(context.Background(), profiles[1], ""); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("connection opened during restore", err)
	}
	close(release)
	r := <-done
	if err = <-failed; err != nil || !r.Completed || !reflect.DeepEqual(r.Scope, expected) || m.IsConnected(1) || restores.Load() != 1 {
		t.Fatal("restore result/publication lost", err)
	}
	if !reflect.DeepEqual(m.ConsumeHTTPArchiveRevocations(), []int64{1, 2}) || len(m.ConsumeHTTPArchiveRevocations()) != 0 {
		t.Fatal("revocation notification changed")
	}
	if err = m.Connect(context.Background(), profiles[0], ""); err != nil {
		t.Fatal(err)
	}
	next, _ := m.KVRequests(1)
	recovered, err := next.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Lease.Close()
	if recovered.RestoreResult == nil || !reflect.DeepEqual(*recovered.RestoreResult, r) || recovered.Lease.Scope().Generation <= r.Scope.Original.Generation || restores.Load() != 1 {
		t.Fatal("recovery changed original receipt or replayed")
	}
}
