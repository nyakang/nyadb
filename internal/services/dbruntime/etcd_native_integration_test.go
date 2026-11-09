package dbruntime_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/etcd"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func etcdRuntimeProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_SESSION_IT") != "1" {
		t.Skip("requires owned native etcd runtime fixture")
	}
	u, err := url.Parse(os.Getenv("NYADB_ETCD_ENDPOINT"))
	if err != nil || u.Hostname() == "" {
		t.Fatal("missing native endpoint")
	}
	p, _ := connections.Defaults(connections.Etcd)
	p.ID, p.Name, p.Host = 91, "native KV runtime", u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	p.Port = uint16(port)
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	p.EtcdOptions.CAFile = os.Getenv("NYADB_ETCD_CA")
	p.EtcdOptions.ClientCertFile = os.Getenv("NYADB_ETCD_CERT")
	p.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_KEY")
	p.EtcdOptions.ConnectTimeoutSeconds, p.EtcdOptions.RequestTimeoutSeconds = 5, 5
	secret := ""
	if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
		p.EtcdOptions.Auth, p.Username, secret = "basic", os.Getenv("NYADB_ETCD_USERNAME"), os.Getenv("NYADB_ETCD_SECRET")
	}
	return p, secret
}
func etcdRuntimeManager(t *testing.T) *dbruntime.Manager {
	t.Helper()
	m := dbruntime.New()
	m.Register(connections.Etcd, dbruntime.ProviderFunc(etcd.OpenSession))
	t.Cleanup(func() { _ = m.Close() })
	return m
}
func etcdRuntimeReserve(t *testing.T, m *dbruntime.Manager, id uint64) contract.KVRequestLease {
	t.Helper()
	p, err := m.KVRequests(91)
	if err != nil {
		t.Fatal(err)
	}
	l, err := p.ReserveKV(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}
func etcdRuntimeRequest(t *testing.T, method string, message proto.Message) contract.KVRequest {
	t.Helper()
	b, err := protojson.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return contract.KVRequest{Method: "/etcdserverpb." + method, JSON: string(b)}
}
func etcdRuntimeDo(t *testing.T, l contract.KVRequestLease, method string, message proto.Message) contract.KVResult {
	t.Helper()
	p, err := l.Prepare(etcdRuntimeRequest(t, method, message))
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Execute(context.Background(), p)
	if err != nil || !r.Completed || r.WriteOutcomeUnknown {
		t.Fatal("native runtime completion", err)
	}
	return r
}
func TestEtcdNativeRuntimeOriginalPublicationSnapshotsAndExplicitRecovery(t *testing.T) {
	p, secret := etcdRuntimeProfile(t)
	p.EtcdOptions.PrefixEncoding, p.EtcdOptions.Prefix = "base64", base64.StdEncoding.EncodeToString([]byte{0, 255, 'r'})
	m := etcdRuntimeManager(t)
	if _, err := m.ConnectSession(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	l := etcdRuntimeReserve(t, m, 1)
	key := []byte("/runtime/" + t.Name())
	key = append(key, 0, 255)
	etcdRuntimeDo(t, l, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255, 1}})
	parked := etcdRuntimeDo(t, l, "KV/Range", &pb.RangeRequest{Key: key})
	plan, err := l.Prepare(etcdRuntimeRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("must not replay")}))
	if err != nil {
		t.Fatal(err)
	}
	before := l.Scope()
	before.Prefix[0] = 1
	if l.Scope().Prefix[0] != 0 {
		t.Fatal("public prefix aliases")
	}
	replacement, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Generation() <= l.Scope().Generation {
		t.Fatal("public generation reused")
	}
	if _, err = l.Execute(context.Background(), plan); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("stale lease accepted")
	}
	if !parked.Completed || !bytes.Equal(parked.Reply.Entries[0].Value, []byte{0, 255, 1}) {
		t.Fatal("parked original receipt changed")
	}
	provider, _ := m.KVRequests(p.ID)
	recovered, err := provider.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Lease.Close()
	if recovered.Lease.Scope().Generation != replacement.Generation() {
		t.Fatal("recovery generation wrong")
	}
	if _, err = recovered.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original plan crossed recovery")
	}
	current := etcdRuntimeDo(t, recovered.Lease, "KV/Range", &pb.RangeRequest{Key: key})
	if current.Reply.Entries[0].Version != 1 || !bytes.Equal(current.Reply.Entries[0].Value, []byte{0, 255, 1}) {
		t.Fatal("recovery replayed")
	}
	if _, ok := connections.Describe(connections.Etcd); ok {
		t.Fatal("private runtime published product")
	}
}

// Transparent native proxy forwards original authentication metadata over the
// fixture's real TLS/mTLS upstream, and faults only after a real Put receipt.
type etcdRuntimeFault struct {
	pb.UnimplementedKVServer
	pb.UnimplementedAuthServer
	pb.UnimplementedMaintenanceServer
	upstream      *grpc.ClientConn
	key           []byte
	mode          string
	entered, done chan struct{}
	calls         atomic.Int32
	receipt       *pb.PutResponse
}

func (f *etcdRuntimeFault) context(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	return metadata.NewOutgoingContext(ctx, md.Copy())
}
func (f *etcdRuntimeFault) Authenticate(ctx context.Context, r *pb.AuthenticateRequest) (*pb.AuthenticateResponse, error) {
	return pb.NewAuthClient(f.upstream).Authenticate(f.context(ctx), r)
}
func (f *etcdRuntimeFault) Status(ctx context.Context, r *pb.StatusRequest) (*pb.StatusResponse, error) {
	return pb.NewMaintenanceClient(f.upstream).Status(f.context(ctx), r)
}
func (f *etcdRuntimeFault) Range(ctx context.Context, r *pb.RangeRequest) (*pb.RangeResponse, error) {
	return pb.NewKVClient(f.upstream).Range(f.context(ctx), r)
}
func (f *etcdRuntimeFault) Put(ctx context.Context, r *pb.PutRequest) (*pb.PutResponse, error) {
	client := pb.NewKVClient(f.upstream)
	reply, err := client.Put(f.context(ctx), r)
	if err != nil || !bytes.Equal(r.Key, f.key) {
		return reply, err
	}
	if f.calls.Add(1) != 1 {
		return nil, status.Error(codes.Internal, "original Put replayed")
	}
	f.receipt = proto.Clone(reply).(*pb.PutResponse)
	later, err := client.Put(f.context(ctx), &pb.PutRequest{Key: bytes.Clone(f.key), Value: []byte("later-independent")})
	if err != nil || later.Header.Revision <= reply.Header.Revision {
		return nil, status.Error(codes.Internal, "later independent Put failed")
	}
	close(f.entered)
	defer close(f.done)
	if f.mode == "replace" {
		<-ctx.Done()
		return nil, status.Error(codes.Canceled, "original publication closed")
	}
	return nil, status.Error(codes.Unavailable, "actual processed receipt withheld")
}
func etcdRuntimeProxy(t *testing.T, p connections.Profile, key []byte, mode string) (connections.Profile, *etcdRuntimeFault) {
	t.Helper()
	var creds credentials.TransportCredentials = insecure.NewCredentials()
	if p.TLSMode == "verify-full" {
		pem, err := os.ReadFile(p.EtcdOptions.CAFile)
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			t.Fatal("invalid fixture CA")
		}
		tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.Host, RootCAs: roots}
		if p.EtcdOptions.ClientCertFile != "" {
			pair, err := tls.LoadX509KeyPair(p.EtcdOptions.ClientCertFile, p.EtcdOptions.ClientKeyFile)
			if err != nil {
				t.Fatal(err)
			}
			tc.Certificates = []tls.Certificate{pair}
		}
		creds = credentials.NewTLS(tc)
	}
	conn, err := grpc.NewClient("passthrough:///"+net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), grpc.WithTransportCredentials(creds), grpc.WithDisableRetry(), grpc.WithNoProxy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &etcdRuntimeFault{upstream: conn, key: bytes.Clone(key), mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterKVServer(server, f)
	pb.RegisterAuthServer(server, f)
	pb.RegisterMaintenanceServer(server, f)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	p.Host = "127.0.0.1"
	p.Port = uint16(listener.Addr().(*net.TCPAddr).Port)
	p.TLSMode = "disable"
	p.EtcdOptions.CAFile, p.EtcdOptions.ClientCertFile, p.EtcdOptions.ClientKeyFile = "", "", ""
	return p, f
}
func TestEtcdNativeRuntimeActualProcessedPutFaultAndGenerationReplacement(t *testing.T) {
	p, secret := etcdRuntimeProfile(t)
	for _, mode := range []string{"lost", "replace"} {
		t.Run(mode, func(t *testing.T) {
			key := []byte("/runtime/" + t.Name())
			proxy, f := etcdRuntimeProxy(t, p, key, mode)
			m := etcdRuntimeManager(t)
			if err := m.Connect(context.Background(), proxy, secret); err != nil {
				t.Fatal(err)
			}
			l := etcdRuntimeReserve(t, m, 1)
			plan, err := l.Prepare(etcdRuntimeRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("original")}))
			if err != nil {
				t.Fatal(err)
			}
			type completion struct {
				r   contract.KVResult
				err error
			}
			out := make(chan completion, 1)
			go func() { r, err := l.Execute(context.Background(), plan); out <- completion{r, err} }()
			kvAwait(t, f.entered)
			if mode == "replace" {
				if err = m.Connect(context.Background(), p, secret); err != nil {
					t.Fatal(err)
				}
			}
			var original completion
			select {
			case original = <-out:
			case <-time.After(8 * time.Second):
				t.Fatal("original Put did not settle")
			}
			kvAwait(t, f.done)
			if original.err == nil || !original.r.Attempted || original.r.Completed || !original.r.WriteOutcomeUnknown || original.r.Request != plan.Snapshot() || f.calls.Load() != 1 || f.receipt == nil {
				t.Fatal("actual processed original outcome erased/replayed")
			}
			provider, _ := m.KVRequests(p.ID)
			recovery, err := provider.RecoverKV(l)
			if err != nil {
				t.Fatal(err)
			}
			defer recovery.Lease.Close()
			if mode == "lost" && !recovery.Last.WriteOutcomeUnknown {
				t.Fatal("recovery discarded retained uncertainty")
			}
			if _, err = recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("fresh recovery accepted original plan")
			}
			observed := etcdRuntimeDo(t, recovery.Lease, "KV/Range", &pb.RangeRequest{Key: key})
			if len(observed.Reply.Entries) != 1 || observed.Reply.Entries[0].Version != 2 || string(observed.Reply.Entries[0].Value) != "later-independent" || f.calls.Load() != 1 {
				t.Fatal("later independent value overwritten")
			}
		})
	}
}
func TestEtcdNativeRuntimeLateOriginalHandshakeCannotCloseReplacement(t *testing.T) {
	p, secret := etcdRuntimeProfile(t)
	m := dbruntime.New()
	defer m.Close()
	opened, release := make(chan *etcd.Session, 1), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	m.Register(connections.Etcd, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
		raw, err := etcd.OpenSession(ctx, p, secret)
		if err == nil && calls.Add(1) == 1 {
			opened <- raw.(*etcd.Session)
			<-release
		}
		return raw, err
	}))
	out := make(chan error, 1)
	go func() { _, err := m.ConnectSession(context.Background(), p, secret); out <- err }()
	var original *etcd.Session
	select {
	case original = <-opened:
	case <-time.After(8 * time.Second):
		t.Fatal("original native handshake not observed")
	}
	replacement, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	if err = <-out; err == nil || original.Alive() || m.Generation(p.ID) != replacement.Generation() {
		t.Fatal("late original damaged published replacement")
	}
	etcdRuntimeDo(t, etcdRuntimeReserve(t, m, 1), "KV/Range", &pb.RangeRequest{Key: []byte("/runtime-replacement")})
}
