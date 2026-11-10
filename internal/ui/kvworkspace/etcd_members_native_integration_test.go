package kvworkspace

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func requirePublicMembers(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PUBLIC_MEMBERS_IT") != "1" {
		t.Skip("requires owned public member cluster")
	}
}
func nativeMemberTargets(t *testing.T) map[string]string {
	t.Helper()
	var targets []struct{ Name, Endpoint string }
	if json.Unmarshal([]byte(os.Getenv("NYADB_ETCD_MEMBER_TARGETS")), &targets) != nil || len(targets) != 4 {
		t.Fatal("missing explicit owned member targets")
	}
	out := map[string]string{}
	for _, target := range targets {
		out[target.Name] = target.Endpoint
	}
	return out
}
func selectNativeVoters(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.ChooseMethod(d, MembersMethod)
	s.ObserveMembers(d)
	consume(t, s, d)
	targets := nativeMemberTargets(t)
	if d.members == nil || d.members.directory == nil || len(d.members.directory.Members) != 4 {
		t.Fatal("complete native directory missing", d.Error())
	}
	for _, name := range []string{"node0", "node1", "node2"} {
		var member contract.KVMember
		for _, m := range d.members.directory.Members {
			if m.Name == name {
				member = m
				break
			}
		}
		if member.ID == 0 || member.IsLearner {
			t.Fatal("original voter missing", name)
		}
		s.SetMemberTarget(d, contract.KVMemberTarget{MemberID: member.ID, Endpoint: targets[name], Version: os.Getenv("NYADB_ETCD_VERSION")}, true)
	}
}

func TestEtcdNativeMembersWorkspaceOriginalSelectedVotersCheckedBatchAndCompleteFiles(t *testing.T) {
	requirePublicMembers(t)
	p, secret := workspaceProfile(t)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, _ := workspaceNativeState(t, p, secret)
			selectNativeVoters(t, s, d)
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			workspaceCapture(t, tt, language, size, "members-selection")
			workspaceClick(t, tt, "检查所选成员")
			consume(t, s, d)
			tt.Frame()
			if !memberBindingsReady(d, d.members.bindings) {
				t.Fatal("explicit native candidates did not bind", d.Error())
			}
			workspaceCapture(t, tt, language, size, "members-bound")
			workspaceClick(t, tt, "预览 Defragment")
			if d.confirmation == nil || d.Busy() {
				t.Fatal("pure checked preview dispatched")
			}
			workspaceCapture(t, tt, language, size, "members-confirmation")
			workspaceClick(t, tt, "我确认执行此不可变原成员批次")
			workspaceClick(t, tt, "确认执行 Defragment 批次")
			consume(t, s, d)
			tt.Frame()
			batch := d.members.batch
			if batch == nil || batch.Error != "" || !batch.Ended || len(batch.Targets) != 3 || len(batch.Rounds) != 3 {
				t.Fatal("native checked batch incomplete", d.Error())
			}
			for _, target := range batch.Targets {
				r := target.Result
				if target.State != "succeeded" || r == nil || !r.Result.NativeCaptured || len(r.Result.Native) != 0 || r.Result.Header != (contract.KVHeader{}) || !r.DirectoryCheck.Result.Completed || !r.Before.Completed || r.Scope.Original.Generation != batch.Scope.Generation {
					t.Fatal("native empty ACK/original complete observations changed")
				}
				var ack pb.DefragmentResponse
				if proto.Unmarshal(r.Result.Native, &ack) != nil || ack.Header != nil {
					t.Fatal("empty native ACK replaced by borrowed header")
				}
			}
			for _, round := range batch.Rounds {
				if len(round.Remaining) > 1 {
					for _, r := range round.Statuses {
						if r.Scope.Member.ID == round.Chosen && r.Result.Reply.Status.Leader == round.Chosen {
							t.Fatal("selected leader executed before followers")
						}
					}
				}
			}
			workspaceCapture(t, tt, language, size, "members-receipt")
			workspaceClick(t, tt, "复制完整成员记录")
			want, err := s.MembersRecord(d)
			if err != nil || tt.Clipboard() != string(want) {
				t.Fatal("native member copy incomplete", err)
			}
			var archive []byte
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "member.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
				var b bytes.Buffer
				err := write(&b)
				archive = bytes.Clone(b.Bytes())
				return err
			}})
			workspaceClick(t, tt, "导出成员记录")
			consume(t, s, d)
			if !bytes.Equal(want, archive) {
				t.Fatal("complete native member export changed")
			}
			workspaceClick(t, tt, "保存请求")
			consume(t, s, d)
			file := bytes.Clone(archive)
			decoded, err := DecodeRequest(file)
			if err != nil || decoded != d.Request() {
				t.Fatal("member selection text file changed", err)
			}
			s.SetFiles(Files{OpenPath: func() (string, error) { return "member.nyakv", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(file)), nil }})
			workspaceClick(t, tt, "打开请求文本")
			consume(t, s, d)
			s.Run(d)
			if d.confirmation != nil || d.Busy() || d.members.observation != nil || d.members.bindings != nil || d.members.batch.Error != "" {
				t.Fatal("request file restored authority or replayed batch")
			}
			workspaceCapture(t, tt, language, size, "members-text-only-file")
		}
	}
}

type memberWorkspaceFault struct {
	*workspaceFault
	pb.UnimplementedClusterServer
	memberMode                               string
	memberCalls                              atomic.Int32
	memberEntered, memberDone, memberRelease chan struct{}
	memberReceipt                            *pb.DefragmentResponse
}

func (f *memberWorkspaceFault) MemberList(ctx context.Context, r *pb.MemberListRequest) (*pb.MemberListResponse, error) {
	return pb.NewClusterClient(f.upstream).MemberList(f.context(ctx), r)
}
func (f *memberWorkspaceFault) Defragment(ctx context.Context, r *pb.DefragmentRequest) (*pb.DefragmentResponse, error) {
	if f.memberCalls.Add(1) != 1 {
		return nil, status.Error(codes.Internal, "original member replayed")
	}
	ack, err := pb.NewMaintenanceClient(f.upstream).Defragment(f.context(ctx), r)
	if err != nil {
		return ack, err
	}
	f.memberReceipt = proto.Clone(ack).(*pb.DefragmentResponse)
	close(f.memberEntered)
	defer close(f.memberDone)
	switch f.memberMode {
	case "lost":
		return nil, status.Error(codes.Unavailable, "actual processed member ACK withheld")
	case "cancel", "close", "replace", "disconnect":
		<-ctx.Done()
		return nil, ctx.Err()
	case "park":
		select {
		case <-f.memberRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case "foreign":
		ack.Header = &pb.ResponseHeader{ClusterId: 99, MemberId: 99, Revision: 1, RaftTerm: 1}
	}
	return ack, nil
}
func memberWorkspaceProxy(t *testing.T, p connections.Profile, mode string, tlsServer bool) (string, connections.Profile, *memberWorkspaceFault) {
	t.Helper()
	original := p
	_, base := workspaceProxy(t, p, nil, "unused")
	f := &memberWorkspaceFault{workspaceFault: base, memberMode: mode, memberEntered: make(chan struct{}), memberDone: make(chan struct{}), memberRelease: make(chan struct{})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var options []grpc.ServerOption
	scheme := "http"
	if tlsServer && original.TLSMode != "disable" {
		dir := filepath.Dir(os.Getenv("NYADB_ETCD_CA"))
		pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"))
		if err != nil {
			t.Fatal(err)
		}
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
		if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
			pem, err := os.ReadFile(os.Getenv("NYADB_ETCD_CA"))
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(pem) {
				t.Fatal("owned CA invalid")
			}
			cfg.ClientCAs, cfg.ClientAuth = roots, tls.RequireAndVerifyClientCert
		}
		options = append(options, grpc.Creds(credentials.NewTLS(cfg)))
		scheme = "https"
	}
	server := grpc.NewServer(options...)
	pb.RegisterKVServer(server, f)
	pb.RegisterAuthServer(server, f)
	pb.RegisterMaintenanceServer(server, f)
	pb.RegisterClusterServer(server, f)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	p.Host, p.Port = "localhost", uint16(listener.Addr().(*net.TCPAddr).Port)
	if scheme == "http" {
		p.TLSMode = "disable"
		p.EtcdOptions.CAFile, p.EtcdOptions.ClientCertFile, p.EtcdOptions.ClientKeyFile = "", "", ""
	}
	return scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), p, f
}

func TestEtcdNativeMembersWorkspaceActualProcessedFaultsParkingAndOriginalRecovery(t *testing.T) {
	requirePublicMembers(t)
	p, secret := workspaceProfile(t)
	for _, mode := range []string{"lost", "cancel", "close", "replace", "disconnect", "foreign", "park"} {
		t.Run(mode, func(t *testing.T) {
			endpoint, proxy, f := memberWorkspaceProxy(t, p, mode, false)
			s, d, m := workspaceNativeState(t, proxy, secret)
			s.ChooseMethod(d, MembersMethod)
			s.ObserveMembers(d)
			consume(t, s, d)
			s.SetMemberTarget(d, contract.KVMemberTarget{MemberID: d.scope.Header.MemberID, Endpoint: endpoint, Version: d.scope.Version}, true)
			s.BindMembers(d)
			consume(t, s, d)
			s.Run(d)
			if d.confirmation == nil {
				t.Fatal("native fault target did not bind", d.Error())
			}
			original := d.scope.Clone()
			s.Confirm(d, true)
			await(t, f.memberEntered)
			active := true
			s.deps.Active = func(*Document) bool { return active }
			switch mode {
			case "cancel":
				s.RequestCancel(d)
			case "close":
				d.members.bindings.entries[0].lease.Close()
			case "replace":
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal(err)
				}
			case "disconnect":
				m.Disconnect(p.ID)
			case "park":
				active = false
				close(f.memberRelease)
				await(t, d.job.finished)
				s.Consume()
				if !d.Busy() || d.members.batch != nil {
					t.Fatal("parked original completion entered another active document")
				}
				active = true
			}
			consume(t, s, d)
			await(t, f.memberDone)
			if f.memberCalls.Load() != 1 || f.memberReceipt == nil || d.members.batch == nil || d.members.batch.Scope.Generation != original.Generation {
				t.Fatal("actual original ACK/original batch lost")
			}
			if mode == "park" {
				if d.members.batch.Targets[0].State != "succeeded" || d.hasUnknownWrites() {
					t.Fatal("parked complete ACK changed")
				}
			} else {
				if d.members.batch.Targets[0].State != "unknown" || d.members.unknown == nil {
					t.Fatal("processed ACK loss became definite", d.Error())
				}
			}
			if mode == "lost" {
				captureMembersFaultImages(t, s, d, "members-unknown", false)
			}
			if err := m.Connect(context.Background(), proxy, secret); err != nil {
				t.Fatal(err)
			}
			d.needsRecovery = true
			s.Recover(d)
			consume(t, s, d)
			if d.needsRecovery || d.members.bindings != nil || d.members.observation != nil || len(d.members.recovered) != 1 || d.members.recovered[0].Scope.Original.Generation != original.Generation || f.memberCalls.Load() != 1 {
				t.Fatal("pure member recovery rewrote/replayed original evidence", d.Error())
			}
			workspaceInput(s, d, workspaceRequest(t, "Maintenance/Status", &pb.StatusRequest{}))
			s.Run(d)
			consume(t, s, d)
			if mode != "park" && (d.members.unknown == nil || !d.hasUnknownWrites()) {
				t.Fatal("later ordinary read settled original unknown")
			}
			if mode == "lost" {
				captureMembersFaultImages(t, s, d, "members-recovered", false)
				captureMembersFaultImages(t, s, d, "members-unknown-close", true)
			}
		})
	}
}

func TestEtcdNativeMembersWorkspacePartialBatchRealAckLossAndRestrictedLearner(t *testing.T) {
	requirePublicMembers(t)
	p, secret := workspaceProfile(t)
	s, d, _ := workspaceNativeState(t, p, secret)
	selectNativeVoters(t, s, d)
	s.BindMembers(d)
	consume(t, s, d)
	if !memberBindingsReady(d, d.members.bindings) {
		t.Fatal("original cluster bindings", d.Error())
	}
	// Order from a fresh original observation, then fault the second follower.
	var followers []int
	for i, entry := range d.members.bindings.entries {
		r, err := entry.lease.ObserveStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if r.Result.Reply.Status.Leader != entry.receipt.Target.MemberID {
			followers = append(followers, i)
		}
	}
	if len(followers) != 2 {
		t.Fatal("missing original two followers")
	}
	i := followers[1]
	target := d.members.bindings.entries[i].receipt.Target
	u, err := url.Parse(target.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	memberProfile := p
	memberProfile.Host = u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	memberProfile.Port = uint16(port)
	endpoint, _, fault := memberWorkspaceProxy(t, memberProfile, "lost", true)
	s.SetMemberTarget(d, contract.KVMemberTarget{MemberID: target.MemberID, Endpoint: endpoint, Version: target.Version}, true)
	s.BindMembers(d)
	consume(t, s, d)
	s.Run(d)
	if d.confirmation == nil {
		t.Fatal("immutable original partial batch absent", d.Error())
	}
	s.Confirm(d, true)
	consume(t, s, d)
	await(t, fault.memberDone)
	counts := map[string]int{}
	for _, target := range d.members.batch.Targets {
		counts[target.State]++
	}
	if counts["succeeded"] != 1 || counts["unknown"] != 1 || counts["not-executed"] != 1 || fault.memberCalls.Load() != 1 || !d.hasUnknownWrites() {
		t.Fatal("actual partial batch states lost", counts, d.Error())
	}
	if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
		limited := p
		limited.Username, limited.EtcdOptions.Auth, limited.EtcdOptions.Prefix = "restricted", "basic", "/allowed/"
		ls, ld, _ := workspaceNativeState(t, limited, secret)
		selectNativeVoters(t, ls, ld)
		ls.BindMembers(ld)
		consume(t, ls, ld)
		ls.Run(ld)
		ls.Confirm(ld, true)
		consume(t, ls, ld)
		if ld.members.batch == nil || ld.hasUnknownWrites() {
			t.Fatal("restricted maintenance denial became unknown")
		}
		failed, notExecuted := 0, 0
		for _, target := range ld.members.batch.Targets {
			if target.State == "failed" {
				failed++
			}
			if target.State == "not-executed" {
				notExecuted++
			}
		}
		if failed != 1 || notExecuted != 2 {
			t.Fatal("restricted batch continued after definite native denial")
		}
	}
	learnerFound := false
	for _, member := range d.members.directory.Members {
		if member.IsLearner {
			learnerFound = true
			if len(member.PeerURLs) != 1 || len(member.ClientURLs) != 1 {
				t.Fatal("complete learner metadata missing")
			}
			for _, target := range d.members.bindings.entries {
				s.SetMemberTarget(d, target.receipt.Target, false)
			}
			s.SetMemberTarget(d, contract.KVMemberTarget{MemberID: member.ID, Endpoint: nativeMemberTargets(t)[member.Name], Version: os.Getenv("NYADB_ETCD_VERSION")}, true)
			s.BindMembers(d)
			consume(t, s, d)
			if d.members.bindings == nil || d.members.bindings.entries[0].lease == nil {
				t.Fatal("original authenticated learner Status binding unavailable", d.Error())
			}
			s.ObserveMemberStatus(d, 0)
			consume(t, s, d)
			if len(d.members.statuses) == 0 || !d.members.statuses[len(d.members.statuses)-1].Result.Reply.Status.IsLearner || !d.hasUnknownWrites() {
				t.Fatal("learner Status changed original unknown barrier")
			}
			captureMembersFaultImages(t, s, d, "members-learner-status", false)
		}
	}
	if !learnerFound {
		t.Fatal("original learner absent")
	}
}

func captureMembersFaultImages(t *testing.T, s *State, d *Document, stage string, leave bool) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	d.selectedMembers = true
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			if leave {
				s.GuardLeave(d, nil)
			}
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			workspaceCapture(t, tt, language, size, stage)
			if leave {
				workspaceClick(t, tt, "取消")
				if s.leave != nil || d.members.unknown == nil {
					t.Fatal("cancel leave erased original member uncertainty")
				}
			}
		}
	}
}
