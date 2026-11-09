package etcd

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Synthetic observed identities exercise the injected opener's binding guard;
// real transport/authentication acceptance belongs to the native fixture.
func sessionIdentityClient(cluster uint64) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{ctx: ctx, cancel: cancel, identity: Identity{Header: Header{ClusterID: cluster, MemberID: 3, Revision: 1, RaftTerm: 1}, Version: "3.7.3"}, connClosed: true}
}
func TestSessionChangedObservedIdentityRevokesOnlyOriginal(t *testing.T) {
	p, _ := connections.Defaults(connections.Etcd)
	p.ID = 1
	s := newSession(p, "", sessionIdentityClient(2))
	defer s.Close()
	different := sessionIdentityClient(9)
	replacement := sessionIdentityClient(9)
	defer replacement.Close()
	s.opener = func(context.Context, connections.Profile, string) (*Client, error) { return different, nil }
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Preconnect(context.Background()); !errors.Is(err, contract.ErrRevoked) || s.Alive() || different.Alive() || !replacement.Alive() {
		t.Fatal("changed identity accepted or foreign replacement closed", err)
	}
	if _, err = s.ReserveKV(2); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("revoked session reserved a document")
	}
}
func TestSessionRecoveryRejectsForeignScopeBeforeClosingOrOpening(t *testing.T) {
	p, _ := connections.Defaults(connections.Etcd)
	p.ID = 1
	first := newSession(p, "", sessionIdentityClient(2))
	defer first.Close()
	p.EtcdOptions.Prefix = "different"
	second := newSession(p, "", sessionIdentityClient(2))
	defer second.Close()
	var opens atomic.Int32
	second.opener = func(context.Context, connections.Profile, string) (*Client, error) {
		opens.Add(1)
		return nil, ErrInvalid
	}
	original, err := first.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = second.RecoverKV(original); !errors.Is(err, contract.ErrRevoked) || original.CheckKVLease() != nil || opens.Load() != 0 {
		t.Fatal("foreign recovery altered original")
	}
	var nilLease *kvLease
	if _, err = second.RecoverKV(nilLease); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("typed nil recovery accepted")
	}
}

func TestSessionOriginalAndConcurrentOpenerProfilesOwnVariants(t *testing.T) {
	p, _ := connections.Defaults(connections.Etcd)
	p.ID = 1
	p.Variants = []connections.ConnectionVariant{{Name: "original"}}
	s := newSession(p, "", sessionIdentityClient(2))
	defer s.Close()
	p.Variants[0].Name = "external change"
	if s.profile.Variants[0].Name != "original" {
		t.Fatal("session aliases external profile")
	}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	s.opener = func(_ context.Context, p connections.Profile, _ string) (*Client, error) {
		p.Variants[0].Name = "opener change"
		entered <- struct{}{}
		<-release
		return sessionIdentityClient(2), nil
	}
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			c, err := s.open(context.Background())
			if c != nil {
				_ = c.Close()
			}
			done <- err
		}()
	}
	<-entered
	<-entered
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if s.profile.Variants[0].Name != "original" {
		t.Fatal("concurrent opener mutated original snapshot")
	}
}
