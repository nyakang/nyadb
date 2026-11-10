package etcd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func TestEtcdNativeMemberSessionClusterFourTargetsAndActualMetadataChanges(t *testing.T) {
	if os.Getenv("NYADB_ETCD_MEMBER_CLUSTER_IT") != "1" {
		t.Skip("requires owned four-member fixture")
	}
	s := memberSessionNative(t)
	l := reserveNativeKV(t, s, 61)
	var targets []struct{ Name, Endpoint string }
	if err := json.Unmarshal([]byte(os.Getenv("NYADB_ETCD_MEMBER_TARGETS")), &targets); err != nil || len(targets) != 4 {
		t.Fatal("missing explicit cluster targets", err)
	}
	f := l.(contract.KVMembers)
	o, err := f.ObserveMembers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	d := o.Snapshot()
	if len(d.Members) != 4 {
		t.Fatal("original complete four-member directory")
	}
	leases := map[string]contract.KVMemberLease{}
	for _, target := range targets {
		var member contract.KVMember
		for _, candidate := range d.Members {
			if candidate.Name == target.Name {
				member = candidate
			}
		}
		if member.ID == 0 || len(member.ClientURLs) != 1 || member.ClientURLs[0] == target.Endpoint {
			t.Fatal("advertised URL replaced by NAT candidate")
		}
		b, err := f.BindMember(context.Background(), o, contract.KVMemberTarget{MemberID: member.ID, Endpoint: target.Endpoint, Version: os.Getenv("NYADB_ETCD_VERSION")})
		if err != nil || b.Lease == nil || !sameMember(b.Scope.Member, member) {
			t.Fatal("original selected target bind", err)
		}
		leases[target.Name] = b.Lease
		defer b.Lease.Close()
		status, err := b.Lease.ObserveStatus(context.Background())
		if err != nil || !status.Result.Completed || status.Result.Header.MemberID != member.ID || status.Result.Reply.Status.IsLearner != member.IsLearner {
			t.Fatal("original member Status", err)
		}
		plan, err := b.Lease.PrepareDefragment()
		if member.IsLearner {
			if !errors.Is(err, contract.ErrUnsupported) {
				t.Fatal("learner maintenance published", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		r, err := b.Lease.ExecuteMember(context.Background(), plan)
		if err != nil || !r.Result.Completed || !r.Result.NativeCaptured || len(r.Result.Native) != 0 || r.Before.Header.MemberID != member.ID || r.Scope.Endpoint != target.Endpoint || !r.DirectoryCheck.Result.Completed {
			t.Fatal("original voter ACK/observations lost", err)
		}
		t.Log("original_member_ack", target.Name, member.ID, r.Scope.LeaseID, r.Sequence, r.Before.Reply.Status.Leader)
	}
	if len(leases) != 4 {
		t.Fatal("member transports merged")
	}
	if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
		p, secret := sessionProfile(t)
		p.Username, p.EtcdOptions.Auth, p.EtcdOptions.Prefix = "restricted", "basic", "/allowed/"
		raw, err := OpenSession(context.Background(), p, secret)
		if err != nil {
			t.Fatal(err)
		}
		limited := raw.(*Session)
		defer limited.Close()
		limitedLease := reserveNativeKV(t, limited, 62)
		m := nativeBindRootMember(t, limitedLease, targets[0].Endpoint)
		plan, _ := m.PrepareDefragment()
		r, err := m.ExecuteMember(context.Background(), plan)
		code := "PermissionDenied"
		if strings.HasPrefix(s.identity.Version, "3.5.") {
			code = "Unknown"
		}
		if err == nil || r.Result.Code != code || r.Result.WriteOutcomeUnknown || !r.Before.Completed || !r.DirectoryCheck.Result.Completed {
			t.Fatal("multi-member key grant authorized maintenance", err, r.Result.Code)
		}
	}
	if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
		p, _ := sessionProfile(t)
		p.Username, p.EtcdOptions.Auth = "", "certificate"
		raw, err := OpenSession(context.Background(), p, "")
		if err != nil {
			t.Fatal(err)
		}
		certificate := raw.(*Session)
		defer certificate.Close()
		certificateLease := reserveNativeKV(t, certificate, 63)
		m := nativeBindRootMember(t, certificateLease, targets[0].Endpoint)
		plan, _ := m.PrepareDefragment()
		r, err := m.ExecuteMember(context.Background(), plan)
		if err != nil || !r.Result.Completed || m.(*memberLease).client.principal != "root" {
			t.Fatal("original certificate member identity lost", err)
		}
	}
	// These are explicit controls of this disposable fixture, not advertised
	// member-management operations. Change the real raft directory's peer URL.
	admin := nativeClient(t)
	verifyNativeFollowerGate(t, leases)
	selected := leases["node1"]
	original := selected.Scope().Member
	if len(original.PeerURLs) != 1 {
		t.Fatal("missing original peer URL")
	}
	control := pb.NewClusterClient(admin.conn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	changed := []string{strings.TrimSuffix(original.PeerURLs[0], ":2380") + ":2381"}
	if _, err := control.MemberUpdate(admin.authContext(ctx), &pb.MemberUpdateRequest{ID: original.ID, PeerURLs: changed}); err != nil {
		t.Fatal("owned actual peer replacement", err)
	}
	restored := false
	defer func() {
		if !restored {
			cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			_, err := control.MemberUpdate(admin.authContext(cleanup), &pb.MemberUpdateRequest{ID: original.ID, PeerURLs: original.PeerURLs})
			if err != nil {
				t.Error("owned peer restoration", err)
			}
		}
	}()
	plan, _ := selected.PrepareDefragment()
	rejected, err := selected.ExecuteMember(context.Background(), plan)
	if !errors.Is(err, contract.ErrRevoked) || rejected.Result.Attempted || rejected.Result.WriteOutcomeUnknown || !rejected.DirectoryCheck.Result.Completed {
		t.Fatal("actual changed peer URL dispatched", err)
	}
	var observed contract.KVMember
	for _, member := range rejected.DirectoryCheck.Members {
		if member.ID == original.ID {
			observed = member
		}
	}
	if len(observed.PeerURLs) != 1 || observed.PeerURLs[0] != changed[0] {
		t.Fatal("actual changed directory evidence lost")
	}
	t.Log("actual_peer_replacement_rejected", original.ID, original.PeerURLs, observed.PeerURLs)
	if _, err := control.MemberUpdate(admin.authContext(ctx), &pb.MemberUpdateRequest{ID: original.ID, PeerURLs: original.PeerURLs}); err != nil {
		t.Fatal("owned peer restore", err)
	}
	restored = true
	// etcd's strict reconfiguration check requires sustained peer health after
	// restoring the original URL, beyond one successful directory observation.
	time.Sleep(6 * time.Second)
	// Make the removed voter a follower so its removal does not race a leader
	// election with the sole original document's fresh linearizable directory.
	leaderID := nativeDo(t, admin, &pb.StatusRequest{}).Native().(*pb.StatusResponse).Leader
	if leaderID != s.identity.MemberID {
		var leader *Client
		for _, lease := range leases {
			if lease.Scope().Member.ID == leaderID {
				leader = lease.(*memberLease).client
			}
		}
		if leader == nil {
			t.Fatal("missing captured leader fixture control")
		}
		if _, err := pb.NewMaintenanceClient(leader.conn).MoveLeader(leader.authContext(ctx), &pb.MoveLeaderRequest{TargetID: s.identity.MemberID}); err != nil {
			t.Fatal("owned leader stabilization", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for nativeDo(t, admin, &pb.StatusRequest{}).Native().(*pb.StatusResponse).Leader != s.identity.MemberID {
			if time.Now().After(deadline) {
				t.Fatal("original root did not become leader")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// Removing a different voter leaves the original root and another voter.
	// The original selected lease must preserve its identity and reject before
	// dispatch using a new authoritative directory receipt.
	removed := leases["node2"]
	removedScope := removed.Scope()
	if _, err := control.MemberRemove(admin.authContext(ctx), &pb.MemberRemoveRequest{ID: removedScope.Member.ID}); err != nil {
		t.Fatal("owned actual voter removal", err)
	}
	plan, _ = removed.PrepareDefragment()
	rejected, err = removed.ExecuteMember(context.Background(), plan)
	if !errors.Is(err, contract.ErrRevoked) || rejected.Result.Attempted || rejected.Result.WriteOutcomeUnknown || !rejected.DirectoryCheck.Result.Completed || len(rejected.DirectoryCheck.Members) != 3 || rejected.Scope.Member.ID != removedScope.Member.ID {
		t.Fatal("actual removed member dispatched or relabeled", err)
	}
	t.Log("actual_member_removal_rejected", removedScope.Member.ID, rejected.Scope.LeaseID, len(rejected.DirectoryCheck.Members))
}

func verifyNativeFollowerGate(t *testing.T, leases map[string]contract.KVMemberLease) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	selected := leases["node1"]
	other := leases["node0"]
	move := func(target uint64) {
		status, err := selected.ObserveStatus(ctx)
		if err != nil {
			t.Fatal("owned ordering observation", err)
		}
		leader := status.Result.Reply.Status.Leader
		if leader != target {
			var transport *Client
			for _, lease := range leases {
				if lease.Scope().Member.ID == leader {
					transport = lease.(*memberLease).client
				}
			}
			if transport == nil {
				t.Fatal("missing original leader control")
			}
			_, err := pb.NewMaintenanceClient(transport.conn).MoveLeader(transport.authContext(ctx), &pb.MoveLeaderRequest{TargetID: target})
			if err != nil {
				t.Fatal("owned leadership transfer", err)
			}
		}
		for {
			r, err := selected.ObserveStatus(ctx)
			if err != nil {
				t.Fatal("owned leadership stabilization", err)
			}
			if r.Result.Reply.Status.Leader == target {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("owned leadership did not stabilize")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	move(other.Scope().Member.ID)
	p, err := selected.(contract.KVMemberFollowerPreparation).PrepareFollowerDefragment()
	if err != nil {
		t.Fatal(err)
	}
	// Actual raft transfer occurs after the preparation/ordering observation.
	move(selected.Scope().Member.ID)
	r, err := selected.ExecuteMember(ctx, p)
	if !errors.Is(err, contract.ErrKVMemberBecameLeader) || r.Result.Attempted || r.Result.WriteOutcomeUnknown || !r.Before.Completed || r.Before.Reply.Status.Leader != selected.Scope().Member.ID || !r.DirectoryCheck.Result.Completed || selected.CheckMemberLease() != nil {
		t.Fatal("actual follower-to-leader change dispatched", err)
	}
	if _, err := selected.ExecuteMember(ctx, p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("ordering preparation replayed", err)
	}
	move(other.Scope().Member.ID)
	p, err = selected.(contract.KVMemberFollowerPreparation).PrepareFollowerDefragment()
	if err != nil {
		t.Fatal(err)
	}
	r, err = selected.ExecuteMember(ctx, p)
	if err != nil || !r.Result.Completed || !r.Result.NativeCaptured || len(r.Result.Native) != 0 || !r.Before.Completed || r.Before.Reply.Status.Leader != other.Scope().Member.ID {
		t.Fatal("actual guarded follower ACK", err)
	}
	t.Log("actual_follower_leader_gate", selected.Scope().Member.ID, other.Scope().Member.ID)
}
