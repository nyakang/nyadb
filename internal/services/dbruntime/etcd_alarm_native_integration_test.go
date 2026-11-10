package dbruntime_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func alarmRuntimeProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_ALARM_IT") != "1" {
		t.Skip("requires owned original Alarm runtime fixture")
	}
	return etcdRuntimeProfile(t)
}
func alarmRuntimeControl(t *testing.T, p connections.Profile, secret string) func(uint64, bool) {
	t.Helper()
	conn := etcdRuntimeTransport(t, p)
	ctx := context.Background()
	if p.EtcdOptions.Auth == "basic" {
		r, err := pb.NewAuthClient(conn).Authenticate(ctx, &pb.AuthenticateRequest{Name: p.Username, Password: secret})
		if err != nil {
			t.Fatal("owned Alarm setup authentication failed")
		}
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("token", r.Token))
	}
	return func(id uint64, active bool) {
		t.Helper()
		action := pb.AlarmRequest_DEACTIVATE
		if active {
			action = pb.AlarmRequest_ACTIVATE
		}
		r, err := pb.NewMaintenanceClient(conn).Alarm(ctx, &pb.AlarmRequest{Action: action, MemberID: id, Alarm: pb.AlarmType_NOSPACE})
		if err != nil || r == nil || r.Header == nil {
			t.Fatal("owned runtime Alarm setup failed", err)
		}
	}
}
func TestEtcdNativeAlarmRuntimeOriginalInventoryPublicationAndPureRecovery(t *testing.T) {
	p, secret := alarmRuntimeProfile(t)
	p.EtcdOptions.Prefix = "/unrelated-alarm-prefix/"
	m := etcdRuntimeManager(t)
	publication, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	lease := etcdRuntimeReserve(t, m, 404)
	original := lease.Scope()
	control := alarmRuntimeControl(t, p, secret)
	control(original.Header.MemberID, true)
	defer control(original.Header.MemberID, false)
	inventory, ok := lease.(contract.KVMaintenance)
	if !ok || !slices.Contains(inventory.MaintenanceMethods(), "/etcdserverpb.Maintenance/Alarm") {
		t.Fatal("Alarm inventory missing")
	}
	for _, ok := range []bool{implementsAuth(lease), implementsMembers(lease), implementsBrowser(lease)} {
		if !ok {
			t.Fatal("native maintenance binding lost another original facet")
		}
	}
	methods := inventory.MaintenanceMethods()
	methods[0] = "modified"
	if inventory.MaintenanceMethods()[0] == "modified" {
		t.Fatal("runtime exposed mutable inventory")
	}
	input := etcdRuntimeRequest(t, "Maintenance/Alarm", &pb.AlarmRequest{Action: pb.AlarmRequest_DEACTIVATE, MemberID: original.Header.MemberID, Alarm: pb.AlarmType_NOSPACE})
	plan, err := lease.Prepare(input)
	if err != nil || !plan.Writes() {
		t.Fatal(err)
	}
	receipt, err := lease.Execute(context.Background(), plan)
	if err != nil || !receipt.Completed || !receipt.NativeCaptured || receipt.WriteOutcomeUnknown || receipt.Reply == nil || len(receipt.Reply.Alarms) != 1 || receipt.Reply.Alarms[0].MemberID != original.Header.MemberID || lease.Scope().Generation != publication.Generation() {
		t.Fatal("original runtime Alarm receipt lost", err)
	}
	replacement, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := provider.RecoverKV(lease)
	if err != nil || recovery.Lease == nil || recovery.Lease.Scope().Generation != replacement.Generation() || replacement.Generation() <= original.Generation {
		t.Fatal("Alarm recovery followed old publication", err)
	}
	defer recovery.Lease.Close()
	if _, err := recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("old Alarm preparation crossed recovery")
	}
	if _, err := lease.Prepare(input); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old Alarm scope remained authorized")
	}
	read := etcdRuntimeDo(t, recovery.Lease, "Maintenance/Alarm", &pb.AlarmRequest{})
	if len(read.Reply.Alarms) != 0 || len(receipt.Reply.Alarms) != 1 || lease.Scope().Generation != original.Generation || receipt.Request != plan.Snapshot() {
		t.Fatal("recovery/read altered original ACK or generation")
	}
}
func implementsAuth(l contract.KVRequestLease) bool {
	_, ok := l.(contract.KVAuthManagement)
	return ok
}
func implementsMembers(l contract.KVRequestLease) bool { _, ok := l.(contract.KVMembers); return ok }
func implementsBrowser(l contract.KVRequestLease) bool {
	_, ok := l.(contract.KVLeaseBrowsing)
	return ok
}

type alarmRuntimeFault struct {
	*etcdRuntimeFault
	reads                  bool
	mode                   string
	calls                  atomic.Int32
	entered, done, release chan struct{}
	request                *pb.AlarmRequest
	receipt                *pb.AlarmResponse
}

func (f *alarmRuntimeFault) Alarm(ctx context.Context, r *pb.AlarmRequest) (*pb.AlarmResponse, error) {
	reply, err := pb.NewMaintenanceClient(f.upstream).Alarm(f.context(ctx), r)
	if err != nil {
		return reply, err
	}
	if (r.Action == pb.AlarmRequest_GET) != f.reads {
		return reply, nil
	}
	if f.calls.Add(1) != 1 {
		return nil, status.Error(codes.Internal, "original Alarm replayed")
	}
	f.request = proto.Clone(r).(*pb.AlarmRequest)
	f.receipt = proto.Clone(reply).(*pb.AlarmResponse)
	close(f.entered)
	defer close(f.done)
	switch f.mode {
	case "lost":
		return nil, status.Error(codes.Unavailable, "actual original Alarm ACK withheld")
	case "cancel", "close", "replace", "disconnect":
		<-ctx.Done()
		return nil, ctx.Err()
	case "foreign":
		reply.Header.MemberId++
	case "parked-complete":
		<-f.release
	}
	return reply, nil
}
func alarmRuntimeProxy(t *testing.T, p connections.Profile, mode string, reads bool) (connections.Profile, *alarmRuntimeFault) {
	t.Helper()
	f := &alarmRuntimeFault{etcdRuntimeFault: &etcdRuntimeFault{upstream: etcdRuntimeTransport(t, p)}, mode: mode, reads: reads, entered: make(chan struct{}), done: make(chan struct{}), release: make(chan struct{})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var options []grpc.ServerOption
	if p.TLSMode == "verify-full" {
		folder := strings.TrimSuffix(p.EtcdOptions.CAFile, "ca.crt")
		pair, err := tls.LoadX509KeyPair(folder+"server.crt", folder+"server.key")
		if err != nil {
			t.Fatal(err)
		}
		tc := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
		if p.EtcdOptions.ClientCertFile != "" {
			raw, err := os.ReadFile(p.EtcdOptions.CAFile)
			if err != nil {
				t.Fatal(err)
			}
			tc.ClientCAs = x509.NewCertPool()
			if !tc.ClientCAs.AppendCertsFromPEM(raw) {
				t.Fatal("invalid owned CA")
			}
			tc.ClientAuth = tls.RequireAndVerifyClientCert
		}
		options = append(options, grpc.Creds(credentials.NewTLS(tc)))
		p.Host = "localhost"
	} else {
		p.Host = "127.0.0.1"
	}
	p.Port = uint16(listener.Addr().(*net.TCPAddr).Port)
	server := grpc.NewServer(options...)
	pb.RegisterKVServer(server, f)
	pb.RegisterAuthServer(server, f)
	pb.RegisterMaintenanceServer(server, f)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	return p, f
}
func TestEtcdNativeAlarmRuntimeActualProcessedFaultsOriginalPublicationAndRecoveryNeverReplay(t *testing.T) {
	p, secret := alarmRuntimeProfile(t)
	control := alarmRuntimeControl(t, p, secret)
	for _, reads := range []bool{false, true} {
		for _, mode := range []string{"lost", "cancel", "close", "replace", "disconnect", "foreign", "parked-complete"} {
			t.Run(strconv.FormatBool(reads)+"/"+mode, func(t *testing.T) {
				proxy, f := alarmRuntimeProxy(t, p, mode, reads)
				m := etcdRuntimeManager(t)
				publication, err := m.ConnectSession(context.Background(), proxy, secret)
				if err != nil {
					t.Fatal(err)
				}
				lease := etcdRuntimeReserve(t, m, 505)
				original := lease.Scope()
				control(original.Header.MemberID, true)
				defer control(original.Header.MemberID, false)
				request := &pb.AlarmRequest{Action: pb.AlarmRequest_DEACTIVATE, MemberID: original.Header.MemberID, Alarm: pb.AlarmType_NOSPACE}
				if reads {
					request = &pb.AlarmRequest{}
				}
				plan, err := lease.Prepare(etcdRuntimeRequest(t, "Maintenance/Alarm", request))
				if err != nil || f.calls.Load() != 0 {
					t.Fatal("pure preparation sent Alarm", err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type completion struct {
					r   contract.KVResult
					err error
				}
				out := make(chan completion, 1)
				go func() { r, e := lease.Execute(ctx, plan); out <- completion{r, e} }()
				compactionRuntimeWait(t, f.entered)
				switch mode {
				case "cancel":
					cancel()
				case "close":
					if err := lease.Close(); err != nil {
						t.Fatal(err)
					}
				case "replace":
					if _, err := m.ConnectSession(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
				case "disconnect":
					if err := m.Disconnect(p.ID); err != nil {
						t.Fatal(err)
					}
				case "parked-complete":
					other := p
					other.ID++
					if _, err := m.ConnectSession(context.Background(), other, secret); err != nil {
						t.Fatal(err)
					}
					close(f.release)
				}
				got := <-out
				compactionRuntimeWait(t, f.done)
				if f.calls.Load() != 1 || !proto.Equal(f.request, request) || f.receipt == nil || got.r.Request != plan.Snapshot() || lease.Scope().Generation != publication.Generation() || f.receipt.Header.ClusterId != original.Header.ClusterID {
					t.Fatal("processed original Alarm relabeled")
				}
				if mode == "parked-complete" {
					if got.err != nil || !got.r.Completed || got.r.WriteOutcomeUnknown || !got.r.NativeCaptured {
						t.Fatal("parked original ACK lost", got.err)
					}
				} else if got.err == nil || !got.r.Attempted || got.r.Completed || got.r.WriteOutcomeUnknown == reads {
					t.Fatal("processed original uncertainty changed", got.err)
				}
				if mode == "foreign" && (!got.r.NativeCaptured || got.r.Header.MemberID == original.Header.MemberID) {
					t.Fatal("foreign original native evidence lost")
				}
				if mode == "disconnect" {
					if err := m.Connect(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
				}
				provider, err := m.KVRequests(p.ID)
				if err != nil {
					t.Fatal(err)
				}
				recovery, err := provider.RecoverKV(lease)
				if err != nil || recovery.Lease == nil || f.calls.Load() != 1 {
					t.Fatal("pure recovery replayed Alarm", err)
				}
				defer recovery.Lease.Close()
				if (mode == "lost" || mode == "cancel" || mode == "foreign") && (recovery.Last.WriteOutcomeUnknown == reads || !bytes.Equal(recovery.Last.Native, got.r.Native)) {
					t.Fatal("recovery lost original evidence")
				}
				if _, err := recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
					t.Fatal("old original plan crossed recovery")
				}
				// The independent probe uses the original native control transport, not the
				// retired relay, so it cannot replay the captured action.
				fresh := etcdRuntimeManager(t)
				if err := fresh.Connect(context.Background(), p, secret); err != nil {
					t.Fatal(err)
				}
				later := etcdRuntimeDo(t, etcdRuntimeReserve(t, fresh, 606), "Maintenance/Alarm", &pb.AlarmRequest{})
				if reads && len(later.Reply.Alarms) != 1 || !reads && len(later.Reply.Alarms) != 0 || f.calls.Load() != 1 || lease.Scope().Generation != original.Generation {
					t.Fatal("later observation settled/replayed original action")
				}
			})
		}
	}
}
