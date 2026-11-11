package dbruntime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/etcd"
	"github.com/nyakang/nyadb/internal/services/snapshotfiles"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func snapshotArtifactProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_SNAPSHOT_ARTIFACT_IT") != "1" {
		t.Skip("requires owned original Snapshot artifact fixture")
	}
	return snapshotRuntimeProfile(t)
}
func snapshotArtifactStore(t *testing.T) (*snapshotfiles.KVSnapshotStore, string) {
	t.Helper()
	directory := t.TempDir()
	s, e := snapshotfiles.NewKVSnapshotStore(directory, 128<<20)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	})
	return s, directory
}
func snapshotArtifactOriginalFiles(t *testing.T, a contract.KVSnapshotArtifact, frames []*pb.SnapshotResponse) {
	t.Helper()
	r := a.Receipt()
	snapshot, e := a.OpenSnapshot()
	if e != nil {
		t.Fatal(e)
	}
	defer snapshot.Close()
	journal, e := a.OpenJournal()
	if e != nil {
		t.Fatal(e)
	}
	defer journal.Close()
	var count uint64
	e = etcd.ReadSnapshotJournal(journal, r.JournalBytes, func(record etcd.SnapshotJournalRecord) error {
		if record.Kind != etcd.SnapshotJournalFrameKind {
			return nil
		}
		frame := &pb.SnapshotResponse{}
		if e := proto.Unmarshal(record.Raw, frame); e != nil {
			return e
		}
		if frames != nil && (int(count) >= len(frames) || !proto.Equal(frame, frames[count])) {
			t.Fatal("artifact frame changed from actual original source")
		}
		if r.Original.Completed {
			b := make([]byte, len(frame.Blob))
			if _, e := io.ReadFull(snapshot, b); e != nil {
				return e
			}
			if !bytes.Equal(b, frame.Blob) {
				t.Fatal("immutable output differs from original full frame")
			}
		}
		count++
		return nil
	})
	if e != nil || count != r.Original.Frames {
		t.Fatal("original journal invalid", count, e)
	}
	if r.Complete {
		var extra [1]byte
		if n, e := snapshot.Read(extra[:]); n != 0 || e != io.EOF {
			t.Fatal("artifact output has trailing bytes", n, e)
		}
	}
	second, e := a.OpenSnapshot()
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	sum := sha256.New()
	n, e := io.Copy(sum, second)
	if e != nil || uint64(n) != r.SnapshotBytes || !bytes.Equal(sum.Sum(nil), r.SnapshotSHA256) {
		t.Fatal("immutable original file digest differs", e)
	}
}
func TestEtcdNativeSnapshotArtifactOriginalCompleteFilesPureRecoveryAndExactCleanup(t *testing.T) {
	p, secret := snapshotArtifactProfile(t)
	p.EtcdOptions.Prefix = "/snapshot-artifact-original-browse/"
	m := etcdRuntimeManager(t)
	if e := m.Connect(context.Background(), p, secret); e != nil {
		t.Fatal(e)
	}
	lease := etcdRuntimeReserve(t, m, 901)
	store, directory := snapshotArtifactStore(t)
	preparation, e := store.Prepare(lease, snapshotRuntimeRequest())
	if e != nil {
		t.Fatal(e)
	}
	scope := preparation.Scope()
	artifact, r, e := store.Capture(context.Background(), preparation)
	if e != nil || artifact == nil || !r.Stored || !r.FilesSynced || !r.Complete || !reflect.DeepEqual(r.Original.Scope, scope) {
		t.Fatal("complete original disk capture failed", r, e)
	}
	snapshotArtifactOriginalFiles(t, artifact, nil)
	if _, _, e := store.Capture(context.Background(), preparation); !errors.Is(e, contract.ErrKVPlanUsed) {
		t.Fatal("archive replayed native Snapshot", e)
	}
	publication, e := m.ConnectSession(context.Background(), p, secret)
	if e != nil {
		t.Fatal(e)
	}
	provider, _ := m.KVRequests(p.ID)
	recovery, e := provider.RecoverKV(lease)
	if e != nil || recovery.SnapshotResult == nil || !reflect.DeepEqual(recovery.SnapshotResult.Scope, scope) || publication.Generation() <= scope.Original.Generation {
		t.Fatal("pure recovery relabeled complete original artifact", e)
	}
	defer recovery.Lease.Close()
	snapshotArtifactOriginalFiles(t, artifact, nil)
	if !reflect.DeepEqual(artifact.Receipt().Original.Scope, scope) {
		t.Fatal("recovery modified original artifact scope")
	}
	if e := artifact.Close(); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(directory)
	if e != nil || len(entries) != 0 {
		t.Fatal("exact original artifact cleanup failed", entries, e)
	}
}
func TestEtcdNativeSnapshotArtifactActualSourceFaultsCancellationParkingAndRetainedEvidence(t *testing.T) {
	p, secret := snapshotArtifactProfile(t)
	for _, mode := range []string{"lost", "foreign", "cancel", "close", "store-close", "replace", "disconnect", "extensions", "parked-complete"} {
		t.Run(mode, func(t *testing.T) {
			relay := mode
			if mode == "store-close" {
				relay = "cancel"
			}
			proxy, fault := snapshotRuntimeProxy(t, p, relay, nil)
			m := etcdRuntimeManager(t)
			publication, e := m.ConnectSession(context.Background(), proxy, secret)
			if e != nil {
				t.Fatal(e)
			}
			lease := etcdRuntimeReserve(t, m, 902)
			store, directory := snapshotArtifactStore(t)
			preparation, e := store.Prepare(lease, snapshotRuntimeRequest())
			if e != nil || fault.calls.Load() != 0 {
				t.Fatal("artifact preparation dispatched", e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completed struct {
				a contract.KVSnapshotArtifact
				r contract.KVSnapshotCapture
				e error
			}
			done := make(chan completed, 1)
			go func() { a, r, e := store.Capture(ctx, preparation); done <- completed{a, r, e} }()
			select {
			case <-fault.entered:
			case early := <-done:
				t.Fatal("original artifact source not reached", early.e)
			}
			switch mode {
			case "cancel":
				cancel()
			case "close":
				lease.Close()
			case "store-close":
				if e := store.Close(); e != nil {
					t.Fatal(e)
				}
			case "replace":
				if _, e := m.ConnectSession(context.Background(), proxy, secret); e != nil {
					t.Fatal(e)
				}
			case "disconnect":
				if e := m.Disconnect(p.ID); e != nil {
					t.Fatal(e)
				}
			case "parked-complete":
				other := p
				other.ID++
				if _, e := m.ConnectSession(context.Background(), other, secret); e != nil {
					t.Fatal(e)
				}
				close(fault.release)
			}
			result := <-done
			<-fault.done
			r := result.r
			if fault.calls.Load() != 1 || !fault.eof || !r.Original.Ended || r.Original.Scope.DocumentID != 902 || r.Original.Scope.Original.Generation != publication.Generation() {
				t.Fatal("artifact source/identity changed", r, result.e)
			}
			if mode == "extensions" || mode == "parked-complete" {
				if result.e != nil || result.a == nil || !r.Complete || !r.Stored {
					t.Fatal("original complete source not retained", r, result.e)
				}
				frames := make([]*pb.SnapshotResponse, len(fault.frames))
				for i, frame := range fault.frames {
					frames[i] = proto.Clone(frame).(*pb.SnapshotResponse)
					if mode == "extensions" {
						frames[i].ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
					}
					if mode == "foreign" && i == 0 {
						frames[i].Header = &pb.ResponseHeader{ClusterId: 91}
					}
				}
				snapshotArtifactOriginalFiles(t, result.a, frames)
			} else {
				if result.e == nil || r.Complete || r.Original.Completed {
					t.Fatal("failed original source became a complete archive", r, result.e)
				}
				if mode == "lost" || mode == "foreign" {
					if result.a == nil || !r.Stored || r.StorageError != "" {
						t.Fatal("complete original failure evidence not retained", r, result.e)
					}
					frames := make([]*pb.SnapshotResponse, len(fault.frames))
					for i, frame := range fault.frames {
						frames[i] = proto.Clone(frame).(*pb.SnapshotResponse)
						if mode == "extensions" {
							frames[i].ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
						}
						if mode == "foreign" && i == 0 {
							frames[i].Header = &pb.ResponseHeader{ClusterId: 91}
						}
					}
					snapshotArtifactOriginalFiles(t, result.a, frames)
				}
			}
			if result.a != nil {
				if e := result.a.Close(); e != nil {
					t.Fatal(e)
				}
			}
			entries, e := os.ReadDir(directory)
			if e != nil || len(entries) != 0 {
				t.Fatal("own original files survived cleanup", entries, e)
			}
		})
	}
}
func TestEtcdNativeSnapshotArtifactCompleteFilesNeverSettleEarlierActualUnknownWrite(t *testing.T) {
	p, secret := snapshotArtifactProfile(t)
	key := []byte("/snapshot-artifact/" + t.Name())
	proxy, fault := snapshotRuntimeProxy(t, p, "put-lost", key)
	m := etcdRuntimeManager(t)
	if e := m.Connect(context.Background(), proxy, secret); e != nil {
		t.Fatal(e)
	}
	lease := etcdRuntimeReserve(t, m, 903)
	request := etcdRuntimeRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("original")})
	plan, _ := lease.Prepare(request)
	unknown, e := lease.Execute(context.Background(), plan)
	<-fault.etcdRuntimeFault.done
	if e == nil || !unknown.WriteOutcomeUnknown || fault.etcdRuntimeFault.receipt == nil {
		t.Fatal("did not lose actual committed Put acknowledgement")
	}
	provider, _ := m.KVRequests(p.ID)
	recovery, e := provider.RecoverKV(lease)
	if e != nil || !recovery.Last.WriteOutcomeUnknown {
		t.Fatal("original unknown receipt lost", e)
	}
	fresh := recovery.Lease
	defer fresh.Close()
	store, _ := snapshotArtifactStore(t)
	preparation, e := store.Prepare(fresh, snapshotRuntimeRequest())
	if e != nil {
		t.Fatal(e)
	}
	artifact, r, e := store.Capture(context.Background(), preparation)
	if e != nil || artifact == nil || !r.Complete || !recovery.Last.WriteOutcomeUnknown || fault.calls.Load() != 1 {
		t.Fatal("complete artifact settled original unknown write", e)
	}
	snapshotArtifactOriginalFiles(t, artifact, fault.frames)
	read, _ := fresh.Prepare(etcdRuntimeRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
	later, e := fresh.Execute(context.Background(), read)
	if e != nil || later.Reply == nil || len(later.Reply.Entries) != 1 || string(later.Reply.Entries[0].Value) != "later-independent" || !recovery.Last.WriteOutcomeUnknown {
		t.Fatal("later independent read settled earlier unknown write", e)
	}
	blocked, _ := fresh.Prepare(request)
	if _, e := fresh.Execute(context.Background(), blocked); !errors.Is(e, contract.ErrKVWriteUnknown) || fault.etcdRuntimeFault.calls.Load() != 1 {
		t.Fatal("disk capture reopened original write barrier", e)
	}
}
func TestEtcdNativeSnapshotArtifactRestrictedIdentityRetainsExactNativeDenialJournal(t *testing.T) {
	p, secret := snapshotArtifactProfile(t)
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" {
		// This fixture mode has no restricted identity. Its required raw/session
		// cases separately enable real authentication and validate native denials.
		p.Username = ""
		p.EtcdOptions.Auth = "none"
	} else {
		p.Username = "restricted"
		if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
			p.EtcdOptions.ClientCertFile = os.Getenv("NYADB_ETCD_ACCESS_CERT")
			p.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_ACCESS_KEY")
		}
	}
	m := etcdRuntimeManager(t)
	if e := m.Connect(context.Background(), p, secret); e != nil {
		t.Fatal(e)
	}
	lease := etcdRuntimeReserve(t, m, 904)
	store, _ := snapshotArtifactStore(t)
	preparation, e := store.Prepare(lease, snapshotRuntimeRequest())
	if e != nil {
		t.Fatal(e)
	}
	artifact, r, e := store.Capture(context.Background(), preparation)
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" {
		if e != nil || artifact == nil || !r.Complete {
			t.Fatal("original anonymous disk capture failed", e)
		}
	} else {
		expected := "PermissionDenied"
		if strings.HasPrefix(lease.Scope().Version, "3.5.") {
			expected = "Unknown"
		}
		if e == nil || artifact == nil || !r.Stored || r.Complete || r.Original.Completed || len(r.Original.NativeStatus) == 0 || r.SnapshotBytes != 0 || r.Original.NativeCode != expected || lease.CheckKVLease() != nil {
			t.Fatal("native restricted original denial changed", r, e)
		}
	}
	journal, e := artifact.OpenJournal()
	if e != nil {
		t.Fatal(e)
	}
	defer journal.Close()
	var terminal etcd.SnapshotJournalTerminal
	e = etcd.ReadSnapshotJournal(journal, r.JournalBytes, func(record etcd.SnapshotJournalRecord) error {
		if record.Kind == etcd.SnapshotJournalTerminalKind {
			return json.Unmarshal(record.Raw, &terminal)
		}
		return nil
	})
	if e != nil || !bytes.Equal(terminal.NativeStatus, r.Original.NativeStatus) {
		t.Fatal("complete native denial journal changed", e)
	}
}
