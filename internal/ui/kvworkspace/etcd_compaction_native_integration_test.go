package kvworkspace

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func requireCompactionWorkspace(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PUBLIC_COMPACTION_IT") != "1" {
		t.Skip("requires owned public Compact fixture")
	}
}

func compactionWorkspaceSeed(t *testing.T, s *State, d *Document) ([]byte, int64) {
	t.Helper()
	key := workspaceKey(t)
	workspaceExecute(t, s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("before")}))
	last := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("after")}))
	return key, last.Result.Header.Revision
}

func compactionWorkspacePrepare(t *testing.T, s *State, d *Document, revision int64, physical bool) {
	t.Helper()
	workspaceInput(s, d, DefaultCompactionRequest())
	s.SetCompactionInputs(d, strconv.FormatInt(revision, 10), physical)
	s.ObserveCompaction(d)
	consume(t, s, d)
	if !compactionObservationReady(d, d.compactionObservation) {
		t.Fatal("original independent Status/Range observation missing", d.Error())
	}
	s.PrepareCompaction(d)
	if d.confirmation == nil || d.Busy() {
		t.Fatal("original Compact preview missing or automatically executed", d.Error())
	}
}

func compactionWorkspaceBounds(t *testing.T, tt *ui.Tester, size [2]int, labels ...string) {
	t.Helper()
	for _, label := range labels {
		r, ok := tt.Find(locales.Text(label))
		if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
			t.Fatal("original Compact control clipped", label, size, r)
		}
	}
}

func TestEtcdNativeCompactionWorkspaceBilingualLogicalPhysicalOriginalControlsAndArchives(t *testing.T) {
	requireCompactionWorkspace(t)
	p, secret := workspaceProfile(t)
	p.EtcdOptions.PrefixEncoding, p.EtcdOptions.Prefix = "base64", "AP8="
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, physical := range []bool{false, true} {
				s, d, _ := workspaceNativeState(t, p, secret)
				key, revision := compactionWorkspaceSeed(t, s, d)
				workspaceInput(s, d, DefaultCompactionRequest())
				s.SetCompactionInputs(d, strconv.FormatInt(revision, 10), false)
				tt := workspaceTester(s, d, size[0], size[1])
				tt.Frame()
				if physical {
					workspaceClick(t, tt, "等待物理压缩完成")
				}
				phase := "compact-logical-"
				if physical {
					phase = "compact-physical-"
				}
				compactionWorkspaceBounds(t, tt, size, "压缩 revision", "等待物理压缩完成", "检查 revision")
				workspaceCapture(t, tt, language, size, phase+"form")
				workspaceClick(t, tt, "检查 revision")
				consume(t, s, d)
				tt.Frame()
				if !compactionObservationReady(d, d.compactionObservation) || d.compactionObservation.Physical != physical {
					t.Fatal("public independent observation missing", d.Error())
				}
				workspaceCapture(t, tt, language, size, phase+"observation")
				workspaceClick(t, tt, "复制完整观察")
				want, _ := compactionObservationJSON(d.compactionObservation.Clone())
				if tt.Clipboard() != string(want) {
					t.Fatal("complete Status/Range copy changed")
				}
				var exported []byte
				s.SetFiles(Files{SavePath: func(string) (string, error) { return "compact.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
					var b bytes.Buffer
					err := write(&b)
					exported = bytes.Clone(b.Bytes())
					return err
				}})
				workspaceClick(t, tt, "导出 Compact 观察")
				consume(t, s, d)
				if !bytes.Equal(want, exported) {
					t.Fatal("complete observation export changed")
				}
				workspaceClick(t, tt, "预览 Compact")
				if d.confirmation == nil {
					t.Fatal("explicit preview unavailable", d.Error())
				}
				compactionWorkspaceBounds(t, tt, size, "确认压缩原集群历史", "复制完整 Compact 预览", "我确认压缩此原集群的历史", "确认执行 Compact", "取消")
				workspaceCapture(t, tt, language, size, phase+"confirmation")
				workspaceClick(t, tt, "复制完整 Compact 预览")
				preview, _ := compactionPreviewJSON(d.confirmation.compaction)
				if tt.Clipboard() != string(preview) {
					t.Fatal("immutable preview copy changed")
				}
				// A later independent write changes current revision. The captured
				// observation is not a CAS or a promise about confirmation time.
				admin, ad, _ := workspaceNativeState(t, p, secret)
				workspaceExecute(t, admin, ad, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("later")}))
				workspaceClick(t, tt, "我确认压缩此原集群的历史")
				workspaceClick(t, tt, "确认执行 Compact")
				consume(t, s, d)
				tt.Frame()
				r := d.last
				native := &pb.CompactionResponse{}
				if r == nil || r.Compaction == nil || !r.Result.Completed || r.Result.WriteOutcomeUnknown || r.Compaction.Revision != revision || r.Compaction.Physical != physical || proto.Unmarshal(r.Result.Native, native) != nil || native.Header == nil || r.Result.Header.Revision <= r.Compaction.Observation.Steps[0].Result.Header.Revision {
					t.Fatal("actual Compact acknowledgement or earlier observation lost", d.Error())
				}
				workspaceCapture(t, tt, language, size, phase+"receipt")
				workspaceClick(t, tt, "复制完整 JSON")
				want, _ = s.OperationRecord(d)
				if tt.Clipboard() != string(want) {
					t.Fatal("complete operation copy changed")
				}
				workspaceClick(t, tt, "导出操作记录")
				consume(t, s, d)
				if !bytes.Equal(want, exported) {
					t.Fatal("original native receipt export changed")
				}
				workspaceClick(t, tt, "保存请求")
				consume(t, s, d)
				file := bytes.Clone(exported)
				decoded, err := DecodeRequest(file)
				if err != nil || decoded != d.Request() {
					t.Fatal("Compact request file changed", err)
				}
				s.SetFiles(Files{OpenPath: func() (string, error) { return "compact.nyakv", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(file)), nil }})
				before := d.last
				s.OpenRequest(d)
				consume(t, s, d)
				if d.Busy() || d.last != before || d.confirmation != nil {
					t.Fatal("opening file executed Compact")
				}
				if !s.GuardLeave(d, nil) {
					t.Fatal("saved observed Compact lost leave guard")
				}
				s.DecideLeave(ui.Services{}, false)
				plan, _ := ad.lease.Prepare(workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key, Revision: revision - 1}))
				old, e := ad.lease.Execute(context.Background(), plan)
				if e == nil || old.Code != "OutOfRange" {
					t.Fatal("prefix incorrectly limited whole-cluster Compact")
				}
				current := workspaceExecute(t, admin, ad, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
				if len(current.Result.Reply.Entries) != 1 || !bytes.Equal(current.Result.Reply.Entries[0].Value, []byte("later")) {
					t.Fatal("Compact changed current value")
				}
			}
		}
	}
}

type compactWorkspaceFault struct {
	base                   *workspaceFault
	mode                   string
	calls                  atomic.Int32
	entered, done, release chan struct{}
	request                *pb.CompactionRequest
	receipt                *pb.CompactionResponse
}

func (f *compactWorkspaceFault) serve(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	request, response := authWorkspaceMessages(method)
	if method == CompactMethod {
		request, response = &pb.CompactionRequest{}, &pb.CompactionResponse{}
	}
	if request == nil {
		return status.Error(codes.Unimplemented, "owned Compact relay unsupported method")
	}
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	if err := f.base.upstream.Invoke(f.base.context(stream.Context()), method, request, response); err != nil {
		return err
	}
	if method == CompactMethod {
		if f.calls.Add(1) != 1 {
			return status.Error(codes.Internal, "original Compact replayed")
		}
		f.request, f.receipt = proto.Clone(request).(*pb.CompactionRequest), proto.Clone(response).(*pb.CompactionResponse)
		close(f.entered)
		defer close(f.done)
		switch f.mode {
		case "lost":
			return status.Error(codes.Unavailable, "processed Compact reply withheld")
		case "cancel", "close", "replace", "disconnect":
			<-stream.Context().Done()
			return stream.Context().Err()
		case "park":
			select {
			case <-f.release:
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		case "foreign":
			response.(*pb.CompactionResponse).Header.ClusterId++
		case "missing-header":
			response.(*pb.CompactionResponse).Header = nil
		case "extensions":
			response.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
		}
	}
	return stream.SendMsg(response)
}

func compactWorkspaceProxy(t *testing.T, p connections.Profile, mode string) (connections.Profile, *compactWorkspaceFault) {
	t.Helper()
	_, base := workspaceProxy(t, p, nil, "unused")
	f := &compactWorkspaceFault{base: base, mode: mode, entered: make(chan struct{}), done: make(chan struct{}), release: make(chan struct{})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnknownServiceHandler(f.serve))
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	p.Host, p.Port, p.TLSMode = "127.0.0.1", uint16(listener.Addr().(*net.TCPAddr).Port), "disable"
	p.EtcdOptions.CAFile, p.EtcdOptions.ClientCertFile, p.EtcdOptions.ClientKeyFile = "", "", ""
	return p, f
}

func TestEtcdNativeCompactionWorkspaceActualProcessedFaultsParkingRecoveryAndIndependentReads(t *testing.T) {
	requireCompactionWorkspace(t)
	p, secret := workspaceProfile(t)
	for _, physical := range []bool{false, true} {
		for _, mode := range []string{"lost", "cancel", "close", "park", "replace", "disconnect", "foreign", "missing-header", "extensions"} {
			t.Run(strconv.FormatBool(physical)+"/"+mode, func(t *testing.T) {
				admin, ad, _ := workspaceNativeState(t, p, secret)
				key, revision := compactionWorkspaceSeed(t, admin, ad)
				proxy, fault := compactWorkspaceProxy(t, p, mode)
				s, d, m := workspaceNativeState(t, proxy, secret)
				compactionWorkspacePrepare(t, s, d, revision, physical)
				original := d.scope.Clone()
				active := true
				s.deps.Active = func(candidate *Document) bool { return active && candidate == d }
				s.Confirm(d, true)
				finished := d.job.finished
				await(t, fault.entered)
				switch mode {
				case "cancel":
					s.RequestCancel(d)
					if !d.Busy() {
						t.Fatal("cancel settled before acknowledgement")
					}
				case "close":
					s.Close(d)
				case "replace":
					if err := m.Connect(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
				case "disconnect":
					s.DisconnectProfile(d.profileID)
					m.Disconnect(d.profileID)
				case "park":
					active = false
					close(fault.release)
					await(t, finished)
					s.Consume()
					if !d.Busy() || d.last != nil {
						t.Fatal("parked completion entered current document")
					}
					active = true
				}
				await(t, finished)
				await(t, fault.done)
				if fault.receipt == nil || fault.request.Revision != revision || fault.request.Physical != physical {
					t.Fatal("fault preceded actual server processing")
				}
				if mode == "close" {
					if d.Editor != nil || d.lease != nil || d.compactionObservation != nil {
						t.Fatal("close retained original caches")
					}
					return
				}
				consume(t, s, d)
				if mode == "extensions" || mode == "park" {
					if d.last == nil || !d.last.Result.Completed || d.last.Compaction == nil || d.unknown != nil {
						t.Fatal("actual acknowledged Compact became unknown", d.Error())
					}
					if mode == "extensions" {
						native := &pb.CompactionResponse{}
						if proto.Unmarshal(d.last.Result.Native, native) != nil || !bytes.Equal(native.ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) {
							t.Fatal("native extension dropped")
						}
					}
					return
				}
				if d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || d.unknown.Compaction == nil || d.unknown.Scope.Generation != original.Generation || len(d.unknown.Compaction.Observation.Steps) != 2 || !d.NeedsRecovery() {
					t.Fatal("original unknown Compact or full observation lost", d.Error())
				}
				unknown := d.unknown
				if mode == "lost" {
					workspaceFaultImages(t, s, d, "compact-unknown-"+strconv.FormatBool(physical), true)
				}
				if err := m.Connect(context.Background(), p, secret); err != nil {
					t.Fatal(err)
				}
				s.Recover(d)
				consume(t, s, d)
				if fault.calls.Load() != 1 || compactionObservationReady(d, d.compactionObservation) || d.unknown != unknown {
					t.Fatal("recovery reused/replayed original write")
				}
				s.ObserveCompaction(d)
				consume(t, s, d)
				s.PrepareCompaction(d)
				if d.confirmation != nil || fault.calls.Load() != 1 {
					t.Fatal("later observation cleared original unknown")
				}
				workspaceExecute(t, s, d, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
				if d.unknown != unknown || !bytes.Equal(d.last.Result.Reply.Entries[0].Value, []byte("after")) {
					t.Fatal("later current read settled missing original receipt")
				}
				plan, _ := ad.lease.Prepare(workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key, Revision: revision - 1}))
				r, e := ad.lease.Execute(context.Background(), plan)
				if e == nil || r.Code != "OutOfRange" || d.unknown != unknown {
					t.Fatal("historical rejection substituted for original acknowledgement")
				}
				s.deps.EventsProviderFor = m.KVEvents
				workspaceInput(s, d, eventHistoryInput(key, revision-1, revision))
				if d.NeedsRecovery() {
					s.Recover(d)
					consume(t, s, d)
					if d.eventReceipt != nil || d.unknown != unknown {
						t.Fatal("event recovery executed history or changed unknown Compact")
					}
				}
				s.Run(d)
				consume(t, s, d)
				if d.unknown != unknown || d.eventReceipt == nil || d.eventReceipt.History == nil || d.eventReceipt.History.Complete {
					t.Fatal("compacted history settled unknown Compact or moved original window", d.Error())
				}
				s.ClearEventReceipt(d)
				consume(t, s, d)
				workspaceInput(s, d, eventWatchInput(key, nil, revision-1))
				s.Run(d)
				consume(t, s, d)
				if d.unknown != unknown || d.eventReceipt == nil || !d.eventReceipt.Batch.Ended {
					t.Fatal("compacted Watch settled unknown Compact", d.Error())
				}
				workspaceInput(s, d, DefaultCompactionRequest())
				s.SetCompactionInputs(d, strconv.FormatInt(revision+1, 10), physical)
				s.ObserveCompaction(d)
				consume(t, s, d)
				s.Run(d)
				if d.confirmation != nil || d.unknown != unknown || fault.calls.Load() != 1 {
					t.Fatal("changed revision bypassed document-wide write barrier")
				}
				if mode == "lost" {
					tt := workspaceTester(s, d, 1280, 800)
					workspaceClick(t, tt, "原未确认回执")
					if !d.selectedUnknown || d.selectedEvents || d.selectedRenewal || d.selectedBrowser {
						t.Fatal("original unknown Compact control displayed a later read")
					}
					workspaceFaultImages(t, s, d, "compact-unknown-recovered-"+strconv.FormatBool(physical), false)
					before, err := s.OperationRecord(d)
					if err != nil || !bytes.Contains(before, []byte(`"write_outcome_unknown": true`)) || !bytes.Contains(before, []byte(`"earlier_observation"`)) {
						t.Fatal("recovered original unknown operation record incomplete", err)
					}
					workspaceClick(t, tt, "复制完整 JSON")
					if tt.Clipboard() != string(before) {
						t.Fatal("original unknown Compact clipboard displayed a later event record")
					}
				}
			})
		}
	}
}

func TestEtcdNativeCompactionWorkspaceActualFutureRepeatedAndRestrictedDenials(t *testing.T) {
	requireCompactionWorkspace(t)
	p, secret := workspaceProfile(t)
	s, d, _ := workspaceNativeState(t, p, secret)
	_, revision := compactionWorkspaceSeed(t, s, d)
	for i, n := range []int64{revision + 1000, revision, revision} {
		compactionWorkspacePrepare(t, s, d, n, false)
		s.Confirm(d, true)
		consume(t, s, d)
		r := d.last
		if i != 1 {
			if r.Result.Completed || r.Result.WriteOutcomeUnknown || r.Result.Code != "OutOfRange" || d.NeedsRecovery() {
				t.Fatal("native future/already-compacted refusal became acknowledgement or unknown", d.Error())
			}
		} else if !r.Result.Completed {
			t.Fatal("valid native compaction rejected", d.Error())
		}
	}
	_, base := workspaceProxy(t, p, nil, "unused")
	auth := pb.NewAuthClient(base.upstream)
	ctx := context.Background()
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" {
		secret = os.Getenv("NYADB_ETCD_SECRET")
		if _, err := auth.UserAdd(ctx, &pb.AuthUserAddRequest{Name: "root", Password: secret}); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.UserGrantRole(ctx, &pb.AuthUserGrantRoleRequest{User: "root", Role: "root"}); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.AuthEnable(ctx, &pb.AuthEnableRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	root, err := auth.Authenticate(ctx, &pb.AuthenticateRequest{Name: "root", Password: secret})
	if err != nil {
		t.Fatal(err)
	}
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("token", root.Token))
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" {
		t.Cleanup(func() {
			if _, err := auth.AuthDisable(ctx, &pb.AuthDisableRequest{}); err != nil {
				t.Error(err)
			}
			auth.UserDelete(context.Background(), &pb.AuthUserDeleteRequest{Name: "root"})
		})
	}
	user := authWorkspaceName(t)
	if _, err := auth.UserAdd(ctx, &pb.AuthUserAddRequest{Name: user, Password: secret}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { auth.UserDelete(ctx, &pb.AuthUserDeleteRequest{Name: user}) })
	limited := p
	limited.EtcdOptions.Auth, limited.Username = "basic", user
	ls, ld, _ := workspaceNativeState(t, limited, secret)
	compactionWorkspacePrepare(t, ls, ld, revision+1, true)
	if ld.compactionObservation.Steps[1].Result.Code != "PermissionDenied" {
		t.Fatal("actual probe permission refusal lost")
	}
	ls.Confirm(ld, true)
	consume(t, ls, ld)
	if ld.last.Result.Completed || ld.last.Result.WriteOutcomeUnknown || ld.last.Result.Code != "PermissionDenied" || ld.NeedsRecovery() {
		t.Fatal("actual nonroot Compact bypassed native permissions", ld.Error())
	}
	if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
		cert := p
		cert.EtcdOptions.Auth, cert.Username = "certificate", ""
		cs, cd, _ := workspaceNativeState(t, cert, "")
		_, n := compactionWorkspaceSeed(t, cs, cd)
		compactionWorkspacePrepare(t, cs, cd, n, true)
		cs.Confirm(cd, true)
		consume(t, cs, cd)
		if !cd.last.Result.Completed || cd.last.Compaction == nil {
			t.Fatal("actual certificate root identity failed Compact", cd.Error())
		}
	}
}
