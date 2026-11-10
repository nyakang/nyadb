package shell

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func requirePublicCompaction(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PUBLIC_COMPACTION_IT") != "1" {
		t.Skip("requires owned public Compact fixture")
	}
}

func TestEtcdNativeShellPublicCompactionOriginalFormsCheckedConfirmationAndCompleteArchives(t *testing.T) {
	requirePublicCompaction(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	s, tt, services, p, _ := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	client := etcdPublicFixtureClient(t, p, parsed.Password)
	for _, physical := range []bool{false, true} {
		prefix, _ := connections.EtcdPrefixBytes(p.EtcdOptions)
		key := append(bytes.Clone(prefix), []byte("compact-"+strconv.FormatBool(physical))...)
		first := etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("before")}).(*pb.PutResponse)
		etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("after")})
		if !s.OpenKVRequests(p.ID) {
			t.Fatal("public original KV document entry missing")
		}
		d := s.currentTab().kv
		etcdPublicWait(t, tt, func() bool { return !d.Busy() })
		s.kvWorkspace.ChooseMethod(d, kvworkspace.CompactMethod)
		s.kvWorkspace.SetCompactionInputs(d, strconv.FormatInt(first.Header.Revision, 10), false)
		tt.Frame()
		if physical {
			etcdPublicClick(t, tt, "等待物理压缩完成")
		}
		phase := "compact-public-logical-"
		if physical {
			phase = "compact-public-physical-"
		}
		captureEtcdPublic(t, s, phase+"form")
		etcdPublicClick(t, tt, "检查 revision")
		etcdPublicWait(t, tt, func() bool { return !d.Busy() })
		observation, ok := d.CompactionObservation()
		if !ok || !observation.Captured || observation.Physical != physical || observation.Scope.Namespace.ProfileID != p.ID || len(observation.Steps) != 2 {
			t.Fatal("public original read sequence missing", d.Error())
		}
		captureEtcdPublic(t, s, phase+"observation")
		etcdPublicClick(t, tt, "复制完整观察")
		observedJSON := []byte(tt.Clipboard())
		var archive []byte
		s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "compact.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
			var b bytes.Buffer
			err := write(&b)
			archive = bytes.Clone(b.Bytes())
			return err
		}})
		etcdPublicClick(t, tt, "导出 Compact 观察")
		etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
		if !bytes.Equal(archive, observedJSON) {
			t.Fatal("public complete observation export changed")
		}
		etcdPublicClick(t, tt, "预览 Compact")
		if _, ok := d.Confirmation(); !ok || d.Busy() {
			t.Fatal("preview automatically executed Compact")
		}
		captureEtcdPublic(t, s, phase+"confirmation")
		etcdPublicClick(t, tt, "复制完整 Compact 预览")
		if !bytes.Contains([]byte(tt.Clipboard()), []byte(`"earlier_observation"`)) || !bytes.Contains([]byte(tt.Clipboard()), []byte(`"original_read_receipts"`)) {
			t.Fatal("public immutable preview incomplete")
		}
		etcdPublicClick(t, tt, "我确认压缩此原集群的历史")
		etcdPublicClick(t, tt, "确认执行 Compact")
		etcdPublicWait(t, tt, func() bool { return !d.Busy() })
		r, ok := d.LastReceipt()
		if !ok || !r.Result.Completed || r.Result.WriteOutcomeUnknown || r.Compaction == nil || r.Compaction.Physical != physical || r.Compaction.Revision != first.Header.Revision {
			t.Fatal("public Compact acknowledgement lost", d.Error())
		}
		captureEtcdPublic(t, s, phase+"receipt")
		etcdPublicClick(t, tt, "复制完整 JSON")
		want, err := s.kvWorkspace.OperationRecord(d)
		if err != nil || tt.Clipboard() != string(want) {
			t.Fatal("public operation copy changed", err)
		}
		etcdPublicClick(t, tt, "导出操作记录")
		etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
		if !bytes.Equal(want, archive) {
			t.Fatal("public original native acknowledgement export changed")
		}
		etcdPublicClick(t, tt, "保存请求")
		etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
		file := bytes.Clone(archive)
		decoded, err := kvworkspace.DecodeRequest(file)
		if err != nil || decoded != d.Request() {
			t.Fatal("public native Compact request file lost", err)
		}
		s.SetKVFiles(kvworkspace.Files{OpenPath: func() (string, error) { return "compact.nyakv", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(file)), nil }})
		etcdPublicClick(t, tt, "打开请求文本")
		etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
		opened, _ := d.LastReceipt()
		if d.Busy() || opened.Authored != r.Authored || !bytes.Equal(opened.Result.Native, r.Result.Native) {
			t.Fatal("opening public request file replayed Compact")
		}
		captureEtcdPublic(t, s, phase+"reopened-file")
		if !s.kvWorkspace.GuardLeave(d, nil) {
			t.Fatal("saved observed public Compact bypassed leave decision")
		}
		s.kvWorkspace.DecideLeave(*services, false)
	}
}

func TestEtcdNativeShellPublicCompactionParkedPublicationAndRecoveryNeverExecutes(t *testing.T) {
	requirePublicCompaction(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	s, tt, _, p, grid := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	client := etcdPublicFixtureClient(t, p, parsed.Password)
	key := []byte("/compact-public-parked/" + strconv.FormatInt(time.Now().UnixNano(), 36))
	first := etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("before")}).(*pb.PutResponse)
	etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("after")})
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("public KV original entry missing")
	}
	tab, d := s.currentTab(), s.currentTab().kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	s.kvWorkspace.ChooseMethod(d, kvworkspace.CompactMethod)
	s.kvWorkspace.SetCompactionInputs(d, strconv.FormatInt(first.Header.Revision, 10), true)
	original, scope := d.Request(), d.Scope()
	s.kvWorkspace.ObserveCompaction(d)
	for _, candidate := range s.tabs {
		if candidate.data == grid {
			s.selectTab(candidate.id)
		}
	}
	time.Sleep(80 * time.Millisecond)
	tt.Frame()
	if !d.Busy() {
		t.Fatal("parked Compact observation admitted by another tab")
	}
	if err := s.runtime.Connect(context.Background(), p, parsed.Password); err != nil {
		t.Fatal(err)
	}
	s.selectTab(tab.id)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	old, ok := d.CompactionObservation()
	if !ok || old.Scope.Generation != scope.Generation || !d.NeedsRecovery() {
		t.Fatal("retired original observation moved publication")
	}
	captureEtcdPublic(t, s, "compact-public-recovery-required")
	etcdPublicClick(t, tt, "建立新会话（不执行现有请求）")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	retained, _ := d.CompactionObservation()
	if d.NeedsRecovery() || d.Scope().Generation <= scope.Generation || d.Request() != original || retained.Scope.Generation != old.Scope.Generation || len(retained.Steps) != len(old.Steps) {
		t.Fatal("public recovery erased original observation or authored text")
	}
	s.kvWorkspace.Run(d)
	if _, ok := d.Confirmation(); ok || d.Busy() {
		t.Fatal("public recovery reused retired original observation")
	}
	captureEtcdPublic(t, s, "compact-public-recovered-retained")
}
