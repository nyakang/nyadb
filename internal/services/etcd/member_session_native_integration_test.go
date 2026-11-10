package etcd

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func memberSessionNative(t *testing.T) *Session {
	t.Helper()
	if os.Getenv("NYADB_ETCD_MEMBER_SESSION_IT") != "1" {
		t.Skip("requires owned member session fixture")
	}
	return nativeSession(t)
}

func nativeBindRootMember(t *testing.T, l contract.KVRequestLease, endpoint string) contract.KVMemberLease {
	t.Helper()
	f := l.(contract.KVMembers)
	o, err := f.ObserveMembers(context.Background())
	if err != nil {
		t.Fatal("original MemberList", err)
	}
	defer o.Close()
	s := l.Scope()
	b, err := f.BindMember(context.Background(), o, contract.KVMemberTarget{MemberID: s.Header.MemberID, Endpoint: endpoint, Version: s.Version})
	if err != nil || b.Lease == nil {
		t.Fatal("original selected member", err)
	}
	t.Cleanup(func() { b.Lease.Close() })
	return b.Lease
}

func TestEtcdNativeMemberSessionOriginalDirectoryTransportAndEmptyAck(t *testing.T) {
	s := memberSessionNative(t)
	l := reserveNativeKV(t, s, 31)
	cfg, _ := nativeConfig(t)
	m := nativeBindRootMember(t, l, cfg.Endpoint)
	if m.Scope().Original.Header != l.Scope().Header || m.Scope().DocumentID != 31 || m.Scope().Directory.Result.Header.Revision != 0 || !m.Scope().Handshake.Completed {
		t.Fatal("original directory/handshake scope changed")
	}
	p, err := m.PrepareDefragment()
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.ExecuteMember(context.Background(), p)
	if err != nil || !r.Result.Completed || !r.Result.NativeCaptured || len(r.Result.Native) != 0 || r.Result.Header != (contract.KVHeader{}) || r.Result.Reply.Kind != "defragment" || !r.Before.Completed || !r.DirectoryCheck.Result.Completed || r.Sequence != 1 {
		t.Fatal("empty native ACK/original observations lost", err, r)
	}
	if _, err := m.ExecuteMember(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("one-use member write replayed", err)
	}
	status, err := m.ObserveStatus(context.Background())
	if err != nil || !status.Result.Completed || status.Sequence <= r.Sequence {
		t.Fatal("independent original Status", err)
	}
	if l.LastResult().Attempted {
		t.Fatal("member receipts replaced ordinary KV receipt")
	}
}

func TestEtcdNativeMemberSessionActualProcessedFaultsAndPureRecovery(t *testing.T) {
	original := memberSessionNative(t)
	cfg, secret := nativeConfig(t)
	admin := nativeClient(t)
	for _, mode := range []string{"lost", "cancel", "close", "retire", "foreign", "extensions"} {
		t.Run(mode, func(t *testing.T) {
			root, _ := memberRelayClient(t, admin, cfg, secret, "ok")
			s := newSession(original.profile, secret, root)
			s.opener = func(ctx context.Context, _ connections.Profile, _ string) (*Client, error) {
				return Open(ctx, root.config, secret)
			}
			defer s.Close()
			l := reserveNativeKV(t, s, 39)
			faultMode := mode
			if mode == "retire" {
				faultMode = "close"
			}
			target, f := memberRelayClient(t, admin, cfg, secret, faultMode)
			m := nativeBindRootMember(t, l, target.config.Endpoint)
			p, _ := m.PrepareDefragment()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				r   contract.KVMemberResult
				err error
			}
			out := make(chan completion, 1)
			go func() { r, err := m.ExecuteMember(ctx, p); out <- completion{r, err} }()
			select {
			case <-f.entered:
			case <-time.After(8 * time.Second):
				t.Fatal("native Defragment not processed")
			}
			switch mode {
			case "cancel":
				cancel()
			case "close":
				m.Close()
			case "retire":
				s.Close()
			}
			got := <-out
			select {
			case <-f.done:
			case <-time.After(8 * time.Second):
				t.Fatal("original executor did not acknowledge")
			}
			if f.calls.Load() != 1 || f.receipt == nil || !got.r.Before.Completed || !got.r.DirectoryCheck.Result.Completed {
				t.Fatal("actual original ACK/observations missing", got.err)
			}
			if mode == "extensions" {
				if got.err != nil || !got.r.Result.Completed || got.r.Result.WriteOutcomeUnknown || len(got.r.Result.Native) == 0 {
					t.Fatal("successful extended ACK changed", got.err)
				}
			} else if got.err == nil || !got.r.Result.WriteOutcomeUnknown || got.r.Result.Completed {
				t.Fatal("processed native unknown settled", got.err)
			}
			fresh := memberSessionNative(t)
			recovery, err := fresh.RecoverKV(l)
			if err != nil || recovery.Lease == nil || len(recovery.MemberResults) != 1 || f.calls.Load() != 1 || recovery.MemberResults[0].Scope.DocumentID != 39 || recovery.MemberResults[0].Scope.LeaseID != got.r.Scope.LeaseID {
				t.Fatal("recovery lost original evidence/replayed", err, len(recovery.MemberResults))
			}
			defer recovery.Lease.Close()
			if mode != "extensions" {
				write, _ := recovery.Lease.Prepare(nativeKVRequest(t, &pb.PutRequest{Key: []byte("/member-session-no-replay"), Value: []byte("blocked")}))
				if _, err := recovery.Lease.Execute(context.Background(), write); !errors.Is(err, contract.ErrKVWriteUnknown) {
					t.Fatal("pure recovery cleared original unknown barrier", err)
				}
			}
		})
	}
}

func TestEtcdNativeMemberSessionRestrictedIdentityAndCertificateTransport(t *testing.T) {
	_ = memberSessionNative(t)
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" {
		return
	}
	p, secret := sessionProfile(t)
	p.Username, p.EtcdOptions.Auth, p.EtcdOptions.Prefix = "restricted", "basic", "/allowed/"
	raw, err := OpenSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	l := reserveNativeKV(t, s, 43)
	o, err := l.(contract.KVMembers).ObserveMembers(context.Background())
	if err != nil || o == nil || !o.Snapshot().Result.Completed || o.Snapshot().Result.WriteOutcomeUnknown {
		t.Fatal("restricted identity's actual MemberList receipt lost", err)
	}
	defer o.Close()
	cfg, _ := nativeConfig(t)
	b, err := l.(contract.KVMembers).BindMember(context.Background(), o, contract.KVMemberTarget{MemberID: l.Scope().Header.MemberID, Endpoint: cfg.Endpoint, Version: l.Scope().Version})
	if err != nil || b.Lease == nil {
		t.Fatal("restricted member Status handshake required maintenance permission", err)
	}
	defer b.Lease.Close()
	plan, _ := b.Lease.PrepareDefragment()
	r, err := b.Lease.ExecuteMember(context.Background(), plan)
	code := "PermissionDenied"
	if strings.HasPrefix(l.Scope().Version, "3.5.") {
		code = "Unknown"
	}
	if err == nil || !r.Result.Attempted || r.Result.Completed || r.Result.WriteOutcomeUnknown || r.Result.Code != code || !r.Before.Completed || !r.DirectoryCheck.Result.Completed {
		t.Fatal("MemberList authorized Defragment or definite native denial changed", err, r.Result.Code)
	}
	if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
		p, _ = sessionProfile(t)
		p.Username, p.EtcdOptions.Auth = "", "certificate"
		raw, err = OpenSession(context.Background(), p, "")
		if err != nil {
			t.Fatal("certificate root session", err)
		}
		certificate := raw.(*Session)
		defer certificate.Close()
		lease := reserveNativeKV(t, certificate, 44)
		cfg, _ := nativeConfig(t)
		m := nativeBindRootMember(t, lease, cfg.Endpoint)
		if m.(*memberLease).client.principal != "root" || m.(*memberLease).client.config.Auth != "none" {
			t.Fatal("member lost original certificate identity")
		}
		plan, _ := m.PrepareDefragment()
		r, err := m.ExecuteMember(context.Background(), plan)
		if err != nil || !r.Result.Completed || strings.Contains(r.Result.JSON, "password") {
			t.Fatal("certificate maintenance ACK", err)
		}
	}
}
