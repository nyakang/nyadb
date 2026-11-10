package shell

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func requireShellAlarms(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PUBLIC_ALARMS_IT") != "1" {
		t.Skip("requires owned public original Alarm workbench fixture")
	}
}

// Fixture ACTIVATE deliberately uses the native stub, outside the product's
// GET/exact-DEACTIVATE inventory. It never supplies document write authority.
func shellAlarmControl(t *testing.T, p connections.Profile, secret string) func(uint64, pb.AlarmType, bool) {
	t.Helper()
	_, base := etcdPublicProxy(t, p, nil, "unused")
	ctx := context.Background()
	if p.EtcdOptions.Auth == "basic" {
		r, err := pb.NewAuthClient(base.upstream).Authenticate(ctx, &pb.AuthenticateRequest{Name: p.Username, Password: secret})
		if err != nil {
			t.Fatal("owned Alarm fixture authentication", err)
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

func shellAlarmChoose(t *testing.T, tt *ui.Tester, label, value string) {
	t.Helper()
	etcdPublicClick(t, tt, label)
	etcdPublicClick(t, tt, value)
}
func shellAlarmSelect(t *testing.T, tt *ui.Tester, id uint64) {
	t.Helper()
	if err := tt.Click(strconv.FormatUint(id, 10)); err != nil {
		t.Fatal("public original alarm row missing", err)
	}
	tt.Frame()
	etcdPublicClick(t, tt, "选择此原告警")
}
func shellAlarmConfirm(t *testing.T, tt *ui.Tester) {
	t.Helper()
	etcdPublicClick(t, tt, "预览解除告警")
	etcdPublicClick(t, tt, "我确认执行此不可变原告警批次")
	etcdPublicClick(t, tt, "确认执行 Disarm 批次")
}

func TestEtcdNativeAlarmsShellPublicCheckedOriginalFormsFilesAndCompleteArchives(t *testing.T) {
	requireShellAlarms(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	control := shellAlarmControl(t, parsed.Profile, parsed.Password)
	s, tt, _, p, _ := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("public original KV entry missing")
	}
	d := s.currentTab().kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	shellAlarmChoose(t, tt, "KV 方法", "Alarms")
	if d.Request() != kvworkspace.DefaultAlarmsRequest() {
		t.Fatal("public inventory did not publish Alarm method")
	}
	captureEtcdPublic(t, s, "alarms-public-form")
	id := d.Scope().Header.MemberID
	control(id, pb.AlarmType_NOSPACE, true)
	control(math.MaxUint64, pb.AlarmType_CORRUPT, true)
	shellAlarmChoose(t, tt, "告警类型过滤", "CORRUPT")
	etcdPublicClick(t, tt, "读取告警列表")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	shellAlarmSelect(t, tt, math.MaxUint64)
	captureEtcdPublic(t, s, "alarms-public-filter-selection")
	shellAlarmChoose(t, tt, "告警类型过滤", "全部告警")
	etcdPublicClick(t, tt, "读取告警列表")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	shellAlarmSelect(t, tt, id)
	shellAlarmSelect(t, tt, math.MaxUint64)
	captureEtcdPublic(t, s, "alarms-public-selection")
	etcdPublicClick(t, tt, "预览解除告警")
	captureEtcdPublic(t, s, "alarms-public-confirmation")
	etcdPublicClick(t, tt, "复制完整告警批次预览")
	if !bytes.Contains([]byte(tt.Clipboard()), []byte("18446744073709551615")) || !bytes.Contains([]byte(tt.Clipboard()), []byte("original_get_receipt")) {
		t.Fatal("public immutable copy omitted original evidence")
	}
	etcdPublicClick(t, tt, "我确认执行此不可变原告警批次")
	etcdPublicClick(t, tt, "确认执行 Disarm 批次")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	b, ok := d.AlarmBatchReceipt()
	if !ok || !b.Ended || b.Error != "" || len(b.Targets) != 2 {
		t.Fatal("public original checked Alarm batch did not settle", d.Error())
	}
	for _, target := range b.Targets {
		ack := &pb.AlarmResponse{}
		if target.State != "succeeded" || target.Before == nil || target.Result == nil || proto.Unmarshal(target.Result.Result.Native, ack) != nil || len(ack.Alarms) != 1 || ack.Alarms[0].MemberID != target.Alarm.MemberID || ack.Header.ClusterId != b.Scope.Header.ClusterID {
			t.Fatal("public original removed-alarm ACK or before GET lost")
		}
	}
	captureEtcdPublic(t, s, "alarms-public-receipt")
	shellAlarmChoose(t, tt, "告警证据视图", "派发前 GET")
	captureEtcdPublic(t, s, "alarms-public-before-get")
	shellAlarmChoose(t, tt, "告警证据视图", "原 Disarm ACK")
	captureEtcdPublic(t, s, "alarms-public-native-ack")
	etcdPublicClick(t, tt, "复制完整告警记录")
	want, err := s.kvWorkspace.AlarmsRecord(d)
	if err != nil || tt.Clipboard() != string(want) {
		t.Fatal("public complete archive copy changed", err)
	}
	var archive []byte
	s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "alarms.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var buffer bytes.Buffer
		err := write(&buffer)
		archive = bytes.Clone(buffer.Bytes())
		return err
	}})
	etcdPublicClick(t, tt, "导出告警记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	if !bytes.Equal(archive, want) {
		t.Fatal("public export changed complete original archive")
	}
	etcdPublicClick(t, tt, "编辑告警选择")
	etcdPublicClick(t, tt, "读取告警列表")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	later, ok := d.AlarmObservation()
	retained, _ := d.AlarmBatchReceipt()
	if !ok || len(later.Receipt.Result.Reply.Alarms) != 0 || !reflect.DeepEqual(b, retained) {
		t.Fatal("later empty GET overwrote original removed ACK")
	}
	captureEtcdPublic(t, s, "alarms-public-later-empty-get")
	etcdPublicClick(t, tt, "保存请求")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	file := bytes.Clone(archive)
	s.SetKVFiles(kvworkspace.Files{OpenPath: func() (string, error) { return "alarms.nyakv", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(file)), nil }})
	etcdPublicClick(t, tt, "打开请求文本")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	s.kvWorkspace.Run(d)
	if _, ok := d.Confirmation(); ok || d.Busy() {
		t.Fatal("public request file restored authority or replayed Disarm")
	}
	captureEtcdPublic(t, s, "alarms-public-text-only-file")
	control(id, pb.AlarmType_NOSPACE, false)
	control(math.MaxUint64, pb.AlarmType_CORRUPT, false)
}

type shellAlarmFault struct {
	*etcdPublicFault
	alarmMode                             string
	alarmCalls                            atomic.Int32
	alarmEntered, alarmDone, alarmRelease chan struct{}
	alarmReceipt                          *pb.AlarmResponse
}

func (f *shellAlarmFault) Alarm(ctx context.Context, r *pb.AlarmRequest) (*pb.AlarmResponse, error) {
	ack, err := pb.NewMaintenanceClient(f.upstream).Alarm(f.context(ctx), r)
	if err != nil || r.Action != pb.AlarmRequest_DEACTIVATE || r.MemberID != math.MaxUint64 {
		return ack, err
	}
	if f.alarmCalls.Add(1) != 1 {
		return nil, status.Error(codes.Internal, "original Alarm replayed")
	}
	f.alarmReceipt = proto.Clone(ack).(*pb.AlarmResponse)
	close(f.alarmEntered)
	defer close(f.alarmDone)
	if f.alarmMode == "lost" {
		return nil, status.Error(codes.Unavailable, "actual processed original Alarm ACK withheld")
	}
	select {
	case <-f.alarmRelease:
		return ack, nil
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
}

func shellAlarmProxy(t *testing.T, p connections.Profile, mode string) (connections.Profile, *shellAlarmFault) {
	t.Helper()
	_, base := etcdPublicProxy(t, p, nil, "unused")
	f := &shellAlarmFault{etcdPublicFault: base, alarmMode: mode, alarmEntered: make(chan struct{}), alarmDone: make(chan struct{}), alarmRelease: make(chan struct{})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var options []grpc.ServerOption
	if p.TLSMode == "verify-full" {
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

func TestEtcdNativeAlarmsShellPublicUnknownParkingAndPureRecoveryRetainOriginals(t *testing.T) {
	requireShellAlarms(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	control := shellAlarmControl(t, parsed.Profile, parsed.Password)
	for _, mode := range []string{"lost", "park"} {
		proxy, f := shellAlarmProxy(t, parsed.Profile, mode)
		s, tt, _, p, grid := openEtcdPublicGrid(t, proxy, parsed.Password)
		t.Cleanup(s.CloseMySQLSessions)
		if !s.OpenKVRequests(p.ID) {
			t.Fatal("public original request entry missing")
		}
		tab, d := s.currentTab(), s.currentTab().kv
		etcdPublicWait(t, tt, func() bool { return !d.Busy() })
		original := d.Scope()
		for _, id := range []uint64{math.MaxUint64 - 1, math.MaxUint64, math.MaxUint64 - 2} {
			control(id, pb.AlarmType_NOSPACE, true)
		}
		shellAlarmChoose(t, tt, "KV 方法", "Alarms")
		etcdPublicClick(t, tt, "读取告警列表")
		etcdPublicWait(t, tt, func() bool { return !d.Busy() })
		for _, id := range []uint64{math.MaxUint64 - 1, math.MaxUint64, math.MaxUint64 - 2} {
			shellAlarmSelect(t, tt, id)
		}
		shellAlarmConfirm(t, tt)
		etcdPublicSignal(t, f.alarmEntered)
		if mode == "park" {
			for _, candidate := range s.tabs {
				if candidate.data == grid {
					if err := tt.Click(candidate.title); err != nil {
						t.Fatal("public grid tab click failed", err)
					}
					tt.Frame()
				}
			}
			close(f.alarmRelease)
			etcdPublicSignal(t, f.alarmDone)
			tt.Frame()
			if !d.Busy() {
				t.Fatal("public parked original completion consumed in another tab")
			}
			captureEtcdPublic(t, s, "alarms-public-parked-completion")
			if err := tt.Click(tab.title); err != nil {
				t.Fatal("public original request tab click failed", err)
			}
			tt.Frame()
			etcdPublicWait(t, tt, func() bool { return !d.Busy() })
			b, ok := d.AlarmBatchReceipt()
			if !ok || b.Targets[2].State != "succeeded" || b.Scope.Generation != original.Generation {
				t.Fatal("parked actual success lost original scope")
			}
			captureEtcdPublic(t, s, "alarms-public-parked-original-success")
		} else {
			etcdPublicWait(t, tt, func() bool { return !d.Busy() })
			b, ok := d.AlarmBatchReceipt()
			unknown, hasUnknown := d.AlarmUnknownReceipt()
			if !ok || !hasUnknown || b.Targets[0].State != "succeeded" || b.Targets[1].State != "unknown" || b.Targets[2].State != "not-executed" || b.Targets[2].Before != nil || !unknown.Result.WriteOutcomeUnknown {
				t.Fatal("public partial unknown replayed or lost original evidence", d.Error())
			}
			captureEtcdPublic(t, s, "alarms-public-unknown")
			etcdPublicClick(t, tt, "原未确认告警回执")
			captureEtcdPublic(t, s, "alarms-public-original-unknown-ack")
			if err := s.runtime.Connect(context.Background(), p, parsed.Password); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			captureEtcdPublic(t, s, "alarms-public-recovery-required")
			etcdPublicClick(t, tt, "建立新会话（不执行现有请求）")
			etcdPublicWait(t, tt, func() bool { return !d.Busy() })
			retained, ok := d.AlarmUnknownReceipt()
			if !ok || d.NeedsRecovery() || !reflect.DeepEqual(unknown, retained) || d.Scope().Generation <= original.Generation || f.alarmCalls.Load() != 1 {
				t.Fatal("public pure recovery relabelled/replayed original unknown", d.Error())
			}
			etcdPublicClick(t, tt, "编辑告警选择")
			etcdPublicClick(t, tt, "读取告警列表")
			etcdPublicWait(t, tt, func() bool { return !d.Busy() })
			retained, _ = d.AlarmUnknownReceipt()
			if !reflect.DeepEqual(unknown, retained) {
				t.Fatal("explicit later GET settled missing ACK")
			}
			captureEtcdPublic(t, s, "alarms-public-recovered-explicit-get")
			shellAlarmChoose(t, tt, "KV 方法", "Put")
			d.Editor.Buffer.Set(`{"key":"L2FsbG93ZWQv","value":"dg=="}`)
			s.kvWorkspace.Run(d)
			if _, ok := d.Confirmation(); ok || d.Busy() {
				t.Fatal("unknown Alarm allowed cross-method write")
			}
			etcdPublicClick(t, tt, "告警回执")
			captureEtcdPublic(t, s, "alarms-public-cross-method-retained")
			s.kvWorkspace.GuardLeave(d, nil)
			captureEtcdPublic(t, s, "alarms-public-unknown-close")
			etcdPublicClick(t, tt, "取消")
			if _, ok := d.AlarmUnknownReceipt(); !ok {
				t.Fatal("cancelled close erased original unknown")
			}
		}
		etcdPublicSignal(t, f.alarmDone)
		if f.alarmCalls.Load() != 1 || f.alarmReceipt == nil || f.alarmReceipt.Header.ClusterId != original.Header.ClusterID {
			t.Fatal("original actual processed Alarm ownership lost")
		}
		for _, id := range []uint64{math.MaxUint64 - 1, math.MaxUint64, math.MaxUint64 - 2} {
			control(id, pb.AlarmType_NOSPACE, false)
		}
		s.CloseMySQLSessions()
	}
}

func shellAlarmBounds(t *testing.T, tt *ui.Tester, phase string, size [2]int) {
	t.Helper()
	var labels []string
	switch phase {
	case "alarms-public-form":
		labels = []string{"读取告警列表", "告警类型过滤"}
	case "alarms-public-selection", "alarms-public-filter-selection":
		labels = []string{"读取告警列表", "原集群告警列表", "选择此原告警"}
	case "alarms-public-confirmation":
		labels = []string{"不可变原告警批次", "复制完整告警批次预览", "我确认执行此不可变原告警批次", "确认执行 Disarm 批次", "取消"}
	case "alarms-public-receipt", "alarms-public-unknown", "alarms-public-parked-original-success":
		labels = []string{"原告警逐步结果", "告警证据视图"}
	case "alarms-public-before-get", "alarms-public-native-ack", "alarms-public-original-unknown-ack":
		labels = []string{"告警原生观察", "告警证据视图"}
	}
	for _, label := range labels {
		r, ok := tt.Find(locales.Text(label))
		if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
			t.Fatal("public Alarm control clipped", phase, label, r)
		}
	}
	if phase == "alarms-public-unknown" || phase == "alarms-public-parked-original-success" {
		viewport, visible := tt.Find(locales.Text("原告警逐步结果"))
		if !visible {
			t.Fatal("public Alarm result table missing")
		}
		for _, id := range []uint64{math.MaxUint64 - 1, math.MaxUint64, math.MaxUint64 - 2} {
			r, ok := tt.Find(strconv.FormatUint(id, 10))
			if !ok || r.W <= 0 || r.H <= 0 || r.X < viewport.X || r.Y < viewport.Y || r.X+r.W > viewport.X+viewport.W+1 || r.Y+r.H > viewport.Y+viewport.H+1 || r.Y+r.H > float32(size[1])+1 {
				t.Fatal("public Alarm result row clipped", phase, id, r)
			}
		}
	}
	if phase == "alarms-public-cross-method-retained" {
		r, ok := tt.Find(locales.Text("告警原生观察"))
		if !ok || r.H < 45 || r.Y+r.H > float32(size[1])+1 {
			t.Fatal("public cross-method original Alarm evidence clipped", r)
		}
	}
}
