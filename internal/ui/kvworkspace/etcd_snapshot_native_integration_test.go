package kvworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/etcd"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/services/snapshotfiles"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func requireSnapshotWorkspace(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PUBLIC_SNAPSHOT_IT") != "1" {
		t.Skip("requires owned public original Snapshot fixture")
	}
}
func snapshotNativeAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatal("original native Snapshot operation did not acknowledge completion")
	}
}
func snapshotNativeConsume(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for d.Busy() || d.FileBusy() {
		s.Consume()
		if time.Now().After(deadline) {
			t.Fatal("original native Snapshot document did not settle", d.Error())
		}
		time.Sleep(time.Millisecond)
	}
}
func snapshotNativeCapture(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.PrepareSnapshot(d)
	if d.snapshot == nil || d.snapshot.confirmation == nil {
		t.Fatal("native original Snapshot confirmation missing", d.Error())
	}
	d.snapshot.confirmation.checked = true
	s.ConfirmSnapshot(d)
	snapshotNativeConsume(t, s, d)
}
func snapshotNativeChooseFile(t *testing.T, s *State, d *Document, kind contract.KVSnapshotFileKind) {
	t.Helper()
	s.SelectSnapshotFile(d, kind)
	snapshotNativeConsume(t, s, d)
	if d.snapshot.fileConfirmation == nil {
		t.Fatal("native original file confirmation missing", d.Error())
	}
}
func snapshotNativePublish(t *testing.T, s *State, d *Document) {
	t.Helper()
	d.snapshot.fileConfirmation.checked = true
	s.ConfirmSnapshotFile(d)
	snapshotNativeConsume(t, s, d)
}
func snapshotNativeStore(t *testing.T, s *State, path string) *snapshotfiles.KVSnapshotStore {
	t.Helper()
	store, err := snapshotfiles.NewKVSnapshotStore(t.TempDir(), 512<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.CloseAll()
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	s.SetSnapshotOperations(store, SnapshotFiles{Operations: store, SavePath: func(contract.KVSnapshotFileKind, string) (string, error) { return path, nil }})
	return store
}
func snapshotNativeBytes(t *testing.T, d *Document) ([]byte, []byte) {
	t.Helper()
	r, ok := d.SnapshotReceipt()
	if !ok || d.snapshot.artifact == nil {
		t.Fatal("original artifact missing", d.Error())
	}
	read := func(open func() (io.ReadCloser, error)) []byte {
		reader, err := open()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		raw, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	backend, journal := read(d.snapshot.artifact.OpenSnapshot), read(d.snapshot.artifact.OpenJournal)
	var frames uint64
	var concatenated bytes.Buffer
	err := etcd.ReadSnapshotJournal(bytes.NewReader(journal), uint64(len(journal)), func(record etcd.SnapshotJournalRecord) error {
		if record.Kind == etcd.SnapshotJournalFrameKind {
			frame := &pb.SnapshotResponse{}
			if err := proto.Unmarshal(record.Raw, frame); err != nil {
				return err
			}
			concatenated.Write(frame.Blob)
			frames++
		}
		return nil
	})
	if err != nil || frames != r.Original.Frames || r.Complete && !bytes.Equal(backend, concatenated.Bytes()) {
		t.Fatal("original backend and complete journal differ", err)
	}
	bhash, jhash := sha256.Sum256(backend), sha256.Sum256(journal)
	if !bytes.Equal(bhash[:], r.SnapshotSHA256) || !bytes.Equal(jhash[:], r.JournalSHA256) || uint64(len(backend)) != r.SnapshotBytes || uint64(len(journal)) != r.JournalBytes {
		t.Fatal("original artifact digest mismatch")
	}
	return backend, journal
}
func snapshotNativeMatch(t *testing.T, path string, want []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, want) {
		t.Fatal("native publication changed complete original bytes", err)
	}
}
func TestEtcdNativeSnapshotWorkspaceBilingualCheckedOriginalFilesAndCompleteRecords(t *testing.T) {
	requireSnapshotWorkspace(t)
	p, secret := workspaceProfile(t)
	p.EtcdOptions.Prefix = "/snapshot-public-browsing-only/"
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			t.Run(language+"/"+strconv.Itoa(size[0]), func(t *testing.T) {
				s, d, m := workspaceNativeState(t, p, secret)
				path := filepath.Join(t.TempDir(), "original-backend.db")
				snapshotNativeStore(t, s, path)
				workspaceInput(s, d, DefaultSnapshotRequest())
				tt := workspaceTester(s, d, size[0], size[1])
				tt.Frame()
				workspaceCapture(t, tt, language, size, "snapshot-inputs")
				workspaceClick(t, tt, "限额 JSON")
				workspaceCapture(t, tt, language, size, "snapshot-input-json")
				workspaceClick(t, tt, "Snapshot 限额表单")
				workspaceClick(t, tt, "预览 Snapshot 捕获")
				if d.snapshot.confirmation == nil || d.snapshot.confirmation.checked || d.Busy() {
					t.Fatal("native source preparation dispatched")
				}
				workspaceCapture(t, tt, language, size, "snapshot-capture-confirmation")
				workspaceClick(t, tt, "我确认捕获此原集群的完整后端")
				workspaceClick(t, tt, "确认捕获 Snapshot")
				snapshotNativeConsume(t, s, d)
				r, ok := d.SnapshotReceipt()
				if !ok || !r.Complete || r.Original.Scope.DocumentID != d.id || !sameEventScope(r.Original.Scope.Original, d.scope) {
					t.Fatal("public capture lost original source", d.Error())
				}
				backend, journal := snapshotNativeBytes(t, d)
				tt.Frame()
				workspaceCapture(t, tt, language, size, "snapshot-source")
				alarmWorkspaceChoose(t, tt, "Snapshot 证据视图", "私有归档校验")
				workspaceCapture(t, tt, language, size, "snapshot-artifact")
				for _, replace := range []bool{false, true} {
					old := bytes.Repeat([]byte("original preserved user backup\x00\xff"), 100)
					if replace {
						if err := os.WriteFile(path, old, 0600); err != nil {
							t.Fatal(err)
						}
					}
					workspaceClick(t, tt, "保存原 backend 文件")
					snapshotNativeConsume(t, s, d)
					tt.Frame()
					check, action, stage := "我确认保存此原文件", "确认保存 Snapshot 文件", "snapshot-new-confirmation"
					if replace {
						check, action, stage = "我确认替换此已检查文件并保留旧备份", "确认替换 Snapshot 文件", "snapshot-replace-confirmation"
					}
					workspaceCapture(t, tt, language, size, stage)
					workspaceClick(t, tt, check)
					workspaceClick(t, tt, action)
					snapshotNativeConsume(t, s, d)
					snapshotNativeMatch(t, path, backend)
					file := d.SnapshotFileReceipts()[len(d.snapshot.files)-1]
					if !file.Published || !file.DirectorySynced || !reflect.DeepEqual(file.Original.Source, r) {
						t.Fatal("native publication proof missing", d.Error())
					}
					if replace {
						snapshotNativeMatch(t, file.DisplacedPath, old)
					}
				}
				alarmWorkspaceChoose(t, tt, "Snapshot 证据视图", "用户文件发布")
				workspaceCapture(t, tt, language, size, "snapshot-file-publication")
				tt.Scroll(float32(size[0]-30), float32(size[1]-60), 0, 1000)
				tt.Frame()
				workspaceCapture(t, tt, language, size, "snapshot-preserved-backup")
				if err := m.Disconnect(p.ID); err != nil {
					t.Fatal(err)
				}
				s.DisconnectProfile(p.ID)
				s.deps.SnapshotFiles.SavePath = func(contract.KVSnapshotFileKind, string) (string, error) { return path + ".journal", nil }
				workspaceClick(t, tt, "保存完整原帧日志")
				snapshotNativeConsume(t, s, d)
				workspaceClick(t, tt, "我确认保存此原文件")
				workspaceClick(t, tt, "确认保存 Snapshot 文件")
				snapshotNativeConsume(t, s, d)
				snapshotNativeMatch(t, path+".journal", journal)
				workspaceCapture(t, tt, language, size, "snapshot-disconnected-journal")
				if _, err := m.ConnectSession(context.Background(), p, secret); err != nil {
					t.Fatal(err)
				}
				s.Recover(d)
				snapshotNativeConsume(t, s, d)
				if d.NeedsRecovery() || !reflect.DeepEqual(*d.snapshot.original, r) || d.scope.Generation <= r.Original.Scope.Original.Generation {
					t.Fatal("public recovery relabeled original capture", d.Error())
				}
				workspaceCapture(t, tt, language, size, "snapshot-pure-recovery")
				archive := path + ".record.json"
				s.SetFiles(Files{SavePath: func(string) (string, error) { return archive, nil }, WriteStream: queries.WriteStream})
				workspaceClick(t, tt, "复制完整 Snapshot 记录")
				want, err := s.SnapshotRecord(d)
				if err != nil || tt.Clipboard() != string(want) {
					t.Fatal("native record copy lost original bytes")
				}
				workspaceClick(t, tt, "导出 Snapshot 记录")
				snapshotNativeConsume(t, s, d)
				snapshotNativeMatch(t, archive, want)
				alarmWorkspaceChoose(t, tt, "Snapshot 证据视图", "完整 Snapshot 记录")
				workspaceCapture(t, tt, language, size, "snapshot-complete-record")
			})
		}
	}
}

type workspaceSnapshotFault struct {
	*workspaceFault
	mode                   string
	calls                  atomic.Int32
	entered, done, release chan struct{}
	frames                 []*pb.SnapshotResponse
}

func (f *workspaceSnapshotFault) Snapshot(request *pb.SnapshotRequest, stream pb.Maintenance_SnapshotServer) error {
	if f.calls.Add(1) != 1 {
		return status.Error(codes.Internal, "original Snapshot replayed")
	}
	defer close(f.done)
	source, err := pb.NewMaintenanceClient(f.upstream).Snapshot(f.context(stream.Context()), request)
	if err != nil {
		return err
	}
	for {
		frame, err := source.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		frame.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
		f.frames = append(f.frames, proto.Clone(frame).(*pb.SnapshotResponse))
		if err := stream.Send(frame); err != nil {
			return err
		}
	}
	close(f.entered)
	if f.mode == "lost" {
		return status.Error(codes.Unavailable, "actual original Snapshot EOF withheld")
	}
	if f.mode == "parked" {
		select {
		case <-f.release:
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
	if f.mode == "cancel" || f.mode == "close" {
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	return nil
}
func snapshotWorkspaceProxy(t *testing.T, p connections.Profile, mode string, key []byte) (connections.Profile, *workspaceSnapshotFault) {
	t.Helper()
	var fault *workspaceSnapshotFault
	proxy, _ := workspaceProxyServices(t, p, key, "lost", nil, func(base *workspaceFault) pb.MaintenanceServer {
		fault = &workspaceSnapshotFault{workspaceFault: base, mode: mode, entered: make(chan struct{}), done: make(chan struct{}), release: make(chan struct{})}
		return fault
	})
	return proxy, fault
}
func TestEtcdNativeSnapshotWorkspaceActualFaultJournalParkingCancellationAndClose(t *testing.T) {
	requireSnapshotWorkspace(t)
	p, secret := workspaceProfile(t)
	for _, mode := range []string{"lost", "parked", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			proxy, fault := snapshotWorkspaceProxy(t, p, mode, nil)
			s, d, _ := workspaceNativeState(t, proxy, secret)
			path := filepath.Join(t.TempDir(), "failure-journal")
			snapshotNativeStore(t, s, path)
			workspaceInput(s, d, DefaultSnapshotRequest())
			s.PrepareSnapshot(d)
			d.snapshot.confirmation.checked = true
			s.ConfirmSnapshot(d)
			snapshotNativeAwait(t, fault.entered)
			if mode == "close" {
				original := d.snapshot
				s.Close(d)
				snapshotNativeAwait(t, fault.done)
				if s.documents[d.id] != nil || original.artifact != nil || fault.calls.Load() != 1 {
					t.Fatal("close retained or replayed original Snapshot")
				}
				return
			}
			if mode == "cancel" {
				s.RequestCancel(d)
			}
			if mode == "parked" {
				s.deps.Active = func(*Document) bool { return false }
				close(fault.release)
				snapshotNativeAwait(t, d.job.finished)
				s.Consume()
				if d.snapshot.original != nil || d.job == nil {
					t.Fatal("parked original source entered inactive document")
				}
				s.deps.Active = nil
			}
			snapshotNativeConsume(t, s, d)
			snapshotNativeAwait(t, fault.done)
			r, ok := d.SnapshotReceipt()
			if mode == "cancel" {
				// Source cancellation does not complete independent disk verification.
				// Preserve native evidence without granting authority over an unverified file.
				if !ok || !r.Original.Ended || !r.Original.Canceled || r.Stored || r.Complete || d.snapshot.artifact != nil || r.StorageError == "" || fault.calls.Load() != 1 {
					t.Fatal("cancelled native source gained artifact authority or lost its original receipt", r, d.Error())
				}
				s.SelectSnapshotFile(d, contract.KVSnapshotJournalFile)
				if d.FileBusy() {
					t.Fatal("cancelled unverified storage granted journal publication")
				}
				d.snapshot.recordView = 1
				snapshotBilingualCapture(t, s, d, "snapshot-canceled-storage", false)
				d.snapshot.recordView = 3
				snapshotBilingualCapture(t, s, d, "snapshot-canceled-record", false)
				return
			}
			if !ok || mode == "parked" && !r.Complete || mode != "parked" && r.Complete || !r.Original.JournalComplete || !r.FilesSynced || fault.calls.Load() != 1 {
				t.Fatal("actual source fault lost complete native journal", d.Error())
			}
			_, journal := snapshotNativeBytes(t, d)
			var index int
			err := etcd.ReadSnapshotJournal(bytes.NewReader(journal), uint64(len(journal)), func(record etcd.SnapshotJournalRecord) error {
				if record.Kind == etcd.SnapshotJournalFrameKind {
					frame := &pb.SnapshotResponse{}
					if err := proto.Unmarshal(record.Raw, frame); err != nil {
						return err
					}
					if index >= len(fault.frames) || !proto.Equal(frame, fault.frames[index]) {
						return errors.New("original frame extension or bytes changed")
					}
					index++
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "lost" {
				d.snapshot.recordView = 0
				snapshotBilingualCapture(t, s, d, "snapshot-failure-source", false)
				d.snapshot.recordView = 1
				snapshotBilingualCapture(t, s, d, "snapshot-failure-artifact", false)
			} else {
				d.snapshot.recordView = 0
				snapshotBilingualCapture(t, s, d, "snapshot-parked-source", false)
			}
			snapshotNativeChooseFile(t, s, d, contract.KVSnapshotJournalFile)
			snapshotNativePublish(t, s, d)
			snapshotNativeMatch(t, path, journal)
			d.snapshot.recordView = 2
			if mode == "lost" {
				snapshotBilingualCapture(t, s, d, "snapshot-failure-journal", false)
				d.snapshot.recordView = 3
				snapshotBilingualCapture(t, s, d, "snapshot-failure-record", false)
			} else {
				snapshotBilingualCapture(t, s, d, "snapshot-parked-file", false)
			}
			if mode != "parked" {
				s.SelectSnapshotFile(d, contract.KVSnapshotBackendFile)
				if d.FileBusy() {
					t.Fatal("failure journal granted backend save authority")
				}
			}
		})
	}
}
func TestEtcdNativeSnapshotWorkspaceActualUnknownPutRemainsIndependentAcrossCaptureFilesAndRecovery(t *testing.T) {
	requireSnapshotWorkspace(t)
	p, secret := workspaceProfile(t)
	key := workspaceKey(t)
	proxy, fault := snapshotWorkspaceProxy(t, p, "complete", key)
	s, d, m := workspaceNativeState(t, proxy, secret)
	path := filepath.Join(t.TempDir(), "original-backend")
	snapshotNativeStore(t, s, path)
	write := workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("original native unknown")})
	workspaceInput(s, d, write)
	s.Run(d)
	s.Confirm(d, true)
	snapshotNativeConsume(t, s, d)
	snapshotNativeAwait(t, fault.workspaceFault.done)
	unknown := d.unknown
	if unknown == nil || !unknown.Result.WriteOutcomeUnknown || fault.receipt == nil {
		t.Fatal("original processed Put ACK fault missing")
	}
	s.Recover(d)
	snapshotNativeConsume(t, s, d)
	workspaceInput(s, d, DefaultSnapshotRequest())
	snapshotNativeCapture(t, s, d)
	backend, _ := snapshotNativeBytes(t, d)
	snapshotNativeChooseFile(t, s, d, contract.KVSnapshotBackendFile)
	snapshotNativePublish(t, s, d)
	snapshotNativeMatch(t, path, backend)
	if _, err := m.ConnectSession(context.Background(), proxy, secret); err != nil {
		t.Fatal(err)
	}
	s.DisconnectProfile(p.ID)
	s.Recover(d)
	snapshotNativeConsume(t, s, d)
	if d.unknown != unknown || !d.hasUnknownWrites() || fault.calls.Load() != 1 || fault.workspaceFault.calls.Load() != 1 {
		t.Fatal("Snapshot/recovery settled or replayed earlier native Put")
	}
	d.selectedSnapshot, d.selectedUnknown = false, true
	snapshotBilingualCapture(t, s, d, "snapshot-earlier-unknown", false)
	workspaceInput(s, d, write)
	s.Run(d)
	if d.confirmation != nil || d.Busy() {
		t.Fatal("Snapshot granted another unknown document write")
	}
}
