package etcd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func memberNativeClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("NYADB_ETCD_MEMBER_NATIVE_IT") != "1" {
		t.Skip("requires owned member protocol fixture")
	}
	return nativeClient(t)
}

func TestEtcdNativeMemberDirectoryZeroRevisionAndOriginalEmptyDefragmentAck(t *testing.T) {
	c := memberNativeClient(t)
	list := nativeDo(t, c, &pb.MemberListRequest{})
	v := list.Native().(*pb.MemberListResponse)
	if list.Header.ClusterID != c.Identity().ClusterID || list.Header.MemberID != c.Identity().MemberID || list.Header.Revision != 0 || v.Header.Revision != 0 || len(v.Members) != 1 || v.Members[0].ID != c.Identity().MemberID || len(v.Members[0].ClientURLs) == 0 {
		t.Fatal("original member directory borrowed a KV revision or lost identity", v)
	}
	for _, linearizable := range []bool{false, true} {
		nativeDo(t, c, &pb.MemberListRequest{Linearizable: linearizable})
	}
	cfg, secret := nativeConfig(t)
	m, handshake, err := OpenMemberObserved(context.Background(), cfg, secret, c.Identity())
	if err != nil {
		t.Fatal("explicit original member open", err)
	}
	if !handshake.Attempted || !handshake.Completed || handshake.Native() == nil || handshake.Header.MemberID != c.Identity().MemberID {
		t.Fatal("actual original member handshake receipt lost")
	}
	defer m.Close()
	r := nativeDo(t, m, &pb.DefragmentRequest{})
	native := r.Native().(*pb.DefragmentResponse)
	before, ok := r.Before()
	if !ok || !before.Completed || before.Header.ClusterID != c.Identity().ClusterID || before.Header.MemberID != c.Identity().MemberID || before.Native().(*pb.StatusResponse).Version != c.Identity().Version || native.Header != nil || proto.Size(native) != 0 || r.Header != (Header{}) {
		t.Fatal("empty member ACK/independent full Status were rewritten", r)
	}
	if b, _ := json.Marshal(native); string(b) != "{}" {
		t.Fatal("native empty ACK was not retained", string(b))
	}
	wrong := c.Identity()
	wrong.MemberID++
	foreign, rejected, err := OpenMemberObserved(context.Background(), cfg, secret, wrong)
	if err == nil || foreign != nil || !rejected.Attempted || rejected.Completed || rejected.Native() == nil || rejected.Header.MemberID != c.Identity().MemberID || rejected.WriteOutcomeUnknown {
		if foreign != nil {
			foreign.Close()
		}
		t.Fatal("foreign target opened or actual rejected handshake was rewritten", err)
	}
	if !c.Alive() || !m.Alive() {
		t.Fatal("rejected target closed captured original clients")
	}
}

func TestEtcdNativeMemberDefragmentActualProcessedFaultsNeverReplayOrSettleByLaterStatus(t *testing.T) {
	admin := memberNativeClient(t)
	cfg, secret := nativeConfig(t)
	for _, mode := range []string{"lost", "cancel", "close", "foreign", "zero-header", "extensions", "before-foreign", "before-member"} {
		t.Run(mode, func(t *testing.T) {
			c, f := memberRelayClient(t, admin, cfg, secret, mode)
			p, err := c.Prepare(&pb.DefragmentRequest{})
			if err != nil || !p.Writes() || f.calls.Load() != 0 {
				t.Fatal("preparation dispatched original maintenance", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				r   Response
				err error
			}
			out := make(chan outcome, 1)
			go func() { r, err := c.Execute(ctx, p); out <- outcome{r, err} }()
			beforeFailure := mode == "before-foreign" || mode == "before-member"
			if !beforeFailure {
				waitSignal(t, f.entered)
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					if err := c.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			result := <-out
			before, exists := result.r.Before()
			if !exists || before.Native() == nil || !before.Attempted || before.WriteOutcomeUnknown {
				t.Fatal("lost original pre-dispatch Status", result.err)
			}
			if beforeFailure {
				if result.err == nil || result.r.Attempted || result.r.Completed || result.r.WriteOutcomeUnknown || before.Completed || f.calls.Load() != 0 {
					t.Fatal("changed member dispatched maintenance", result.err)
				}
				return
			}
			waitSignal(t, f.done)
			if f.receipt == nil || f.receipt.Header != nil || proto.Size(f.receipt) != 0 || f.request == nil || proto.Size(f.request) != 0 || f.calls.Load() != 1 || !before.Completed {
				t.Fatal("fault was not after a genuine original empty server ACK")
			}
			if mode == "extensions" {
				if result.err != nil || !result.r.Completed || result.r.WriteOutcomeUnknown || !bytes.Equal(result.r.Native().ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) {
					t.Fatal("original maintenance extension lost", result.err)
				}
			} else if result.err == nil || !result.r.Attempted || result.r.Completed || !result.r.WriteOutcomeUnknown || c.Alive() {
				t.Fatal("processed missing/invalid ACK confirmed or transport reused", result.err)
			}
			// DB size, leader and later Status observations cannot acknowledge the
			// original operation. They are deliberately on another original client.
			nativeDo(t, admin, &pb.StatusRequest{})
			nativeDo(t, admin, &pb.MemberListRequest{})
			if _, err := c.Execute(context.Background(), p); err == nil || f.calls.Load() != 1 || result.r.Completed != (mode == "extensions") {
				t.Fatal("later observation replayed or settled original maintenance")
			}
		})
	}
}

func TestEtcdNativeMemberDefragmentActualRestrictedTokenAndCertificateIdentity(t *testing.T) {
	_ = memberNativeClient(t)
	admin := accessNativeAdmin(t)
	key := nativeKey(t)
	user := "member-" + strconv.FormatUint(admin.Identity().MemberID, 36)
	accessNativeUser(t, admin, user, user, []*authpb.Permission{{PermType: authpb.READWRITE, Key: key}})
	cfg, _ := nativeConfig(t)
	cfg.Auth, cfg.Username = "basic", user
	limited, err := OpenMember(context.Background(), cfg, os.Getenv("NYADB_ETCD_SECRET"), admin.Identity())
	if err != nil {
		t.Fatal("maintenance-only handshake required KV probe permission", err)
	}
	defer limited.Close()
	p, _ := limited.Prepare(&pb.DefragmentRequest{})
	r, err := limited.Execute(context.Background(), p)
	before, ok := r.Before()
	expectedCode := codes.PermissionDenied
	if strings.HasPrefix(admin.Identity().Version, "3.5.") {
		expectedCode = codes.Unknown
	}
	if status.Code(err) != expectedCode || r.Code != expectedCode || r.Completed || r.WriteOutcomeUnknown || !limited.Alive() || !ok || !before.Completed || !r.Attempted {
		t.Fatal("key permission authorized maintenance or denial misclassified", err, r)
	}
	if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
		cfg, _ = nativeConfig(t)
		cfg.Auth, cfg.Username = "none", ""
		certificate, err := OpenMember(context.Background(), cfg, "", admin.Identity())
		if err != nil {
			t.Fatal(err)
		}
		defer certificate.Close()
		nativeDo(t, certificate, &pb.DefragmentRequest{})
		if certificate.principal != "root" {
			t.Fatal("original certificate principal changed")
		}
	}
}

func TestEtcdNativeMemberDefragmentBeforeDispatchCancellationAndOriginalPlanOwnership(t *testing.T) {
	c := memberNativeClient(t)
	p, _ := c.Prepare(&pb.DefragmentRequest{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := c.Execute(ctx, p)
	if !errors.Is(err, context.Canceled) || r.Attempted || r.Completed || r.WriteOutcomeUnknown {
		t.Fatal("canceled pre-dispatch operation sent", err)
	}
	if _, ok := r.Before(); ok {
		t.Fatal("canceled request performed an automatic observation")
	}
	other := memberNativeClient(t)
	if _, err := other.Execute(context.Background(), p); !errors.Is(err, ErrUsed) {
		t.Fatal("foreign original-member plan used", err)
	}
	// Only this explicit call uses the still-unattempted original request.
	if r, err := c.Execute(context.Background(), p); err != nil || !r.Completed {
		t.Fatal("explicit uncanceled original dispatch failed", err)
	}
	if _, err := c.Execute(context.Background(), p); !errors.Is(err, ErrUsed) {
		t.Fatal("original member request replayed", err)
	}
}
