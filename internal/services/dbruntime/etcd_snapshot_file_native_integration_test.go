package dbruntime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func snapshotFileEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_SNAPSHOT_FILE_IT") != "1" {
		t.Skip("requires owned original Snapshot file publication fixture")
	}
}
func snapshotFileRead(t *testing.T, a contract.KVSnapshotArtifact, journal bool) []byte {
	t.Helper()
	var reader io.ReadCloser
	var err error
	if journal {
		reader, err = a.OpenJournal()
	} else {
		reader, err = a.OpenSnapshot()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	b, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func snapshotFileMatches(t *testing.T, path string, b []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, b) {
		t.Fatal("published file differs from complete original bytes", path, err)
	}
}
func TestEtcdNativeSnapshotFileOriginalBackendJournalNewAndReplacementAfterDisconnect(t *testing.T) {
	snapshotFileEnabled(t)
	p, secret := snapshotArtifactProfile(t)
	p.EtcdOptions.Prefix = "/snapshot-file-original-browsing-only/"
	proxy, fault := snapshotRuntimeProxy(t, p, "extensions", nil)
	m := etcdRuntimeManager(t)
	if e := m.Connect(context.Background(), proxy, secret); e != nil {
		t.Fatal(e)
	}
	lease := etcdRuntimeReserve(t, m, 905)
	store, _ := snapshotArtifactStore(t)
	capture, e := store.Prepare(lease, snapshotRuntimeRequest())
	if e != nil {
		t.Fatal(e)
	}
	a, original, e := store.Capture(context.Background(), capture)
	if e != nil || a == nil || !original.Complete {
		t.Fatal(e)
	}
	frames := make([]*pb.SnapshotResponse, len(fault.frames))
	for i, frame := range fault.frames {
		frames[i] = proto.Clone(frame).(*pb.SnapshotResponse)
		frames[i].ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
	}
	snapshotArtifactOriginalFiles(t, a, frames)
	snapshot, journal := snapshotFileRead(t, a, false), snapshotFileRead(t, a, true)
	// Saved original files are independent of current public session publication.
	if e := m.Disconnect(p.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := m.ConnectSession(context.Background(), proxy, secret); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []contract.KVSnapshotFileKind{contract.KVSnapshotBackendFile, contract.KVSnapshotJournalFile} {
		for _, replace := range []bool{false, true} {
			t.Run(string(kind)+map[bool]string{false: "-new", true: "-replace"}[replace], func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "original-file")
				old := bytes.Repeat([]byte("larger original old backup\x00\xff"), 100000)
				if replace {
					if e := os.WriteFile(path, old, 0600); e != nil {
						t.Fatal(e)
					}
				}
				plan, e := store.PrepareFile(context.Background(), a, contract.KVSnapshotFileChoice{Path: path, Kind: kind, Replace: replace})
				if e != nil {
					t.Fatal(e)
				}
				inspection := plan.Observation()
				inspection.Source.Original.Scope.Original.Prefix[0] = 'x'
				result, e := store.PublishFile(context.Background(), plan)
				if e != nil || !result.Published || !result.DestinationMatched || !result.PathMatched || !result.FileSynced || !result.DirectorySynced || !result.Ended || !reflect.DeepEqual(result.Original.Source, original) {
					t.Fatal(result, e)
				}
				want := snapshot
				if kind == contract.KVSnapshotJournalFile {
					want = journal
				}
				snapshotFileMatches(t, path, want)
				sum := sha256.Sum256(want)
				if !bytes.Equal(result.SHA256, sum[:]) || result.BytesWritten != uint64(len(want)) {
					t.Fatal("output proof changed", result)
				}
				if replace {
					if !result.Displaced || !result.DisplacedRetained || result.DisplacedPath == "" {
						t.Fatal(result)
					}
					snapshotFileMatches(t, result.DisplacedPath, old)
				}
				if e := plan.Close(); e != nil {
					t.Fatal(e)
				}
				if replace {
					snapshotFileMatches(t, result.DisplacedPath, old)
				}
				if _, e := store.PublishFile(context.Background(), plan); !errors.Is(e, contract.ErrKVPlanUsed) {
					t.Fatal("file plan reused", e)
				}
			})
		}
	}
	if fault.calls.Load() != 1 {
		t.Fatal("file saving/reconnect replayed native Snapshot", fault.calls.Load())
	}
	if !reflect.DeepEqual(a.Receipt(), original) {
		t.Fatal("file saving changed native original receipt")
	}
}
func TestEtcdNativeSnapshotFileActualUnknownPutBarrierSurvivesOriginalFilePublication(t *testing.T) {
	snapshotFileEnabled(t)
	p, secret := snapshotArtifactProfile(t)
	key := []byte("/snapshot-file/" + t.Name())
	proxy, fault := snapshotRuntimeProxy(t, p, "put-lost", key)
	m := etcdRuntimeManager(t)
	if e := m.Connect(context.Background(), proxy, secret); e != nil {
		t.Fatal(e)
	}
	lease := etcdRuntimeReserve(t, m, 906)
	request := etcdRuntimeRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("original")})
	write, _ := lease.Prepare(request)
	unknown, e := lease.Execute(context.Background(), write)
	<-fault.etcdRuntimeFault.done
	if e == nil || !unknown.WriteOutcomeUnknown || fault.etcdRuntimeFault.receipt == nil {
		t.Fatal("actual committed write fault missing")
	}
	provider, _ := m.KVRequests(p.ID)
	recovery, e := provider.RecoverKV(lease)
	if e != nil || !recovery.Last.WriteOutcomeUnknown {
		t.Fatal(e)
	}
	fresh := recovery.Lease
	defer fresh.Close()
	store, _ := snapshotArtifactStore(t)
	capture, e := store.Prepare(fresh, snapshotRuntimeRequest())
	if e != nil {
		t.Fatal(e)
	}
	a, original, e := store.Capture(context.Background(), capture)
	if e != nil || a == nil || !original.Complete {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "original.db")
	plan, e := store.PrepareFile(context.Background(), a, contract.KVSnapshotFileChoice{Path: path, Kind: contract.KVSnapshotBackendFile})
	if e != nil {
		t.Fatal(e)
	}
	defer plan.Close()
	result, e := store.PublishFile(context.Background(), plan)
	if e != nil || !result.Published || fault.calls.Load() != 1 || !recovery.Last.WriteOutcomeUnknown {
		t.Fatal(result, e)
	}
	snapshotFileMatches(t, path, snapshotFileRead(t, a, false))
	blocked, _ := fresh.Prepare(request)
	if _, e := fresh.Execute(context.Background(), blocked); !errors.Is(e, contract.ErrKVWriteUnknown) || fault.etcdRuntimeFault.calls.Load() != 1 {
		t.Fatal("file publication settled unknown write", e)
	}
}
func TestEtcdNativeSnapshotFileIncompleteBackendRejectedCompleteFailureJournalExported(t *testing.T) {
	snapshotFileEnabled(t)
	p, secret := snapshotArtifactProfile(t)
	proxy, fault := snapshotRuntimeProxy(t, p, "lost", nil)
	m := etcdRuntimeManager(t)
	if e := m.Connect(context.Background(), proxy, secret); e != nil {
		t.Fatal(e)
	}
	lease := etcdRuntimeReserve(t, m, 907)
	store, _ := snapshotArtifactStore(t)
	capture, e := store.Prepare(lease, snapshotRuntimeRequest())
	if e != nil {
		t.Fatal(e)
	}
	a, original, e := store.Capture(context.Background(), capture)
	if e == nil || a == nil || original.Complete || !original.Stored {
		t.Fatal("original incomplete source not retained", original, e)
	}
	snapshotArtifactOriginalFiles(t, a, fault.frames)
	dir := t.TempDir()
	plan, e := store.PrepareFile(context.Background(), a, contract.KVSnapshotFileChoice{Path: filepath.Join(dir, "partial-backend"), Kind: contract.KVSnapshotBackendFile})
	if e == nil || plan != nil {
		t.Fatal("partial backend acquired file publication authority", e)
	}
	if entries, e := os.ReadDir(dir); e != nil || len(entries) != 0 {
		t.Fatal("partial artifact touched chosen directory", entries, e)
	}
	journalPath := filepath.Join(dir, "original-failure-journal")
	plan, e = store.PrepareFile(context.Background(), a, contract.KVSnapshotFileChoice{Path: journalPath, Kind: contract.KVSnapshotJournalFile})
	if e != nil {
		t.Fatal("complete original failure journal rejected", e)
	}
	defer plan.Close()
	result, e := store.PublishFile(context.Background(), plan)
	if e != nil || !result.Published || result.Original.Source.Complete || !result.Original.Source.Original.JournalComplete {
		t.Fatal(result, e)
	}
	snapshotFileMatches(t, journalPath, snapshotFileRead(t, a, true))
	if fault.calls.Load() != 1 {
		t.Fatal("incomplete source retried Snapshot")
	}
}
