package etcd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func nativeRenewalClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("NYADB_ETCD_RENEWAL_IT") != "1" {
		t.Skip("requires owned original lease-renewal fixture")
	}
	return nativeClient(t)
}
func renewalNativeDone(t *testing.T, r *RenewalRun) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(8 * time.Second):
		t.Fatal("original renewal executor did not settle")
	}
}
func TestEtcdNativeRenewalOriginalExactLeaseSurvivesThenExpiresAfterExplicitStop(t *testing.T) {
	admin, c := nativeRenewalClient(t), nativeRenewalClient(t)
	grant := nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 3, ID: 9007199254740993}).Native().(*pb.LeaseGrantResponse)
	key := nativeKey(t)
	key = append(key, bytes.Repeat([]byte{255}, 4096-len(key))...)
	put := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255, 7}, Lease: grant.ID})
	request := &pb.LeaseKeepAliveRequest{ID: grant.ID}
	plan, err := c.PrepareRenewal(request)
	if err != nil {
		t.Fatal(err)
	}
	request.ID = 1
	r, err := c.StartRenewal(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Request().ID != grant.ID {
		t.Fatal("run replaced original exact ID")
	}
	if _, err := c.StartRenewal(context.Background(), plan); !errors.Is(err, ErrBusy) {
		t.Fatal("parallel original lease admitted", err)
	}
	started := time.Now()
	deadline := started.Add(12 * time.Second)
	var receipts []RenewalReceipt
	for time.Now().Before(deadline) {
		batch := r.Poll()
		receipts = append(receipts, batch...)
		if len(batch) > 0 && time.Since(started) >= time.Duration(grant.TTL+1)*time.Second {
			break
		}
		if terminal, ended := r.Terminal(); ended {
			t.Fatal("authorized continuous run stopped", terminal)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(receipts) < 3 || time.Since(started) < time.Duration(grant.TTL+1)*time.Second {
		t.Fatal("lease was not maintained past its original TTL")
	}
	observed := nativeDo(t, admin, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse)
	if len(observed.Kvs) != 1 || observed.Kvs[0].Lease != grant.ID || observed.Kvs[0].Version != 1 || observed.Kvs[0].ModRevision != put.Header.Revision || !bytes.Equal(observed.Kvs[0].Value, []byte{0, 255, 7}) {
		t.Fatal("renewal changed original key identity/value/MVCC")
	}
	for i, v := range receipts {
		original := &pb.LeaseKeepAliveResponse{}
		if proto.Unmarshal(v.Native, original) != nil || v.Sequence != uint64(i+1) || !v.Attempted || !v.Completed || v.WriteOutcomeUnknown || v.LeaseID != grant.ID || v.TTL <= 0 || original.ID != grant.ID || original.TTL != v.TTL || header(original.Header) != v.Header {
			t.Fatal("original renewal receipt changed")
		}
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	terminal, ended := r.Terminal()
	if !ended || terminal.Reason != "stopped" || terminal.WriteOutcomeUnknown || terminal.CaptureIncomplete || terminal.Attempts != terminal.Acknowledged {
		t.Fatal("acknowledged stop became uncertain", terminal)
	}
	if _, err := c.StartRenewal(context.Background(), plan); !errors.Is(err, ErrUsed) {
		t.Fatal("original continuous authorization replayed", err)
	}
	nativeDo(t, c, &pb.RangeRequest{Key: key})
	expires := time.Now().Add(8 * time.Second)
	for time.Now().Before(expires) {
		if len(nativeDo(t, admin, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("stopped original lease was renewed again")
}

type renewalNativeFault struct {
	upstream       *Client
	after          int32
	mode           string
	streams, calls atomic.Int32
	entered, done  chan struct{}
	receipt        *pb.LeaseKeepAliveResponse
}

func (f *renewalNativeFault) serve(_ any, downstream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(downstream)
	if method != "/etcdserverpb.Lease/LeaseKeepAlive" {
		bridge := &nativeFault{upstream: f.upstream}
		return bridge.serve(nil, downstream)
	}
	f.streams.Add(1)
	upstream, err := pb.NewLeaseClient(f.upstream.conn).LeaseKeepAlive(f.upstream.authContext(downstream.Context()))
	if err != nil {
		return err
	}
	defer upstream.CloseSend()
	for {
		request := &pb.LeaseKeepAliveRequest{}
		if err := downstream.RecvMsg(request); err != nil {
			return err
		}
		if err := upstream.Send(request); err != nil {
			return err
		}
		response, err := upstream.Recv()
		if err != nil {
			return err
		}
		if f.calls.Add(1) == f.after {
			f.receipt = proto.Clone(response).(*pb.LeaseKeepAliveResponse)
			close(f.entered)
			switch f.mode {
			case "lost":
				close(f.done)
				return status.Error(codes.Unavailable, "original processed renewal reply discarded")
			case "cancel", "close", "deadline", "stop":
				<-downstream.Context().Done()
				close(f.done)
				return downstream.Context().Err()
			case "identity":
				response.ID++
				close(f.done)
			case "bytes":
				response.ProtoReflect().SetUnknown(bytes.Repeat([]byte{0xa0, 0x06, 0x07}, renewalFrameBytes))
				close(f.done)
			}
		}
		if err := downstream.SendMsg(response); err != nil {
			return err
		}
	}
}
func renewalFaultClient(t *testing.T, upstream *Client, after int32, mode string) (*Client, *renewalNativeFault) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &renewalNativeFault{upstream: upstream, after: after, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
	server := grpc.NewServer(grpc.UnknownServiceHandler(f.serve))
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	cfg, secret := nativeConfig(t)
	cfg.Endpoint = "http://" + listener.Addr().String()
	cfg.CAFile, cfg.ClientCertFile, cfg.ClientKeyFile, cfg.ServerName = "", "", "", ""
	if mode == "deadline" {
		cfg.RequestTimeout = time.Second
	}
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, f
}
func TestEtcdNativeRenewalActualProcessedFirstAndLaterReplyLossNeverReconnects(t *testing.T) {
	admin := nativeRenewalClient(t)
	for _, after := range []int32{1, 2} {
		for _, mode := range []string{"lost", "cancel", "close", "deadline", "identity", "bytes", "stop"} {
			if mode == "stop" && after == 1 {
				continue
			}
			t.Run(strconv.Itoa(int(after))+"/"+mode, func(t *testing.T) {
				grant := nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 6}).Native().(*pb.LeaseGrantResponse)
				c, fault := renewalFaultClient(t, admin, after, mode)
				plan, err := c.PrepareRenewal(&pb.LeaseKeepAliveRequest{ID: grant.ID})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type result struct {
					r   *RenewalRun
					err error
				}
				out := make(chan result, 1)
				go func() { r, err := c.StartRenewal(ctx, plan); out <- result{r, err} }()
				var got result
				if mode == "stop" {
					got = <-out
				}
				waitSignal(t, fault.entered)
				if mode == "cancel" {
					cancel()
				}
				if mode == "close" {
					if err := c.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "stop" {
					_ = got.r.Stop(context.Background())
				} else {
					got = <-out
				}
				if got.r == nil {
					t.Fatal("processed original attempt lost owning run", got.err)
				}
				defer got.r.Close()
				renewalNativeDone(t, got.r)
				waitSignal(t, fault.done)
				terminal, _ := got.r.Terminal()
				receipts := got.r.Poll()
				if fault.streams.Load() != 1 || fault.calls.Load() != after || fault.receipt.ID != grant.ID || fault.receipt.TTL <= 0 || !terminal.WriteOutcomeUnknown || terminal.Attempts != uint64(after) || terminal.Acknowledged != uint64(after-1) || c.Alive() {
					t.Fatal("unconfirmed original renewal replayed/reclassified", terminal)
				}
				if len(receipts) != int(after) || !receipts[len(receipts)-1].WriteOutcomeUnknown || receipts[len(receipts)-1].Completed {
					t.Fatal("unknown renewal replaced earlier confirmation", receipts)
				}
				if mode == "identity" && len(receipts[len(receipts)-1].Native) == 0 {
					t.Fatal("malformed original native response discarded")
				}
				if mode == "bytes" && !terminal.CaptureIncomplete {
					t.Fatal("oversized raw response silently dropped")
				}
				if _, err := c.StartRenewal(context.Background(), plan); err == nil || fault.streams.Load() != 1 || fault.calls.Load() != after {
					t.Fatal("retired original authorization reopened stream")
				}
				// Independent later TTL observation never confirms that discarded reply.
				nativeDo(t, admin, &pb.LeaseTimeToLiveRequest{ID: grant.ID})
				if again, _ := got.r.Terminal(); !again.WriteOutcomeUnknown {
					t.Fatal("later TTL read resolved original unknown receipt")
				}
				nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: grant.ID})
			})
		}
	}
}
func TestEtcdNativeRenewalActualRevocationMissingLeaseAndBoundedReceipts(t *testing.T) {
	admin := nativeRenewalClient(t)
	for _, mode := range []string{"revoked", "missing", "budget"} {
		t.Run(mode, func(t *testing.T) {
			c := nativeRenewalClient(t)
			grant := nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 3}).Native().(*pb.LeaseGrantResponse)
			if mode == "missing" {
				nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: grant.ID})
			}
			plan, err := c.PrepareRenewal(&pb.LeaseKeepAliveRequest{ID: grant.ID})
			if err != nil {
				t.Fatal(err)
			}
			var admissions atomic.Int32
			var r *RenewalRun
			if mode == "budget" {
				r, err = c.startRenewal(context.Background(), plan, func(int) bool { return admissions.Add(1) == 1 })
			} else {
				r, err = c.StartRenewal(context.Background(), plan)
			}
			if r == nil || mode != "missing" && err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if mode == "revoked" {
				nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: grant.ID})
			}
			renewalNativeDone(t, r)
			terminal, _ := r.Terminal()
			receipts := r.Poll()
			if terminal.WriteOutcomeUnknown || terminal.Attempts != terminal.Acknowledged {
				t.Fatal("actual native acknowledgement became unknown", terminal)
			}
			if mode == "budget" {
				if terminal.Reason != "overflow" || !terminal.CaptureIncomplete || len(receipts) != 1 || !receipts[0].Completed {
					t.Fatal("budget silently evicted original receipt", terminal)
				}
				nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: grant.ID})
			} else if terminal.Reason != "expired" || len(receipts) == 0 || !receipts[len(receipts)-1].Completed || receipts[len(receipts)-1].TTL != 0 {
				t.Fatal("native expiry not preserved", terminal, receipts)
			}
		})
	}
}
func TestEtcdNativeRenewalActualPermissionRevocationStopsOnlyOriginalRun(t *testing.T) {
	_ = nativeRenewalClient(t)
	admin := accessNativeAdmin(t)
	key := nativeKey(t)
	user := "renew-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	accessNativeUser(t, admin, user, user, []*authpb.Permission{{PermType: authpb.READWRITE, Key: key}})
	cfg, secret := nativeConfig(t)
	cfg.Auth, cfg.Username, cfg.ProbeKey = "basic", user, key
	secret = os.Getenv("NYADB_ETCD_SECRET")
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	grant := nativeDo(t, c, &pb.LeaseGrantRequest{TTL: 6}).Native().(*pb.LeaseGrantResponse)
	nativeDo(t, c, &pb.PutRequest{Key: key, Value: []byte("original"), Lease: grant.ID})
	plan, err := c.PrepareRenewal(&pb.LeaseKeepAliveRequest{ID: grant.ID})
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.StartRenewal(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := pb.NewAuthClient(admin.conn).RoleRevokePermission(admin.authContext(context.Background()), &pb.AuthRoleRevokePermissionRequest{Role: user, Key: key}); err != nil {
		t.Fatal(err)
	}
	renewalNativeDone(t, r)
	terminal, _ := r.Terminal()
	receipts := r.Poll()
	if terminal.Reason != "rejected" || terminal.WriteOutcomeUnknown || terminal.Acknowledged < 1 || len(receipts) < 2 || !receipts[0].Completed || receipts[len(receipts)-1].Completed || receipts[len(receipts)-1].WriteOutcomeUnknown {
		t.Fatal("actual permission revocation lost original receipts", terminal)
	}
	if !admin.Alive() {
		t.Fatal("original renewal affected independent administrative channel")
	}
	nativeDo(t, admin, &pb.LeaseRevokeRequest{ID: grant.ID})
}
