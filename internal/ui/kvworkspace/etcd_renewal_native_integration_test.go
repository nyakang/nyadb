package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func nativeRenewalState(t *testing.T) (*State, *Document, *dbruntime.Manager, connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_RENEWAL_WORKSPACE_IT") != "1" {
		t.Skip("requires owned continuous renewal workspace fixture")
	}
	p, secret := workspaceProfile(t)
	s, d, m := workspaceNativeState(t, p, secret)
	s.deps.RenewalProviderFor = m.KVRenewal
	return s, d, m, p, secret
}
func renewalInput(id int64) contract.KVRequest {
	return contract.KVRequest{Method: RenewalMethod, JSON: `{"ID":"` + strconv.FormatInt(id, 10) + `"}`}
}
func TestEtcdNativeRenewalWorkspaceOriginalConfirmationContinuousTTLStopAndArchives(t *testing.T) {
	functional, doc, manager, p, secret := nativeRenewalState(t)
	// Short-TTL lifetime evidence runs independently of screenshot rendering.
	grant := workspaceExecute(t, functional, doc, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{ID: 9007199254740993, TTL: 3}))
	key := workspaceKey(t)
	put := workspaceExecute(t, functional, doc, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255}, Lease: grant.Result.Reply.Lease.ID}))
	observer := functional.Open(2, p.ID, DefaultRequest())
	workspaceInput(functional, doc, renewalInput(grant.Result.Reply.Lease.ID))
	functional.Run(doc)
	functional.ConfirmRenewal(doc, true)
	started := time.Now()
	eventEventually(t, func() bool {
		functional.Consume()
		return renewalAcknowledged(doc.renewalReceipt) >= 5 && time.Since(started) > 3*time.Second
	})
	observed := workspaceExecute(t, functional, observer, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
	if len(observed.Result.Reply.Entries) != 1 || observed.Result.Reply.Entries[0].ModRevision != put.Result.Header.Revision {
		t.Fatal("original short-TTL lease expired during authorized renewal")
	}
	functional.StopRenewal(doc)
	consume(t, functional, doc)
	deadline := time.Now().Add(8 * time.Second)
	expired := false
	for time.Now().Before(deadline) {
		read := workspaceExecute(t, functional, observer, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
		if len(read.Result.Reply.Entries) == 0 {
			expired = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !expired {
		t.Fatal("stopped short-TTL original run continued renewing")
	}
	functional.CloseAll()
	manager.Close()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, m := workspaceNativeState(t, p, secret)
			s.deps.RenewalProviderFor = m.KVRenewal
			grant := workspaceExecute(t, s, d, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{ID: 9007199254740993, TTL: 60}))
			id := grant.Result.Reply.Lease.ID
			key := append(workspaceKey(t), []byte(language+strconv.Itoa(size[0]))...)
			key = append(key, bytes.Repeat([]byte{255}, 4096-len(key))...)
			put := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255}, Lease: id}))
			admin := s.Open(2, p.ID, DefaultRequest())
			workspaceInput(s, d, renewalInput(id))
			authored := d.Request()
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			workspaceCapture(t, tt, language, size, "renewal-request")
			workspaceClick(t, tt, "开始持续续租")
			if d.Busy() || d.renewalConfirmation == nil {
				t.Fatal("UI started without continuous authorization")
			}
			workspaceCapture(t, tt, language, size, "renewal-confirmation")
			// A changed editor cannot consume an earlier immutable authorization.
			d.Editor.Buffer.Set(renewalInput(id + 1).JSON)
			s.ConfirmRenewal(d, true)
			if d.Busy() || d.renewalConfirmation != nil {
				t.Fatal("changed lease ID confirmed")
			}
			d.Editor.Buffer.Set(authored.JSON)
			workspaceClick(t, tt, "开始持续续租")
			workspaceClick(t, tt, "我授权持续续租此原租约")
			workspaceClick(t, tt, "确认开始持续续租")
			eventEventually(t, func() bool { tt.Frame(); return renewalAcknowledged(d.renewalReceipt) >= 1 })
			workspaceCapture(t, tt, language, size, "renewal-active")
			observed := workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
			if len(observed.Result.Reply.Entries) != 1 || observed.Result.Reply.Entries[0].Lease != id || observed.Result.Reply.Entries[0].ModRevision != put.Result.Header.Revision {
				t.Fatal("continuous renewal changed/expired original key")
			}
			workspaceClick(t, tt, "停止持续续租")
			consume(t, s, d)
			tt.Frame()
			r := d.renewalReceipt
			if r == nil || r.Authored != authored || r.Batch.Request.LeaseID != id || !r.Batch.Ended || r.Batch.Terminal.WriteOutcomeUnknown || r.Batch.Terminal.Attempts != r.Batch.Terminal.Acknowledged {
				t.Fatal("original acknowledged stop receipt changed", d.Error())
			}
			for _, receipt := range r.Batch.Receipts {
				native := &pb.LeaseKeepAliveResponse{}
				if proto.Unmarshal(receipt.Native, native) != nil || native.ID != id || native.TTL != receipt.TTL || !receipt.Completed {
					t.Fatal("full original protobuf changed")
				}
			}
			workspaceCapture(t, tt, language, size, "renewal-stopped")
			workspaceClick(t, tt, "复制完整续租记录")
			consume(t, s, d)
			want := []byte(tt.Clipboard())
			if !bytes.Contains(want, []byte(`"original_lease_id": "9007199254740993"`)) || !bytes.Contains(want, []byte(`"native_protobuf"`)) {
				t.Fatal("copy lost complete renewal record")
			}
			var exported []byte
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "original-renewal.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
				var b bytes.Buffer
				err := write(&b)
				exported = bytes.Clone(b.Bytes())
				return err
			}})
			workspaceClick(t, tt, "导出续租记录")
			consume(t, s, d)
			if !bytes.Equal(exported, want) {
				t.Fatal("export differs from complete copy")
			}
			workspaceExecute(t, s, admin, workspaceRequest(t, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: id}))
			workspaceClick(t, tt, "清除续租回执")
			consume(t, s, d)
			tt.Frame()
			workspaceCapture(t, tt, language, size, "renewal-cleared")
			s.CloseAll()
			m.Close()
		}
	}
}

type renewalWorkspaceFault struct {
	pb.UnimplementedLeaseServer
	base           *workspaceFault
	mode           string
	after          int32
	streams, calls atomic.Int32
	entered, done  chan struct{}
	receipt        *pb.LeaseKeepAliveResponse
}

func (f *renewalWorkspaceFault) LeaseGrant(ctx context.Context, r *pb.LeaseGrantRequest) (*pb.LeaseGrantResponse, error) {
	return pb.NewLeaseClient(f.base.upstream).LeaseGrant(f.base.context(ctx), r)
}
func (f *renewalWorkspaceFault) LeaseRevoke(ctx context.Context, r *pb.LeaseRevokeRequest) (*pb.LeaseRevokeResponse, error) {
	return pb.NewLeaseClient(f.base.upstream).LeaseRevoke(f.base.context(ctx), r)
}
func (f *renewalWorkspaceFault) LeaseTimeToLive(ctx context.Context, r *pb.LeaseTimeToLiveRequest) (*pb.LeaseTimeToLiveResponse, error) {
	return pb.NewLeaseClient(f.base.upstream).LeaseTimeToLive(f.base.context(ctx), r)
}
func (f *renewalWorkspaceFault) LeaseKeepAlive(down pb.Lease_LeaseKeepAliveServer) error {
	f.streams.Add(1)
	up, err := pb.NewLeaseClient(f.base.upstream).LeaseKeepAlive(f.base.context(down.Context()))
	if err != nil {
		return err
	}
	defer up.CloseSend()
	for {
		request, err := down.Recv()
		if err != nil {
			return err
		}
		if err = up.Send(request); err != nil {
			return err
		}
		response, err := up.Recv()
		if err != nil {
			return err
		}
		if f.calls.Add(1) == f.after {
			f.receipt = proto.Clone(response).(*pb.LeaseKeepAliveResponse)
			close(f.entered)
			if f.mode == "lost" {
				close(f.done)
				return status.Error(codes.Unavailable, "actual original renewal receipt discarded")
			}
			<-down.Context().Done()
			close(f.done)
			return down.Context().Err()
		}
		if err = down.Send(response); err != nil {
			return err
		}
	}
}
func TestEtcdNativeRenewalWorkspaceActualProcessedFirstAndLaterReceiptsAndRecovery(t *testing.T) {
	_, _, _, p, secret := nativeRenewalState(t)
	for _, after := range []int32{1, 2} {
		for _, mode := range []string{"lost", "cancel", "stop", "disconnect", "replace", "close", "parked"} {
			t.Run(strconv.Itoa(int(after))+"/"+mode, func(t *testing.T) {
				var fault *renewalWorkspaceFault
				proxy, _ := workspaceProxyWithLease(t, p, nil, "unused", func(base *workspaceFault) pb.LeaseServer {
					fault = &renewalWorkspaceFault{base: base, mode: mode, after: after, entered: make(chan struct{}), done: make(chan struct{})}
					return fault
				})
				s, d, m := workspaceNativeState(t, proxy, secret)
				s.deps.RenewalProviderFor = m.KVRenewal
				grant := workspaceExecute(t, s, d, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{TTL: 3}))
				id := grant.Result.Reply.Lease.ID
				workspaceInput(s, d, renewalInput(id))
				authored := d.Request()
				scope := d.Scope()
				s.Run(d)
				s.ConfirmRenewal(d, true)
				await(t, fault.entered)
				if after == 2 {
					eventEventually(t, func() bool { s.Consume(); return len(d.renewalReceipt.Batch.Receipts) >= 1 })
				}
				active := true
				s.deps.Active = func(*Document) bool { return active }
				if mode == "parked" {
					active = false
					s.RequestCancel(d)
				}
				if mode == "cancel" {
					s.RequestCancel(d)
				}
				if mode == "stop" {
					s.StopRenewal(d)
				}
				if mode == "disconnect" {
					s.DisconnectProfile(p.ID)
				}
				if mode == "replace" {
					if err := m.Connect(context.Background(), p, secret); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "close" {
					s.Close(d)
				} else {
					if mode == "parked" {
						op := d.renewalJob
						await(t, op.finished)
						s.Consume()
						if d.renewalJob != op {
							t.Fatal("parked terminal consumed")
						}
						active = true
					}
					consume(t, s, d)
					r, ok := d.UnknownRenewalReceipt()
					if !ok || r.Authored != authored || !sameEventScope(r.Scope, scope) || !r.Batch.Terminal.WriteOutcomeUnknown || r.Batch.Terminal.Attempts != uint64(after) || len(r.Batch.Receipts) != int(after) {
						t.Fatal("processed renewal lost original evidence", d.Error())
					}
					if err := m.Connect(context.Background(), p, secret); err != nil {
						t.Fatal(err)
					}
					s.Consume()
					s.Recover(d)
					consume(t, s, d)
					if d.NeedsRecovery() || d.renewalUnknown == nil {
						t.Fatal("recovery failed/lost unknown renewal", d.Error())
					}
					workspaceInput(s, d, workspaceRequest(t, "Lease/LeaseTimeToLive", &pb.LeaseTimeToLiveRequest{ID: id, Keys: true}))
					s.Recover(d)
					consume(t, s, d)
					s.Run(d)
					consume(t, s, d)
					retained, _ := d.UnknownRenewalReceipt()
					if retained.Authored != r.Authored || !retained.Batch.Terminal.WriteOutcomeUnknown {
						t.Fatal("TTL read reclassified original renewal")
					}
					workspaceInput(s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: workspaceKey(t), Value: []byte("never")}))
					s.Run(d)
					if d.confirmation != nil || d.Busy() {
						t.Fatal("native one-shot write bypassed renewal unknown barrier")
					}
					workspaceInput(s, d, authored)
					s.Run(d)
					if d.renewalConfirmation != nil {
						t.Fatal("new continuous run bypassed original unknown")
					}
					raw, err := s.RenewalRecord(d)
					if err != nil {
						t.Fatal(err)
					}
					var record map[string]json.RawMessage
					if json.Unmarshal(raw, &record) != nil || !strings.Contains(string(raw), `"write_outcome_unknown": true`) {
						t.Fatal("unknown archive absent")
					}
				}
				await(t, fault.done)
				if fault.calls.Load() != after || fault.streams.Load() != 1 || fault.receipt.ID != id || fault.receipt.TTL <= 0 {
					t.Fatal("captured original executor replayed or changed lease")
				}
				s.CloseAll()
				m.Close()
			})
		}
	}
}

func TestEtcdNativeRenewalWorkspaceActualOneShotUnknownBlocksContinuousAuthorization(t *testing.T) {
	_, _, _, p, secret := nativeRenewalState(t)
	key := workspaceKey(t)
	proxy, fault := workspaceProxy(t, p, key, "lost")
	s, d, m := workspaceNativeState(t, proxy, secret)
	s.deps.RenewalProviderFor = m.KVRenewal
	workspaceInput(s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255}}))
	s.Run(d)
	s.Confirm(d, true)
	consume(t, s, d)
	original, ok := d.UnknownReceipt()
	if !ok || !original.Result.WriteOutcomeUnknown {
		t.Fatal("actual one-shot unknown fixture did not occur")
	}
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.Consume()
	s.Recover(d)
	consume(t, s, d)
	workspaceInput(s, d, renewalInput(9007199254740993))
	s.Run(d)
	if d.Busy() || d.renewalConfirmation != nil || d.renewalLease != nil {
		t.Fatal("unknown native one-shot authorized continuous writes")
	}
	retained, _ := d.UnknownReceipt()
	if retained.Authored != original.Authored || !retained.Result.WriteOutcomeUnknown || fault.calls.Load() != 1 {
		t.Fatal("continuous path reclassified/replayed original one-shot write")
	}
}
