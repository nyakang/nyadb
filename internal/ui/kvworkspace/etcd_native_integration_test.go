package kvworkspace

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/etcd"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/theme"
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

func workspaceProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_WORKSPACE_IT") != "1" {
		t.Skip("requires owned native etcd workspace fixture")
	}
	u, err := url.Parse(os.Getenv("NYADB_ETCD_ENDPOINT"))
	if err != nil || u.Hostname() == "" {
		t.Fatal("missing original fixture")
	}
	p, _ := connections.Defaults(connections.Etcd)
	p.ID, p.Name, p.Host = 71, "native KV workspace", u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	p.Port = uint16(port)
	p.EtcdOptions.ConnectTimeoutSeconds, p.EtcdOptions.RequestTimeoutSeconds = 5, 5
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	p.EtcdOptions.CAFile = os.Getenv("NYADB_ETCD_CA")
	p.EtcdOptions.ClientCertFile = os.Getenv("NYADB_ETCD_CERT")
	p.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_KEY")
	secret := ""
	if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
		p.EtcdOptions.Auth, p.Username, secret = "basic", os.Getenv("NYADB_ETCD_USERNAME"), os.Getenv("NYADB_ETCD_SECRET")
	}
	return p, secret
}
func workspaceNativeState(t *testing.T, p connections.Profile, secret string) (*State, *Document, *dbruntime.Manager) {
	t.Helper()
	m := dbruntime.New()
	m.Register(connections.Etcd, dbruntime.ProviderFunc(etcd.OpenSession))
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := New(Dependencies{ProviderFor: m.KVRequests, ProfileName: func(id int64) string { return "原生 KV · " + strconv.FormatInt(id, 10) }})
	d := s.Open(1, p.ID, DefaultRequest())
	t.Cleanup(s.CloseAll)
	return s, d, m
}
func workspaceRequest(t *testing.T, method string, message proto.Message) contract.KVRequest {
	t.Helper()
	raw, err := protojson.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return contract.KVRequest{Method: "/etcdserverpb." + method, JSON: string(raw)}
}
func workspaceInput(s *State, d *Document, r contract.KVRequest) {
	s.SetMethod(d, r.Method)
	d.Editor.Buffer.Set(r.JSON)
}
func workspaceExecute(t *testing.T, s *State, d *Document, r contract.KVRequest) Receipt {
	t.Helper()
	workspaceInput(s, d, r)
	s.Run(d)
	if d.confirmation != nil {
		s.Confirm(d, true)
	}
	consume(t, s, d)
	receipt, ok := d.LastReceipt()
	if !ok || !receipt.Result.Completed || receipt.Result.WriteOutcomeUnknown || d.Error() != "" {
		t.Fatal("native workspace execution", d.Error())
	}
	return receipt
}
func workspaceKey(t *testing.T) []byte {
	return []byte("/workspace/" + t.Name() + "/" + strconv.FormatInt(time.Now().UnixNano(), 36))
}
func workspaceTester(s *State, d *Document, w, h int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.Invalidate = c.Services().Invalidate
		s.Consume()
		s.View(c, d)
		s.Modals(c)
	}, w, h)
}
func workspaceCapture(t *testing.T, tt *ui.Tester, language string, size [2]int, stage string) {
	t.Helper()
	dir := os.Getenv("NYADB_ETCD_WORKSPACE_IMAGES")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, fmtSize(language, size, stage))
	file, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(file, tt.Image())
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal("native image capture")
	}
}
func fmtSize(language string, size [2]int, stage string) string {
	return language + "-" + strconv.Itoa(size[0]) + "x" + strconv.Itoa(size[1]) + "-" + stage + ".png"
}

func workspaceClick(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	tt.Frame()
	if err := tt.Click(locales.Text(label)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

func TestEtcdNativeWorkspaceActualProcessedFaultRecoveryReadbackAndLeave(t *testing.T) {
	p, secret := workspaceProfile(t)
	for _, mode := range []string{"lost", "cancel", "replace"} {
		t.Run(mode, func(t *testing.T) {
			key := workspaceKey(t)
			proxy, fault := workspaceProxy(t, p, key, mode)
			s, d, manager := workspaceNativeState(t, proxy, secret)
			originalScope := d.Scope()
			put := workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("original")})
			workspaceInput(s, d, put)
			s.Run(d)
			s.Confirm(d, true)
			await(t, fault.entered)
			if mode == "cancel" {
				s.RequestCancel(d)
				if !d.Busy() || !d.CancelRequested() {
					t.Fatal("cancel discarded owning execution before UI receipt")
				}
			} else if mode == "replace" {
				if err := manager.Connect(context.Background(), p, secret); err != nil {
					t.Fatal(err)
				}
			}
			consume(t, s, d)
			await(t, fault.done)
			unknown, ok := d.UnknownReceipt()
			if !ok || !unknown.Result.Attempted || unknown.Result.Completed || !unknown.Result.WriteOutcomeUnknown || unknown.Authored != put || unknown.Scope.Generation != originalScope.Generation || !d.NeedsRecovery() || fault.calls.Load() != 1 || fault.receipt == nil {
				t.Fatal("actual processed write lost original uncertainty")
			}
			if mode == "lost" {
				workspaceFaultImages(t, s, d, "unknown", false)
			}
			s.Recover(d)
			consume(t, s, d)
			if d.NeedsRecovery() || fault.calls.Load() != 1 || d.last != d.unknown {
				t.Fatal("explicit recovery dispatched or erased original receipt")
			}
			s.Run(d)
			if d.Busy() || d.confirmation != nil || fault.calls.Load() != 1 {
				t.Fatal("unknown original write replay enabled")
			}
			observed := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
			if len(observed.Result.Reply.Entries) != 1 || observed.Result.Reply.Entries[0].Version != 2 || string(observed.Result.Reply.Entries[0].Value) != "later-independent" || d.unknown == d.last || d.unknown.Authored != put || fault.calls.Load() != 1 {
				t.Fatal("new observation resolved unknown receipt or replayed original")
			}
			if mode == "lost" {
				workspaceFaultImages(t, s, d, "recovery-read", false)
				workspaceFaultImages(t, s, d, "unknown-close", true)
			}
			continued := false
			if !s.GuardLeave(d, func(ui.Services) { continued = true }) {
				t.Fatal("unknown write bypassed original close decision")
			}
			s.DecideLeave(ui.Services{}, false)
			if continued || d.unknown == nil {
				t.Fatal("cancel close erased uncertainty")
			}
			s.GuardLeave(d, func(ui.Services) { continued = true })
			s.DecideLeave(ui.Services{}, true)
			if !continued || !d.unknown.Result.WriteOutcomeUnknown {
				t.Fatal("discard decision claimed completion/rollback")
			}
			s.Close(d)
			if !d.closed || d.Editor != nil || d.last != nil || d.unknown != nil || d.lease != nil {
				t.Fatal("original close retained request caches")
			}
		})
	}
}

func workspaceFaultImages(t *testing.T, s *State, d *Document, stage string, leave bool) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			if leave {
				s.GuardLeave(d, func(ui.Services) {})
			}
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			workspaceCapture(t, tt, language, size, stage)
			if leave {
				workspaceClick(t, tt, "取消")
				if s.leave != nil || d.unknown == nil {
					t.Fatal("native cancel-close control lost original receipt")
				}
			}
		}
	}
}

func TestEtcdNativeWorkspaceParkedOriginalCompletionAndStalePreview(t *testing.T) {
	p, secret := workspaceProfile(t)
	s, d, manager := workspaceNativeState(t, p, secret)
	active := true
	s.deps.Active = func(doc *Document) bool { return active && doc == d }
	key := workspaceKey(t)
	first := workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("original")})
	workspaceInput(s, d, first)
	s.Run(d)
	s.Confirm(d, true)
	job := d.job
	await(t, job.finished)
	active = false
	s.Consume()
	if d.last != nil || d.job != job {
		t.Fatal("parked completion consumed outside original document")
	}
	active = true
	s.Consume()
	if d.last == nil || !d.last.Result.Completed || d.last.Authored != first {
		t.Fatal("original parked completion missing")
	}
	workspaceInput(s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("never-write")}))
	s.Run(d)
	if d.confirmation == nil {
		t.Fatal("write preview missing")
	}
	originalGeneration := d.Scope().Generation
	if err := manager.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.Confirm(d, true)
	if d.Busy() || !d.NeedsRecovery() || d.confirmation != nil || !d.last.Result.Completed {
		t.Fatal("replaced session executed old preview or erased confirmed original")
	}
	s.Recover(d)
	consume(t, s, d)
	if d.Scope().Generation <= originalGeneration {
		t.Fatal("explicit recovery reused original generation")
	}
	read := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
	if string(read.Result.Reply.Entries[0].Value) != "original" || read.Result.Reply.Entries[0].Version != 1 {
		t.Fatal("stale preview changed real key")
	}
}

func TestEtcdNativeWorkspaceLocalizedNativeControlsAndCompletePreview(t *testing.T) {
	p, secret := workspaceProfile(t)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, _ := workspaceNativeState(t, p, secret)
			key := workspaceKey(t)
			key = append(key, []byte(language+strconv.Itoa(size[0]))...)
			value := []byte{0, 255, 1}
			put := workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: value})
			workspaceInput(s, d, put)
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			workspaceCapture(t, tt, language, size, "request-draft")
			workspaceClick(t, tt, "运行 KV 请求")
			if d.Busy() || d.confirmation == nil {
				t.Fatal("native run control dispatched unconfirmed write")
			}
			workspaceCapture(t, tt, language, size, "write-preview")
			canonical := d.confirmation.plan.Snapshot()
			workspaceClick(t, tt, "复制完整请求")
			if tt.Clipboard() != canonical.Method+"\n"+canonical.JSON {
				t.Fatal("confirmation copy changed immutable request")
			}
			workspaceClick(t, tt, "我确认执行此原生写入请求")
			workspaceClick(t, tt, "确认执行 KV 写入")
			consume(t, s, d)
			tt.Frame()
			if d.last == nil || !d.last.Result.Completed || d.last.Authored != put {
				t.Fatal("native checkbox confirmation lost original write")
			}
			workspaceCapture(t, tt, language, size, "put-receipt")
			rangeReceipt := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
			tt.Frame()
			workspaceCapture(t, tt, language, size, "binary-range")
			workspaceClick(t, tt, "复制完整 JSON")
			if tt.Clipboard() != rangeReceipt.Result.JSON {
				t.Fatal("native clipboard changed response")
			}
			txn := workspaceRequest(t, "KV/Txn", &pb.TxnRequest{Compare: []*pb.Compare{{Key: key, Target: pb.Compare_VERSION, Result: pb.Compare_EQUAL, TargetUnion: &pb.Compare_Version{Version: 0}}}, Success: []*pb.RequestOp{{Request: &pb.RequestOp_RequestPut{RequestPut: &pb.PutRequest{Key: key, Value: []byte("never")}}}}, Failure: []*pb.RequestOp{{Request: &pb.RequestOp_RequestRange{RequestRange: &pb.RangeRequest{Key: key}}}}})
			result := workspaceExecute(t, s, d, txn)
			if result.Result.Reply.Succeeded || !result.Result.Completed {
				t.Fatal("false comparison changed native outcome")
			}
			tt.Frame()
			workspaceCapture(t, tt, language, size, "txn-false")
			grant := workspaceExecute(t, s, d, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{TTL: 30}))
			tt.Frame()
			workspaceCapture(t, tt, language, size, "lease-receipt")
			workspaceExecute(t, s, d, workspaceRequest(t, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: grant.Result.Reply.Lease.ID}))
		}
	}
}
func TestEtcdNativeWorkspaceMethodsPureConfirmationAndExactReceipts(t *testing.T) {
	p, secret := workspaceProfile(t)
	p.EtcdOptions.PrefixEncoding, p.EtcdOptions.Prefix = "base64", "AP8="
	s, d, _ := workspaceNativeState(t, p, secret)
	key := append(workspaceKey(t), 0, 255)
	put := workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255, 1}})
	workspaceInput(s, d, put)
	s.Run(d)
	if d.Busy() || d.last != nil || d.confirmation == nil {
		t.Fatal("pure original preparation dispatched")
	}
	s.Confirm(d, false)
	if d.Busy() {
		t.Fatal("unchecked write dispatched")
	}
	s.Confirm(d, true)
	consume(t, s, d)
	read := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
	if !bytes.Equal(read.Result.Reply.Entries[0].Key, key) || !bytes.Equal(read.Result.Reply.Entries[0].Value, []byte{0, 255, 1}) || !bytes.Equal(read.Scope.Prefix, []byte{0, 255}) {
		t.Fatal("absolute binary input lost original scope")
	}
	grant := workspaceExecute(t, s, d, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{TTL: 30, ID: 9007199254740993}))
	id := grant.Result.Reply.Lease.ID
	if id != 9007199254740993 {
		t.Fatal("lease integer narrowed")
	}
	workspaceExecute(t, s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("leased"), Lease: id, PrevKv: true}))
	for _, r := range []contract.KVRequest{workspaceRequest(t, "Lease/LeaseTimeToLive", &pb.LeaseTimeToLiveRequest{ID: id, Keys: true}), workspaceRequest(t, "Lease/LeaseKeepAlive", &pb.LeaseKeepAliveRequest{ID: id}), workspaceRequest(t, "Lease/LeaseLeases", &pb.LeaseLeasesRequest{}), workspaceRequest(t, "Maintenance/Status", &pb.StatusRequest{})} {
		workspaceExecute(t, s, d, r)
	}
	txn := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Txn", &pb.TxnRequest{Compare: []*pb.Compare{{Key: key, Result: pb.Compare_EQUAL, Target: pb.Compare_CREATE, TargetUnion: &pb.Compare_CreateRevision{CreateRevision: 0}}}, Success: []*pb.RequestOp{{Request: &pb.RequestOp_RequestPut{RequestPut: &pb.PutRequest{Key: key, Value: []byte("must not write")}}}}, Failure: []*pb.RequestOp{{Request: &pb.RequestOp_RequestRange{RequestRange: &pb.RangeRequest{Key: key}}}}}))
	if txn.Result.Reply.Succeeded || txn.Result.Reply.Operations[0].Entries[0].Lease != id || ReceiptStatus(txn.Result) != locales.Text("KV 比较未满足") {
		t.Fatal("native comparison false changed meaning")
	}
	workspaceExecute(t, s, d, workspaceRequest(t, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: id}))
	workspaceExecute(t, s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("delete")}))
	deleted := workspaceExecute(t, s, d, workspaceRequest(t, "KV/DeleteRange", &pb.DeleteRangeRequest{Key: key, PrevKv: true}))
	if deleted.Result.Reply.Deleted != 1 {
		t.Fatal("native delete receipt lost")
	}
	if d, ok := connections.Describe(connections.Etcd); !ok || !d.Capabilities.KeyValues || d.Capabilities.Query {
		t.Fatal("native KV workspace descriptor boundary changed")
	}
}
func TestEtcdNativeWorkspaceMVCCAndDeniedRequestRemainExplicit(t *testing.T) {
	p, secret := workspaceProfile(t)
	s, d, _ := workspaceNativeState(t, p, secret)
	key := workspaceKey(t)
	original := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("original")}))
	revision := original.Result.Header.Revision
	workspaceExecute(t, s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("later")}))
	old := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key, Revision: revision}))
	current := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
	if string(old.Result.Reply.Entries[0].Value) != "original" || string(current.Result.Reply.Entries[0].Value) != "later" {
		t.Fatal("fixed revision request silently changed")
	}
	if p.EtcdOptions.Auth == "basic" && p.EtcdOptions.ClientCertFile == "" {
		p.Username, p.EtcdOptions.Prefix = "restricted", "/allowed/"
		limited, doc, _ := workspaceNativeState(t, p, secret)
		workspaceExecute(t, limited, doc, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: []byte("/allowed/key")}))
		workspaceInput(limited, doc, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: []byte("/allowed/key"), Value: []byte("denied")}))
		limited.Run(doc)
		limited.Confirm(doc, true)
		consume(t, limited, doc)
		if doc.last == nil || doc.last.Result.Code != "PermissionDenied" || doc.last.Result.Completed || doc.unknown != nil || doc.needsRecovery {
			t.Fatal("readable probe inferred write permission or changed definite rejection")
		}
	}
}
func TestEtcdNativeWorkspaceLargeClipboardFilesAndCapturedSave(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := workspaceProfile(t)
	s, d, _ := workspaceNativeState(t, p, secret)
	key := workspaceKey(t)
	value := bytes.Repeat([]byte{0, 255, 1, 2}, 30<<10)
	workspaceExecute(t, s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: value}))
	receipt := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
	if len(receipt.Result.JSON) <= previewLimit {
		t.Fatal("fixture did not exceed preview")
	}
	tt := workspaceTester(s, d, 720, 520)
	tt.Frame()
	if err := tt.Click(locales.Text("复制完整 JSON")); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != receipt.Result.JSON {
		t.Fatal("clipboard truncated complete receipt")
	}
	folder := t.TempDir()
	s.SetFiles(Files{SavePath: func(name string) (string, error) { return filepath.Join(folder, name), nil }, WriteStream: queries.WriteStream, OpenReader: func(path string) (io.ReadCloser, error) { return os.Open(path) }})
	s.ExportOperationRecord(d)
	consume(t, s, d)
	raw, err := os.ReadFile(filepath.Join(folder, "operation.nyakv-receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if json.Unmarshal(raw, &record) != nil {
		t.Fatal("invalid complete operation record")
	}
	var native []byte
	if json.Unmarshal(record["native_protobuf"], &native) != nil {
		t.Fatal("native protobuf absent")
	}
	actual := &pb.RangeResponse{}
	if proto.Unmarshal(native, actual) != nil || !bytes.Equal(actual.Kvs[0].Value, value) {
		t.Fatal("complete native export changed bytes")
	}
	saved := d.Request()
	s.SaveRequest(d, true)
	consume(t, s, d)
	requestRaw, err := os.ReadFile(filepath.Join(folder, "Range.nyakv"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := DecodeRequest(requestRaw)
	if err != nil || reopened != saved || d.Dirty() {
		t.Fatal("original request file did not round-trip")
	}
	s.SetFiles(Files{OpenPath: func() (string, error) { return filepath.Join(folder, "Range.nyakv"), nil }, OpenReader: func(path string) (io.ReadCloser, error) { return os.Open(path) }})
	d.Editor.Buffer.Set("replace only after explicit file open")
	s.OpenRequest(d)
	consume(t, s, d)
	if d.Request() != saved || d.Busy() || d.last.Result.Request != receipt.Result.Request {
		t.Fatal("request load executed or replaced original receipt")
	}
	if !strings.Contains(receipt.Result.JSON, base64.StdEncoding.EncodeToString(value)) {
		t.Fatal("native binary JSON changed")
	}
}

// Transparent native proxy forwards original authentication metadata over the
// fixture's real TLS/mTLS upstream, and faults only after a real Put receipt.
type workspaceFault struct {
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

func (f *workspaceFault) context(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	return metadata.NewOutgoingContext(ctx, md.Copy())
}
func (f *workspaceFault) AuthStatus(ctx context.Context, r *pb.AuthStatusRequest) (*pb.AuthStatusResponse, error) {
	return pb.NewAuthClient(f.upstream).AuthStatus(f.context(ctx), r)
}
func (f *workspaceFault) UserGet(ctx context.Context, r *pb.AuthUserGetRequest) (*pb.AuthUserGetResponse, error) {
	return pb.NewAuthClient(f.upstream).UserGet(f.context(ctx), r)
}
func (f *workspaceFault) RoleGet(ctx context.Context, r *pb.AuthRoleGetRequest) (*pb.AuthRoleGetResponse, error) {
	return pb.NewAuthClient(f.upstream).RoleGet(f.context(ctx), r)
}
func (f *workspaceFault) Authenticate(ctx context.Context, r *pb.AuthenticateRequest) (*pb.AuthenticateResponse, error) {
	return pb.NewAuthClient(f.upstream).Authenticate(f.context(ctx), r)
}
func (f *workspaceFault) Status(ctx context.Context, r *pb.StatusRequest) (*pb.StatusResponse, error) {
	return pb.NewMaintenanceClient(f.upstream).Status(f.context(ctx), r)
}
func (f *workspaceFault) Range(ctx context.Context, r *pb.RangeRequest) (*pb.RangeResponse, error) {
	return pb.NewKVClient(f.upstream).Range(f.context(ctx), r)
}
func (f *workspaceFault) Put(ctx context.Context, r *pb.PutRequest) (*pb.PutResponse, error) {
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
	if f.mode == "replace" || f.mode == "cancel" {
		<-ctx.Done()
		return nil, status.Error(codes.Canceled, "original publication closed")
	}
	return nil, status.Error(codes.Unavailable, "actual processed receipt withheld")
}
func workspaceProxy(t *testing.T, p connections.Profile, key []byte, mode string) (connections.Profile, *workspaceFault) {
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
	f := &workspaceFault{upstream: conn, key: bytes.Clone(key), mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
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
