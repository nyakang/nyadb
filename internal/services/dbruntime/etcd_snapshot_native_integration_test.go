package dbruntime_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/etcd"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func snapshotRuntimeProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_SNAPSHOT_SESSION_IT") != "1" {
		t.Skip("requires owned original Snapshot runtime fixture")
	}
	return etcdRuntimeProfile(t)
}
func snapshotRuntimeRequest() contract.KVSnapshotRequest {
	return contract.KVSnapshotRequest{BodyBytes: 32 << 20, JournalBytes: 64 << 20, Frames: 4096}
}
func TestEtcdNativeSnapshotRuntimeOriginalAllFacetsExactPublicationAndPureRecovery(t *testing.T) {
	p, secret := snapshotRuntimeProfile(t)
	p.EtcdOptions.Prefix = "/original-snapshot-runtime-browse/"
	m := etcdRuntimeManager(t)
	m.Connect(context.Background(), p, secret)
	publication, e := m.ConnectSession(context.Background(), p, secret)
	if e != nil {
		t.Fatal(e)
	}
	lease := etcdRuntimeReserve(t, m, 801)
	original := lease.Scope()
	facet, ok := lease.(contract.KVSnapshots)
	if !ok || !implementsAuth(lease) || !implementsBrowser(lease) || !implementsMembers(lease) {
		t.Fatal("original runtime lost optional facets")
	}
	if _, ok := lease.(contract.KVMaintenance); !ok {
		t.Fatal("Snapshot replaced maintenance")
	}
	plan, e := facet.PrepareSnapshot(snapshotRuntimeRequest())
	if e != nil || plan.Scope().Original.Generation != publication.Generation() || plan.Scope().Original.Generation == 1 || plan.Scope().DocumentID != 801 {
		t.Fatal("public Snapshot preparation uses contextual/foreign generation", e)
	}
	var body, journal bytes.Buffer
	r, e := facet.ExecuteSnapshot(context.Background(), plan, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal})
	if e != nil || !r.Completed || !r.SourceComplete || r.Scope.Original.Generation != original.Generation || r.Scope.DocumentID != 801 || r.Scope.Original.Namespace.ProfileID != p.ID || r.SnapshotBytesWritten != uint64(body.Len()) {
		t.Fatal("native runtime receipt lost original publication", e)
	}
	if e := etcd.ReadSnapshotJournal(bytes.NewReader(journal.Bytes()), uint64(journal.Len()), func(etcd.SnapshotJournalRecord) error { return nil }); e != nil {
		t.Fatal("original native frame journal incomplete", e)
	}
	foreign := etcdRuntimeReserve(t, m, 802)
	var otherBody, otherJournal bytes.Buffer
	if _, e := foreign.(contract.KVSnapshots).ExecuteSnapshot(context.Background(), plan, contract.KVSnapshotOutputs{Snapshot: &otherBody, Journal: &otherJournal}); !errors.Is(e, contract.ErrKVPlanUsed) || otherBody.Len() != 0 {
		t.Fatal("Snapshot plan entered another document output")
	}
	next, e := m.ConnectSession(context.Background(), p, secret)
	if e != nil {
		t.Fatal(e)
	}
	provider, _ := m.KVRequests(p.ID)
	recovery, e := provider.RecoverKV(lease)
	if e != nil || recovery.SnapshotResult == nil || !recovery.SnapshotResult.Completed || recovery.SnapshotResult.Scope.Original.Generation != original.Generation || recovery.Lease.Scope().Generation != next.Generation() || next.Generation() <= original.Generation {
		t.Fatal("pure recovery relabeled original Snapshot", e)
	}
	defer recovery.Lease.Close()
	if _, e := recovery.Lease.(contract.KVSnapshots).ExecuteSnapshot(context.Background(), plan, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal}); !errors.Is(e, contract.ErrKVPlanUsed) {
		t.Fatal("recovery restored old Snapshot plan")
	}
}

type snapshotRuntimeFault struct {
	*etcdRuntimeFault
	mode                   string
	calls                  atomic.Int32
	entered, done, release chan struct{}
	frames                 []*pb.SnapshotResponse
	eof                    bool
}

func (f *snapshotRuntimeFault) Snapshot(request *pb.SnapshotRequest, stream pb.Maintenance_SnapshotServer) error {
	if f.calls.Add(1) != 1 {
		return status.Error(codes.Internal, "original Snapshot replayed")
	}
	announced := false
	defer func() {
		if !announced {
			close(f.entered)
		}
		close(f.done)
	}()
	source, e := pb.NewMaintenanceClient(f.upstream).Snapshot(f.context(stream.Context()), request)
	if e != nil {
		return e
	}
	for {
		frame, e := source.Recv()
		if e == io.EOF {
			f.eof = true
			break
		}
		if e != nil {
			return e
		}
		f.frames = append(f.frames, proto.Clone(frame).(*pb.SnapshotResponse))
	}
	close(f.entered)
	announced = true
	for i, original := range f.frames {
		frame := proto.Clone(original).(*pb.SnapshotResponse)
		if f.mode == "foreign" && i == 0 {
			frame.Header = &pb.ResponseHeader{ClusterId: 91}
		}
		if f.mode == "extensions" {
			frame.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
		}
		if e := stream.Send(frame); e != nil {
			return e
		}
	}
	switch f.mode {
	case "lost":
		return status.Error(codes.Unavailable, "actual original Snapshot EOF withheld")
	case "cancel", "close", "replace", "disconnect":
		<-stream.Context().Done()
		return stream.Context().Err()
	case "parked-complete":
		select {
		case <-f.release:
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
	return nil
}
func snapshotRuntimeProxy(t *testing.T, p connections.Profile, mode string, key []byte) (connections.Profile, *snapshotRuntimeFault) {
	t.Helper()
	f := &snapshotRuntimeFault{etcdRuntimeFault: &etcdRuntimeFault{upstream: etcdRuntimeTransport(t, p), key: bytes.Clone(key), entered: make(chan struct{}), done: make(chan struct{})}, mode: mode, entered: make(chan struct{}), done: make(chan struct{}), release: make(chan struct{})}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	var options []grpc.ServerOption
	if p.TLSMode == "verify-full" {
		folder := strings.TrimSuffix(p.EtcdOptions.CAFile, "ca.crt")
		pair, e := tls.LoadX509KeyPair(folder+"server.crt", folder+"server.key")
		if e != nil {
			t.Fatal(e)
		}
		tc := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
		if p.EtcdOptions.ClientCertFile != "" {
			raw, e := os.ReadFile(p.EtcdOptions.CAFile)
			if e != nil {
				t.Fatal(e)
			}
			tc.ClientCAs = x509.NewCertPool()
			if !tc.ClientCAs.AppendCertsFromPEM(raw) {
				t.Fatal("invalid owned CA")
			}
			tc.ClientAuth = tls.RequireAndVerifyClientCert
		}
		options = append(options, grpc.Creds(credentials.NewTLS(tc)))
		p.Host = "localhost"
	} else {
		p.Host = "127.0.0.1"
	}
	p.Port = uint16(listener.Addr().(*net.TCPAddr).Port)
	server := grpc.NewServer(options...)
	pb.RegisterKVServer(server, f)
	pb.RegisterAuthServer(server, f)
	pb.RegisterMaintenanceServer(server, f)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	return p, f
}
func TestEtcdNativeSnapshotRuntimeActualSourceFaultsOriginalPublicationParkingAndRecovery(t *testing.T) {
	p, secret := snapshotRuntimeProfile(t)
	for _, mode := range []string{"lost", "cancel", "close", "replace", "disconnect", "foreign", "extensions", "parked-complete"} {
		t.Run(mode, func(t *testing.T) {
			proxy, fault := snapshotRuntimeProxy(t, p, mode, nil)
			m := etcdRuntimeManager(t)
			publication, e := m.ConnectSession(context.Background(), proxy, secret)
			if e != nil {
				t.Fatal(e)
			}
			lease := etcdRuntimeReserve(t, m, 803)
			original := lease.Scope()
			facet := lease.(contract.KVSnapshots)
			plan, e := facet.PrepareSnapshot(snapshotRuntimeRequest())
			if e != nil || fault.calls.Load() != 0 {
				t.Fatal("pure runtime preparation dispatched Snapshot", e)
			}
			var body, journal bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completed struct {
				r contract.KVSnapshotResult
				e error
			}
			done := make(chan completed, 1)
			go func() {
				r, e := facet.ExecuteSnapshot(ctx, plan, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal})
				done <- completed{r, e}
			}()
			select {
			case <-fault.entered:
			case early := <-done:
				t.Fatal("original runtime source not reached", early.e)
			}
			switch mode {
			case "cancel":
				cancel()
			case "close":
				lease.Close()
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
			r, e := result.r, result.e
			if fault.calls.Load() != 1 || !fault.eof || len(fault.frames) < 2 || !r.Ended || r.Scope.Original.Generation != publication.Generation() || r.Scope.Original.Namespace.ProfileID != p.ID || r.Scope.DocumentID != 803 {
				t.Fatal("actual source receipt used another publication")
			}
			if mode == "extensions" || mode == "parked-complete" {
				if e != nil || !r.Completed || !r.SourceComplete {
					t.Fatal("original complete source lost", e)
				}
			} else {
				if e == nil || r.Completed {
					t.Fatal("failed original source became successful", e)
				}
			}
			if mode == "lost" && (!r.ChecksumMatched || r.EOF || r.SourceComplete || len(r.NativeStatus) == 0) {
				t.Fatal("native checksum settled withheld EOF")
			}
			if mode == "foreign" && len(r.RejectedNative) == 0 {
				t.Fatal("foreign native frame evidence lost")
			}
			if e := etcd.ReadSnapshotJournal(bytes.NewReader(journal.Bytes()), uint64(journal.Len()), func(record etcd.SnapshotJournalRecord) error {
				if mode == "extensions" && record.Kind == etcd.SnapshotJournalFrameKind {
					frame := &pb.SnapshotResponse{}
					if proto.Unmarshal(record.Raw, frame) != nil || !bytes.Equal(frame.ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) {
						t.Fatal("native extensions discarded")
					}
				}
				return nil
			}); e != nil {
				t.Fatal("original full frame evidence lost", e)
			}
			if mode == "close" {
				if facet.LastSnapshotResult().Ended {
					t.Fatal("explicit close retained native cache")
				}
				return
			}
			if mode == "disconnect" {
				if e := m.Connect(context.Background(), proxy, secret); e != nil {
					t.Fatal(e)
				}
			}
			provider, e := m.KVRequests(p.ID)
			if e != nil {
				t.Fatal(e)
			}
			recovery, e := provider.RecoverKV(lease)
			if e != nil || recovery.SnapshotResult == nil || recovery.SnapshotResult.Scope.Original.Generation != original.Generation || !bytes.Equal(recovery.SnapshotResult.NativeStatus, r.NativeStatus) || recovery.Lease.Scope().Generation != m.Generation(p.ID) || fault.calls.Load() != 1 {
				t.Fatal("pure recovery lost original scope/receipt or replayed Snapshot", e)
			}
			defer recovery.Lease.Close()
			if _, e := recovery.Lease.(contract.KVSnapshots).ExecuteSnapshot(context.Background(), plan, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal}); !errors.Is(e, contract.ErrKVPlanUsed) {
				t.Fatal("recovery restored original stream authority")
			}
		})
	}
}
func TestEtcdNativeSnapshotRuntimeCompleteReadNeverSettlesEarlierActualUnknownWrite(t *testing.T) {
	p, secret := snapshotRuntimeProfile(t)
	key := []byte("/snapshot-runtime/" + t.Name())
	proxy, fault := snapshotRuntimeProxy(t, p, "put-lost", key)
	m := etcdRuntimeManager(t)
	if e := m.Connect(context.Background(), proxy, secret); e != nil {
		t.Fatal(e)
	}
	lease := etcdRuntimeReserve(t, m, 804)
	request := etcdRuntimeRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("original")})
	plan, _ := lease.Prepare(request)
	unknown, e := lease.Execute(context.Background(), plan)
	<-fault.etcdRuntimeFault.done
	if e == nil || !unknown.WriteOutcomeUnknown || fault.etcdRuntimeFault.receipt == nil || fault.etcdRuntimeFault.calls.Load() != 1 {
		t.Fatal("fault did not follow actual original committed write")
	}
	provider, _ := m.KVRequests(p.ID)
	recovery, e := provider.RecoverKV(lease)
	if e != nil || !recovery.Last.WriteOutcomeUnknown {
		t.Fatal("original unknown evidence lost", e)
	}
	fresh := recovery.Lease
	defer fresh.Close()
	facet := fresh.(contract.KVSnapshots)
	snapshot, _ := facet.PrepareSnapshot(snapshotRuntimeRequest())
	var body, journal bytes.Buffer
	r, e := facet.ExecuteSnapshot(context.Background(), snapshot, contract.KVSnapshotOutputs{Snapshot: &body, Journal: &journal})
	if e != nil || !r.Completed || !recovery.Last.WriteOutcomeUnknown || fault.calls.Load() != 1 {
		t.Fatal("complete native Snapshot settled earlier unknown write", e)
	}
	read, _ := fresh.Prepare(etcdRuntimeRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
	later, e := fresh.Execute(context.Background(), read)
	if e != nil || later.Reply == nil || len(later.Reply.Entries) != 1 || string(later.Reply.Entries[0].Value) != "later-independent" || !recovery.Last.WriteOutcomeUnknown {
		t.Fatal("later read replaced actual original unknown write", e)
	}
	blocked, _ := fresh.Prepare(request)
	if _, e := fresh.Execute(context.Background(), blocked); !errors.Is(e, contract.ErrKVWriteUnknown) || fault.etcdRuntimeFault.calls.Load() != 1 {
		t.Fatal("Snapshot/read bypassed unknown-write barrier")
	}
	if !facet.LastSnapshotResult().Completed {
		t.Fatal("later KV response replaced original Snapshot")
	}
}
