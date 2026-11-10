package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func requireShellMembers(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PUBLIC_MEMBERS_IT") != "1" {
		t.Skip("requires owned public member workbench")
	}
}

func TestEtcdNativeShellPublicMembersCheckedOriginalBatchFilesAndCompleteArchives(t *testing.T) {
	requireShellMembers(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	s, tt, _, p, _ := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("public original KV entry missing")
	}
	d := s.currentTab().kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	s.kvWorkspace.ChooseMethod(d, kvworkspace.MembersMethod)
	etcdPublicClick(t, tt, "读取成员目录")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	directory, ok := d.MemberDirectoryReceipt()
	if !ok || len(directory.Members) != 4 {
		t.Fatal("public complete native directory missing", d.Error())
	}
	var targets []struct{ Name, Endpoint string }
	if json.Unmarshal([]byte(os.Getenv("NYADB_ETCD_MEMBER_TARGETS")), &targets) != nil {
		t.Fatal("missing explicit native aliases")
	}
	for _, target := range targets {
		for _, member := range directory.Members {
			if member.Name == target.Name && !member.IsLearner {
				s.kvWorkspace.SetMemberTarget(d, contract.KVMemberTarget{MemberID: member.ID, Endpoint: target.Endpoint, Version: os.Getenv("NYADB_ETCD_VERSION")}, true)
			}
		}
	}
	tt.Frame()
	if err := tt.Click(strconv.FormatUint(directory.Result.Header.MemberID, 10)); err != nil {
		t.Fatal("public member ID row selection missing", err)
	}
	tt.Frame()
	captureEtcdPublic(t, s, "members-public-selection")
	etcdPublicClick(t, tt, "检查所选成员")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	captureEtcdPublic(t, s, "members-public-bound")
	etcdPublicClick(t, tt, "预览 Defragment")
	if _, ok := d.Confirmation(); !ok {
		t.Fatal("public immutable member preview absent", d.Error())
	}
	captureEtcdPublic(t, s, "members-public-confirmation")
	etcdPublicClick(t, tt, "我确认执行此不可变原成员批次")
	etcdPublicClick(t, tt, "确认执行 Defragment 批次")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	batch, ok := d.MemberBatchReceipt()
	if !ok || len(batch.Targets) != 3 || batch.Error != "" || !batch.Ended {
		t.Fatal("public member batch did not complete", d.Error())
	}
	for _, target := range batch.Targets {
		if target.State != "succeeded" || target.Result == nil || !target.Result.Result.NativeCaptured || len(target.Result.Result.Native) != 0 || target.Result.Result.Header != (contract.KVHeader{}) {
			t.Fatal("public native zero ACK lost")
		}
	}
	captureEtcdPublic(t, s, "members-public-receipt")
	etcdPublicClick(t, tt, "复制完整成员记录")
	want, err := s.kvWorkspace.MembersRecord(d)
	if err != nil || string(want) != tt.Clipboard() {
		t.Fatal("public complete copy changed", err)
	}
	var archive []byte
	s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "member.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		archive = bytes.Clone(b.Bytes())
		return err
	}})
	etcdPublicClick(t, tt, "导出成员记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	if !bytes.Equal(want, archive) {
		t.Fatal("public archive truncated original member evidence")
	}
	etcdPublicClick(t, tt, "保存请求")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	file := bytes.Clone(archive)
	s.SetKVFiles(kvworkspace.Files{OpenPath: func() (string, error) { return "member.nyakv", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(file)), nil }})
	etcdPublicClick(t, tt, "打开请求文本")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	s.kvWorkspace.Run(d)
	if _, ok := d.Confirmation(); ok || d.Busy() {
		t.Fatal("public file supplied member authority or replayed")
	}
	captureEtcdPublic(t, s, "members-public-text-only-file")
}

type shellMemberFault struct {
	*etcdPublicFault
	pb.UnimplementedClusterServer
	memberCalls   atomic.Int32
	memberReceipt *pb.DefragmentResponse
}

func (f *shellMemberFault) MemberList(ctx context.Context, r *pb.MemberListRequest) (*pb.MemberListResponse, error) {
	return pb.NewClusterClient(f.upstream).MemberList(f.context(ctx), r)
}
func (f *shellMemberFault) Defragment(ctx context.Context, r *pb.DefragmentRequest) (*pb.DefragmentResponse, error) {
	if f.memberCalls.Add(1) != 1 {
		return nil, status.Error(codes.Internal, "original member replayed")
	}
	ack, err := pb.NewMaintenanceClient(f.upstream).Defragment(f.context(ctx), r)
	if err != nil {
		return ack, err
	}
	f.memberReceipt = proto.Clone(ack).(*pb.DefragmentResponse)
	return nil, status.Error(codes.Unavailable, "actual processed original member ACK withheld")
}
func shellMemberProxy(t *testing.T, p connections.Profile) (connections.Profile, *shellMemberFault) {
	t.Helper()
	_, base := etcdPublicProxy(t, p, nil, "unused")
	f := &shellMemberFault{etcdPublicFault: base}
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
	p.Host, p.Port, p.TLSMode = "127.0.0.1", uint16(listener.Addr().(*net.TCPAddr).Port), "disable"
	p.EtcdOptions.CAFile, p.EtcdOptions.ClientCertFile, p.EtcdOptions.ClientKeyFile = "", "", ""
	return p, f
}

func TestEtcdNativeShellPublicMembersUnknownParkingPublicationAndRecoveryRetainOriginals(t *testing.T) {
	requireShellMembers(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	proxy, f := shellMemberProxy(t, parsed.Profile)
	s, tt, _, p, grid := openEtcdPublicGrid(t, proxy, parsed.Password)
	defer s.CloseMySQLSessions()
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("public original request entry missing")
	}
	tab, d := s.currentTab(), s.currentTab().kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	s.kvWorkspace.ChooseMethod(d, kvworkspace.MembersMethod)
	s.kvWorkspace.ObserveMembers(d)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	s.kvWorkspace.SetMemberTarget(d, contract.KVMemberTarget{MemberID: d.Scope().Header.MemberID, Endpoint: "http://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Version: d.Scope().Version}, true)
	s.kvWorkspace.BindMembers(d)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	s.kvWorkspace.Run(d)
	if _, ok := d.Confirmation(); !ok {
		t.Fatal("public captured member lease missing", d.Error())
	}
	original := d.Scope()
	s.kvWorkspace.Confirm(d, true)
	for _, candidate := range s.tabs {
		if candidate.data == grid {
			s.selectTab(candidate.id)
		}
	}
	time.Sleep(80 * time.Millisecond)
	tt.Frame()
	if !d.Busy() {
		t.Fatal("parked unknown completion admitted in a different tab")
	}
	s.selectTab(tab.id)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	unknown, ok := d.MemberUnknownReceipt()
	if !ok || !unknown.Result.WriteOutcomeUnknown || !unknown.Before.Completed || !unknown.DirectoryCheck.Result.Completed || f.memberCalls.Load() != 1 || f.memberReceipt == nil {
		t.Fatal("actual unknown original member evidence lost", d.Error())
	}
	captureEtcdPublic(t, s, "members-public-unknown")
	if err := s.runtime.Connect(context.Background(), p, parsed.Password); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	captureEtcdPublic(t, s, "members-public-recovery-required")
	etcdPublicClick(t, tt, "建立新会话（不执行现有请求）")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	retained, ok := d.MemberUnknownReceipt()
	if !ok || d.NeedsRecovery() || d.Scope().Generation <= original.Generation || retained.Scope.Original.Generation != original.Generation || !bytes.Equal(retained.Before.Native, unknown.Before.Native) || f.memberCalls.Load() != 1 {
		t.Fatal("public pure recovery retagged/replayed or erased member evidence", d.Error())
	}
	s.kvWorkspace.ChooseMethod(d, "/etcdserverpb.Maintenance/Status")
	d.Editor.Buffer.Set("{}")
	s.kvWorkspace.Run(d)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	if _, ok := d.MemberUnknownReceipt(); !ok {
		t.Fatal("later Status cleared public member unknown")
	}
	captureEtcdPublic(t, s, "members-public-recovered-retained")
	s.kvWorkspace.GuardLeave(d, nil)
	captureEtcdPublic(t, s, "members-public-unknown-close")
}
