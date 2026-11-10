package etcd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func renewalNativeSession(t *testing.T) *Session {
	t.Helper()
	if os.Getenv("NYADB_ETCD_RENEWAL_SESSION_IT") != "1" {
		t.Skip("requires owned original renewal session fixture")
	}
	return nativeSession(t)
}
func renewalSessionWait(t *testing.T, r contract.KVRenewalRun, predicate func(contract.KVRenewalBatch) bool) contract.KVRenewalBatch {
	t.Helper()
	var out contract.KVRenewalBatch
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		b := r.Poll()
		out.Request, out.Ended, out.Terminal = b.Request, b.Ended, b.Terminal
		out.Receipts = append(out.Receipts, b.Receipts...)
		if predicate(out) {
			return out
		}
		if b.Ended {
			t.Fatal("original renewal ended before required receipt", b.Terminal)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("original renewal did not settle/acknowledge")
	return out
}
func TestEtcdNativeRenewalSessionOriginalAuthorizationIndependentKVAndRecoveryBudget(t *testing.T) {
	s := renewalNativeSession(t)
	admin := nativeRenewalClient(t)
	grant := nativeDo(t, admin, &pb.LeaseGrantRequest{ID: 9007199254740993, TTL: 3}).Native().(*pb.LeaseGrantResponse)
	key := nativeKey(t)
	key = append(key, bytes.Repeat([]byte{255}, 4096-len(key))...)
	put := nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255}, Lease: grant.ID})
	l := renewalTestLease(t, s, 1)
	var opens atomic.Int32
	opener := s.opener
	s.opener = func(ctx context.Context, p connections.Profile, secret string) (*Client, error) {
		opens.Add(1)
		return opener(ctx, p, secret)
	}
	authored := contract.KVRenewalRequest{LeaseID: grant.ID}
	p, err := l.Prepare(authored)
	if err != nil || opens.Load() != 0 {
		t.Fatal("prepare did I/O", err)
	}
	if err = l.Preconnect(context.Background()); err != nil || opens.Load() != 1 {
		t.Fatal("preconnect failed", err)
	}
	r, err := l.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	receipts := renewalSessionWait(t, r, func(b contract.KVRenewalBatch) bool {
		return len(b.Receipts) >= 5 && time.Since(start) > time.Duration(grant.TTL)*time.Second
	})
	if _, err = l.Prepare(authored); !errors.Is(err, contract.ErrKVBusy) {
		t.Fatal("active executor authorized parallel writes", err)
	}
	independent := reserveNativeKV(t, s, 1)
	observed := nativeKVDo(t, independent, &pb.RangeRequest{Key: key})
	if len(observed.Reply.Entries) != 1 || observed.Reply.Entries[0].ModRevision != put.Header.Revision || observed.Reply.Entries[0].Lease != grant.ID {
		t.Fatal("renewal changed key or blocked independent KV")
	}
	for _, receipt := range receipts.Receipts {
		original := &pb.LeaseKeepAliveResponse{}
		if proto.Unmarshal(receipt.Native, original) != nil || original.ID != grant.ID || original.TTL != receipt.TTL || receipt.JSON == "" || receipt.WriteOutcomeUnknown || !receipt.Completed {
			t.Fatal("original renewal receipt changed")
		}
	}
	charged := s.retainedBytes
	if charged == 0 {
		t.Fatal("transferred receipts not charged")
	}
	if err = r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := opens.Load()
	recovery, err := s.RecoverKVRenewal(l)
	if err != nil || recovery.Lease == nil || opens.Load() != before || s.retainedBytes != charged || recovery.Batch.Request != authored || !recovery.Batch.Ended || recovery.Batch.Terminal.WriteOutcomeUnknown {
		t.Fatal("recovery reconnected/released receipts", err)
	}
	defer recovery.Lease.Close()
	if _, err = recovery.Lease.Start(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("old preparation transferred", err)
	}
	// No recovery or reserved replacement renews the stopped lease.
	deadline := time.Now().Add(8 * time.Second)
	expired := false
	for time.Now().Before(deadline) {
		if len(nativeDo(t, admin, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse).Kvs) == 0 {
			expired = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !expired {
		t.Fatal("recovery resumed old renewal")
	}
	if err = recovery.Lease.ClearReceipts(); err != nil || s.retainedBytes != 0 {
		t.Fatal("explicit receipt disposal failed", err)
	}
}
func TestEtcdNativeRenewalSessionActualProcessedReceiptsRetirementAndUnknownRecovery(t *testing.T) {
	for _, after := range []int32{1, 2} {
		for _, mode := range []string{"lost", "cancel", "stop", "retire", "recovery", "identity", "bytes"} {
			if after == 1 && mode == "stop" {
				continue
			}
			t.Run(strconv.Itoa(int(after))+"/"+mode, func(t *testing.T) {
				s := renewalNativeSession(t)
				admin := nativeRenewalClient(t)
				grant := nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 3}).Native().(*pb.LeaseGrantResponse)
				proxyMode := mode
				if mode == "retire" || mode == "recovery" {
					proxyMode = "cancel"
				}
				c, fault := renewalFaultClient(t, admin, after, proxyMode)
				var opens atomic.Int32
				s.opener = func(context.Context, connections.Profile, string) (*Client, error) { opens.Add(1); return c, nil }
				l := renewalTestLease(t, s, 1)
				p, err := l.Prepare(contract.KVRenewalRequest{LeaseID: grant.ID})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type outcome struct {
					r   contract.KVRenewalRun
					err error
				}
				out := make(chan outcome, 1)
				go func() { r, err := l.Start(ctx, p); out <- outcome{r, err} }()
				var result outcome
				if after == 2 {
					result = <-out
					if result.err != nil || result.r == nil {
						t.Fatal("first acknowledgement failed", result.err)
					}
				}
				select {
				case <-fault.entered:
				case <-time.After(8 * time.Second):
					t.Fatal("upstream did not process original renewal")
				}
				var recovery contract.KVRenewalRecovery
				switch mode {
				case "cancel":
					cancel()
				case "stop":
					if after == 1 {
						cancel()
					} else {
						_ = result.r.Stop(context.Background())
					}
				case "retire":
					if err = s.Close(); err != nil {
						t.Fatal(err)
					}
				case "recovery":
					fresh := renewalNativeSession(t)
					recovery, err = fresh.RecoverKVRenewal(l)
					if err != nil {
						t.Fatal("pending original recovery failed", err)
					}
				}
				if after == 1 {
					result = <-out
				}
				if result.r == nil {
					t.Fatal("original first failed attempt receipt discarded", result.err)
				}
				var batch contract.KVRenewalBatch
				if mode == "recovery" {
					batch = recovery.Batch
				} else {
					batch = renewalSessionWait(t, result.r, func(b contract.KVRenewalBatch) bool { return b.Ended })
				}
				if !batch.Ended || !batch.Terminal.WriteOutcomeUnknown || batch.Terminal.Attempts != uint64(after) || batch.Terminal.Acknowledged != uint64(after-1) || len(batch.Receipts) != int(after) {
					t.Fatal("processed original acknowledgement reclassified", batch.Terminal, len(batch.Receipts))
				}
				if !batch.Receipts[len(batch.Receipts)-1].WriteOutcomeUnknown || batch.Receipts[len(batch.Receipts)-1].LeaseID != grant.ID && mode != "identity" {
					t.Fatal("unknown original receipt changed")
				}
				if mode != "recovery" {
					if _, err = l.Prepare(contract.KVRenewalRequest{LeaseID: grant.ID}); !errors.Is(err, contract.ErrKVRenewalUncertain) {
						t.Fatal("unknown write not isolated", err)
					}
					fresh := renewalNativeSession(t)
					recovery, err = fresh.RecoverKVRenewal(l)
					if err != nil {
						t.Fatal(err)
					}
				}
				defer recovery.Lease.Close()
				if !recovery.Batch.Terminal.WriteOutcomeUnknown {
					t.Fatal("recovery erased terminal barrier")
				}
				if err = recovery.Lease.Preconnect(context.Background()); err != nil {
					t.Fatal(err)
				}
				// An independent current TTL observation does not confirm the lost receipt.
				nativeDo(t, admin, &pb.LeaseTimeToLiveRequest{ID: grant.ID, Keys: true})
				if _, err = recovery.Lease.Prepare(contract.KVRenewalRequest{LeaseID: grant.ID}); !errors.Is(err, contract.ErrKVRenewalUncertain) {
					t.Fatal("TTL/recovery authorized replay", err)
				}
				if err = recovery.Lease.ClearReceipts(); !errors.Is(err, contract.ErrKVRenewalUncertain) {
					t.Fatal("clear erased unknown outcome", err)
				}
				if fault.streams.Load() != 1 || fault.calls.Load() != after || opens.Load() != 1 || fault.receipt.ID != grant.ID || fault.receipt.TTL <= 0 {
					t.Fatal("original executor reconnected or processed wrong request")
				}
			})
		}
	}
}
func TestEtcdNativeRenewalSessionActualRetainedDocumentOverflowStopsOriginalRun(t *testing.T) {
	s := renewalNativeSession(t)
	admin := nativeRenewalClient(t)
	grant := nativeDo(t, admin, &pb.LeaseGrantRequest{TTL: 3}).Native().(*pb.LeaseGrantResponse)
	l := renewalTestLease(t, s, 1)
	// Retained prior document capture leaves room for one original valid frame.
	original := nativeDo(t, admin, &pb.LeaseKeepAliveRequest{ID: grant.ID}).Native().(*pb.LeaseKeepAliveResponse)
	firstCharge := (128 + proto.Size(original)*4) * 4
	if !l.admit((renewalDocumentBytes - firstCharge - 128) / 4) {
		t.Fatal("initial retained budget failed")
	}
	p, err := l.Prepare(contract.KVRenewalRequest{LeaseID: grant.ID})
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Start(context.Background(), p)
	if err != nil {
		t.Fatal("first original acknowledgement did not fit", err)
	}
	b := renewalSessionWait(t, r, func(b contract.KVRenewalBatch) bool { return b.Ended })
	if !b.Terminal.CaptureIncomplete || b.Terminal.WriteOutcomeUnknown || b.Terminal.Attempts != 2 || b.Terminal.Acknowledged != 2 || len(b.Receipts) != 1 || !b.Receipts[0].Completed {
		t.Fatal("retained overflow evicted/reclassified original receipts", b.Terminal, len(b.Receipts))
	}
	if err = l.ClearReceipts(); err != nil || s.retainedBytes != 0 {
		t.Fatal("settled known disposal failed", err)
	}
}
