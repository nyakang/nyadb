package etcd

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math"
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
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func alarmNativeClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("NYADB_ETCD_ALARM_IT") != "1" {
		t.Skip("requires owned original Alarm fixture")
	}
	return nativeClient(t)
}
func alarmSetup(t *testing.T, c *Client, id uint64, kind pb.AlarmType, active bool) {
	t.Helper()
	action := pb.AlarmRequest_DEACTIVATE
	if active {
		action = pb.AlarmRequest_ACTIVATE
	}
	r, err := pb.NewMaintenanceClient(c.conn).Alarm(c.authContext(context.Background()), &pb.AlarmRequest{Action: action, MemberID: id, Alarm: kind})
	if err != nil || r == nil || !c.matches(r.Header) {
		t.Fatal("owned alarm setup failed", err)
	}
}
func alarmRemoveOwned(t *testing.T, c *Client, id uint64, kind pb.AlarmType) {
	t.Helper()
	alarmSetup(t, c, id, kind, false)
}

func TestEtcdNativeAlarmOriginalListTypeFilterExactDisarmAndCompleteReceipts(t *testing.T) {
	admin := alarmNativeClient(t)
	empty := nativeDo(t, admin, &pb.AlarmRequest{}).Native().(*pb.AlarmResponse)
	if len(empty.Alarms) != 0 || !admin.matches(empty.Header) {
		t.Fatal("fixture must start with an actual empty Alarm list")
	}
	id := admin.Identity().MemberID
	profile, secret := sessionProfile(t)
	profile.EtcdOptions.Prefix = "/unrelated-original-prefix/"
	raw, err := OpenSession(context.Background(), profile, secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	lease := reserveNativeKV(t, s, 101)
	if err := lease.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	alarmSetup(t, admin, id, pb.AlarmType_NOSPACE, true)
	defer alarmRemoveOwned(t, admin, id, pb.AlarmType_NOSPACE)
	// A stale/foreign member ID can remain in the original cluster's alarm store.
	// It is native observation, not proof of a currently connected physical member.
	alarmSetup(t, admin, math.MaxUint64, pb.AlarmType_CORRUPT, true)
	defer alarmRemoveOwned(t, admin, math.MaxUint64, pb.AlarmType_CORRUPT)
	ignored, err := pb.NewMaintenanceClient(admin.conn).Alarm(admin.authContext(context.Background()), &pb.AlarmRequest{MemberID: id})
	if err != nil || len(ignored.Alarms) != 2 {
		t.Fatal("actual native GET member_id semantics changed", err)
	}
	if _, err := admin.Prepare(&pb.AlarmRequest{MemberID: id}); !errors.Is(err, ErrInvalid) {
		t.Fatal("ignored member filter accepted")
	}
	if !slices.Contains(lease.(contract.KVMaintenance).MaintenanceMethods(), "/etcdserverpb.Maintenance/Alarm") {
		t.Fatal("original lease lost Alarm inventory")
	}
	all := nativeKVDo(t, lease, &pb.AlarmRequest{})
	if all.Reply == nil || all.Reply.Kind != "alarm" || len(all.Reply.Alarms) != 2 || !all.NativeCaptured || all.JSON == "" || all.Header.ClusterID != s.identity.ClusterID {
		t.Fatal("native Alarm list lost original scope")
	}
	corrupt := nativeKVDo(t, lease, &pb.AlarmRequest{Alarm: pb.AlarmType_CORRUPT})
	if len(corrupt.Reply.Alarms) != 1 || corrupt.Reply.Alarms[0].MemberID != math.MaxUint64 || !bytes.Contains([]byte(corrupt.JSON), []byte(strconv.FormatUint(math.MaxUint64, 10))) {
		t.Fatal("type filter or unsigned native identity changed")
	}
	request := nativeKVRequest(t, &pb.AlarmRequest{Action: pb.AlarmRequest_DEACTIVATE, MemberID: math.MaxUint64, Alarm: pb.AlarmType_CORRUPT})
	plan, err := lease.Prepare(request)
	if err != nil || !plan.Writes() {
		t.Fatal("Alarm preparation failed", err)
	}
	original := plan.Snapshot()
	request.JSON = "{}"
	result, err := lease.Execute(context.Background(), plan)
	native := &pb.AlarmResponse{}
	if err != nil || !result.Completed || result.WriteOutcomeUnknown || result.Request != original || proto.Unmarshal(result.Native, native) != nil || len(native.Alarms) != 1 || native.Alarms[0].MemberID != math.MaxUint64 || result.Header != neutralHeader(native.Header) {
		t.Fatal("original removed-alarm ACK lost", err)
	}
	if _, err := lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original Alarm plan replayed")
	}
	later := nativeKVDo(t, lease, &pb.AlarmRequest{})
	if len(later.Reply.Alarms) != 1 || later.Reply.Alarms[0].MemberID != id || len(result.Reply.Alarms) != 1 || result.Reply.Alarms[0].MemberID != math.MaxUint64 {
		t.Fatal("later list overwrote original removed ACK")
	}
	absent := nativeKVDo(t, lease, &pb.AlarmRequest{Action: pb.AlarmRequest_DEACTIVATE, MemberID: math.MaxUint64, Alarm: pb.AlarmType_CORRUPT})
	if !absent.Completed || !absent.NativeCaptured || len(absent.Reply.Alarms) != 0 || absent.Header.ClusterID != s.identity.ClusterID {
		t.Fatal("actual already-absent ACK invented an alarm or lacked capture")
	}
	if len(all.Reply.Alarms) != 2 {
		t.Fatal("later read aliases original list")
	}
}

func TestEtcdNativeAlarmRestrictedTokenAndCertificateOriginalPermissions(t *testing.T) {
	admin := alarmNativeClient(t)
	cfg, secret := nativeConfig(t)
	id := admin.Identity().MemberID
	alarmSetup(t, admin, id, pb.AlarmType_NOSPACE, true)
	defer alarmRemoveOwned(t, admin, id, pb.AlarmType_NOSPACE)
	if cfg.Auth == "basic" {
		restricted := cfg
		restricted.Username = "restricted"
		restricted.ProbeKey = []byte("/allowed/key")
		if cfg.ClientCertFile != "" {
			restricted.ClientCertFile = os.Getenv("NYADB_ETCD_ACCESS_CERT")
			restricted.ClientKeyFile = os.Getenv("NYADB_ETCD_ACCESS_KEY")
			if restricted.ClientCertFile == "" {
				t.Fatal("missing owned restricted certificate")
			}
		}
		c, err := Open(context.Background(), restricted, secret)
		if err != nil {
			t.Fatal("original restricted open", err)
		}
		defer c.Close()
		readPlan, _ := c.Prepare(&pb.AlarmRequest{})
		observed, readErr := c.Execute(context.Background(), readPlan)
		if readErr != nil || !observed.Completed || len(observed.Native().(*pb.AlarmResponse).Alarms) != 1 {
			t.Fatal("authenticated Alarm read incorrectly requires admin", readErr)
		}
		p, _ := c.Prepare(&pb.AlarmRequest{Action: pb.AlarmRequest_DEACTIVATE, MemberID: id, Alarm: pb.AlarmType_NOSPACE})
		denied, err := c.Execute(context.Background(), p)
		code := status.Code(err)
		t.Logf("Original Alarm denial: %s / %s", code, status.Convert(err).Message())
		if err == nil || denied.Completed || denied.WriteOutcomeUnknown || !c.Alive() || code != codes.PermissionDenied {
			t.Fatal("original maintenance denial changed native code or uncertainty", err)
		}
		still := nativeDo(t, admin, &pb.AlarmRequest{}).Native().(*pb.AlarmResponse)
		if len(still.Alarms) != 1 {
			t.Fatal("denied original Disarm modified alarm")
		}
	}
	if cfg.ClientCertFile != "" {
		certificate := cfg
		certificate.Auth, certificate.Username = "none", ""
		c, err := Open(context.Background(), certificate, "")
		if err != nil {
			t.Fatal("original certificate open", err)
		}
		defer c.Close()
		r := nativeDo(t, c, &pb.AlarmRequest{Action: pb.AlarmRequest_DEACTIVATE, MemberID: id, Alarm: pb.AlarmType_NOSPACE})
		if len(r.Native().(*pb.AlarmResponse).Alarms) != 1 {
			t.Fatal("certificate original native Disarm lost removed entry")
		}
	}
}

type alarmFault struct {
	upstream      *Client
	mode          string
	reads         bool
	calls         atomic.Int32
	entered, done chan struct{}
	request       *pb.AlarmRequest
	receipt       *pb.AlarmResponse
}

func (f *alarmFault) serve(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	if method != "/etcdserverpb.Maintenance/Alarm" {
		return (&nativeFault{upstream: f.upstream}).serve(nil, stream)
	}
	request := &pb.AlarmRequest{}
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	reply, err := pb.NewMaintenanceClient(f.upstream.conn).Alarm(f.upstream.authContext(stream.Context()), request)
	if err != nil {
		return err
	}
	if (request.Action == pb.AlarmRequest_GET) != f.reads {
		return stream.SendMsg(reply)
	}
	if f.calls.Add(1) != 1 {
		return status.Error(codes.Internal, "original Alarm replayed")
	}
	f.request = proto.Clone(request).(*pb.AlarmRequest)
	f.receipt = proto.Clone(reply).(*pb.AlarmResponse)
	close(f.entered)
	defer close(f.done)
	switch f.mode {
	case "lost":
		return status.Error(codes.Unavailable, "actual completed original Alarm reply withheld")
	case "cancel", "close":
		<-stream.Context().Done()
		return stream.Context().Err()
	case "foreign":
		reply.Header.ClusterId++
	case "member":
		reply.Header.MemberId++
	case "missing-header":
		reply.Header = nil
	case "extensions":
		reply.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
		if len(reply.Alarms) > 0 {
			reply.Alarms[0].ProtoReflect().SetUnknown([]byte{0xa0, 6, 8})
		}
	}
	return stream.SendMsg(reply)
}
func alarmProxy(t *testing.T, admin *Client, mode string, reads bool) (*Client, *alarmFault) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &alarmFault{upstream: admin, mode: mode, reads: reads, entered: make(chan struct{}), done: make(chan struct{})}
	cfg, secret := nativeConfig(t)
	var options []grpc.ServerOption
	if cfg.CAFile != "" {
		serverCert := strings.TrimSuffix(cfg.CAFile, "ca.crt") + "server.crt"
		serverKey := strings.TrimSuffix(cfg.CAFile, "ca.crt") + "server.key"
		certificate, err := tls.LoadX509KeyPair(serverCert, serverKey)
		if err != nil {
			t.Fatal(err)
		}
		tc := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
		if cfg.ClientCertFile != "" {
			raw, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				t.Fatal(err)
			}
			tc.ClientCAs = x509.NewCertPool()
			if !tc.ClientCAs.AppendCertsFromPEM(raw) {
				t.Fatal("invalid fixture CA")
			}
			tc.ClientAuth = tls.RequireAndVerifyClientCert
		}
		options = append(options, grpc.Creds(credentials.NewTLS(tc)))
		cfg.Endpoint = "https://localhost:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	} else {
		cfg.Endpoint = "http://" + listener.Addr().String()
	}
	options = append(options, grpc.UnknownServiceHandler(f.serve))
	server := grpc.NewServer(options...)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal("original Alarm relay handshake", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, f
}

func TestEtcdNativeAlarmActualProcessedReadAndDisarmFaultsRetainOriginalsNeverReplay(t *testing.T) {
	admin := alarmNativeClient(t)
	id := admin.Identity().MemberID
	for _, reads := range []bool{false, true} {
		for _, mode := range []string{"lost", "cancel", "close", "foreign", "member", "missing-header", "extensions"} {
			t.Run(strconv.FormatBool(reads)+"/"+mode, func(t *testing.T) {
				alarmSetup(t, admin, id, pb.AlarmType_NOSPACE, true)
				defer alarmRemoveOwned(t, admin, id, pb.AlarmType_NOSPACE)
				c, f := alarmProxy(t, admin, mode, reads)
				profile, secret := sessionProfile(t)
				s := newSession(profile, secret, nativeClient(t))
				defer s.Close()
				var opens atomic.Int32
				s.opener = func(ctx context.Context, p connections.Profile, secret string) (*Client, error) {
					if opens.Add(1) == 1 {
						return c, nil
					}
					return OpenProfile(ctx, p, secret)
				}
				lease := reserveNativeKV(t, s, 202)
				request := &pb.AlarmRequest{Action: pb.AlarmRequest_DEACTIVATE, MemberID: id, Alarm: pb.AlarmType_NOSPACE}
				if reads {
					request = &pb.AlarmRequest{}
				}
				plan, err := lease.Prepare(nativeKVRequest(t, request))
				if err != nil || opens.Load() != 0 || f.calls.Load() != 0 {
					t.Fatal("pure Alarm preparation performed I/O", err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type completion struct {
					r   contract.KVResult
					err error
				}
				out := make(chan completion, 1)
				go func() { r, e := lease.Execute(ctx, plan); out <- completion{r, e} }()
				waitSignal(t, f.entered)
				if mode == "cancel" {
					cancel()
				}
				if mode == "close" {
					if err := lease.Close(); err != nil {
						t.Fatal(err)
					}
				}
				got := <-out
				waitSignal(t, f.done)
				if f.calls.Load() != 1 || f.receipt == nil || !proto.Equal(f.request, request) || got.r.Request != plan.Snapshot() {
					t.Fatal("fault did not target original processed RPC")
				}
				if mode == "extensions" {
					native := &pb.AlarmResponse{}
					if got.err != nil || !got.r.Completed || got.r.WriteOutcomeUnknown || !got.r.NativeCaptured || proto.Unmarshal(got.r.Native, native) != nil || !bytes.Equal(native.ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) || !bytes.Equal(native.Alarms[0].ProtoReflect().GetUnknown(), []byte{0xa0, 6, 8}) {
						t.Fatal("complete valid original Alarm extensions lost", got.err)
					}
				} else if got.err == nil || !got.r.Attempted || got.r.Completed || got.r.WriteOutcomeUnknown == reads || lease.CheckKVLease() == nil {
					t.Fatal("actual fault changed original uncertainty", got.err)
				}
				if mode == "foreign" || mode == "member" || mode == "missing-header" {
					if !got.r.NativeCaptured || got.r.JSON == "" {
						t.Fatal("invalid original native receipt erased")
					}
				}
				if mode != "extensions" {
					recovery, err := s.RecoverKV(lease)
					if err != nil || recovery.Lease == nil {
						t.Fatal(err)
					}
					defer recovery.Lease.Close()
					if _, err := recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
						t.Fatal("recovery accepted original plan")
					}
					if f.calls.Load() != 1 || opens.Load() != 1 {
						t.Fatal("recovery connected or replayed Alarm")
					}
					if !reads {
						blocked, _ := recovery.Lease.Prepare(nativeKVRequest(t, &pb.AlarmRequest{Action: pb.AlarmRequest_DEACTIVATE, MemberID: id, Alarm: pb.AlarmType_NOSPACE}))
						if r, e := recovery.Lease.Execute(context.Background(), blocked); !errors.Is(e, contract.ErrKVWriteUnknown) || r.Attempted || opens.Load() != 1 {
							t.Fatal("actual recovered write uncertainty released")
						}
						fresh := nativeKVDo(t, recovery.Lease, &pb.AlarmRequest{})
						if !fresh.Completed || len(fresh.Reply.Alarms) != 0 || !got.r.WriteOutcomeUnknown || f.calls.Load() != 1 {
							t.Fatal("later recovered read settled original Disarm")
						}
					}
				}
				later := nativeDo(t, admin, &pb.AlarmRequest{}).Native().(*pb.AlarmResponse)
				if reads && len(later.Alarms) != 1 || !reads && len(later.Alarms) != 0 {
					t.Fatal("actual upstream action did not complete")
				}
				if mode != "extensions" && got.r.WriteOutcomeUnknown == reads {
					t.Fatal("later list changed retained original outcome")
				}
			})
		}
	}
}

func TestEtcdNativeAlarmOriginalCancellationBusyOwnershipAndUnknownWriteBarrier(t *testing.T) {
	admin := alarmNativeClient(t)
	id := admin.Identity().MemberID
	alarmSetup(t, admin, id, pb.AlarmType_NOSPACE, true)
	defer alarmRemoveOwned(t, admin, id, pb.AlarmType_NOSPACE)
	c, f := alarmProxy(t, admin, "cancel", false)
	p, err := c.Prepare(&pb.AlarmRequest{Action: pb.AlarmRequest_DEACTIVATE, MemberID: id, Alarm: pb.AlarmType_NOSPACE})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancelBefore := context.WithCancel(context.Background())
	cancelBefore()
	if r, err := c.Execute(cancelled, p); !errors.Is(err, context.Canceled) || r.Attempted || f.calls.Load() != 0 || p.used.Load() {
		t.Fatal("pre-cancel dispatched or consumed request")
	}
	other := nativeClient(t)
	if _, err := other.Execute(context.Background(), p); !errors.Is(err, ErrUsed) || p.used.Load() {
		t.Fatal("foreign transport accepted original plan")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan error, 1)
	go func() { _, err := c.Execute(ctx, p); out <- err }()
	waitSignal(t, f.entered)
	read, _ := c.Prepare(&pb.AlarmRequest{})
	if _, err := c.Execute(context.Background(), read); !errors.Is(err, ErrBusy) || read.used.Load() {
		t.Fatal("busy original executor accepted another request")
	}
	cancel()
	if err := <-out; err == nil {
		t.Fatal("processed cancellation returned success")
	}
	waitSignal(t, f.done)
	if _, err := c.Execute(context.Background(), p); err == nil || f.calls.Load() != 1 {
		t.Fatal("interrupted original Disarm replayed")
	}
	// Read-only Alarm may inspect a lease whose prior ordinary/member write is
	// unconfirmed, but must never reset that independent write barrier.
	profile, secret := sessionProfile(t)
	raw, err := OpenSession(context.Background(), profile, secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	lease := reserveNativeKV(t, s, 303).(*kvLease)
	lease.writeUnknown = true
	lease.memberUnknown = &memberReceipt{result: contract.KVMemberResult{Result: contract.KVResult{WriteOutcomeUnknown: true}}}
	observed := nativeKVDo(t, lease, &pb.AlarmRequest{})
	if !observed.Completed || !lease.writeUnknown || lease.memberUnknown == nil {
		t.Fatal("Alarm read cleared another original write uncertainty")
	}
	write, _ := lease.Prepare(nativeKVRequest(t, &pb.AlarmRequest{Action: pb.AlarmRequest_DEACTIVATE, MemberID: id, Alarm: pb.AlarmType_NOSPACE}))
	if r, err := lease.Execute(context.Background(), write); !errors.Is(err, contract.ErrKVWriteUnknown) || r.Attempted {
		t.Fatal("Disarm bypassed original write barrier")
	}
}
