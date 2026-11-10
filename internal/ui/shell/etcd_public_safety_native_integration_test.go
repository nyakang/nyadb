package shell

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/etcd"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
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

func etcdPublicNative(t *testing.T, c *etcd.Client, message proto.Message) proto.Message {
	t.Helper()
	plan, err := c.Prepare(message)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Execute(context.Background(), plan)
	if err != nil || !result.Completed || result.WriteOutcomeUnknown {
		t.Fatal("original native fixture request", err)
	}
	return result.Native()
}
func etcdPublicFixtureClient(t *testing.T, p connections.Profile, secret string) *etcd.Client {
	t.Helper()
	client, err := etcd.OpenProfile(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
func openEtcdPublicGrid(t *testing.T, p connections.Profile, secret string) (*State, *ui.Tester, *ui.Services, connections.Profile, *dataworkspace.Document) {
	t.Helper()
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
	s, _, profiles, _ := profileTestState(t)
	p.ID = 0
	p, err := profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	s.sessions.Current().Password = secret
	etcdPublicClick(t, tt, "连接所选数据库")
	etcdPublicWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if !s.runtime.IsConnected(p.ID) || s.sessions.StatusFor(p.ID) != "已连接" {
		t.Fatal("original public sessionbrowser handshake", s.sessions.Current().Error)
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, "kv"))
	etcdPublicWait(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["kv"] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "kv", Category: "键", Object: "keys"})
	d := s.currentTab().data
	if d == nil || d.Object() != s.runtime.Namespace(p.ID, "kv").Object("keyspace", "keys") {
		t.Fatal("public qualified data entry lost original identity")
	}
	etcdPublicWait(t, tt, func() bool { return !d.Busy() && !d.Loading() })
	if d.Error() != "" || d.Page() == nil {
		t.Fatal("public grid not loaded", d.Error())
	}
	return s, tt, &services, p, d
}
func etcdPublicPreview(t *testing.T, tt *ui.Tester) {
	t.Helper()
	etcdPublicClick(t, tt, "保存数据更改")
	etcdPublicWait(t, tt, func() bool { _, ok := tt.Find(locales.Text("确认并提交更改")); return ok })
}
func etcdPublicReceipt(t *testing.T, tt *ui.Tester) contract.KVResult {
	t.Helper()
	etcdPublicClick(t, tt, "原生事务回执")
	etcdPublicClick(t, tt, "复制完整回执")
	var receipt contract.KVResult
	if json.Unmarshal([]byte(tt.Clipboard()), &receipt) != nil {
		t.Fatal("original complete receipt is not recoverable")
	}
	etcdPublicClick(t, tt, "关闭")
	return receipt
}

func TestEtcdNativeShellPublicGridConflictKeepsOriginalAtomicBatch(t *testing.T) {
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	client := etcdPublicFixtureClient(t, parsed.Profile, parsed.Password)
	prefix, _ := connections.EtcdPrefixBytes(parsed.Profile.EtcdOptions)
	a, b := append(bytes.Clone(prefix), 1), append(bytes.Clone(prefix), 2)
	etcdPublicNative(t, client, &pb.PutRequest{Key: a, Value: []byte("before-a")})
	etcdPublicNative(t, client, &pb.PutRequest{Key: b, Value: []byte("before-b")})
	s, tt, services, p, d := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	s.dataWorkspace.EditCell(*services, d, 0, "value", contract.Cell{Kind: contract.Binary, Bytes: []byte("original-a")})
	s.dataWorkspace.EditCell(*services, d, 1, "value", contract.Cell{Kind: contract.Binary, Bytes: []byte("original-b")})
	etcdPublicPreview(t, tt)
	etcdPublicClick(t, tt, "复制完整预览")
	var request pb.TxnRequest
	if protojson.Unmarshal([]byte(tt.Clipboard()), &request) != nil || len(request.Compare) != 10 || len(request.Success) != 4 {
		t.Fatal("public preview lost the original complete atomic batch")
	}
	etcdPublicNative(t, client, &pb.PutRequest{Key: b, Value: []byte("independent-b")})
	etcdPublicClick(t, tt, "确认并提交更改")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	if !d.HasDraft() || !s.runtime.IsConnected(p.ID) || !s.dataWorkspace.CanSave(d) {
		t.Fatal("known CAS conflict lost drafts or marked the original session uncertain")
	}
	for _, pair := range []struct{ key, value []byte }{{a, []byte("before-a")}, {b, []byte("independent-b")}} {
		actual := etcdPublicNative(t, client, &pb.RangeRequest{Key: pair.key}).(*pb.RangeResponse)
		if len(actual.Kvs) != 1 || !bytes.Equal(actual.Kvs[0].Value, pair.value) {
			t.Fatal("public CAS conflict partially applied or overwrote independent change")
		}
	}
	receipt := etcdPublicReceipt(t, tt)
	if !receipt.Completed || receipt.Reply == nil || receipt.Reply.Succeeded || receipt.WriteOutcomeUnknown {
		t.Fatal("public conflict lost its genuine native false receipt")
	}
	captureEtcdPublic(t, s, "conflict")
}

func TestEtcdNativeShellPublicGridOneMiBLeaseOnlyPreservesCompleteBytes(t *testing.T) {
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	client := etcdPublicFixtureClient(t, parsed.Profile, parsed.Password)
	prefix, _ := connections.EtcdPrefixBytes(parsed.Profile.EtcdOptions)
	key := append(bytes.Clone(prefix), 0, 255)
	value := bytes.Repeat([]byte{0, 255, 1, 128}, contract.DataEditMaxBytes/4)
	lease := etcdPublicNative(t, client, &pb.LeaseGrantRequest{TTL: 90}).(*pb.LeaseGrantResponse).ID
	defer etcdPublicNative(t, client, &pb.LeaseRevokeRequest{ID: lease})
	etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: value, Lease: lease})
	s, tt, services, _, d := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	if len(d.Page().Rows) != 1 || !d.Page().Rows[0][1].Truncated || len(d.Page().Rows[0][1].Value.Bytes) != 1024 {
		t.Fatal("public large-value preview lost its bound")
	}
	s.dataWorkspace.EditCell(*services, d, 0, "lease", contract.Cell{Kind: contract.Integer, Text: "0"})
	etcdPublicPreview(t, tt)
	etcdPublicClick(t, tt, "复制完整预览")
	var request pb.TxnRequest
	if protojson.Unmarshal([]byte(tt.Clipboard()), &request) != nil || len(request.Compare) != 5 || len(request.Success) != 2 {
		t.Fatal("large preserved-value preview lost five source comparisons")
	}
	put := request.Success[0].GetRequestPut()
	if put == nil || !put.IgnoreValue || len(put.Value) != 0 || put.IgnoreLease || put.Lease != 0 {
		t.Fatal("public omitted value and explicit zero lease collapsed")
	}
	etcdPublicClick(t, tt, "确认并提交更改")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() && !d.Loading() })
	if d.HasDraft() || d.Error() != "" {
		t.Fatal("public 1 MiB lease-only update failed under default server bounds", d.Error())
	}
	actual := etcdPublicNative(t, client, &pb.RangeRequest{Key: key}).(*pb.RangeResponse)
	if len(actual.Kvs) != 1 || actual.Kvs[0].Lease != 0 || !bytes.Equal(actual.Kvs[0].Value, value) {
		t.Fatal("public lease-only update replaced original complete value")
	}
	receipt := etcdPublicReceipt(t, tt)
	if !receipt.Completed || receipt.Reply == nil || !receipt.Reply.Succeeded || len(receipt.Reply.Operations) != 2 || !bytes.Equal(receipt.Reply.Operations[1].Entries[0].Value, value) {
		t.Fatal("public large saved receipt lost complete value")
	}
	captureEtcdPublic(t, s, "large-saved")
}

// This transparent proxy forwards to the real owned node, captures an actual
// successful Txn, performs a later independent Put, then withholds that reply.
type etcdPublicFault struct {
	pb.UnimplementedKVServer
	pb.UnimplementedAuthServer
	pb.UnimplementedMaintenanceServer
	upstream      *grpc.ClientConn
	key           []byte
	mode          string
	entered, done chan struct{}
	calls         atomic.Int32
	receipt       *pb.TxnResponse
}

func (f *etcdPublicFault) context(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	return metadata.NewOutgoingContext(ctx, md.Copy())
}
func (f *etcdPublicFault) Authenticate(ctx context.Context, r *pb.AuthenticateRequest) (*pb.AuthenticateResponse, error) {
	return pb.NewAuthClient(f.upstream).Authenticate(f.context(ctx), r)
}
func (f *etcdPublicFault) Status(ctx context.Context, r *pb.StatusRequest) (*pb.StatusResponse, error) {
	return pb.NewMaintenanceClient(f.upstream).Status(f.context(ctx), r)
}
func (f *etcdPublicFault) Range(ctx context.Context, r *pb.RangeRequest) (*pb.RangeResponse, error) {
	return pb.NewKVClient(f.upstream).Range(f.context(ctx), r)
}
func (f *etcdPublicFault) Txn(ctx context.Context, r *pb.TxnRequest) (*pb.TxnResponse, error) {
	if f.calls.Add(1) != 1 {
		return nil, status.Error(codes.Internal, "original Txn replayed")
	}
	client := pb.NewKVClient(f.upstream)
	reply, err := client.Txn(f.context(ctx), r)
	if err != nil {
		return reply, err
	}
	if !reply.Succeeded || len(reply.Responses) != 2 || len(reply.Responses[1].GetResponseRange().Kvs) != 1 {
		return nil, status.Error(codes.Internal, "fixture did not process original native Txn")
	}
	f.receipt = proto.Clone(reply).(*pb.TxnResponse)
	later, err := client.Put(f.context(ctx), &pb.PutRequest{Key: bytes.Clone(f.key), Value: []byte("later-independent")})
	if err != nil || later.Header.Revision <= reply.Header.Revision {
		return nil, status.Error(codes.Internal, "later independent write failed")
	}
	close(f.entered)
	defer close(f.done)
	if f.mode != "lost" {
		<-ctx.Done()
	}
	return nil, status.Error(codes.Unavailable, "actual committed original Txn reply withheld")
}
func etcdPublicProxy(t *testing.T, p connections.Profile, key []byte, mode string) (connections.Profile, *etcdPublicFault) {
	t.Helper()
	var creds credentials.TransportCredentials = insecure.NewCredentials()
	if p.TLSMode == "verify-full" {
		pem, err := os.ReadFile(p.EtcdOptions.CAFile)
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			t.Fatal("invalid owned fixture CA")
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
	conn, err := grpc.NewClient("passthrough:///"+net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), grpc.WithTransportCredentials(creds), grpc.WithDisableRetry(), grpc.WithDisableServiceConfig(), grpc.WithNoProxy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &etcdPublicFault{upstream: conn, key: bytes.Clone(key), mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterKVServer(server, f)
	pb.RegisterAuthServer(server, f)
	pb.RegisterMaintenanceServer(server, f)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	p.Host, p.Port, p.TLSMode = "127.0.0.1", uint16(listener.Addr().(*net.TCPAddr).Port), "disable"
	p.EtcdOptions.CAFile, p.EtcdOptions.ClientCertFile, p.EtcdOptions.ClientKeyFile = "", "", ""
	return p, f
}
func etcdPublicSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(20 * time.Second):
		t.Fatal("original native fault did not acknowledge")
	}
}
func TestEtcdNativeShellPublicGridActualTxnLossCancelAndOriginalCloseNeverReplay(t *testing.T) {
	for _, mode := range []string{"lost", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			parsed, err := connections.ParseURI(etcdPublicURI(t))
			if err != nil {
				t.Fatal(err)
			}
			upstream := etcdPublicFixtureClient(t, parsed.Profile, parsed.Password)
			prefix, _ := connections.EtcdPrefixBytes(parsed.Profile.EtcdOptions)
			key := append(bytes.Clone(prefix), 1)
			etcdPublicNative(t, upstream, &pb.PutRequest{Key: key, Value: []byte("before")})
			proxy, fault := etcdPublicProxy(t, parsed.Profile, key, mode)
			s, tt, services, p, d := openEtcdPublicGrid(t, proxy, parsed.Password)
			s.dataWorkspace.EditCell(*services, d, 0, "value", contract.Cell{Kind: contract.Binary, Bytes: []byte("original-write")})
			etcdPublicPreview(t, tt)
			etcdPublicClick(t, tt, "确认并提交更改")
			etcdPublicSignal(t, fault.entered)
			if mode == "close" {
				captureEtcdPublic(t, s, "fault-close")
				s.CloseMySQLSessions()
			} else {
				if mode == "cancel" {
					etcdPublicClick(t, tt, "停止数据操作")
				}
				etcdPublicWait(t, tt, func() bool { return !d.Busy() })
				if !d.HasDraft() || s.dataWorkspace.CanSave(d) || !s.runtime.IsConnected(p.ID) {
					t.Fatal("unknown original Txn lost its draft/write barrier or unrelated session")
				}
				receipt := etcdPublicReceipt(t, tt)
				if receipt.Completed || !receipt.WriteOutcomeUnknown || len(receipt.Native) != 0 {
					t.Fatal("lost native acknowledgement accepted as complete")
				}
				captureEtcdPublic(t, s, "fault-"+mode)
			}
			etcdPublicSignal(t, fault.done)
			if fault.calls.Load() != 1 || fault.receipt == nil || !bytes.Equal(fault.receipt.Responses[1].GetResponseRange().Kvs[0].Key, key) || !bytes.Equal(fault.receipt.Responses[1].GetResponseRange().Kvs[0].Value, []byte("original-write")) {
				t.Fatal("fixture did not capture one genuine original successful Txn")
			}
			actual := etcdPublicNative(t, upstream, &pb.RangeRequest{Key: key}).(*pb.RangeResponse)
			if len(actual.Kvs) != 1 || !bytes.Equal(actual.Kvs[0].Value, []byte("later-independent")) {
				t.Fatal("original close/readback replayed or rolled back a later independent write")
			}
			if mode != "close" {
				s.dataWorkspace.Save(*services, d)
				tt.Frame()
				if fault.calls.Load() != 1 || !d.HasDraft() || s.dataWorkspace.CanSave(d) {
					t.Fatal("later readback or explicit Save replayed unknown original Txn")
				}
			} else if s.runtime.IsConnected(p.ID) {
				t.Fatal("original terminal close retained published session")
			}
		})
	}
}
