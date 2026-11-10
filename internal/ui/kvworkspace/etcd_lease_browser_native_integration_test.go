package kvworkspace

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func requireLeaseBrowserWorkspace(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_LEASE_BROWSER_WORKSPACE_IT") != "1" {
		t.Skip("requires owned public original lease browser acceptance")
	}
}

func TestEtcdNativeLeaseBrowserWorkspaceActualUnknownRenewalReadIsolation(t *testing.T) {
	requireLeaseBrowserWorkspace(t)
	p, secret := workspaceProfile(t)
	proxy, _ := workspaceProxyWithLease(t, p, nil, "unused", func(base *workspaceFault) pb.LeaseServer {
		return &browserWorkspaceFault{renewalWorkspaceFault: &renewalWorkspaceFault{base: base, mode: "lost", after: 1, entered: make(chan struct{}), done: make(chan struct{})}}
	})
	s, d, m := workspaceNativeState(t, proxy, secret)
	s.deps.RenewalProviderFor = m.KVRenewal
	grant := workspaceExecute(t, s, d, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{TTL: 120}))
	id := grant.Result.Reply.Lease.ID
	workspaceInput(s, d, renewalInput(id))
	s.Run(d)
	s.ConfirmRenewal(d, true)
	consume(t, s, d)
	unknown, ok := d.UnknownRenewalReceipt()
	if !ok || !unknown.Batch.Terminal.WriteOutcomeUnknown {
		t.Fatal("actual renewal unknown fixture missing", d.Error())
	}
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.Consume()
	s.Recover(d)
	consume(t, s, d)
	workspaceInput(s, d, DefaultLeaseBrowserRequest(LeaseBrowseMethod))
	s.Recover(d)
	consume(t, s, d)
	s.Run(d)
	consume(t, s, d)
	s.SelectLease(d, id)
	s.ReadSelectedLease(d)
	consume(t, s, d)
	retained, ok := d.UnknownRenewalReceipt()
	if d.browserDetail == nil || !ok || retained.Authored != unknown.Authored || !retained.Batch.Terminal.WriteOutcomeUnknown || retained.Scope.Generation != unknown.Scope.Generation {
		t.Fatal("browsing reclassified original renewal", d.Error())
	}
	workspaceInput(s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: workspaceKey(t), Value: []byte("never")}))
	s.Run(d)
	if d.confirmation != nil || d.Busy() {
		t.Fatal("browser cleared renewal unknown barrier")
	}
	admin := s.Open(2, p.ID, DefaultRequest())
	workspaceExecute(t, s, admin, workspaceRequest(t, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: id}))
}
func TestEtcdNativeLeaseBrowserWorkspaceOriginalListDetailCompleteKeysAndArchives(t *testing.T) {
	requireLeaseBrowserWorkspace(t)
	p, secret := workspaceProfile(t)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, m := workspaceNativeState(t, p, secret)
			admin := s.Open(2, p.ID, DefaultRequest())
			id := int64(9007199254741077)
			workspaceExecute(t, s, admin, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{TTL: 120, ID: id}))
			key := workspaceKey(t)
			key = append(key, bytes.Repeat([]byte{255}, 4096-len(key))...)
			workspaceExecute(t, s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255}, Lease: id}))
			for i := 0; i < 260; i++ {
				k := append(workspaceKey(t), []byte(strconv.Itoa(i))...)
				workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: k, Value: []byte{0}, Lease: id}))
			}
			original, _ := d.LastReceipt()
			workspaceInput(s, d, contract.KVRequest{Method: LeaseBrowseMethod, JSON: `{"limit":200}`})
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			workspaceCapture(t, tt, language, size, "lease-browser-request")
			workspaceClick(t, tt, "读取租约观察")
			consume(t, s, d)
			tt.Frame()
			receipt, ok := d.LeasePageReceipt()
			if !ok || !receipt.Page.Complete {
				t.Fatal("native page unavailable", d.Error())
			}
			list := &pb.LeaseLeasesResponse{}
			if proto.Unmarshal(receipt.Page.List.Native, list) != nil || len(list.Leases) != len(receipt.Page.List.Reply.Lease.IDs) {
				t.Fatal("complete original cluster list changed")
			}
			for _, row := range receipt.Page.Rows {
				ttl := &pb.LeaseTimeToLiveResponse{}
				if proto.Unmarshal(row.Result.Native, ttl) != nil || ttl.ID != row.ID || ttl.TTL != row.Result.Reply.Lease.TTL {
					t.Fatal("original per-row TTL receipt changed")
				}
			}
			workspaceCapture(t, tt, language, size, "lease-browser-list")
			s.SelectLease(d, id)
			tt.Frame()
			workspaceClick(t, tt, "读取所选租约详情")
			consume(t, s, d)
			tt.Frame()
			detail, ok := d.LeaseDetailReceipt()
			if !ok || len(detail.Detail.Result.Reply.Lease.Keys) != 261 {
				t.Fatal("native detail truncated complete keys", d.Error())
			}
			native := &pb.LeaseTimeToLiveResponse{}
			if proto.Unmarshal(detail.Detail.Result.Native, native) != nil || native.ID != id || len(native.Keys) != 261 {
				t.Fatal("complete native detail missing")
			}
			found := false
			for _, k := range native.Keys {
				found = found || bytes.Equal(k, key)
			}
			if !found {
				t.Fatal("maximum binary key lost")
			}
			retained, _ := d.LastReceipt()
			if retained.Authored != original.Authored || !bytes.Equal(retained.Result.Native, original.Result.Native) {
				t.Fatal("browser replaced original one-shot")
			}
			workspaceCapture(t, tt, language, size, "lease-browser-detail")
			workspaceClick(t, tt, "复制完整租约记录")
			consume(t, s, d)
			want := []byte(tt.Clipboard())
			if !bytes.Contains(want, []byte(`"requested_id": "9007199254741077"`)) {
				t.Fatal("copy lost exact ID")
			}
			var exported []byte
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "original-browser.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
				var b bytes.Buffer
				err := write(&b)
				exported = bytes.Clone(b.Bytes())
				return err
			}})
			workspaceClick(t, tt, "导出租约记录")
			consume(t, s, d)
			if !bytes.Equal(want, exported) {
				t.Fatal("complete export differs")
			}
			tt.Frame()
			workspaceCapture(t, tt, language, size, "lease-browser-exported")
			workspaceClick(t, tt, "本次浏览回执")
			tt.Frame()
			workspaceCapture(t, tt, language, size, "lease-browser-original-record")
			workspaceClick(t, tt, "清除浏览回执")
			tt.Frame()
			workspaceCapture(t, tt, language, size, "lease-browser-cleared")
			workspaceExecute(t, s, admin, workspaceRequest(t, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: id}))
			s.CloseAll()
			m.Close()
		}
	}
}

type browserWorkspaceFault struct {
	*renewalWorkspaceFault
	target              string
	mode                string
	listCalls, ttlCalls atomic.Int32
	armed               atomic.Bool
	readOrdinal         atomic.Int32
	entered, done       chan struct{}
	last                proto.Message
}

func (f *browserWorkspaceFault) finish(ctx context.Context, r proto.Message) error {
	f.last = proto.Clone(r)
	close(f.entered)
	if f.mode == "lost" {
		close(f.done)
		return status.Error(codes.Unavailable, "actual original lease read receipt discarded")
	}
	<-ctx.Done()
	close(f.done)
	return ctx.Err()
}
func (f *browserWorkspaceFault) LeaseLeases(ctx context.Context, r *pb.LeaseLeasesRequest) (*pb.LeaseLeasesResponse, error) {
	f.listCalls.Add(1)
	v, err := pb.NewLeaseClient(f.base.upstream).LeaseLeases(f.base.context(ctx), r)
	if err == nil && f.target == "list" && f.armed.CompareAndSwap(true, false) {
		return nil, f.finish(ctx, v)
	}
	return v, err
}
func (f *browserWorkspaceFault) LeaseTimeToLive(ctx context.Context, r *pb.LeaseTimeToLiveRequest) (*pb.LeaseTimeToLiveResponse, error) {
	f.ttlCalls.Add(1)
	v, err := pb.NewLeaseClient(f.base.upstream).LeaseTimeToLive(f.base.context(ctx), r)
	if err == nil && f.armed.Load() && (f.target == "ttl" && f.readOrdinal.Add(1) == 2 || f.target == "detail" && r.Keys) {
		f.armed.Store(false)
		return nil, f.finish(ctx, v)
	}
	return v, err
}
func TestEtcdNativeLeaseBrowserWorkspaceActualProcessedReadFaultsAndParkedOriginalRecovery(t *testing.T) {
	requireLeaseBrowserWorkspace(t)
	p, secret := workspaceProfile(t)
	for _, target := range []string{"list", "ttl", "detail"} {
		for _, mode := range []string{"lost", "cancel", "disconnect", "replace", "close", "parked"} {
			t.Run(target+"/"+mode, func(t *testing.T) {
				var fault *browserWorkspaceFault
				proxy, _ := workspaceProxyWithLease(t, p, nil, "unused", func(base *workspaceFault) pb.LeaseServer {
					fault = &browserWorkspaceFault{renewalWorkspaceFault: &renewalWorkspaceFault{base: base}, target: target, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
					return fault
				})
				s, d, m := workspaceNativeState(t, proxy, secret)
				admin := s.Open(2, p.ID, DefaultRequest())
				ids := make([]int64, 3)
				for i := range ids {
					grant := workspaceExecute(t, s, admin, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{TTL: 120}))
					ids[i] = grant.Result.Reply.Lease.ID
				}
				workspaceInput(s, d, contract.KVRequest{Method: LeaseBrowseMethod, JSON: `{"limit":200}`})
				s.Run(d)
				consume(t, s, d)
				cached := d.browserPage
				if cached == nil {
					t.Fatal("first page", d.Error())
				}
				scope := d.Scope()
				fault.armed.Store(true)
				if target == "detail" {
					s.SelectLease(d, ids[0])
					s.ReadSelectedLease(d)
				} else {
					s.Run(d)
				}
				await(t, fault.entered)
				job := d.job
				active := true
				s.deps.Active = func(*Document) bool { return active }
				switch mode {
				case "cancel":
					s.RequestCancel(d)
				case "disconnect":
					s.DisconnectProfile(p.ID)
				case "replace":
					if err := m.Connect(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
				case "close":
					s.Close(d)
				case "parked":
					active = false
					s.RequestCancel(d)
				}
				if mode != "close" {
					if mode == "parked" {
						await(t, job.finished)
						s.Consume()
						if d.job != job {
							t.Fatal("parked receipt consumed")
						}
						active = true
					}
					consume(t, s, d)
					last, ok := d.LastLeaseBrowserReceipt()
					if !ok || !sameEventScope(last.Scope, scope) || last.Error == "" || d.browserPage != cached {
						t.Fatal("fault replaced/lost original cache", d.Error())
					}
					if target == "ttl" && (last.Page == nil || len(last.Page.Rows) < 2 || last.Page.NextAfter != nil || last.Page.Complete) {
						t.Fatal("partial original TTL receipts lost")
					}
					beforeLists, beforeTTL := fault.listCalls.Load(), fault.ttlCalls.Load()
					if err := m.Connect(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
					s.Consume()
					s.Recover(d)
					consume(t, s, d)
					if d.NeedsRecovery() || fault.listCalls.Load() != beforeLists || fault.ttlCalls.Load() != beforeTTL || d.browserPage != cached {
						t.Fatal("recovery reread or changed original receipt")
					}
					raw, err := s.LeaseBrowserRecord(d)
					if err != nil || !bytes.Contains(raw, []byte(`"error"`)) {
						t.Fatal("failure archive unavailable")
					}
				}
				await(t, fault.done)
				if fault.last == nil {
					t.Fatal("fault was not processed upstream")
				}
				s.Consume()
				s.Recover(admin)
				consume(t, s, admin)
				for _, id := range ids {
					workspaceExecute(t, s, admin, workspaceRequest(t, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: id}))
				}
				s.CloseAll()
				m.Close()
			})
		}
	}
}
func TestEtcdNativeLeaseBrowserWorkspaceActualUnknownPutPureRecoveryAndReadIsolation(t *testing.T) {
	requireLeaseBrowserWorkspace(t)
	p, secret := workspaceProfile(t)
	key := workspaceKey(t)
	proxy, fault := workspaceProxyWithLease(t, p, key, "lost", func(base *workspaceFault) pb.LeaseServer {
		return &browserWorkspaceFault{renewalWorkspaceFault: &renewalWorkspaceFault{base: base}}
	})
	s, d, m := workspaceNativeState(t, proxy, secret)
	s.deps.RenewalProviderFor = m.KVRenewal
	workspaceInput(s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255}}))
	s.Run(d)
	s.Confirm(d, true)
	consume(t, s, d)
	unknown, ok := d.UnknownReceipt()
	if !ok || !unknown.Result.WriteOutcomeUnknown {
		t.Fatal("actual unknown fixture failed")
	}
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.Consume()
	s.Recover(d)
	consume(t, s, d)
	admin := s.Open(2, p.ID, DefaultRequest())
	id := int64(9007199254741077)
	workspaceExecute(t, s, admin, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{TTL: 120, ID: id}))
	workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("later independent value"), Lease: id}))
	workspaceInput(s, d, DefaultLeaseBrowserRequest(LeaseBrowseMethod))
	s.Run(d)
	consume(t, s, d)
	s.SelectLease(d, id)
	s.ReadSelectedLease(d)
	consume(t, s, d)
	if d.browserDetail == nil {
		t.Fatal("read after pure recovery failed", d.Error())
	}
	retained, _ := d.UnknownReceipt()
	if retained.Authored != unknown.Authored || !retained.Result.WriteOutcomeUnknown || fault.calls.Load() != 1 {
		t.Fatal("browser reclassified/replayed unknown")
	}
	workspaceInput(s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("never")}))
	s.Run(d)
	if d.confirmation != nil || d.Busy() {
		t.Fatal("browser cleared unknown barrier")
	}
	workspaceInput(s, d, renewalInput(id))
	s.Run(d)
	if d.renewalConfirmation != nil {
		t.Fatal("browser authorized renewal after unknown")
	}
	got := workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
	if !bytes.Equal(got.Result.Reply.Entries[0].Value, []byte("later independent value")) {
		t.Fatal("old write replayed")
	}
	workspaceExecute(t, s, admin, workspaceRequest(t, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: id}))
}
