package etcd

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func memberTestLease(t *testing.T) (*kvLease, *memberProtocolRelay) {
	t.Helper()
	c, relay := memberRelayClient(t, nil, Config{}, "", "ok")
	p, _ := connections.Defaults(connections.Etcd)
	p.ID = 83
	s := newSession(p, "", c)
	s.opener = func(ctx context.Context, _ connections.Profile, _ string) (*Client, error) {
		return Open(ctx, c.config, "")
	}
	t.Cleanup(func() { s.Close() })
	raw, err := s.ReserveKV(19)
	if err != nil {
		t.Fatal(err)
	}
	l := raw.(*kvLease)
	t.Cleanup(func() { l.Close() })
	return l, relay
}

func memberTestBind(t *testing.T, l *kvLease, endpoint string) *memberLease {
	t.Helper()
	o, err := l.ObserveMembers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	b, err := l.BindMember(context.Background(), o, contract.KVMemberTarget{MemberID: 9, Endpoint: endpoint, Version: "3.7.3"})
	if err != nil || b.Lease == nil {
		t.Fatal("original member bind", err, b.Error)
	}
	return b.Lease.(*memberLease)
}

func TestMemberLeasePureOneUseFullReceiptsAndOriginalBudget(t *testing.T) {
	l, root := memberTestLease(t)
	c, target := memberRelayClient(t, nil, Config{}, "", "extensions")
	m := memberTestBind(t, l, c.config.Endpoint)
	scope := m.Scope()
	if scope.DocumentID != 19 || scope.LeaseID == 0 || scope.Member.ID != 9 || !scope.Handshake.NativeCaptured || scope.Handshake.Reply.Status.Version != "3.7.3" {
		t.Fatal("original binding evidence", scope)
	}
	scope.Directory.Members[0].Name = "mutated"
	if m.Scope().Directory.Members[0].Name != "source" {
		t.Fatal("scope aliases inspection")
	}
	p, err := m.PrepareDefragment()
	if err != nil || target.calls.Load() != 0 || root.calls.Load() != 0 {
		t.Fatal("preparation dispatched")
	}
	copied := reflect.New(reflect.TypeOf(p).Elem())
	copied.Elem().Set(reflect.ValueOf(p).Elem())
	if _, err := m.ExecuteMember(context.Background(), copied.Interface().(contract.KVMemberPreparation)); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("copied preparation acquired authority", err)
	}
	r, err := m.ExecuteMember(context.Background(), p)
	if err != nil || !r.Result.Completed || r.Result.WriteOutcomeUnknown || !r.Result.NativeCaptured || len(r.Result.Native) == 0 || r.Result.Header != (contract.KVHeader{}) || !r.Before.Completed || !r.DirectoryCheck.Result.Completed || r.Sequence != 1 || target.calls.Load() != 1 || root.calls.Load() != 0 {
		t.Fatal("complete original write observations", err, r)
	}
	var ack pb.DefragmentResponse
	if proto.Unmarshal(r.Result.Native, &ack) != nil || len(ack.ProtoReflect().GetUnknown()) == 0 || ack.Header != nil {
		t.Fatal("native ACK extensions/header lost")
	}
	r.Result.Native[0] ^= 1
	if m.LastMemberResult().Result.Native[0] == r.Result.Native[0] {
		t.Fatal("last receipt aliases caller")
	}
	if _, err := m.ExecuteMember(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || target.calls.Load() != 1 {
		t.Fatal("original plan replayed", err)
	}
	copiedLease := reflect.New(reflect.TypeOf(m).Elem())
	copiedLease.Elem().Set(reflect.ValueOf(m).Elem())
	if err := copiedLease.Interface().(contract.KVMemberLease).CheckMemberLease(); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("copied lease authorized", err)
	}
	if err := copiedLease.Interface().(contract.KVMemberLease).Close(); !errors.Is(err, contract.ErrKVPlanUsed) || m.CheckMemberLease() != nil {
		t.Fatal("copied lease closed original", err)
	}
	if err := l.Close(); err != nil || l.s.memberRetainedBytes != 0 || l.memberRetainedBytes != 0 {
		t.Fatal("close retained original member budgets", err, l.s.memberRetainedBytes)
	}
}

func TestMemberLeaseFreshSelectedMetadataRejectsChangesWithoutDispatch(t *testing.T) {
	for _, field := range []string{"name", "peer", "client", "learner", "extensions", "removed", "unrelated"} {
		t.Run(field, func(t *testing.T) {
			l, root := memberTestLease(t)
			c, target := memberRelayClient(t, nil, Config{}, "", "ok")
			m := memberTestBind(t, l, c.config.Endpoint)
			d := &pb.MemberListResponse{Header: &pb.ResponseHeader{ClusterId: 7, MemberId: 9, RaftTerm: 2}, Members: []*pb.Member{{ID: 9, Name: "source", ClientURLs: []string{"http://declared:2379"}}}}
			switch field {
			case "name":
				d.Members[0].Name = "renamed"
			case "peer":
				d.Members[0].PeerURLs = []string{"http://other:2380"}
			case "client":
				d.Members[0].ClientURLs = []string{"http://replaced:2379"}
			case "learner":
				d.Members[0].IsLearner = true
			case "extensions":
				d.Members[0].ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
			case "removed":
				d.Members = []*pb.Member{{ID: 8}} // root validation also rejects removing the original root
			case "unrelated":
				d.Members = append(d.Members, &pb.Member{ID: 8, Name: "unrelated"})
			}
			root.directory.Store(d)
			p, _ := m.PrepareDefragment()
			r, err := m.ExecuteMember(context.Background(), p)
			if !r.DirectoryCheck.Result.NativeCaptured {
				t.Fatal("fresh rejected directory evidence missing", err)
			}
			if field == "unrelated" {
				if err != nil || !r.Result.Completed || target.calls.Load() != 1 {
					t.Fatal("unrelated membership change blocked selected target", err)
				}
			} else if err == nil || r.Result.Attempted || r.Result.WriteOutcomeUnknown || target.calls.Load() != 0 {
				t.Fatal("changed original member dispatched", err)
			}
		})
	}
}

func TestMemberLeaseUnknownSurvivesOtherStatusChildCloseRetirementAndPureRecovery(t *testing.T) {
	l, _ := memberTestLease(t)
	c, target := memberRelayClient(t, nil, Config{}, "", "lost")
	m := memberTestBind(t, l, c.config.Endpoint)
	healthy, _ := memberRelayClient(t, nil, Config{}, "", "ok")
	other := memberTestBind(t, l, healthy.config.Endpoint)
	p, _ := m.PrepareDefragment()
	r, err := m.ExecuteMember(context.Background(), p)
	if err == nil || !r.Result.WriteOutcomeUnknown || !r.Before.Completed || target.calls.Load() != 1 {
		t.Fatal("lost original write settled", err)
	}
	if _, err := other.ObserveStatus(context.Background()); err != nil {
		t.Fatal("independent Status unavailable", err)
	}
	m.Close()
	next, _ := other.PrepareDefragment()
	if _, err := other.ExecuteMember(context.Background(), next); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("another member wrote through unknown barrier", err)
	}
	kv, _ := l.Prepare(contract.KVRequest{Method: "/etcdserverpb.KV/Put", JSON: `{"key":"YQ==","value":"Yg=="}`})
	if _, err := l.Execute(context.Background(), kv); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("parent wrote through member unknown barrier", err)
	}
	if err := l.s.Close(); err != nil {
		t.Fatal(err)
	}
	if l.s.memberRetainedBytes == 0 {
		t.Fatal("retirement discarded parked original evidence")
	}
	root, _ := memberRelayClient(t, nil, Config{}, "", "ok")
	s := newSession(l.s.profile, "", root)
	defer s.Close()
	recovery, err := s.RecoverKV(l)
	if err != nil || recovery.Lease == nil || len(recovery.MemberResults) != 2 || target.calls.Load() != 1 || l.s.memberRetainedBytes != 0 {
		t.Fatal("pure recovery lost original member receipts", err, len(recovery.MemberResults))
	}
	defer recovery.Lease.Close()
	if _, err := s.RecoverKV(l); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("consumed original lease recovered twice", err)
	}
	var unknown *contract.KVMemberResult
	for i := range recovery.MemberResults {
		if recovery.MemberResults[i].Result.WriteOutcomeUnknown {
			unknown = &recovery.MemberResults[i]
		}
	}
	if unknown == nil || unknown.Scope.LeaseID != r.Scope.LeaseID || unknown.Scope.DocumentID != 19 || unknown.Scope.Original.Generation != r.Scope.Original.Generation || !unknown.Before.Completed {
		t.Fatal("recovery rewrote original unknown identity")
	}
	kv, _ = recovery.Lease.Prepare(contract.KVRequest{Method: "/etcdserverpb.KV/Put", JSON: `{"key":"YQ=="}`})
	if _, err := recovery.Lease.Execute(context.Background(), kv); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("recovery cleared unknown barrier", err)
	}
}

func TestMemberLeaseCloseCancelsAndWaitsForOriginalAcknowledgement(t *testing.T) {
	for _, closeParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "child", true: "parent"}[closeParent], func(t *testing.T) {
			l, _ := memberTestLease(t)
			c, target := memberRelayClient(t, nil, Config{}, "", "close")
			m := memberTestBind(t, l, c.config.Endpoint)
			p, _ := m.PrepareDefragment()
			type reply struct {
				r   contract.KVMemberResult
				err error
			}
			done := make(chan reply, 1)
			go func() { r, err := m.ExecuteMember(context.Background(), p); done <- reply{r, err} }()
			select {
			case <-target.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("write did not dispatch")
			}
			if closeParent {
				l.Close()
			} else {
				m.Close()
			}
			select {
			case got := <-done:
				if got.err == nil || !got.r.Result.WriteOutcomeUnknown || !got.r.Before.Completed {
					t.Fatal("close lost original executor evidence", got.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("close returned before executor settled")
			}
			select {
			case <-target.done:
			case <-time.After(5 * time.Second):
				t.Fatal("original server acknowledgement missing")
			}
			if target.calls.Load() != 1 {
				t.Fatal("close replayed")
			}
		})
	}
}

func TestMemberLeaseBudgetFailureBeforeDispatchPreservesPreparation(t *testing.T) {
	l, _ := memberTestLease(t)
	c, target := memberRelayClient(t, nil, Config{}, "", "ok")
	m := memberTestBind(t, l, c.config.Endpoint)
	p, _ := m.PrepareDefragment()
	l.adjustMemberBytes(60 << 20)
	if r, err := m.ExecuteMember(context.Background(), p); !errors.Is(err, ErrBounds) || r.Result.Attempted || target.calls.Load() != 0 {
		t.Fatal("budget admitted native write", err)
	}
	l.adjustMemberBytes(-(60 << 20))
	if r, err := m.ExecuteMember(context.Background(), p); err != nil || !r.Result.Completed || !r.Result.NativeCaptured || len(r.Result.Native) != 0 || r.Result.Header != (contract.KVHeader{}) {
		t.Fatal("valid empty native ACK lost", err)
	}
}

func TestMemberLeaseRejectedCandidateRetainsActualHandshakeAndIndependentVersion(t *testing.T) {
	for _, mode := range []string{"before-foreign", "before-member", "wrong-version", "mixed-version"} {
		t.Run(mode, func(t *testing.T) {
			l, _ := memberTestLease(t)
			faultMode := mode
			if mode == "wrong-version" {
				faultMode = "ok"
			}
			if mode == "mixed-version" {
				faultMode = "before-version"
			}
			c, _ := memberRelayClient(t, nil, Config{}, "", faultMode)
			o, err := l.ObserveMembers(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer o.Close()
			version := "3.7.3"
			if mode == "wrong-version" || mode == "mixed-version" {
				version = "3.5.35"
			}
			before := l.s.memberRetainedBytes
			b, err := l.BindMember(context.Background(), o, contract.KVMemberTarget{MemberID: 9, Endpoint: c.config.Endpoint, Version: version})
			if mode == "mixed-version" {
				if err != nil || b.Lease == nil || b.Scope.Version != "3.5.35" || b.Scope.Original.Version != "3.7.3" || b.Scope.Handshake.Reply.Status.Version != "3.5.35" {
					t.Fatal("target inherited root member version", err)
				}
				b.Lease.Close()
			} else {
				if err == nil || b.Lease != nil || !b.Scope.Handshake.NativeCaptured || b.Scope.Handshake.Completed || b.Scope.Handshake.Reply.Status == nil || l.s.memberRetainedBytes != before || l.CheckKVLease() != nil {
					t.Fatal("failed candidate lost actual Status or retired root", err)
				}
				if mode == "before-foreign" && b.Scope.Handshake.Header.ClusterID != 8 {
					t.Fatal("foreign actual header overwritten")
				}
				if mode == "before-member" && b.Scope.Handshake.Header.MemberID != 10 {
					t.Fatal("foreign actual member overwritten")
				}
			}
		})
	}
}

func TestMemberLeaseLearnerStatusDoesNotEnableDefragment(t *testing.T) {
	l, root := memberTestLease(t)
	root.directory.Store(&pb.MemberListResponse{Header: &pb.ResponseHeader{ClusterId: 7, MemberId: 9, RaftTerm: 2}, Members: []*pb.Member{{ID: 9, Name: "source", IsLearner: true, ClientURLs: []string{"http://declared:2379"}}}})
	c, target := memberRelayClient(t, nil, Config{}, "", "learner")
	m := memberTestBind(t, l, c.config.Endpoint)
	if r, err := m.ObserveStatus(context.Background()); err != nil || !r.Result.Reply.Status.IsLearner {
		t.Fatal("learner Status unavailable", err)
	}
	if _, err := m.PrepareDefragment(); !errors.Is(err, contract.ErrUnsupported) || target.calls.Load() != 0 {
		t.Fatal("learner promoted/maintenance prepared", err)
	}
}

func TestMemberFollowerGuardUsesFinalStatusAndConsumesOnlyOriginalPreparation(t *testing.T) {
	for _, leader := range []uint64{0, 9, 10} {
		t.Run(fmt.Sprint(leader), func(t *testing.T) {
			l, root := memberTestLease(t)
			c, target := memberRelayClient(t, nil, Config{}, "", "ok")
			m := memberTestBind(t, l, c.config.Endpoint)
			follower := &pb.StatusResponse{Header: &pb.ResponseHeader{ClusterId: 7, MemberId: 9, Revision: 11, RaftTerm: 2}, Version: "3.7.3", Leader: 10}
			target.statusObservation.Store(follower)
			ordering, err := m.ObserveStatus(context.Background())
			if err != nil || ordering.Result.Reply.Status.Leader != 10 {
				t.Fatal("earlier follower observation missing", err)
			}
			p, err := m.PrepareFollowerDefragment()
			if err != nil || target.calls.Load() != 0 {
				t.Fatal("pure follower preparation dispatched", err)
			}
			final := proto.Clone(follower).(*pb.StatusResponse)
			final.Leader = leader
			target.statusObservation.Store(final)
			r, err := m.ExecuteMember(context.Background(), p)
			if !r.Before.Completed || !r.Before.NativeCaptured || r.Before.Reply.Status.Leader != leader || !r.DirectoryCheck.Result.Completed || r.Sequence != 2 || root.calls.Load() != 0 {
				t.Fatal("final independent observation lost", err, r)
			}
			if leader == 10 {
				if err != nil || !r.Result.Completed || !r.Result.NativeCaptured || len(r.Result.Native) != 0 || target.calls.Load() != 1 {
					t.Fatal("follower write did not retain native empty ACK", err)
				}
			} else {
				expected := contract.ErrKVMemberBecameLeader
				if leader == 0 {
					expected = contract.ErrKVMemberOrderUnknown
				}
				if !errors.Is(err, expected) || r.Result.Attempted || r.Result.WriteOutcomeUnknown || target.calls.Load() != 0 || m.CheckMemberLease() != nil {
					t.Fatal("changed leadership dispatched or retired transport", err, r)
				}
				if m.LastMemberResult().Before.Reply.Status.Leader != leader || len(l.memberResultsOwned()) != 1 {
					t.Fatal("rejected ordering evidence not retained")
				}
			}
			if _, err := m.ExecuteMember(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("guarded preparation replayed", err)
			}
		})
	}
}
