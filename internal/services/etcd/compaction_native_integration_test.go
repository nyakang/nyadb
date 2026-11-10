package etcd

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
	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func compactionNativeClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("NYADB_ETCD_COMPACTION_IT") != "1" {
		t.Skip("requires owned original Compact fixture")
	}
	return nativeClient(t)
}

func TestEtcdNativeCompactionOriginalLogicalPhysicalWholeClusterAndCompleteReceipts(t *testing.T) {
	admin := compactionNativeClient(t)
	for _, physical := range []bool{false, true} {
		t.Run(strconv.FormatBool(physical), func(t *testing.T) {
			profile, secret := sessionProfile(t)
			profile.EtcdOptions.Prefix = "/inside/"
			raw, err := OpenSession(context.Background(), profile, secret)
			if err != nil {
				t.Fatal(err)
			}
			s := raw.(*Session)
			defer s.Close()
			l := reserveNativeKV(t, s, 1)
			if !slices.Equal(l.(contract.KVMaintenance).MaintenanceMethods(), []string{"/etcdserverpb.KV/Compact", "/etcdserverpb.Maintenance/Alarm"}) {
				t.Fatal("original lease lost maintenance inventory")
			}
			key, outside := append([]byte("/inside/"), nativeKey(t)...), append(nativeKey(t), 0, 255)
			first := nativeDo(t, admin, &pb.PutRequest{Key: outside, Value: []byte("outside-before")})
			nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("inside-before")})
			nativeDo(t, admin, &pb.PutRequest{Key: outside, Value: []byte("outside-after")})
			last := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("inside-after")})
			input := nativeKVRequest(t, &pb.CompactionRequest{Revision: last.Header.Revision, Physical: physical})
			plan, err := l.Prepare(input)
			if err != nil || !plan.Writes() {
				t.Fatal("Compact was not a one-use write", err)
			}
			original := plan.Snapshot()
			input.JSON = `{"revision":"1"}`
			result, err := l.Execute(context.Background(), plan)
			native := &pb.CompactionResponse{}
			if err != nil || !result.Attempted || !result.Completed || result.WriteOutcomeUnknown || result.Request != original || result.Reply == nil || result.Reply.Kind != "compaction" || proto.Unmarshal(result.Native, native) != nil || native.Header == nil || result.Header != neutralHeader(native.Header) || result.Header.Revision != last.Header.Revision || result.JSON == "" {
				t.Fatal("original full Compact receipt changed", err, result)
			}
			if _, err := l.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("original Compact plan replayed", err)
			}
			for _, observedKey := range [][]byte{key, outside} {
				p, _ := l.Prepare(nativeKVRequest(t, &pb.RangeRequest{Key: observedKey, Revision: first.Header.Revision}))
				r, err := l.Execute(context.Background(), p)
				if status.Code(err) != codes.OutOfRange || r.Completed || r.WriteOutcomeUnknown || l.CheckKVLease() != nil {
					t.Fatal("whole-cluster history was not compacted or definite range error retired lease", err)
				}
			}
			inside := nativeKVDo(t, l, &pb.RangeRequest{Key: key})
			out := nativeKVDo(t, l, &pb.RangeRequest{Key: outside})
			if len(inside.Reply.Entries) != 1 || len(out.Reply.Entries) != 1 || !bytes.Equal(inside.Reply.Entries[0].Value, []byte("inside-after")) || !bytes.Equal(out.Reply.Entries[0].Value, []byte("outside-after")) || out.Header.Revision != last.Header.Revision || result.Request != original || result.Reply.Kind != "compaction" {
				t.Fatal("Compact erased current values, changed KV revision, or later read replaced original receipt")
			}
			copy := result.Clone()
			copy.Native[0] ^= 1
			if bytes.Equal(copy.Native, result.Native) {
				t.Fatal("complete original receipt aliases caller")
			}
		})
	}
}

func TestEtcdNativeCompactionActualFutureCompactedAndRestrictedIdentityDefiniteErrors(t *testing.T) {
	_ = compactionNativeClient(t)
	admin := accessNativeAdmin(t)
	key := nativeKey(t)
	first := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("before")})
	last := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("after")})
	future, _ := admin.Prepare(&pb.CompactionRequest{Revision: last.Header.Revision + 1})
	if r, err := admin.Execute(context.Background(), future); status.Code(err) != codes.OutOfRange || r.Completed || r.WriteOutcomeUnknown || !admin.Alive() {
		t.Fatal("future Compact not classified as definite rejection", err)
	}
	nativeDo(t, admin, &pb.CompactionRequest{Revision: last.Header.Revision, Physical: true})
	for _, rev := range []int64{first.Header.Revision, last.Header.Revision} {
		p, _ := admin.Prepare(&pb.CompactionRequest{Revision: rev})
		if r, err := admin.Execute(context.Background(), p); status.Code(err) != codes.OutOfRange || r.Completed || r.WriteOutcomeUnknown || !admin.Alive() {
			t.Fatal("already compacted revision treated as new completion or uncertain write", err)
		}
	}
	user := "compact-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	accessNativeUser(t, admin, user, user, []*authpb.Permission{{PermType: authpb.READWRITE, Key: key}})
	cfg, _ := nativeConfig(t)
	cfg.Auth, cfg.Username, cfg.ProbeKey = "basic", user, key
	// The original token is checked by the native server even when the transport
	// also presents the root CN certificate.
	limited, err := Open(context.Background(), cfg, os.Getenv("NYADB_ETCD_SECRET"))
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	nativeDo(t, limited, &pb.PutRequest{Key: key, Value: []byte("limited-write")})
	revision := nativeDo(t, admin, &pb.RangeRequest{Key: key}).Header.Revision
	p, _ := limited.Prepare(&pb.CompactionRequest{Revision: revision})
	r, err := limited.Execute(context.Background(), p)
	if status.Code(err) != codes.PermissionDenied || r.Completed || r.WriteOutcomeUnknown || !limited.Alive() {
		t.Fatal("key READWRITE permission authorized cluster maintenance", err)
	}
	// Verify the denial did not compact the new exact revision. This observation
	// is independent of the original denial receipt.
	nativeDo(t, limited, &pb.RangeRequest{Key: key, Revision: revision})
	if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
		cfg, _ = nativeConfig(t)
		cfg.Auth, cfg.Username = "none", ""
		certificate, err := Open(context.Background(), cfg, "")
		if err != nil {
			t.Fatal("original certificate handshake", err)
		}
		defer certificate.Close()
		nativeDo(t, certificate, &pb.CompactionRequest{Revision: revision, Physical: true})
		if certificate.principal != "root" {
			t.Fatal("Compact certificate selected another principal")
		}
	}
}

// The relay withholds or alters only a real, completed upstream Compact reply.
// No preparation/read or synthetic reply is used as evidence of compression.
type compactionFault struct {
	upstream      *Client
	mode          string
	calls         atomic.Int32
	entered, done chan struct{}
	request       *pb.CompactionRequest
	receipt       *pb.CompactionResponse
}

func (f *compactionFault) serve(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	if method != "/etcdserverpb.KV/Compact" {
		return (&nativeFault{upstream: f.upstream}).serve(nil, stream)
	}
	request := &pb.CompactionRequest{}
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	if f.calls.Add(1) != 1 {
		return status.Error(codes.Internal, "original Compact replayed")
	}
	reply, err := pb.NewKVClient(f.upstream.conn).Compact(f.upstream.authContext(stream.Context()), request)
	if err != nil {
		return err
	}
	f.request, f.receipt = proto.Clone(request).(*pb.CompactionRequest), proto.Clone(reply).(*pb.CompactionResponse)
	close(f.entered)
	defer close(f.done)
	switch f.mode {
	case "lost":
		return status.Error(codes.Unavailable, "processed original Compact receipt withheld")
	case "cancel", "close", "replace", "disconnect":
		<-stream.Context().Done()
		return stream.Context().Err()
	case "foreign":
		reply.Header.ClusterId++
	case "missing-header":
		reply.Header = nil
	case "extensions":
		reply.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
	}
	return stream.SendMsg(reply)
}

func compactionProxy(t *testing.T, admin *Client, mode string) (*Client, *compactionFault) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &compactionFault{upstream: admin, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
	server := grpc.NewServer(grpc.UnknownServiceHandler(f.serve))
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	cfg, secret := nativeConfig(t)
	cfg.Endpoint = "http://" + listener.Addr().String()
	cfg.CAFile, cfg.ClientCertFile, cfg.ClientKeyFile, cfg.ServerName = "", "", "", ""
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal("original Compact relay handshake", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, f
}

func TestEtcdNativeCompactionActualProcessedLostCancelCloseAndInvalidReceiptsNeverReplay(t *testing.T) {
	admin := compactionNativeClient(t)
	for _, physical := range []bool{false, true} {
		for _, mode := range []string{"lost", "cancel", "close", "foreign", "missing-header", "extensions"} {
			t.Run(strconv.FormatBool(physical)+"/"+mode, func(t *testing.T) {
				key := nativeKey(t)
				old := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("old")})
				last := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("current")})
				c, f := compactionProxy(t, admin, mode)
				p, secret := sessionProfile(t)
				s := newSession(p, secret, nativeClient(t))
				defer s.Close()
				var opens atomic.Int32
				s.opener = func(ctx context.Context, p connections.Profile, secret string) (*Client, error) {
					if opens.Add(1) == 1 {
						return c, nil
					}
					return OpenProfile(ctx, p, secret)
				}
				l := reserveNativeKV(t, s, 1)
				input := nativeKVRequest(t, &pb.CompactionRequest{Revision: last.Header.Revision, Physical: physical})
				plan, err := l.Prepare(input)
				if err != nil || opens.Load() != 0 || f.calls.Load() != 0 {
					t.Fatal("Compact preparation opened client or dispatched RPC", err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type completion struct {
					r   contract.KVResult
					err error
				}
				out := make(chan completion, 1)
				go func() { r, err := l.Execute(ctx, plan); out <- completion{r, err} }()
				waitSignal(t, f.entered)
				if mode == "cancel" {
					cancel()
				}
				if mode == "close" {
					if err := l.Close(); err != nil {
						t.Fatal(err)
					}
				}
				settled := <-out
				waitSignal(t, f.done)
				if f.calls.Load() != 1 || f.request.Revision != last.Header.Revision || f.request.Physical != physical || !admin.matches(f.receipt.Header) || settled.r.Request != plan.Snapshot() {
					t.Fatal("fault was not applied to the original completed Compact")
				}
				if mode == "extensions" {
					if settled.err != nil || !settled.r.Completed || settled.r.WriteOutcomeUnknown {
						t.Fatal("valid complete Compact rejected", settled.err)
					}
					native := &pb.CompactionResponse{}
					if proto.Unmarshal(settled.r.Native, native) != nil || !bytes.Equal(native.ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) {
						t.Fatal("complete original extensions erased")
					}
				} else {
					if settled.err == nil || !settled.r.Attempted || settled.r.Completed || !settled.r.WriteOutcomeUnknown || l.CheckKVLease() == nil {
						t.Fatal("processed Compact replayed or uncertainty erased", settled.err)
					}
					if mode == "foreign" && len(settled.r.Native) == 0 {
						t.Fatal("invalid original native evidence erased")
					}
					// An empty CompactionResponse has a valid zero-byte protobuf
					// encoding. Keep its JSON and invalid header as evidence.
					if mode == "missing-header" {
						fromJSON, fromNative := &pb.CompactionResponse{}, &pb.CompactionResponse{}
						if settled.r.JSON == "" || protojson.Unmarshal([]byte(settled.r.JSON), fromJSON) != nil || proto.Unmarshal(settled.r.Native, fromNative) != nil || !proto.Equal(fromJSON, fromNative) || fromNative.Header != nil || settled.r.Header != (contract.KVHeader{}) {
							t.Fatal("empty native Compact receipt was erased or given an invented header")
						}
					}
				}
				// Observe actual compaction independently before recovery. A later Put
				// creates a still-readable window which recovery must not compress.
				check, _ := admin.Prepare(&pb.RangeRequest{Key: key, Revision: old.Header.Revision})
				if _, err := admin.Execute(context.Background(), check); status.Code(err) != codes.OutOfRange {
					t.Fatal("server had not processed Compact", err)
				}
				later := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("later-independent")})
				recovery, err := s.RecoverKV(l)
				if err != nil || opens.Load() != 1 || f.calls.Load() != 1 {
					t.Fatal("recovery opened or replayed Compact", err)
				}
				defer recovery.Lease.Close()
				if mode != "close" && mode != "extensions" && !recovery.Last.WriteOutcomeUnknown {
					t.Fatal("recovery lost original uncertain Compact receipt")
				}
				if _, err := recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
					t.Fatal("original Compact plan crossed recovery")
				}
				read := nativeKVDo(t, recovery.Lease, &pb.RangeRequest{Key: key, Revision: later.Header.Revision})
				if len(read.Reply.Entries) != 1 || !bytes.Equal(read.Reply.Entries[0].Value, []byte("later-independent")) || f.calls.Load() != 1 || opens.Load() != 2 {
					t.Fatal("explicit later read compressed or replayed old request")
				}
				if mode != "extensions" && !settled.r.WriteOutcomeUnknown {
					t.Fatal("later read cleared independent original uncertainty")
				}
			})
		}
	}
}
