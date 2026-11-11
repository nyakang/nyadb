package shell

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/etcd"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/services/snapshotfiles"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func requireShellSnapshot(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PUBLIC_SNAPSHOT_IT") != "1" {
		t.Skip("requires owned public Snapshot workbench fixture")
	}
}
func shellSnapshotBounds(t *testing.T, tt *ui.Tester, phase string, size [2]int) {
	t.Helper()
	labels := []string{"Snapshot 证据视图", "保存原 backend 文件", "保存完整原帧日志"}
	switch phase {
	case "snapshot-public-inputs":
		labels = []string{"后端字节限额", "原帧日志字节限额", "原帧数量限额", "限额 JSON"}
	case "snapshot-public-capture-confirmation":
		labels = []string{"我确认捕获此原集群的完整后端", "确认捕获 Snapshot", "取消"}
	case "snapshot-public-new-confirmation":
		labels = []string{"我确认保存此原文件", "确认保存 Snapshot 文件", "取消"}
	case "snapshot-public-replace-confirmation":
		labels = []string{"我确认替换此已检查文件并保留旧备份", "确认替换 Snapshot 文件", "取消"}
	case "snapshot-public-preserved-backup":
		labels = []string{"保留旧备份位置"}
	case "snapshot-public-complete-record":
		labels = []string{"完整 Snapshot 记录预览", "复制完整 Snapshot 记录", "导出 Snapshot 记录"}
	}
	for _, label := range labels {
		r, ok := tt.Find(locales.Text(label))
		if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
			t.Fatal("public Snapshot control clipped", phase, label, size, r)
		}
	}
}
func shellSnapshotStore(t *testing.T, s *State, path string) *snapshotfiles.KVSnapshotStore {
	t.Helper()
	store, err := snapshotfiles.NewKVSnapshotStore(t.TempDir(), 512<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.CloseMySQLSessions()
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	s.SetKVSnapshotOperations(store, kvworkspace.SnapshotFiles{Operations: store, SavePath: func(contract.KVSnapshotFileKind, string) (string, error) { return path, nil }})
	return store
}
func shellSnapshotSave(t *testing.T, tt *ui.Tester, d *kvworkspace.Document, replace bool) {
	t.Helper()
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	check, action := "我确认保存此原文件", "确认保存 Snapshot 文件"
	if replace {
		check, action = "我确认替换此已检查文件并保留旧备份", "确认替换 Snapshot 文件"
	}
	etcdPublicClick(t, tt, check)
	etcdPublicClick(t, tt, action)
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
}
func TestEtcdNativeSnapshotShellCheckedOriginalFilesArchivesDisconnectAndRecovery(t *testing.T) {
	requireShellSnapshot(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	s, tt, _, p, _ := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	path := filepath.Join(t.TempDir(), "original-backend.db")
	store := shellSnapshotStore(t, s, path)
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("Snapshot workbench entry missing")
	}
	d := s.currentTab().kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	shellAlarmChoose(t, tt, "KV 方法", "Snapshot")
	if d.Request() != kvworkspace.DefaultSnapshotRequest() {
		t.Fatal("Snapshot default exact limits missing")
	}
	captureEtcdPublic(t, s, "snapshot-public-inputs")
	etcdPublicClick(t, tt, "预览 Snapshot 捕获")
	captureEtcdPublic(t, s, "snapshot-public-capture-confirmation")
	etcdPublicClick(t, tt, "我确认捕获此原集群的完整后端")
	etcdPublicClick(t, tt, "确认捕获 Snapshot")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	original, ok := d.SnapshotReceipt()
	if !ok || !original.Complete || original.Original.Scope.DocumentID != d.ID() || original.Original.Scope.Original.Namespace.ProfileID != p.ID {
		t.Fatal("public original Snapshot incomplete", d.Error())
	}
	captureEtcdPublic(t, s, "snapshot-public-source")
	shellAlarmChoose(t, tt, "Snapshot 证据视图", "私有归档校验")
	captureEtcdPublic(t, s, "snapshot-public-artifact")
	var first []byte
	for _, replace := range []bool{false, true} {
		old := []byte("the actual original user backup\x00\xff")
		if replace {
			if err := os.WriteFile(path, old, 0600); err != nil {
				t.Fatal(err)
			}
		}
		etcdPublicClick(t, tt, "保存原 backend 文件")
		etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
		stage := "snapshot-public-new-confirmation"
		if replace {
			stage = "snapshot-public-replace-confirmation"
		}
		captureEtcdPublic(t, s, stage)
		shellSnapshotSave(t, tt, d, replace)
		files := d.SnapshotFileReceipts()
		r := files[len(files)-1]
		if !r.Published || !r.FileSynced || !r.DirectorySynced || !r.PathMatched || !reflect.DeepEqual(r.Original.Source, original) {
			t.Fatal("public file receipt incomplete", d.Error())
		}
		actual, err := os.ReadFile(path)
		if err != nil || uint64(len(actual)) != original.SnapshotBytes {
			t.Fatal("public file original bytes incomplete", err)
		}
		if replace {
			preserved, err := os.ReadFile(r.DisplacedPath)
			if err != nil || !bytes.Equal(preserved, old) || !bytes.Equal(actual, first) {
				t.Fatal("public replacement lost original or backup", err)
			}
		} else {
			first = bytes.Clone(actual)
		}
	}
	shellAlarmChoose(t, tt, "Snapshot 证据视图", "用户文件发布")
	captureEtcdPublic(t, s, "snapshot-public-file-publication")
	captureEtcdPublic(t, s, "snapshot-public-preserved-backup")
	if err := s.runtime.Disconnect(p.ID); err != nil {
		t.Fatal(err)
	}
	s.kvWorkspace.DisconnectProfile(p.ID)
	s.SetKVSnapshotOperations(store, kvworkspace.SnapshotFiles{Operations: store, SavePath: func(contract.KVSnapshotFileKind, string) (string, error) { return path + ".journal", nil }})
	etcdPublicClick(t, tt, "保存完整原帧日志")
	shellSnapshotSave(t, tt, d, false)
	journal, err := os.ReadFile(path + ".journal")
	if err != nil || uint64(len(journal)) != original.JournalBytes || !d.NeedsRecovery() {
		t.Fatal("disconnected original journal publication failed", err)
	}
	var concatenated bytes.Buffer
	err = etcd.ReadSnapshotJournal(bytes.NewReader(journal), uint64(len(journal)), func(record etcd.SnapshotJournalRecord) error {
		if record.Kind != etcd.SnapshotJournalFrameKind {
			return nil
		}
		frame := &pb.SnapshotResponse{}
		if err := proto.Unmarshal(record.Raw, frame); err != nil {
			return err
		}
		concatenated.Write(frame.Blob)
		return nil
	})
	if err != nil || !bytes.Equal(concatenated.Bytes(), first) {
		t.Fatal("Shell original backend differs from its full native journal", err)
	}
	captureEtcdPublic(t, s, "snapshot-public-disconnected-journal")
	if _, err := s.runtime.ConnectSession(context.Background(), p, parsed.Password); err != nil {
		t.Fatal(err)
	}
	s.kvWorkspace.Recover(d)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	recovered, _ := d.SnapshotReceipt()
	if d.NeedsRecovery() || !reflect.DeepEqual(recovered, original) || d.Scope().Generation <= original.Original.Scope.Original.Generation {
		t.Fatal("Shell recovery rebound or erased the original source", d.Error())
	}
	captureEtcdPublic(t, s, "snapshot-public-pure-recovery")
	s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return path + ".record.json", nil }, WriteStream: queries.WriteStream})
	etcdPublicClick(t, tt, "复制完整 Snapshot 记录")
	want, err := s.kvWorkspace.SnapshotRecord(d)
	if err != nil || tt.Clipboard() != string(want) {
		t.Fatal("Shell clipboard lost full original archive")
	}
	etcdPublicClick(t, tt, "导出 Snapshot 记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	actual, err := os.ReadFile(path + ".record.json")
	if err != nil || !bytes.Equal(actual, want) {
		t.Fatal("Shell file export lost full original archive", err)
	}
	shellAlarmChoose(t, tt, "Snapshot 证据视图", "完整 Snapshot 记录")
	captureEtcdPublic(t, s, "snapshot-public-complete-record")
}

type shellParkedSnapshotStore struct {
	*snapshotfiles.KVSnapshotStore
	entered, release, returned chan struct{}
	artifact                   contract.KVSnapshotArtifact
}

func (x *shellParkedSnapshotStore) Capture(ctx context.Context, prep contract.KVSnapshotCapturePreparation) (contract.KVSnapshotArtifact, contract.KVSnapshotCapture, error) {
	a, r, err := x.KVSnapshotStore.Capture(ctx, prep)
	x.artifact = a
	close(x.entered)
	select {
	case <-x.release:
	case <-ctx.Done():
	}
	close(x.returned)
	return a, r, err
}
func TestEtcdNativeSnapshotShellOriginalParkingInactiveCloseAndTerminalStoreOwnership(t *testing.T) {
	requireShellSnapshot(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	s, tt, _, p, _ := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	path := filepath.Join(t.TempDir(), "parked-original")
	store := shellSnapshotStore(t, s, path)
	x := &shellParkedSnapshotStore{KVSnapshotStore: store, entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	s.SetKVSnapshotOperations(x, kvworkspace.SnapshotFiles{Operations: store, SavePath: func(contract.KVSnapshotFileKind, string) (string, error) { return path, nil }})
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("first original KV document missing")
	}
	d, originalTab := s.currentTab().kv, s.currentTab().id
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	shellAlarmChoose(t, tt, "KV 方法", "Snapshot")
	etcdPublicClick(t, tt, "预览 Snapshot 捕获")
	etcdPublicClick(t, tt, "我确认捕获此原集群的完整后端")
	etcdPublicClick(t, tt, "确认捕获 Snapshot")
	select {
	case <-x.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("original native capture did not complete")
	}
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("neighbor document missing")
	}
	neighbor, activeID := s.currentTab().kv, s.currentTab().id
	close(x.release)
	<-x.returned
	tt.Frame()
	if _, ok := d.SnapshotReceipt(); ok || !d.Busy() {
		t.Fatal("parked capture entered inactive document")
	}
	if _, ok := neighbor.SnapshotReceipt(); ok {
		t.Fatal("parked capture entered active neighbor")
	}
	s.selectTab(originalTab)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	if r, ok := d.SnapshotReceipt(); !ok || !r.Complete || r.Original.Scope.DocumentID != originalTab {
		t.Fatal("original parked source lost")
	}
	captureEtcdPublic(t, s, "snapshot-public-original-parked")
	etcdPublicClick(t, tt, "保存原 backend 文件")
	shellSnapshotSave(t, tt, d, false)
	published, err := os.ReadFile(path)
	if err != nil || len(published) == 0 {
		t.Fatal("original parked Snapshot was not saved", err)
	}
	s.selectTab(activeID)
	s.closeTabID(originalTab)
	tt.Frame()
	etcdPublicClick(t, tt, "放弃本地内容并继续")
	if s.currentTab().id != activeID || s.currentTab().kv != neighbor {
		t.Fatal("inactive Snapshot close changed active document identity")
	}
	if _, err := x.artifact.OpenJournal(); err == nil {
		t.Fatal("closed original Snapshot artifact remains available")
	}
	// Already published user files are outside store terminal cleanup.
	s.CloseMySQLSessions()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if retained, err := os.ReadFile(path); err != nil || !bytes.Equal(retained, published) {
		t.Fatal("terminal cleanup deleted a published user file", err)
	}
}
