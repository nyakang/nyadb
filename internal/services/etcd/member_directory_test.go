package etcd

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMemberDirectoryOriginalHandleAuthorityCloseAndIndependentUnknownReceipt(t *testing.T) {
	c, _ := memberRelayClient(t, nil, Config{}, "", "ok")
	p, _ := connections.Defaults(connections.Etcd)
	p.ID = 81
	s := newSession(p, "", c)
	defer s.Close()
	s.opener = func(ctx context.Context, _ connections.Profile, _ string) (*Client, error) {
		return Open(ctx, c.config, "")
	}
	raw, err := s.ReserveKV(17)
	if err != nil {
		t.Fatal(err)
	}
	l := raw.(*kvLease)
	l.last = contract.KVResult{Request: contract.KVRequest{Method: "original-write"}, Native: []byte{7}, WriteOutcomeUnknown: true}
	observation, err := l.ObserveMembers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	o := observation.(*memberObservation)
	view := o.Snapshot()
	if view.DocumentID != 17 || view.Scope.Namespace.ProfileID != p.ID || view.Result.Header.Revision != 0 || !view.Result.Completed || !view.Result.NativeCaptured || len(view.Members) != 1 || !l.LastResult().WriteOutcomeUnknown || l.LastResult().Request.Method != "original-write" || s.memberRetainedBytes == 0 {
		t.Fatal("directory replaced original write or lost complete scoped native receipt", view)
	}
	view.Members[0].ClientURLs[0] = "forged"
	view.Result.Native[0] ^= 1
	target := contract.KVMemberTarget{MemberID: 9}
	d, member, err := l.memberDirectory(o, target)
	if err != nil || member.ClientURLs[0] != "http://declared:2379" || d.Result.Native[0] == view.Result.Native[0] {
		t.Fatal("inspection altered original authority", err)
	}
	forged := *o
	if _, _, err := l.memberDirectory(&forged, target); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("copied handle acquired authority", err)
	}
	if err := o.Close(); err != nil || s.memberRetainedBytes != 0 || len(o.Snapshot().Members) != 0 {
		t.Fatal("close did not release original cache", err)
	}
	if _, _, err := l.memberDirectory(o, target); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed observation remained usable", err)
	}
	second, err := l.ObserveMembers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil || s.memberRetainedBytes != 0 {
		t.Fatal("original document close retained directory budget", err)
	}
	if len(second.Snapshot().Members) != 1 {
		t.Fatal("revocation erased caller-owned original read evidence")
	}
	if _, _, err := l.memberDirectory(second, target); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("revoked document retained target authority", err)
	}
	second.Close()
}
