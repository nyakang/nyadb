package kvworkspace

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func requireLeaseManagementWorkspace(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_LEASE_MANAGEMENT_IT") != "1" {
		t.Skip("requires owned original lease management acceptance")
	}
}

func managementGrant(t *testing.T, s *State, d *Document, id, ttl int64) Receipt {
	t.Helper()
	s.OpenLeaseGrant(d)
	if d.leaseManagement == nil {
		t.Fatal("dedicated Grant unavailable", d.Error())
	}
	d.leaseManagement.id, d.leaseManagement.ttl = strconv.FormatInt(id, 10), strconv.FormatInt(ttl, 10)
	s.PrepareLeaseManagement(d)
	if d.confirmation == nil {
		t.Fatal("native Grant not prepared", d.Error())
	}
	s.Confirm(d, true)
	consume(t, s, d)
	r, ok := d.LastReceipt()
	if !ok {
		t.Fatal("Grant receipt missing")
	}
	return r
}
func managementRevokePreview(t *testing.T, s *State, d *Document, id int64) {
	t.Helper()
	s.OpenLeaseRevoke(d)
	if d.leaseManagement == nil {
		t.Fatal("dedicated Revoke unavailable", d.Error())
	}
	d.leaseManagement.id = strconv.FormatInt(id, 10)
	s.PrepareLeaseManagement(d)
	consume(t, s, d)
	if d.confirmation == nil || d.confirmation.management.Observation == nil {
		t.Fatal("fresh Revoke observation missing", d.Error())
	}
}

func TestEtcdNativeLeaseManagementWorkspaceActualGrantRevokeAndChangingObservation(t *testing.T) {
	requireLeaseManagementWorkspace(t)
	p, secret := workspaceProfile(t)
	s, d, _ := workspaceNativeState(t, p, secret)
	workspaceInput(s, d, DefaultLeaseBrowserRequest(LeaseBrowseMethod))
	original := d.Request()
	generated := managementGrant(t, s, d, 0, 120)
	if !generated.Result.Completed || generated.Result.Reply.Lease.ID == 0 || generated.LeaseManagement.RequestedID != 0 {
		t.Fatal("actual generated Grant identity missing", d.Error())
	}
	id := generated.Result.Reply.Lease.ID
	s.ReadGrantedLease(d)
	consume(t, s, d)
	if d.browserDetail == nil || d.browserDetail.Detail.Request.ID != id {
		t.Fatal("generated ID detail missing", d.Error())
	}
	admin := s.Open(2, p.ID, DefaultRequest())
	managementRevokePreview(t, s, d, id)
	s.Confirm(d, true)
	consume(t, s, d)
	if !d.last.Result.Completed {
		t.Fatal("generated lease revoke failed", d.Error())
	}
	signed := int64(9007199254741337)
	grant := managementGrant(t, s, d, signed, 120)
	if !grant.Result.Completed || grant.Result.Reply.Lease.ID != signed {
		t.Fatal("requested ID narrowed", d.Error())
	}
	duplicate := managementGrant(t, s, d, signed, 120)
	if duplicate.Result.Completed || duplicate.Result.WriteOutcomeUnknown || duplicate.Result.Code != codes.FailedPrecondition.String() {
		t.Fatal("duplicate ID outcome changed", duplicate.Result.Code, d.Error())
	}
	key1, key2 := workspaceKey(t), append(workspaceKey(t), []byte("later")...)
	workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key1, Value: []byte{0, 255}, Lease: signed}))
	managementRevokePreview(t, s, d, signed)
	observation := d.confirmation.management.Observation.Clone()
	if len(observation.Result.Reply.Lease.Keys) != 1 || !bytes.Equal(observation.Result.Reply.Lease.Keys[0], key1) {
		t.Fatal("original attached key observation wrong")
	}
	workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key2, Value: []byte("later"), Lease: signed}))
	s.Confirm(d, true)
	consume(t, s, d)
	if !d.last.Result.Completed || len(d.last.LeaseManagement.Observation.Result.Reply.Lease.Keys) != 1 {
		t.Fatal("revoke guessed atomic observation", d.Error())
	}
	for _, key := range [][]byte{key1, key2} {
		r := workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key}))
		if len(r.Result.Reply.Entries) != 0 {
			t.Fatal("native Revoke failed to remove changed attached keys")
		}
	}
	// The same ID can be granted again after preview. The native API has no
	// incarnation precondition; the immutable authorization still revokes it.
	managementGrant(t, s, d, signed, 120)
	managementRevokePreview(t, s, d, signed)
	workspaceExecute(t, s, admin, workspaceRequest(t, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: signed}))
	workspaceExecute(t, s, admin, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{ID: signed, TTL: 120}))
	workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key2, Value: []byte("new incarnation"), Lease: signed}))
	s.Confirm(d, true)
	consume(t, s, d)
	if !d.last.Result.Completed {
		t.Fatal("same-ID native revoke failed", d.Error())
	}
	if r := workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Range", &pb.RangeRequest{Key: key2})); len(r.Result.Reply.Entries) != 0 {
		t.Fatal("Revoke became client-key simulation")
	}
	managementRevokePreview(t, s, d, signed)
	if d.confirmation.management.Observation.Result.Reply.Lease.TTL != -1 {
		t.Fatal("missing lease sentinel changed")
	}
	s.Confirm(d, true)
	consume(t, s, d)
	if d.last.Result.Completed || d.last.Result.WriteOutcomeUnknown || d.last.Result.Code != codes.NotFound.String() {
		t.Fatal("missing native Revoke outcome changed", d.Error())
	}
	workspaceInput(s, d, original)
	if d.Request() != original {
		t.Fatal("management changed browser editor")
	}
}

func TestEtcdNativeLeaseManagementWorkspaceBilingualCompletePreviewAndRenewalShortcut(t *testing.T) {
	requireLeaseManagementWorkspace(t)
	p, secret := workspaceProfile(t)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, m := workspaceNativeState(t, p, secret)
			s.deps.RenewalProviderFor = m.KVRenewal
			workspaceInput(s, d, DefaultLeaseBrowserRequest(LeaseBrowseMethod))
			s.Run(d)
			consume(t, s, d)
			tt := workspaceTester(s, d, size[0], size[1])
			s.OpenLeaseGrant(d)
			tt.Frame()
			workspaceCapture(t, tt, language, size, "lease-management-grant-form")
			s.PrepareLeaseManagement(d)
			tt.Frame()
			workspaceCapture(t, tt, language, size, "lease-management-grant-confirmation")
			workspaceClick(t, tt, "我确认执行此原生写入请求")
			workspaceClick(t, tt, "确认执行 KV 写入")
			consume(t, s, d)
			id := d.last.Result.Reply.Lease.ID
			tt.Frame()
			workspaceCapture(t, tt, language, size, "lease-management-grant-receipt")
			admin := s.Open(2, p.ID, DefaultRequest())
			key := workspaceKey(t)
			key = append(key, bytes.Repeat([]byte{255}, 4096-len(key))...)
			workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255}, Lease: id}))
			s.ReadGrantedLease(d)
			consume(t, s, d)
			s.PrepareRenewalFromLeaseDetail(d)
			if d.renewalConfirmation == nil || d.renewalConfirmation.plan.Snapshot().LeaseID != id || d.renewalJob != nil {
				t.Fatal("shortcut started/changed renewal", d.Error())
			}
			tt.Frame()
			workspaceCapture(t, tt, language, size, "lease-management-renewal-confirmation")
			s.CancelConfirmation(d)
			s.SetMethod(d, LeaseDetailMethod)
			managementRevokePreview(t, s, d, id)
			tt.Frame()
			workspaceCapture(t, tt, language, size, "lease-management-revoke-confirmation")
			workspaceClick(t, tt, "复制完整撤销观察")
			if !bytes.Contains([]byte(tt.Clipboard()), []byte(`"requested_complete_keys": true`)) {
				t.Fatal("original observation clipboard incomplete")
			}
			workspaceClick(t, tt, "我确认执行此原生写入请求")
			workspaceClick(t, tt, "确认执行 KV 写入")
			consume(t, s, d)
			tt.Frame()
			workspaceCapture(t, tt, language, size, "lease-management-revoke-receipt")
			raw, err := s.OperationRecord(d)
			if err != nil || !bytes.Contains(raw, []byte(`"earlier_revoke_observation"`)) {
				t.Fatal("native complete observation export missing", err)
			}
			s.CloseAll()
			m.Close()
		}
	}
}

type managementWorkspaceFault struct {
	*renewalWorkspaceFault
	kind, mode             string
	entered, done, release chan struct{}
	calls                  atomic.Int32
	receipt                proto.Message
}

func (f *managementWorkspaceFault) processed(ctx context.Context, receipt proto.Message) error {
	if f.calls.Add(1) != 1 {
		return status.Error(codes.Internal, "original lease write replayed")
	}
	f.receipt = proto.Clone(receipt)
	close(f.entered)
	defer close(f.done)
	if f.mode == "parked" {
		<-f.release
		return nil
	}
	if f.mode == "lost" {
		return status.Error(codes.Unavailable, "actual processed lease reply withheld")
	}
	<-ctx.Done()
	return status.Error(codes.Canceled, "original lease executor canceled")
}
func (f *managementWorkspaceFault) LeaseGrant(ctx context.Context, r *pb.LeaseGrantRequest) (*pb.LeaseGrantResponse, error) {
	reply, err := f.renewalWorkspaceFault.LeaseGrant(ctx, r)
	if err != nil || f.kind != "grant" {
		return reply, err
	}
	if f.mode == "foreign-id" {
		f.calls.Add(1)
		f.receipt = proto.Clone(reply)
		close(f.entered)
		close(f.done)
		altered := proto.Clone(reply).(*pb.LeaseGrantResponse)
		altered.ID++
		return altered, nil
	}
	return reply, f.processed(ctx, reply)
}
func (f *managementWorkspaceFault) LeaseRevoke(ctx context.Context, r *pb.LeaseRevokeRequest) (*pb.LeaseRevokeResponse, error) {
	reply, err := f.renewalWorkspaceFault.LeaseRevoke(ctx, r)
	if err != nil || f.kind != "revoke" {
		return reply, err
	}
	return reply, f.processed(ctx, reply)
}

func TestEtcdNativeLeaseManagementWorkspaceActualProcessedFaultsOriginalRecoveryAndClose(t *testing.T) {
	requireLeaseManagementWorkspace(t)
	p, secret := workspaceProfile(t)
	for _, kind := range []string{"grant", "revoke"} {
		for _, mode := range []string{"lost", "cancel", "disconnect", "replace", "close", "parked"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				var f *managementWorkspaceFault
				proxy, _ := workspaceProxyWithLease(t, p, nil, "unused", func(base *workspaceFault) pb.LeaseServer {
					f = &managementWorkspaceFault{renewalWorkspaceFault: &renewalWorkspaceFault{base: base}, kind: kind, mode: mode, entered: make(chan struct{}), done: make(chan struct{}), release: make(chan struct{})}
					return f
				})
				s, d, m := workspaceNativeState(t, proxy, secret)
				workspaceInput(s, d, DefaultLeaseBrowserRequest(LeaseBrowseMethod))
				original := d.Request()
				scope := d.Scope()
				adminState, admin, _ := workspaceNativeState(t, p, secret)
				var id int64
				key := workspaceKey(t)
				if kind == "revoke" {
					grant := workspaceExecute(t, adminState, admin, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{TTL: 120}))
					id = grant.Result.Reply.Lease.ID
					workspaceExecute(t, adminState, admin, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("attached"), Lease: id}))
					managementRevokePreview(t, s, d, id)
				} else {
					s.OpenLeaseGrant(d)
					s.PrepareLeaseManagement(d)
				}
				s.Confirm(d, true)
				await(t, f.entered)
				job := d.job
				active := true
				s.deps.Active = func(*Document) bool { return active }
				switch mode {
				case "cancel":
					s.RequestCancel(d)
				case "disconnect":
					s.DisconnectProfile(d.profileID)
					_ = m.Disconnect(d.profileID)
				case "replace":
					if err := m.Connect(context.Background(), proxy, secret); err != nil {
						t.Fatal(err)
					}
				case "close":
					s.Close(d)
				case "parked":
					active = false
					close(f.release)
					await(t, job.finished)
					s.Consume()
					if d.job != job || d.last != nil {
						t.Fatal("parked original write admitted")
					}
					active = true
				}
				await(t, f.done)
				if kind == "grant" {
					id = f.receipt.(*pb.LeaseGrantResponse).ID
					defer workspaceExecute(t, adminState, admin, workspaceRequest(t, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: id}))
				}
				if mode == "close" {
					if !d.closed || d.last != nil || d.unknown != nil {
						t.Fatal("close retained original caches")
					}
					return
				}
				consume(t, s, d)
				if f.calls.Load() != 1 || d.Request() != original || d.last.Scope.Generation != scope.Generation || d.last.LeaseManagement == nil {
					t.Fatal("original immutable write lost", d.Error())
				}
				if mode == "parked" {
					if !d.last.Result.Completed || d.unknown != nil {
						t.Fatal("acknowledged parked write became unknown")
					}
					return
				}
				unknown := d.unknown
				if unknown == nil || !unknown.Result.WriteOutcomeUnknown || unknown.Result.Completed || !d.NeedsRecovery() {
					t.Fatal("processed original write uncertainty lost", d.Error())
				}
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal(err)
				}
				s.Recover(d)
				consume(t, s, d)
				if d.NeedsRecovery() || d.unknown != unknown || f.calls.Load() != 1 {
					t.Fatal("recovery erased/replayed original")
				}
				s.Run(d)
				consume(t, s, d)
				s.ReadGrantedLease(d)
				s.OpenLeaseGrant(d)
				s.OpenLeaseRevoke(d)
				if d.confirmation != nil || d.leaseManagement != nil || f.calls.Load() != 1 || d.unknown != unknown {
					t.Fatal("read guessed generated ID/settled unknown/replayed")
				}
				if kind == "revoke" {
					workspaceInput(s, d, contract.KVRequest{Method: LeaseDetailMethod, JSON: `{"ID":"` + strconv.FormatInt(id, 10) + `","keys":true}`})
					s.Run(d)
					consume(t, s, d)
					if d.browserDetail == nil || d.browserDetail.Detail.Result.Reply.Lease.TTL != -1 || d.unknown != unknown {
						t.Fatal("later absent TTL settled unknown revoke", d.Error())
					}
				}
			})
		}
	}
}

func TestEtcdNativeLeaseManagementWorkspaceNaturalExpirationBeforeConfirmation(t *testing.T) {
	requireLeaseManagementWorkspace(t)
	p, secret := workspaceProfile(t)
	s, d, _ := workspaceNativeState(t, p, secret)
	workspaceInput(s, d, DefaultLeaseBrowserRequest(LeaseBrowseMethod))
	grant := managementGrant(t, s, d, 0, 1)
	id := grant.Result.Reply.Lease.ID
	managementRevokePreview(t, s, d, id)
	// Wait for this one-second owned lease to expire. The immutable preview
	// remains earlier evidence; native Revoke must report its actual outcome.
	time.Sleep(3 * time.Second)
	s.Confirm(d, true)
	consume(t, s, d)
	if d.last.Result.Completed || d.last.Result.WriteOutcomeUnknown || d.last.Result.Code != codes.NotFound.String() || d.last.LeaseManagement.Observation.Result.Reply.Lease.TTL < 0 {
		t.Fatal("expiration changed original observation/native outcome", d.Error())
	}
}

func TestEtcdNativeLeaseManagementWorkspaceActualPermissionChangeRejectsRevoke(t *testing.T) {
	requireLeaseManagementWorkspace(t)
	p, secret := workspaceProfile(t)
	_, base := workspaceProxyWithLease(t, p, nil, "unused", nil)
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
			if _, err := auth.UserDelete(context.Background(), &pb.AuthUserDeleteRequest{Name: "root"}); err != nil {
				t.Error(err)
			}
		})
	}
	name := "lease-manage-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	key := workspaceKey(t)
	if _, err := auth.UserAdd(ctx, &pb.AuthUserAddRequest{Name: name, Password: secret}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.RoleAdd(ctx, &pb.AuthRoleAddRequest{Name: name}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.RoleGrantPermission(ctx, &pb.AuthRoleGrantPermissionRequest{Name: name, Perm: &authpb.Permission{PermType: authpb.READWRITE, Key: key}}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.UserGrantRole(ctx, &pb.AuthUserGrantRoleRequest{User: name, Role: name}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		auth.UserDelete(ctx, &pb.AuthUserDeleteRequest{Name: name})
		auth.RoleDelete(ctx, &pb.AuthRoleDeleteRequest{Role: name})
	})
	limited := p
	limited.Username = name
	limited.EtcdOptions.Auth = "basic"
	s, d, _ := workspaceNativeState(t, limited, secret)
	workspaceInput(s, d, DefaultLeaseBrowserRequest(LeaseBrowseMethod))
	grant := managementGrant(t, s, d, 0, 120)
	if !grant.Result.Completed {
		t.Fatal("actual nonroot Grant denied", d.Error())
	}
	id := grant.Result.Reply.Lease.ID
	leases := pb.NewLeaseClient(base.upstream)
	t.Cleanup(func() { leases.LeaseRevoke(ctx, &pb.LeaseRevokeRequest{ID: id}) })
	if _, err := pb.NewKVClient(base.upstream).Put(ctx, &pb.PutRequest{Key: key, Value: []byte("authorized earlier"), Lease: id}); err != nil {
		t.Fatal(err)
	}
	managementRevokePreview(t, s, d, id)
	if len(d.confirmation.management.Observation.Result.Reply.Lease.Keys) != 1 {
		t.Fatal("original authorized observation missing key")
	}
	// A later key outside the original user's grant changes the actual server
	// decision without changing the immutable earlier observation.
	foreign := append(bytes.Clone(key), []byte("/outside")...)
	if _, err := pb.NewKVClient(base.upstream).Put(ctx, &pb.PutRequest{Key: foreign, Value: []byte("outside grant"), Lease: id}); err != nil {
		t.Fatal(err)
	}
	s.Confirm(d, true)
	consume(t, s, d)
	if d.last.Result.Completed || d.last.Result.WriteOutcomeUnknown || d.last.Result.Code != codes.PermissionDenied.String() || len(d.last.LeaseManagement.Observation.Result.Reply.Lease.Keys) != 1 {
		t.Fatal("changed native permission bypassed/reclassified original", d.Error())
	}
	observed, err := leases.LeaseTimeToLive(ctx, &pb.LeaseTimeToLiveRequest{ID: id, Keys: true})
	if err != nil || observed.TTL < 0 || len(observed.Keys) != 2 {
		t.Fatal("rejected Revoke deleted original lease", err)
	}
}

func TestEtcdNativeLeaseManagementWorkspaceForeignGrantReplyKeepsUnknownNativeEvidence(t *testing.T) {
	requireLeaseManagementWorkspace(t)
	p, secret := workspaceProfile(t)
	var fault *managementWorkspaceFault
	proxy, _ := workspaceProxyWithLease(t, p, nil, "unused", func(base *workspaceFault) pb.LeaseServer {
		fault = &managementWorkspaceFault{renewalWorkspaceFault: &renewalWorkspaceFault{base: base}, kind: "grant", mode: "foreign-id", entered: make(chan struct{}), done: make(chan struct{})}
		return fault
	})
	s, d, m := workspaceNativeState(t, proxy, secret)
	workspaceInput(s, d, DefaultLeaseBrowserRequest(LeaseBrowseMethod))
	id := int64(9007199254741557)
	r := managementGrant(t, s, d, id, 120)
	await(t, fault.done)
	if r.Result.Completed || !r.Result.WriteOutcomeUnknown || len(r.Result.Native) == 0 || d.unknown == nil || !d.NeedsRecovery() {
		t.Fatal("foreign Grant identity admitted as acknowledgement", d.Error())
	}
	native := &pb.LeaseGrantResponse{}
	if proto.Unmarshal(r.Result.Native, native) != nil || native.ID != id+1 || fault.receipt.(*pb.LeaseGrantResponse).ID != id {
		t.Fatal("original unexpected native evidence discarded")
	}
	adminState, admin, _ := workspaceNativeState(t, p, secret)
	defer workspaceExecute(t, adminState, admin, workspaceRequest(t, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: id}))
	if err := m.Connect(context.Background(), proxy, secret); err != nil {
		t.Fatal(err)
	}
	s.Recover(d)
	consume(t, s, d)
	s.ReadGrantedLease(d)
	if d.Busy() || d.browserDetail != nil || fault.calls.Load() != 1 {
		t.Fatal("foreign ID admitted or original Grant replayed after recovery")
	}
}
