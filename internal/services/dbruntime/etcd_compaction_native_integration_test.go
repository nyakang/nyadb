package dbruntime_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func compactionRuntimeProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_COMPACTION_IT") != "1" {
		t.Skip("requires owned original Compact runtime fixture")
	}
	return etcdRuntimeProfile(t)
}

func compactionRuntimeWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("original Compact executor did not acknowledge")
	}
}

func TestEtcdNativeCompactionRuntimeOriginalPublicationInventoryAndPureRecovery(t *testing.T) {
	p, secret := compactionRuntimeProfile(t)
	p.EtcdOptions.Prefix = "/original-maintenance-prefix/"
	m := etcdRuntimeManager(t)
	publication, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	lease := etcdRuntimeReserve(t, m, 1)
	inventory, ok := lease.(contract.KVMaintenance)
	if !ok || !slices.Equal(inventory.MaintenanceMethods(), []string{"/etcdserverpb.KV/Compact", "/etcdserverpb.Maintenance/Alarm"}) {
		t.Fatal("original publication lost maintenance inventory")
	}
	if _, ok := lease.(contract.KVAuthManagement); !ok {
		t.Fatal("adding Compact lost Auth facet")
	}
	if _, ok := lease.(contract.KVLeaseBrowsing); !ok {
		t.Fatal("adding Compact lost lease browser facet")
	}
	key := []byte("/outside-original-prefix/" + t.Name())
	etcdRuntimeDo(t, lease, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("before")})
	last := etcdRuntimeDo(t, lease, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("current")})
	input := etcdRuntimeRequest(t, "KV/Compact", &pb.CompactionRequest{Revision: last.Header.Revision, Physical: true})
	plan, err := lease.Prepare(input)
	if err != nil || !plan.Writes() {
		t.Fatal(err)
	}
	result, err := lease.Execute(context.Background(), plan)
	native := &pb.CompactionResponse{}
	if err != nil || !result.Completed || result.WriteOutcomeUnknown || proto.Unmarshal(result.Native, native) != nil || result.Header.Revision != last.Header.Revision {
		t.Fatal("published native Compact receipt changed", err)
	}
	originalScope := lease.Scope()
	replacement, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Generation() <= publication.Generation() || !errors.Is(lease.CheckKVLease(), contract.ErrRevoked) {
		t.Fatal("original Compact lease followed replacement")
	}
	provider, _ := m.KVRequests(p.ID)
	recovery, err := provider.RecoverKV(lease)
	if err != nil || recovery.Lease.Scope().Generation != replacement.Generation() || recovery.Last.Attempted {
		t.Fatal("recovery invented native Compact or changed publication", err)
	}
	defer recovery.Lease.Close()
	if _, ok := recovery.Lease.(contract.KVMaintenance); !ok {
		t.Fatal("recovery lost maintenance facet")
	}
	if _, err := recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original Compact plan transferred to replacement")
	}
	if _, err := lease.Prepare(input); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old Compact lease dispatched after replacement")
	}
	read := etcdRuntimeDo(t, recovery.Lease, "KV/Range", &pb.RangeRequest{Key: key})
	if len(read.Reply.Entries) != 1 || !bytes.Equal(read.Reply.Entries[0].Value, []byte("current")) || lease.Scope().Generation != originalScope.Generation || originalScope.Generation != publication.Generation() || result.Request != plan.Snapshot() || !result.Completed {
		t.Fatal("recovery altered original scope/receipt or replayed maintenance")
	}
}

type compactionRuntimeFault struct {
	*etcdRuntimeFault
	mode          string
	calls         atomic.Int32
	entered, done chan struct{}
	receipt       *pb.CompactionResponse
	request       *pb.CompactionRequest
}

func (f *compactionRuntimeFault) Compact(ctx context.Context, request *pb.CompactionRequest) (*pb.CompactionResponse, error) {
	if f.calls.Add(1) != 1 {
		return nil, status.Error(codes.Internal, "original Compact replayed")
	}
	reply, err := pb.NewKVClient(f.upstream).Compact(f.context(ctx), request)
	if err != nil {
		return reply, err
	}
	f.request = proto.Clone(request).(*pb.CompactionRequest)
	f.receipt = proto.Clone(reply).(*pb.CompactionResponse)
	close(f.entered)
	defer close(f.done)
	switch f.mode {
	case "lost":
		return nil, status.Error(codes.Unavailable, "original completed Compact reply withheld")
	case "cancel", "close", "replace", "disconnect":
		<-ctx.Done()
		return nil, ctx.Err()
	case "foreign":
		reply.Header.MemberId++
	}
	return reply, nil
}

func compactionRuntimeProxy(t *testing.T, p connections.Profile, mode string) (connections.Profile, *compactionRuntimeFault) {
	t.Helper()
	f := &compactionRuntimeFault{etcdRuntimeFault: &etcdRuntimeFault{upstream: etcdRuntimeTransport(t, p)}, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterKVServer(server, f)
	pb.RegisterAuthServer(server, f)
	pb.RegisterMaintenanceServer(server, f)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	p.Host = "127.0.0.1"
	p.Port = uint16(listener.Addr().(*net.TCPAddr).Port)
	p.TLSMode = "disable"
	p.EtcdOptions.CAFile, p.EtcdOptions.ClientCertFile, p.EtcdOptions.ClientKeyFile = "", "", ""
	return p, f
}

func TestEtcdNativeCompactionRuntimeActualProcessedFaultsOriginalPublicationAndRecoveryNeverReplay(t *testing.T) {
	p, secret := compactionRuntimeProfile(t)
	for _, physical := range []bool{false, true} {
		for _, mode := range []string{"lost", "cancel", "close", "replace", "disconnect", "foreign", "parked-complete"} {
			t.Run(strconv.FormatBool(physical)+"/"+mode, func(t *testing.T) {
				proxy, f := compactionRuntimeProxy(t, p, mode)
				m := etcdRuntimeManager(t)
				publication, err := m.ConnectSession(context.Background(), proxy, secret)
				if err != nil {
					t.Fatal(err)
				}
				lease := etcdRuntimeReserve(t, m, 1)
				originalScope := lease.Scope()
				admin := etcdRuntimeManager(t)
				if err := admin.Connect(context.Background(), p, secret); err != nil {
					t.Fatal(err)
				}
				writer := etcdRuntimeReserve(t, admin, 2)
				key := []byte("/compact-runtime/" + t.Name())
				old := etcdRuntimeDo(t, writer, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("before")})
				last := etcdRuntimeDo(t, writer, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("current")})
				plan, err := lease.Prepare(etcdRuntimeRequest(t, "KV/Compact", &pb.CompactionRequest{Revision: last.Header.Revision, Physical: physical}))
				if err != nil || f.calls.Load() != 0 {
					t.Fatal("Compact preparation dispatched RPC", err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type completion struct {
					r   contract.KVResult
					err error
				}
				out := make(chan completion, 1)
				go func() { r, err := lease.Execute(ctx, plan); out <- completion{r, err} }()
				compactionRuntimeWait(t, f.entered)
				switch mode {
				case "cancel":
					cancel()
				case "close":
					if err := lease.Close(); err != nil {
						t.Fatal(err)
					}
				case "replace":
					if _, err := m.ConnectSession(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
				case "disconnect":
					if err := m.Disconnect(p.ID); err != nil {
						t.Fatal(err)
					}
				case "parked-complete":
					other := p
					other.ID++
					if _, err := m.ConnectSession(context.Background(), other, secret); err != nil {
						t.Fatal(err)
					}
				}
				settled := <-out
				compactionRuntimeWait(t, f.done)
				if f.calls.Load() != 1 || f.request.Revision != last.Header.Revision || f.request.Physical != physical || f.receipt.Header.ClusterId != originalScope.Header.ClusterID || settled.r.Request != plan.Snapshot() || lease.Scope().Generation != publication.Generation() {
					t.Fatal("original completed Compact request/receipt relabeled")
				}
				if mode == "parked-complete" {
					if settled.err != nil || !settled.r.Completed || settled.r.WriteOutcomeUnknown || lease.CheckKVLease() != nil {
						t.Fatal("another profile erased parked original Compact completion", settled.err)
					}
				} else if settled.err == nil || !settled.r.Attempted || settled.r.Completed || !settled.r.WriteOutcomeUnknown {
					t.Fatal("processed Compact fault erased or replayed", settled.err)
				}
				if mode == "foreign" && (len(settled.r.Native) == 0 || settled.r.Header.MemberID == originalScope.Header.MemberID) {
					t.Fatal("foreign native evidence erased")
				}
				check, _ := writer.Prepare(etcdRuntimeRequest(t, "KV/Range", &pb.RangeRequest{Key: key, Revision: old.Header.Revision}))
				if _, err := writer.Execute(context.Background(), check); status.Code(err) != codes.OutOfRange {
					t.Fatal("original server did not compact", err)
				}
				later := etcdRuntimeDo(t, writer, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("later-independent")})
				if mode == "disconnect" {
					if err := m.Connect(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
				}
				provider, err := m.KVRequests(p.ID)
				if err != nil {
					t.Fatal(err)
				}
				recovery, err := provider.RecoverKV(lease)
				if err != nil || recovery.Lease == nil || f.calls.Load() != 1 {
					t.Fatal("pure recovery repeated maintenance", err)
				}
				defer recovery.Lease.Close()
				if (mode == "lost" || mode == "cancel" || mode == "foreign") && !recovery.Last.WriteOutcomeUnknown {
					t.Fatal("pure recovery lost original uncertainty")
				}
				if _, err := recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
					t.Fatal("original preparation crossed recovery")
				}
				read := etcdRuntimeDo(t, recovery.Lease, "KV/Range", &pb.RangeRequest{Key: key, Revision: later.Header.Revision})
				if len(read.Reply.Entries) != 1 || !bytes.Equal(read.Reply.Entries[0].Value, []byte("later-independent")) || f.calls.Load() != 1 || settled.r.Request != plan.Snapshot() {
					t.Fatal("later independent window or original receipt altered")
				}
			})
		}
	}
}
