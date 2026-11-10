package dbruntime_test

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func membersRuntimeProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_MEMBER_SESSION_IT") != "1" {
		t.Skip("requires owned member runtime fixture")
	}
	return etcdRuntimeProfile(t)
}
func membersRuntimeBind(t *testing.T, l contract.KVRequestLease, endpoint string) contract.KVMemberLease {
	t.Helper()
	f := l.(contract.KVMembers)
	o, err := f.ObserveMembers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	b, err := f.BindMember(context.Background(), o, contract.KVMemberTarget{MemberID: l.Scope().Header.MemberID, Endpoint: endpoint, Version: l.Scope().Version})
	if err != nil || b.Lease == nil {
		t.Fatal("native member runtime bind", err)
	}
	t.Cleanup(func() { b.Lease.Close() })
	return b.Lease
}

func TestEtcdNativeMemberRuntimeOriginalPublicationAndEvidenceRecovery(t *testing.T) {
	p, secret := membersRuntimeProfile(t)
	m := etcdRuntimeManager(t)
	for i := 0; i < 2; i++ {
		if err := m.Connect(context.Background(), p, secret); err != nil {
			t.Fatal(err)
		}
	}
	l := etcdRuntimeReserve(t, m, 51)
	member := membersRuntimeBind(t, l, os.Getenv("NYADB_ETCD_ENDPOINT"))
	plan, _ := member.PrepareDefragment()
	r, err := member.ExecuteMember(context.Background(), plan)
	if err != nil || !r.Result.Completed || !r.Result.NativeCaptured || len(r.Result.Native) != 0 || r.Scope.Original.Generation != l.Scope().Generation || r.Scope.Original.Generation == 1 || r.Scope.Directory.Scope.Generation != l.Scope().Generation || r.DirectoryCheck.Scope.Generation != l.Scope().Generation {
		t.Fatal("native original publication/evidence", err)
	}
	before := l.Scope().Generation
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	provider, _ := m.KVRequests(p.ID)
	recovery, err := provider.RecoverKV(l)
	if err != nil || recovery.Lease == nil || len(recovery.MemberResults) != 1 || recovery.Lease.Scope().Generation <= before || recovery.MemberResults[0].Scope.Original.Generation != before || recovery.MemberResults[0].Scope.Directory.Scope.Generation != before || recovery.MemberResults[0].DirectoryCheck.Scope.Generation != before {
		t.Fatal("recovery retagged original member receipt", err)
	}
	defer recovery.Lease.Close()
	if _, err := recovery.Lease.(contract.KVMembers).BindMember(context.Background(), &fakeMemberObservation{member.Scope().Directory}, contract.KVMemberTarget{}); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("inspection DTO became new authority", err)
	}
}

type membersRuntimeFault struct {
	*etcdRuntimeFault
	pb.UnimplementedClusterServer
	mode          string
	memberCalls   atomic.Int32
	entered, done chan struct{}
	receipt       *pb.DefragmentResponse
}

func (f *membersRuntimeFault) MemberList(ctx context.Context, r *pb.MemberListRequest) (*pb.MemberListResponse, error) {
	return pb.NewClusterClient(f.upstream).MemberList(f.context(ctx), r)
}
func (f *membersRuntimeFault) Defragment(ctx context.Context, r *pb.DefragmentRequest) (*pb.DefragmentResponse, error) {
	if f.memberCalls.Add(1) != 1 {
		return nil, status.Error(codes.Internal, "member request replayed")
	}
	ack, err := pb.NewMaintenanceClient(f.upstream).Defragment(f.context(ctx), r)
	if err != nil {
		return ack, err
	}
	f.receipt = proto.Clone(ack).(*pb.DefragmentResponse)
	close(f.entered)
	defer close(f.done)
	switch f.mode {
	case "lost":
		return nil, status.Error(codes.Unavailable, "actual member ACK withheld")
	case "cancel", "close", "replace", "disconnect":
		<-ctx.Done()
		return nil, ctx.Err()
	case "foreign":
		ack.Header = &pb.ResponseHeader{ClusterId: 99, MemberId: 99, Revision: 1, RaftTerm: 1}
	}
	return ack, nil
}

func membersRuntimeProxy(t *testing.T, p connections.Profile, mode string) (connections.Profile, *membersRuntimeFault) {
	t.Helper()
	f := &membersRuntimeFault{etcdRuntimeFault: &etcdRuntimeFault{upstream: etcdRuntimeTransport(t, p)}, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterKVServer(server, f)
	pb.RegisterAuthServer(server, f)
	pb.RegisterMaintenanceServer(server, f)
	pb.RegisterClusterServer(server, f)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	p.Host = "127.0.0.1"
	p.Port = uint16(listener.Addr().(*net.TCPAddr).Port)
	p.TLSMode = "disable"
	p.EtcdOptions.CAFile, p.EtcdOptions.ServerName, p.EtcdOptions.ClientCertFile, p.EtcdOptions.ClientKeyFile = "", "", "", ""
	return p, f
}

func TestEtcdNativeMemberRuntimeActualProcessedPublicationFaults(t *testing.T) {
	p, secret := membersRuntimeProfile(t)
	for _, mode := range []string{"lost", "cancel", "close", "replace", "disconnect", "foreign", "parked-complete"} {
		t.Run(mode, func(t *testing.T) {
			proxy, f := membersRuntimeProxy(t, p, mode)
			m := etcdRuntimeManager(t)
			for i := 0; i < 2; i++ {
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal(err)
				}
			}
			l := etcdRuntimeReserve(t, m, 53)
			member := membersRuntimeBind(t, l, "http://"+net.JoinHostPort(proxy.Host, strconv.Itoa(int(proxy.Port))))
			original := member.Scope()
			plan, _ := member.PrepareDefragment()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				r   contract.KVMemberResult
				err error
			}
			out := make(chan completion, 1)
			go func() { r, err := member.ExecuteMember(ctx, plan); out <- completion{r, err} }()
			compactionRuntimeWait(t, f.entered)
			switch mode {
			case "cancel":
				cancel()
			case "close":
				member.Close()
			case "replace":
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal(err)
				}
			case "disconnect":
				m.Disconnect(p.ID)
			case "parked-complete":
				other := p
				other.ID++
				if err := m.Connect(context.Background(), other, secret); err != nil {
					t.Fatal(err)
				}
			}
			got := <-out
			compactionRuntimeWait(t, f.done)
			if f.memberCalls.Load() != 1 || f.receipt == nil || !got.r.Before.Completed || !got.r.DirectoryCheck.Result.Completed || got.r.Scope.Original.Generation != original.Original.Generation || got.r.DirectoryCheck.Scope.Generation != original.Original.Generation {
				t.Fatal("original processed evidence changed", got.err)
			}
			if mode == "parked-complete" {
				if got.err != nil || !got.r.Result.Completed || got.r.Result.WriteOutcomeUnknown {
					t.Fatal("parked original completion rejected", got.err)
				}
			} else if got.err == nil || !got.r.Result.WriteOutcomeUnknown || got.r.Result.Completed {
				t.Fatal("actual unknown write acknowledged", got.err)
			}
			if mode == "foreign" && len(got.r.Result.Native) == 0 {
				t.Fatal("foreign native receipt lost")
			}
			if err := m.Connect(context.Background(), p, secret); err != nil {
				t.Fatal(err)
			}
			provider, _ := m.KVRequests(p.ID)
			recovered, err := provider.RecoverKV(l)
			if err != nil || recovered.Lease == nil || len(recovered.MemberResults) != 1 || recovered.MemberResults[0].Scope.Original.Generation != original.Original.Generation || f.memberCalls.Load() != 1 {
				t.Fatal("original evidence recovery replayed/retagged", err, len(recovered.MemberResults))
			}
			defer recovered.Lease.Close()
		})
	}
}
