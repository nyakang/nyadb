package kvworkspace

import (
	"bytes"
	"context"
	"io"
	"math"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func requirePublicAlarms(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PUBLIC_ALARMS_IT") != "1" {
		t.Skip("requires owned public original Alarm workbench fixture")
	}
}

func TestEtcdNativeAlarmsWorkspacePublicBilingualFormsCheckedBatchFilesAndCompleteRecords(t *testing.T) {
	requirePublicAlarms(t)
	p, secret := workspaceProfile(t)
	control := alarmWorkspaceControl(t, p, secret)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, _ := workspaceNativeState(t, p, secret)
			s.Preconnect(d)
			consume(t, s, d)
			tt := workspaceTester(s, d, size[0], size[1])
			alarmWorkspaceChoose(t, tt, "KV 方法", "Alarms")
			if d.method != AlarmsMethod || d.Request() != DefaultAlarmsRequest() {
				t.Fatal("public method selector did not use its original inventory/template")
			}
			workspaceCapture(t, tt, language, size, "alarm-form")
			id := d.scope.Header.MemberID
			control(id, pb.AlarmType_NOSPACE, true)
			control(math.MaxUint64, pb.AlarmType_CORRUPT, true)
			alarmWorkspaceChoose(t, tt, "告警类型过滤", "CORRUPT")
			workspaceClick(t, tt, "读取告警列表")
			consume(t, s, d)
			tt.Frame()
			if d.alarms.observation.Filter != 2 || len(d.alarms.observation.Receipt.Result.Reply.Alarms) != 1 {
				t.Fatal("public type control changed native GET semantics", d.Error())
			}
			alarmWorkspaceSelect(t, tt, math.MaxUint64)
			workspaceCapture(t, tt, language, size, "alarm-corrupt-filter")
			alarmWorkspaceChoose(t, tt, "告警类型过滤", "全部告警")
			if d.alarms.authority != nil {
				t.Fatal("filter change retained old observation authority")
			}
			workspaceClick(t, tt, "读取告警列表")
			consume(t, s, d)
			tt.Frame()
			alarmWorkspaceSelect(t, tt, id)
			alarmWorkspaceSelect(t, tt, math.MaxUint64)
			compactionWorkspaceBounds(t, tt, size, "读取告警列表", "告警类型过滤", "原集群告警列表", "选择此原告警")
			workspaceCapture(t, tt, language, size, "alarm-selected-pairs")
			workspaceClick(t, tt, "预览解除告警")
			compactionWorkspaceBounds(t, tt, size, "不可变原告警批次", "我确认执行此不可变原告警批次", "确认执行 Disarm 批次", "取消")
			workspaceCapture(t, tt, language, size, "alarm-immutable-preview")
			workspaceClick(t, tt, "复制完整告警批次预览")
			if !bytes.Contains([]byte(tt.Clipboard()), []byte("18446744073709551615")) || !bytes.Contains([]byte(tt.Clipboard()), []byte("original_get_receipt")) {
				t.Fatal("public complete preview copy lost original authority evidence")
			}
			workspaceClick(t, tt, "我确认执行此不可变原告警批次")
			workspaceClick(t, tt, "确认执行 Disarm 批次")
			consume(t, s, d)
			tt.Frame()
			b, ok := d.AlarmBatchReceipt()
			if !ok || !b.Ended || b.Error != "" || len(b.Targets) != 2 {
				t.Fatal("public original batch did not settle", d.Error())
			}
			for _, target := range b.Targets {
				ack := &pb.AlarmResponse{}
				if target.State != "succeeded" || proto.Unmarshal(target.Result.Result.Native, ack) != nil || len(ack.Alarms) != 1 || ack.Alarms[0].MemberID != target.Alarm.MemberID || ack.Header.ClusterId != b.Scope.Header.ClusterID {
					t.Fatal("public removed-alarm native ACK incomplete")
				}
			}
			compactionWorkspaceBounds(t, tt, size, "原告警逐步结果", "告警证据视图")
			workspaceCapture(t, tt, language, size, "alarm-successful-steps")
			alarmWorkspaceChoose(t, tt, "告警证据视图", "派发前 GET")
			workspaceCapture(t, tt, language, size, "alarm-before-get")
			alarmWorkspaceChoose(t, tt, "告警证据视图", "原 Disarm ACK")
			workspaceCapture(t, tt, language, size, "alarm-original-removed-ack")
			want, err := s.AlarmsRecord(d)
			workspaceClick(t, tt, "复制完整告警记录")
			if err != nil || tt.Clipboard() != string(want) {
				t.Fatal("public archive clipboard incomplete", err)
			}
			var exported []byte
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "alarms.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
				var buffer bytes.Buffer
				err := write(&buffer)
				exported = bytes.Clone(buffer.Bytes())
				return err
			}})
			workspaceClick(t, tt, "导出告警记录")
			consume(t, s, d)
			if !bytes.Equal(want, exported) {
				t.Fatal("public complete export changed original archive")
			}
			workspaceClick(t, tt, "编辑告警选择")
			workspaceClick(t, tt, "读取告警列表")
			consume(t, s, d)
			tt.Frame()
			if len(d.alarms.observation.Receipt.Result.Reply.Alarms) != 0 || len(d.alarms.batch.Targets[1].Result.Result.Reply.Alarms) != 1 {
				t.Fatal("later public empty list overwrote removed ACK")
			}
			workspaceCapture(t, tt, language, size, "alarm-later-empty-list")
			workspaceClick(t, tt, "保存请求")
			consume(t, s, d)
			file := bytes.Clone(exported)
			s.SetFiles(Files{OpenPath: func() (string, error) { return "alarms.nyakv", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(file)), nil }})
			workspaceClick(t, tt, "打开请求文本")
			consume(t, s, d)
			tt.Frame()
			s.Run(d)
			if d.confirmation != nil || d.Busy() || d.alarms.authority != nil {
				t.Fatal("public file restored native authority or replayed Disarm")
			}
			workspaceCapture(t, tt, language, size, "alarm-text-only-file")
			control(id, pb.AlarmType_NOSPACE, false)
			control(math.MaxUint64, pb.AlarmType_CORRUPT, false)
			s.CloseAll()
		}
	}
}

func TestEtcdNativeAlarmsWorkspacePublicUnknownRecoveryParkingAndOriginalClose(t *testing.T) {
	requirePublicAlarms(t)
	p, secret := workspaceProfile(t)
	control := alarmWorkspaceControl(t, p, secret)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, mode := range []string{"lost", "park"} {
				proxy, f := alarmWorkspaceProxy(t, p, mode, false, math.MaxUint64)
				s, d, m := workspaceNativeState(t, proxy, secret)
				s.Preconnect(d)
				consume(t, s, d)
				original := d.scope.Clone()
				for _, id := range []uint64{math.MaxUint64 - 1, math.MaxUint64, math.MaxUint64 - 2} {
					control(id, pb.AlarmType_NOSPACE, true)
				}
				tt := workspaceTester(s, d, size[0], size[1])
				alarmWorkspaceChoose(t, tt, "KV 方法", "Alarms")
				workspaceClick(t, tt, "读取告警列表")
				consume(t, s, d)
				tt.Frame()
				for _, id := range []uint64{math.MaxUint64 - 1, math.MaxUint64, math.MaxUint64 - 2} {
					alarmWorkspaceSelect(t, tt, id)
				}
				workspaceClick(t, tt, "预览解除告警")
				workspaceClick(t, tt, "我确认执行此不可变原告警批次")
				workspaceClick(t, tt, "确认执行 Disarm 批次")
				await(t, f.alarmEntered)
				if mode == "park" {
					active := false
					s.deps.Active = func(*Document) bool { return active }
					close(f.alarmRelease)
					await(t, d.job.finished)
					tt.Frame()
					if !d.Busy() || d.alarms.batch != nil {
						t.Fatal("parked public original completion consumed")
					}
					workspaceCapture(t, tt, language, size, "alarm-parked-completion")
					active = true
					consume(t, s, d)
					tt.Frame()
					if d.hasUnknownWrites() || d.alarms.batch.Targets[2].State != "succeeded" {
						t.Fatal("parked public native success changed")
					}
					workspaceCapture(t, tt, language, size, "alarm-parked-original-success")
				} else {
					consume(t, s, d)
					tt.Frame()
					b := d.alarms.batch
					if !d.hasUnknownWrites() || b.Targets[0].State != "succeeded" || b.Targets[1].State != "unknown" || b.Targets[2].State != "not-executed" || b.Targets[2].Before != nil {
						t.Fatal("public partial unknown batch continued")
					}
					workspaceCapture(t, tt, language, size, "alarm-unknown-steps")
					workspaceClick(t, tt, "原未确认告警回执")
					workspaceCapture(t, tt, language, size, "alarm-original-unknown-ack")
					unknown := d.alarms.unknown.Clone()
					if err := m.Connect(context.Background(), p, secret); err != nil {
						t.Fatal(err)
					}
					workspaceClick(t, tt, "建立新会话（不执行现有请求）")
					consume(t, s, d)
					tt.Frame()
					if d.needsRecovery || d.alarms.authority != nil || f.alarmCalls.Load() != 1 || d.alarms.batch.Scope.Generation != original.Generation {
						t.Fatal("public recovery relabelled or replayed", d.Error())
					}
					workspaceClick(t, tt, "编辑告警选择")
					workspaceClick(t, tt, "读取告警列表")
					consume(t, s, d)
					tt.Frame()
					if !reflect.DeepEqual(*d.alarms.unknown, unknown) || len(d.alarms.observation.Receipt.Result.Reply.Alarms) != 1 {
						t.Fatal("later public GET settled missing original ACK")
					}
					workspaceCapture(t, tt, language, size, "alarm-recovered-explicit-get")
					s.Run(d)
					if d.confirmation != nil {
						t.Fatal("public unknown allowed another Disarm")
					}
					s.GuardLeave(d, func(ui.Services) { t.Fatal("cancelled original close continued") })
					tt.Frame()
					workspaceCapture(t, tt, language, size, "alarm-unknown-close-decision")
					workspaceClick(t, tt, "取消")
					if !d.hasUnknownWrites() || d.alarms.batch == nil {
						t.Fatal("cancelled close lost evidence")
					}
				}
				await(t, f.alarmDone)
				if f.alarmCalls.Load() != 1 || f.alarmReceipt == nil || f.alarmReceipt.Header.ClusterId != original.Header.ClusterID {
					t.Fatal("public original actual ACK ownership lost")
				}
				for _, id := range []uint64{math.MaxUint64 - 1, math.MaxUint64, math.MaxUint64 - 2} {
					control(id, pb.AlarmType_NOSPACE, false)
				}
				s.CloseAll()
			}
		}
	}
}

func TestEtcdNativeAlarmsWorkspacePublicRestrictedDenialAndUnknownTypeInspection(t *testing.T) {
	requirePublicAlarms(t)
	p, secret := workspaceProfile(t)
	control := alarmWorkspaceControl(t, p, secret)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			restricted := p
			if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
				restricted.Username = "restricted"
				restricted.EtcdOptions.Prefix = "/allowed/"
				if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
					restricted.Username = "acl-cert"
					restricted.EtcdOptions.ClientCertFile = os.Getenv("NYADB_ETCD_ACCESS_CERT")
					restricted.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_ACCESS_KEY")
				}
			}
			s, d, _ := workspaceNativeState(t, restricted, secret)
			s.Preconnect(d)
			consume(t, s, d)
			id := d.scope.Header.MemberID
			control(id, pb.AlarmType_NOSPACE, true)
			tt := workspaceTester(s, d, size[0], size[1])
			alarmWorkspaceChoose(t, tt, "KV 方法", "Alarms")
			workspaceClick(t, tt, "读取告警列表")
			consume(t, s, d)
			tt.Frame()
			alarmWorkspaceSelect(t, tt, id)
			workspaceClick(t, tt, "预览解除告警")
			workspaceClick(t, tt, "我确认执行此不可变原告警批次")
			workspaceClick(t, tt, "确认执行 Disarm 批次")
			consume(t, s, d)
			tt.Frame()
			b := d.alarms.batch
			if b == nil || !b.Targets[0].Before.Result.Completed || d.hasUnknownWrites() {
				t.Fatal("restricted public GET/exact denial evidence lost")
			}
			if os.Getenv("NYADB_ETCD_MODE") != "anonymous" && (b.Targets[0].State != "failed" || b.Targets[0].Result.Result.Code != "PermissionDenied") {
				t.Fatal("public exact denial changed", d.Error())
			}
			workspaceCapture(t, tt, language, size, "alarm-permission-result")
			control(id, pb.AlarmType_NOSPACE, false)
			s.CloseAll()
			proxy, f := alarmWorkspaceProxy(t, p, "enum", true, 0)
			s, d, _ = workspaceNativeState(t, proxy, secret)
			s.Preconnect(d)
			consume(t, s, d)
			control(d.scope.Header.MemberID, pb.AlarmType_NOSPACE, true)
			tt = workspaceTester(s, d, size[0], size[1])
			alarmWorkspaceChoose(t, tt, "KV 方法", "Alarms")
			workspaceClick(t, tt, "读取告警列表")
			consume(t, s, d)
			tt.Frame()
			if f.alarmCalls.Load() != 1 || d.alarms.observation.Receipt.Result.Reply.Alarms[0].Type != 91 {
				t.Fatal("real processed GET future enum not retained")
			}
			if err := tt.Click(strconv.FormatUint(d.scope.Header.MemberID, 10)); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if err := tt.Click(locales.Text("选择此原告警")); err == nil {
				tt.Frame()
			}
			_, targets, err := parseAlarmInputs(d.Request())
			if err != nil || len(targets) != 0 {
				t.Fatal("unknown public native enum selectable")
			}
			workspaceCapture(t, tt, language, size, "alarm-unknown-enum-inspection")
			control(d.scope.Header.MemberID, pb.AlarmType_NOSPACE, false)
			s.CloseAll()
		}
	}
}
