package kvworkspace

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"math"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

func requireAlarmWorkspace(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_ALARM_WORKSPACE_IT") != "1" {
		t.Skip("requires owned original Alarm checked-batch workspace fixture")
	}
}
func alarmWorkspaceControl(t *testing.T, p connections.Profile, secret string) func(uint64, pb.AlarmType, bool) {
	t.Helper()
	_, base := workspaceProxy(t, p, nil, "unused")
	ctx := context.Background()
	if p.EtcdOptions.Auth == "basic" {
		r, err := pb.NewAuthClient(base.upstream).Authenticate(ctx, &pb.AuthenticateRequest{Name: p.Username, Password: secret})
		if err != nil {
			t.Fatal("owned Alarm setup authentication", err)
		}
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("token", r.Token))
	}
	return func(id uint64, kind pb.AlarmType, active bool) {
		t.Helper()
		action := pb.AlarmRequest_DEACTIVATE
		if active {
			action = pb.AlarmRequest_ACTIVATE
		}
		r, err := pb.NewMaintenanceClient(base.upstream).Alarm(ctx, &pb.AlarmRequest{Action: action, MemberID: id, Alarm: kind})
		if err != nil || r == nil || r.Header == nil {
			t.Fatal("owned Alarm fixture setup", err)
		}
	}
}
func alarmWorkspacePrepare(t *testing.T, s *State, d *Document, targets ...contract.KVAlarm) {
	t.Helper()
	workspaceInput(s, d, DefaultAlarmsRequest())
	s.SetAlarmSelection(d, 0, targets)
	s.ObserveAlarms(d)
	consume(t, s, d)
	if !alarmObservationReady(d) {
		t.Fatal("original actual GET did not authorize selection", d.Error())
	}
	s.PrepareAlarmBatch(d)
	if d.confirmation == nil || d.Busy() {
		t.Fatal("pure original checked batch preparation", d.Error())
	}
}
func TestEtcdNativeAlarmWorkspaceOriginalCheckedPairsAndCompleteArchives(t *testing.T) {
	requireAlarmWorkspace(t)
	p, secret := workspaceProfile(t)
	p.EtcdOptions.PrefixEncoding, p.EtcdOptions.Prefix = "base64", "AP8="
	s, d, _ := workspaceNativeState(t, p, secret)
	s.Preconnect(d)
	consume(t, s, d)
	control := alarmWorkspaceControl(t, p, secret)
	id := d.scope.Header.MemberID
	control(id, pb.AlarmType_NOSPACE, true)
	defer control(id, pb.AlarmType_NOSPACE, false)
	control(math.MaxUint64, pb.AlarmType_CORRUPT, true)
	defer control(math.MaxUint64, pb.AlarmType_CORRUPT, false)
	alarmWorkspacePrepare(t, s, d, contract.KVAlarm{MemberID: id, Type: 1}, contract.KVAlarm{MemberID: math.MaxUint64, Type: 2})
	s.Confirm(d, false)
	if d.Busy() {
		t.Fatal("unchecked native batch sent")
	}
	s.Confirm(d, true)
	consume(t, s, d)
	b, ok := d.AlarmBatchReceipt()
	if !ok || !b.Ended || b.Error != "" || len(b.Targets) != 2 || b.Targets[0].State != "succeeded" || b.Targets[1].State != "succeeded" || len(b.Targets[0].Before.Result.Reply.Alarms) != 2 || len(b.Targets[1].Before.Result.Reply.Alarms) != 1 {
		t.Fatal("fresh sequential GET/Disarm evidence", d.Error())
	}
	for _, target := range b.Targets {
		v := target.Result.Result
		native := &pb.AlarmResponse{}
		if !v.NativeCaptured || !v.Completed || v.WriteOutcomeUnknown || proto.Unmarshal(v.Native, native) != nil || len(native.Alarms) != 1 || native.Alarms[0].MemberID != target.Alarm.MemberID || int32(native.Alarms[0].Alarm) != target.Alarm.Type || native.Header.ClusterId != b.Scope.Header.ClusterID {
			t.Fatal("original removed-alarm ACK lost")
		}
	}
	s.ObserveAlarms(d)
	consume(t, s, d)
	if len(d.alarms.observation.Receipt.Result.Reply.Alarms) != 0 || len(d.alarms.batch.Targets[1].Result.Result.Reply.Alarms) != 1 {
		t.Fatal("later empty GET replaced removed ACK")
	}
	raw, err := s.AlarmsRecord(d)
	if err != nil || !bytes.Contains(raw, []byte(strconv.FormatUint(math.MaxUint64, 10))) || !bytes.Contains(raw, []byte("native_protobuf")) || !bytes.Contains(raw, []byte("fresh_original_get")) {
		t.Fatal("complete archive lost exact original identities", err)
	}
	text, err := EncodeRequest(d.Request())
	restored, decode := DecodeRequest(text)
	if err != nil || decode != nil || restored != d.Request() {
		t.Fatal("Alarm text file changed", err, decode)
	}
	copy := s.Open(2, p.ID, restored)
	s.PrepareAlarmBatch(copy)
	if copy.confirmation != nil || copy.Busy() {
		t.Fatal("request file re-created observation authority")
	}
}
func TestEtcdNativeAlarmWorkspaceStalePairStopsBeforeSendAndTypeWindowsRemainEvidence(t *testing.T) {
	requireAlarmWorkspace(t)
	p, secret := workspaceProfile(t)
	s, d, _ := workspaceNativeState(t, p, secret)
	s.Preconnect(d)
	consume(t, s, d)
	control := alarmWorkspaceControl(t, p, secret)
	id := d.scope.Header.MemberID
	control(id, pb.AlarmType_NOSPACE, true)
	defer control(id, pb.AlarmType_NOSPACE, false)
	control(math.MaxUint64, pb.AlarmType_CORRUPT, true)
	defer control(math.MaxUint64, pb.AlarmType_CORRUPT, false)
	alarmWorkspacePrepare(t, s, d, contract.KVAlarm{MemberID: id, Type: 1}, contract.KVAlarm{MemberID: math.MaxUint64, Type: 2})
	control(id, pb.AlarmType_NOSPACE, false)
	s.Confirm(d, true)
	consume(t, s, d)
	b, _ := d.AlarmBatchReceipt()
	if b.Error == "" || b.Targets[0].State != "not-executed" || b.Targets[0].Result != nil || b.Targets[0].Before == nil || b.Targets[1].Before != nil {
		t.Fatal("stale checked target dispatched or remaining inspected")
	}
	s.SetAlarmSelection(d, 2, []contract.KVAlarm{{MemberID: math.MaxUint64, Type: 2}})
	s.ObserveAlarms(d)
	consume(t, s, d)
	if d.alarms.observation.Filter != 2 || len(d.alarms.observation.Receipt.Result.Reply.Alarms) != 1 || d.alarms.observation.Receipt.Result.Reply.Alarms[0].MemberID != math.MaxUint64 {
		t.Fatal("original type-filter GET altered unsigned stale member ID")
	}
	s.PrepareAlarmBatch(d)
	s.Confirm(d, true)
	consume(t, s, d)
	if d.alarms.batch.Targets[0].State != "succeeded" || len(d.alarms.batch.Targets[0].Before.Result.Reply.Alarms) != 1 {
		t.Fatal("type window did not obtain full fresh GET before exact Disarm")
	}
}

type alarmWorkspaceFault struct {
	*workspaceFault
	alarmMode                             string
	reads                                 bool
	target                                uint64
	alarmCalls                            atomic.Int32
	alarmEntered, alarmDone, alarmRelease chan struct{}
	alarmRequest                          *pb.AlarmRequest
	alarmReceipt                          *pb.AlarmResponse
}

func (f *alarmWorkspaceFault) Alarm(ctx context.Context, r *pb.AlarmRequest) (*pb.AlarmResponse, error) {
	v, err := pb.NewMaintenanceClient(f.upstream).Alarm(f.context(ctx), r)
	if err != nil {
		return v, err
	}
	if f.reads && r.Action != pb.AlarmRequest_GET || !f.reads && (r.Action != pb.AlarmRequest_DEACTIVATE || r.MemberID != f.target) {
		return v, nil
	}
	if f.alarmCalls.Add(1) != 1 {
		return nil, status.Error(codes.Internal, "original checked Alarm request replayed")
	}
	f.alarmRequest = proto.Clone(r).(*pb.AlarmRequest)
	f.alarmReceipt = proto.Clone(v).(*pb.AlarmResponse)
	close(f.alarmEntered)
	defer close(f.alarmDone)
	switch f.alarmMode {
	case "lost":
		return nil, status.Error(codes.Unavailable, "actual original Alarm ACK withheld")
	case "cancel", "close", "replace", "disconnect", "terminal-close":
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	case "park":
		select {
		case <-f.alarmRelease:
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	case "foreign":
		v.Header.MemberId++
	case "missing-header":
		v.Header = nil
	case "extensions":
		v.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
		if len(v.Alarms) > 0 {
			v.Alarms[0].ProtoReflect().SetUnknown([]byte{0xa0, 6, 8})
		}
	}
	return v, nil
}
func alarmWorkspaceProxy(t *testing.T, p connections.Profile, mode string, reads bool, target uint64) (connections.Profile, *alarmWorkspaceFault) {
	t.Helper()
	original := p
	_, base := workspaceProxy(t, p, nil, "unused")
	f := &alarmWorkspaceFault{workspaceFault: base, alarmMode: mode, reads: reads, target: target, alarmEntered: make(chan struct{}), alarmDone: make(chan struct{}), alarmRelease: make(chan struct{})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var options []grpc.ServerOption
	if original.TLSMode == "verify-full" {
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
				t.Fatal("owned Alarm CA invalid")
			}
			cfg.ClientCAs, cfg.ClientAuth = roots, tls.RequireAndVerifyClientCert
		}
		options = append(options, grpc.Creds(credentials.NewTLS(cfg)))
	}
	server := grpc.NewServer(options...)
	pb.RegisterKVServer(server, f)
	pb.RegisterAuthServer(server, f)
	pb.RegisterMaintenanceServer(server, f)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	p.Host, p.Port = "localhost", uint16(listener.Addr().(*net.TCPAddr).Port)
	return p, f
}
func TestEtcdNativeAlarmWorkspaceActualProcessedPartialFaultsParkingRecoveryAndClose(t *testing.T) {
	requireAlarmWorkspace(t)
	p, secret := workspaceProfile(t)
	control := alarmWorkspaceControl(t, p, secret)
	for _, reads := range []bool{false, true} {
		for _, mode := range []string{"lost", "cancel", "close", "replace", "disconnect", "foreign", "missing-header", "extensions", "park", "terminal-close"} {
			t.Run(strconv.FormatBool(reads)+"/"+mode, func(t *testing.T) {
				proxy, f := alarmWorkspaceProxy(t, p, mode, reads, math.MaxUint64)
				s, d, m := workspaceNativeState(t, proxy, secret)
				s.Preconnect(d)
				consume(t, s, d)
				original := d.scope.Clone()
				for _, id := range []uint64{math.MaxUint64 - 1, math.MaxUint64, math.MaxUint64 - 2} {
					control(id, pb.AlarmType_NOSPACE, true)
					defer control(id, pb.AlarmType_NOSPACE, false)
				}
				workspaceInput(s, d, DefaultAlarmsRequest())
				if reads {
					s.ObserveAlarms(d)
				} else {
					alarmWorkspacePrepare(t, s, d, contract.KVAlarm{MemberID: math.MaxUint64 - 1, Type: 1}, contract.KVAlarm{MemberID: math.MaxUint64, Type: 1}, contract.KVAlarm{MemberID: math.MaxUint64 - 2, Type: 1})
					s.Confirm(d, true)
				}
				await(t, f.alarmEntered)
				active := true
				s.deps.Active = func(*Document) bool { return active }
				switch mode {
				case "cancel":
					s.RequestCancel(d)
				case "close":
					_ = d.lease.Close()
				case "terminal-close":
					s.Close(d)
				case "replace":
					if err := m.Connect(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
				case "disconnect":
					if err := m.Disconnect(p.ID); err != nil {
						t.Fatal(err)
					}
				case "park":
					active = false
					close(f.alarmRelease)
					await(t, d.job.finished)
					s.Consume()
					if !d.Busy() || d.alarms != nil && d.alarms.batch != nil {
						t.Fatal("parked Alarm completion consumed")
					}
					active = true
				}
				if mode == "terminal-close" {
					await(t, f.alarmDone)
					if f.alarmCalls.Load() != 1 || d.alarms != nil || d.job != nil || d.lease != nil {
						t.Fatal("terminal close retained or replayed Alarm work")
					}
					return
				}
				consume(t, s, d)
				await(t, f.alarmDone)
				if f.alarmCalls.Load() != 1 || f.alarmReceipt == nil || f.alarmReceipt.Header.ClusterId != original.Header.ClusterID || d.Scope().Generation != original.Generation {
					t.Fatal("original actual Alarm ACK relabelled")
				}
				if reads {
					if d.hasUnknownWrites() || d.alarms.observation == nil {
						t.Fatal("read created unknown write or lost evidence")
					}
					if mode == "park" || mode == "extensions" {
						if !d.alarms.observation.Receipt.Result.Completed {
							t.Fatal("actual complete read changed")
						}
					} else if d.alarms.authority != nil {
						t.Fatal("faulted read granted batch authority")
					}
				} else {
					b, _ := d.AlarmBatchReceipt()
					if b.Targets[0].State != "succeeded" || b.Targets[1].Result == nil {
						t.Fatal("partial original outcomes lost")
					}
					if mode == "park" || mode == "extensions" {
						if b.Targets[1].State != "succeeded" || b.Targets[2].State != "succeeded" || d.hasUnknownWrites() {
							t.Fatal("complete native ACK lost")
						}
					} else if b.Targets[1].State != "unknown" || b.Targets[2].State != "not-executed" || b.Targets[2].Before != nil || !d.hasUnknownWrites() {
						t.Fatal("processed unknown batch continued")
					}
				}
				if mode == "extensions" {
					var result contract.KVResult
					if reads {
						result = d.alarms.observation.Receipt.Result
					} else {
						result = d.alarms.batch.Targets[1].Result.Result
					}
					native := &pb.AlarmResponse{}
					if proto.Unmarshal(result.Native, native) != nil || !bytes.Equal(native.ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) || !bytes.Equal(native.Alarms[0].ProtoReflect().GetUnknown(), []byte{0xa0, 6, 8}) {
						t.Fatal("complete native Alarm extensions lost")
					}
				}
				if mode == "park" || mode == "extensions" {
					return
				}
				// Keep actual upstream state separate from original uncertain ACK.
				for _, id := range []uint64{math.MaxUint64 - 1, math.MaxUint64, math.MaxUint64 - 2} {
					control(id, pb.AlarmType_NOSPACE, false)
				}
				if err := m.Connect(context.Background(), p, secret); err != nil {
					t.Fatal(err)
				}
				d.needsRecovery = true
				beforeRecovery := f.alarmCalls.Load()
				s.Recover(d)
				consume(t, s, d)
				if d.needsRecovery || d.alarms.authority != nil || f.alarmCalls.Load() != beforeRecovery || d.alarms.observation != nil && d.alarms.observation.Scope.Generation != original.Generation {
					t.Fatal("recovery performed I/O or replaced original evidence", d.Error())
				}
				if !reads {
					unknown := d.alarms.unknown.Clone()
					s.ObserveAlarms(d)
					consume(t, s, d)
					if !d.hasUnknownWrites() || !reflect.DeepEqual(*d.alarms.unknown, unknown) || len(d.alarms.observation.Receipt.Result.Reply.Alarms) != 0 {
						t.Fatal("later empty list settled original unknown")
					}
					s.PrepareAlarmBatch(d)
					if d.confirmation != nil {
						t.Fatal("recovered unknown authorized another Disarm")
					}
				}
			})
		}
	}
}
func TestEtcdNativeAlarmWorkspaceOriginalRestrictedReadAndExactWriteDenial(t *testing.T) {
	requireAlarmWorkspace(t)
	p, secret := workspaceProfile(t)
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" { // This fixture has no restricted identity to impersonate.
		control := alarmWorkspaceControl(t, p, secret)
		s, d, _ := workspaceNativeState(t, p, secret)
		s.Preconnect(d)
		consume(t, s, d)
		control(d.scope.Header.MemberID, pb.AlarmType_NOSPACE, true)
		defer control(d.scope.Header.MemberID, pb.AlarmType_NOSPACE, false)
		alarmWorkspacePrepare(t, s, d, contract.KVAlarm{MemberID: d.scope.Header.MemberID, Type: 1})
		s.Confirm(d, true)
		consume(t, s, d)
		if d.alarms.batch.Targets[0].State != "succeeded" {
			t.Fatal("anonymous exact Disarm denied")
		}
		return
	}
	control := alarmWorkspaceControl(t, p, secret)
	p.Username = "restricted"
	restrictedSecret := secret
	p.EtcdOptions.PrefixEncoding, p.EtcdOptions.Prefix = "text", "/allowed/"
	if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
		p.Username = "acl-cert"
		p.EtcdOptions.ClientCertFile = os.Getenv("NYADB_ETCD_ACCESS_CERT")
		p.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_ACCESS_KEY")
	}
	s, d, _ := workspaceNativeState(t, p, restrictedSecret)
	s.Preconnect(d)
	consume(t, s, d)
	control(d.scope.Header.MemberID, pb.AlarmType_NOSPACE, true)
	defer control(d.scope.Header.MemberID, pb.AlarmType_NOSPACE, false)
	alarmWorkspacePrepare(t, s, d, contract.KVAlarm{MemberID: d.scope.Header.MemberID, Type: 1})
	s.Confirm(d, true)
	consume(t, s, d)
	b, _ := d.AlarmBatchReceipt()
	if b.Targets[0].State != "failed" || b.Targets[0].Result.Result.Code != "PermissionDenied" || b.Targets[0].Result.Result.WriteOutcomeUnknown || d.hasUnknownWrites() || !b.Targets[0].Before.Result.Completed {
		t.Fatal("original restricted GET versus Disarm denial changed", d.Error())
	}
}
