package etcd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func snapshotSessionNative(t *testing.T) *Session {
	t.Helper()
	if os.Getenv("NYADB_ETCD_SNAPSHOT_SESSION_IT") != "1" {
		t.Skip("requires owned original Snapshot session fixture")
	}
	return nativeSession(t)
}
func snapshotNativeRequest() contract.KVSnapshotRequest {
	return contract.KVSnapshotRequest{BodyBytes: 32 << 20, JournalBytes: 64 << 20, Frames: 4096}
}
func snapshotProxySession(t *testing.T, admin *Client, mode string) (*Session, *snapshotNativeFault) {
	t.Helper()
	c, f := snapshotNativeProxy(t, admin, mode)
	p, secret := sessionProfile(t)
	u, _ := url.Parse(c.config.Endpoint)
	p.Host = u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	p.Port = uint16(port)
	p.EtcdOptions.Prefix = "/snapshot-original-browse-only/"
	s := newSession(p, secret, c)
	t.Cleanup(func() { s.Close() })
	return s, f
}
func TestEtcdNativeSnapshotSessionOriginalScopeSameLeaseFullFramesAndPureRecovery(t *testing.T) {
	s := snapshotSessionNative(t)
	var opens atomic.Int32
	originalOpener := s.opener
	s.opener = func(ctx context.Context, p connections.Profile, secret string) (*Client, error) {
		opens.Add(1)
		return originalOpener(ctx, p, secret)
	}
	lease := reserveNativeKV(t, s, 701)
	f, ok := lease.(contract.KVSnapshots)
	if !ok {
		t.Fatal("original Snapshot facet absent")
	}
	for _, ok := range []bool{hasSnapshotAuth(lease), hasSnapshotBrowser(lease), hasSnapshotMaintenance(lease), hasSnapshotMembers(lease)} {
		if !ok {
			t.Fatal("Snapshot dropped an existing original facet")
		}
	}
	p, err := f.PrepareSnapshot(snapshotNativeRequest())
	if err != nil || opens.Load() != 0 || p.Scope().DocumentID != 701 {
		t.Fatal("pure Snapshot preparation lost original document", err)
	}
	if err = lease.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	key := nativeKey(t)
	nativeKVDo(t, lease, &pb.PutRequest{Key: key, Value: bytes.Repeat([]byte{0, 255, 31, 9}, 16384)})
	ordinary := lease.LastResult().Clone()
	defer func() { admin := nativeClient(t); nativeDo(t, admin, &pb.DeleteRangeRequest{Key: key}) }()
	var body, journal bytes.Buffer
	r, err := f.ExecuteSnapshot(context.Background(), p, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal})
	if err != nil || !r.Completed || !r.SourceComplete || !r.EOF || !r.JournalComplete || r.Scope.Original.Generation != s.Generation() || r.Scope.DocumentID != 701 || opens.Load() != 1 || !bytes.Equal(lease.LastResult().Native, ordinary.Native) {
		t.Fatal("original Snapshot used another transport/scope or overwrote KV receipt", err)
	}
	raw := lease.(*kvLease)
	if raw.client.LastSnapshot().Ended || s.snapshotRetainedBytes != raw.snapshotCharged || raw.snapshotCharged <= 0 {
		t.Fatal("duplicate native cache or metadata ownership lost")
	}
	read := bytes.NewReader(body.Bytes())
	var frames uint64
	err = ReadSnapshotJournal(bytes.NewReader(journal.Bytes()), uint64(journal.Len()), func(record SnapshotJournalRecord) error {
		if record.Kind != SnapshotJournalFrameKind {
			return nil
		}
		v := &pb.SnapshotResponse{}
		if e := proto.Unmarshal(record.Raw, v); e != nil {
			return e
		}
		if v.Header != nil {
			t.Fatal("nil native frame header borrowed Status")
		}
		b := make([]byte, len(v.Blob))
		if _, e := io.ReadFull(read, b); e != nil {
			return e
		}
		if !bytes.Equal(b, v.Blob) {
			t.Fatal("original snapshot output differs from complete native frame")
		}
		frames++
		return nil
	})
	if err != nil || frames != r.Frames || read.Len() != 0 || r.SnapshotBytesWritten != uint64(body.Len()) {
		t.Fatal("complete original journal/file bytes changed", err)
	}
	foreign := reserveNativeKV(t, s, 702)
	if _, e := foreign.(contract.KVSnapshots).ExecuteSnapshot(context.Background(), p, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal}); !errors.Is(e, contract.ErrKVPlanUsed) || opens.Load() != 1 {
		t.Fatal("foreign document Snapshot plan opened a transport")
	}
	recovery, e := s.RecoverKV(lease)
	if e != nil || recovery.SnapshotResult == nil || !recovery.SnapshotResult.Completed || recovery.SnapshotResult.Scope.DocumentID != 701 || opens.Load() != 1 || s.snapshotRetainedBytes != 0 || raw.LastSnapshotResult().Ended {
		t.Fatal("recovery performed Snapshot IO or lost complete original receipt", e)
	}
	defer recovery.Lease.Close()
	if _, e := recovery.Lease.(contract.KVSnapshots).ExecuteSnapshot(context.Background(), p, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal}); !errors.Is(e, contract.ErrKVPlanUsed) {
		t.Fatal("recovery restored old plan authority")
	}
}
func hasSnapshotAuth(l contract.KVRequestLease) bool {
	_, ok := l.(contract.KVAuthManagement)
	return ok
}
func hasSnapshotBrowser(l contract.KVRequestLease) bool {
	_, ok := l.(contract.KVLeaseBrowsing)
	return ok
}
func hasSnapshotMaintenance(l contract.KVRequestLease) bool {
	_, ok := l.(contract.KVMaintenance)
	return ok
}
func hasSnapshotMembers(l contract.KVRequestLease) bool { _, ok := l.(contract.KVMembers); return ok }
func TestEtcdNativeSnapshotSessionActualSourceFaultsOriginalCloseAndOutputAcknowledgement(t *testing.T) {
	snapshotSessionNative(t)
	admin := nativeClient(t)
	for _, mode := range []string{"lost-eof", "cancel", "close", "extensions", "body-partial", "journal-partial", "writer-close"} {
		t.Run(mode, func(t *testing.T) {
			relayMode := mode
			if mode == "body-partial" || mode == "journal-partial" || mode == "writer-close" {
				relayMode = "normal"
			}
			s, fault := snapshotProxySession(t, admin, relayMode)
			lease := reserveNativeKV(t, s, 703)
			if e := lease.Preconnect(context.Background()); e != nil {
				t.Fatal(e)
			}
			facet := lease.(contract.KVSnapshots)
			p, _ := facet.PrepareSnapshot(snapshotNativeRequest())
			var body, journal bytes.Buffer
			outputs := contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal}
			short := &snapshotFailWriter{limit: 3}
			blockedWriter := &snapshotBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
			if mode == "writer-close" {
				outputs.Snapshot = blockedWriter
			}
			if mode == "body-partial" {
				outputs.Snapshot = short
			}
			if mode == "journal-partial" {
				outputs.Journal = short
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completed struct {
				r contract.KVSnapshotResult
				e error
			}
			done := make(chan completed, 1)
			go func() { r, e := facet.ExecuteSnapshot(ctx, p, outputs); done <- completed{r, e} }()
			if mode == "writer-close" {
				select {
				case <-blockedWriter.entered:
				case early := <-done:
					t.Fatal("original writer not reached", early.e)
				}
				closed := make(chan struct{})
				go func() { lease.Close(); close(closed) }()
				select {
				case <-closed:
					t.Fatal("original close preceded writer acknowledgement")
				case <-time.After(25 * time.Millisecond):
				}
				close(blockedWriter.release)
				<-closed
			}
			if mode == "cancel" || mode == "close" {
				select {
				case <-fault.entered:
				case early := <-done:
					t.Fatal("original source fault not reached", early.e)
				}
				if mode == "cancel" {
					cancel()
				} else {
					lease.Close()
				}
			}
			result := <-done
			r, e := result.r, result.e
			if mode == "extensions" {
				if e != nil || !r.Completed {
					t.Fatal("native extensions lost", e)
				}
			} else {
				if e == nil || r.Completed || !r.Ended {
					t.Fatal("failed original source/output became success", e)
				}
			}
			if mode == "journal-partial" {
				if r.Attempted || r.JournalComplete || fault.calls.Load() != 0 || r.CaptureError == "" {
					t.Fatal("partial original archive dispatched Snapshot or claimed capture")
				}
			} else {
				<-fault.done
				if !fault.eof || fault.calls.Load() != 1 || !r.Attempted {
					t.Fatal("fault is not based on one actual complete original native source")
				}
				if !r.JournalComplete {
					t.Fatal("complete failure evidence lost")
				}
				snapshotJournalRecords(t, journal.Bytes())
			}
			if mode == "body-partial" && (r.SnapshotBytesWritten != 3 || r.OutputAcknowledged || len(r.RejectedNative) == 0) {
				t.Fatal("partial output acknowledgement changed")
			}
			if mode == "lost-eof" && (!r.ChecksumMatched || r.EOF || r.SourceComplete || len(r.NativeStatus) == 0) {
				t.Fatal("native checksum substituted for original EOF")
			}
			if mode == "close" || mode == "writer-close" {
				if facet.LastSnapshotResult().Ended || s.snapshotRetainedBytes != 0 {
					t.Fatal("original close retained cache/budget")
				}
			} else {
				recovery, e := s.RecoverKV(lease)
				if e != nil || recovery.SnapshotResult == nil || recovery.SnapshotResult.Scope.DocumentID != 703 || s.snapshotRetainedBytes != 0 {
					t.Fatal("pure recovery lost original failed Snapshot", e)
				}
				recovery.Lease.Close()
				if fault.calls.Load() != map[bool]int32{true: 0, false: 1}[mode == "journal-partial"] {
					t.Fatal("recovery resumed original Snapshot")
				}
			}
			if !admin.Alive() {
				t.Fatal("original document cleanup closed another transport")
			}
		})
	}
}
func TestEtcdNativeSnapshotSessionReadAfterActualUnknownPutKeepsOriginalWriteBarrier(t *testing.T) {
	snapshotSessionNative(t)
	admin := nativeClient(t)
	s, fault := snapshotProxySession(t, admin, "put-lost")
	lease := reserveNativeKV(t, s, 704)
	if e := lease.Preconnect(context.Background()); e != nil {
		t.Fatal(e)
	}
	key := nativeKey(t)
	defer nativeDo(t, admin, &pb.DeleteRangeRequest{Key: key})
	input := nativeKVRequest(t, &pb.PutRequest{Key: key, Value: []byte("original-before-independent")})
	plan, _ := lease.Prepare(input)
	unknown, e := lease.Execute(context.Background(), plan)
	<-fault.putFault.done
	if e == nil || !unknown.WriteOutcomeUnknown || fault.putFault.calls.Load() != 1 || fault.putFault.receipt == nil {
		t.Fatal("fault did not follow actual committed original Put")
	}
	recovery, e := s.RecoverKV(lease)
	if e != nil || !recovery.Last.WriteOutcomeUnknown {
		t.Fatal("original unknown Put evidence lost", e)
	}
	fresh := recovery.Lease
	defer fresh.Close()
	facet := fresh.(contract.KVSnapshots)
	snapshot, _ := facet.PrepareSnapshot(snapshotNativeRequest())
	var body, journal bytes.Buffer
	r, e := facet.ExecuteSnapshot(context.Background(), snapshot, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal})
	if e != nil || !r.Completed || !fresh.(*kvLease).writeUnknown || !recovery.Last.WriteOutcomeUnknown || fault.calls.Load() != 1 {
		t.Fatal("complete Snapshot settled earlier unconfirmed Put", e)
	}
	later := nativeKVDo(t, fresh, &pb.RangeRequest{Key: key})
	if len(later.Reply.Entries) != 1 || string(later.Reply.Entries[0].Value) != "later-independent" || !recovery.Last.WriteOutcomeUnknown {
		t.Fatal("later native read substituted for original Put ACK")
	}
	blocked, _ := fresh.Prepare(input)
	if _, e := fresh.Execute(context.Background(), blocked); !errors.Is(e, contract.ErrKVWriteUnknown) || fault.putFault.calls.Load() != 1 {
		t.Fatal("Snapshot/read permitted another write")
	}
	if !facet.LastSnapshotResult().Completed {
		t.Fatal("later read overwrote independent original Snapshot")
	}
}
func TestEtcdNativeSnapshotSessionRestrictedIdentityRetainsOriginalNativeDenial(t *testing.T) {
	snapshotSessionNative(t)
	p, secret := sessionProfile(t)
	if p.EtcdOptions.Auth != "basic" {
		return
	}
	p.Username = "restricted"
	p.EtcdOptions.Prefix = "/allowed/"
	if p.EtcdOptions.ClientCertFile != "" {
		p.EtcdOptions.ClientCertFile = os.Getenv("NYADB_ETCD_ACCESS_CERT")
		p.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_ACCESS_KEY")
		if p.EtcdOptions.ClientCertFile == "" {
			t.Fatal("missing owned restricted certificate")
		}
	}
	raw, e := OpenSession(context.Background(), p, secret)
	if e != nil {
		t.Fatal(e)
	}
	s := raw.(*Session)
	defer s.Close()
	lease := reserveNativeKV(t, s, 705)
	if e = lease.Preconnect(context.Background()); e != nil {
		t.Fatal(e)
	}
	f := lease.(contract.KVSnapshots)
	plan, _ := f.PrepareSnapshot(snapshotNativeRequest())
	var body, journal bytes.Buffer
	r, e := f.ExecuteSnapshot(context.Background(), plan, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal})
	expected := "PermissionDenied"
	if s.identity.Version == "3.5.35" {
		expected = "Unknown"
	}
	if e == nil || r.NativeCode != expected || !r.Attempted || r.Completed || !r.Ended || !r.JournalComplete || body.Len() != 0 || len(r.NativeStatus) == 0 || lease.CheckKVLease() != nil || r.Scope.Original.Namespace.ProfileID != p.ID {
		t.Fatal("original restricted identity/denial changed", e)
	}
	if !f.LastSnapshotResult().Ended {
		t.Fatal("definite native denial not retained")
	}
	snapshotJournalRecords(t, journal.Bytes())
}
