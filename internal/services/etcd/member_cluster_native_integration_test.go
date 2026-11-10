package etcd

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestEtcdNativeMemberClusterDirectoryExplicitAliasesLearnerAndFreshLeaderObservation(t *testing.T) {
	if os.Getenv("NYADB_ETCD_MEMBER_CLUSTER_IT") != "1" {
		t.Skip("requires owned three-voter/one-learner fixture")
	}
	root := nativeClient(t)
	var targets []struct{ Name, Endpoint string }
	if err := json.Unmarshal([]byte(os.Getenv("NYADB_ETCD_MEMBER_TARGETS")), &targets); err != nil || len(targets) != 4 {
		t.Fatal("incomplete owned member targets", err)
	}
	list := nativeDo(t, root, &pb.MemberListRequest{Linearizable: true})
	directory := list.Native().(*pb.MemberListResponse)
	if len(directory.Members) != 4 || list.Header.Revision != 0 {
		t.Fatal("incomplete real four-member directory", directory)
	}
	logReceipt := func(label string, r Response) {
		b, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(r.Native())
		if err != nil {
			t.Fatal(err)
		}
		t.Log(label, string(b))
	}
	logReceipt("original_directory", list)
	cfg, secret := nativeConfig(t)
	clients := make(map[uint64]*Client)
	var leader, follower *Client
	roles := map[string]bool{}
	for _, target := range targets {
		var member *pb.Member
		for _, v := range directory.Members {
			if v.Name == target.Name {
				member = v
			}
		}
		if member == nil || member.ID == 0 || len(member.ClientURLs) != 1 || member.ClientURLs[0] == target.Endpoint {
			t.Fatal("fixture did not preserve distinct original advertised and explicit NAT alias", target.Name)
		}
		originalURL := member.ClientURLs[0]
		expected := root.Identity()
		expected.MemberID = member.ID
		candidate := cfg
		candidate.Endpoint = target.Endpoint
		c, err := OpenMember(context.Background(), candidate, secret, expected)
		if err != nil {
			t.Fatal("explicit observed member alias/learner transport rejected", target.Name, err)
		}
		t.Cleanup(func() { _ = c.Close() })
		clients[member.ID] = c
		plan, err := c.Prepare(&pb.DefragmentRequest{})
		if err != nil {
			t.Fatal(err)
		}
		r, err := c.Execute(context.Background(), plan)
		before, ok := r.Before()
		if !ok || !before.Completed || before.Header.MemberID != member.ID || before.Header.ClusterID != list.Header.ClusterID || before.Native().(*pb.StatusResponse).IsLearner != member.IsLearner || r.Header != (Header{}) || member.ClientURLs[0] != originalURL {
			t.Fatal("member/role/empty ACK or original URLs changed", target.Name)
		}
		logReceipt(target.Name+"_before", before)
		if member.IsLearner {
			expectedCode := codes.FailedPrecondition
			if strings.HasPrefix(root.Identity().Version, "3.5.") {
				expectedCode = codes.Unavailable
			}
			if status.Code(err) != expectedCode || r.Code != expectedCode || !r.Attempted || r.Completed || r.WriteOutcomeUnknown || r.Native() != nil || !c.Alive() {
				t.Fatal("learner's definite native rejection was concealed", err)
			}
			t.Log(target.Name+"_definite_native_rejection", r.Code.String(), err)
		} else {
			if err != nil || !r.Attempted || !r.Completed || r.WriteOutcomeUnknown || r.Native() == nil || r.Native().(*pb.DefragmentResponse).Header != nil {
				t.Fatal("voter's empty native ACK changed", err)
			}
			logReceipt(target.Name+"_ack", r)
		}
		s := before.Native().(*pb.StatusResponse)
		role := "follower"
		if member.IsLearner {
			role = "learner"
		} else if s.Leader == member.ID {
			role = "leader"
			leader = c
		} else {
			follower = c
		}
		roles[role] = true
		wrong := expected
		wrong.MemberID ^= 1
		if c, err := OpenMember(context.Background(), candidate, secret, wrong); err == nil {
			c.Close()
			t.Fatal("explicit alias silently accepted another member")
		}
		if !member.IsLearner {
			nativeDo(t, c, &pb.MemberListRequest{})
		} else {
			nativeDo(t, c, &pb.StatusRequest{})
		}
	}
	if len(roles) != 3 || leader == nil || follower == nil {
		t.Fatal("real leader/follower/learner roles not exercised", roles)
	}
	// This is fixture control, not an advertised native product operation.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pb.NewMaintenanceClient(leader.conn).MoveLeader(leader.authContext(ctx), &pb.MoveLeaderRequest{TargetID: follower.Identity().MemberID}); err != nil {
		t.Fatal("owned leader transfer failed", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := nativeDo(t, leader, &pb.StatusRequest{})
		if r.Native().(*pb.StatusResponse).Leader == follower.Identity().MemberID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new original leader was not observed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	r := nativeDo(t, leader, &pb.DefragmentRequest{})
	before, ok := r.Before()
	if !ok || before.Native().(*pb.StatusResponse).Leader != follower.Identity().MemberID || before.Header.MemberID != leader.Identity().MemberID {
		t.Fatal("Defragment reused stale leadership/another member")
	}
	logReceipt("changed_leader_before", before)
	logReceipt("changed_leader_ack", r)
	if len(clients) != 4 {
		t.Fatal("original member transports were merged")
	}
}
