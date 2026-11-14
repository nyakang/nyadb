package consul

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConsulNativeHTTPArchiveLeaseOriginalPrivateSourceAndRepeatedRecovery(t *testing.T) {
	c, cfg, token := nativeClient(t)
	nativeDo(t, c, putRequest(keyFor(t, "inside/value"), []byte("lease-original-inside\x00\xff")))
	nativeDo(t, c, putRequest(keyFor(t, "outside/value"), []byte("lease-original-outside\xff\x00")))
	cfg.Prefix, cfg.Datacenter = keyFor(t, "inside/"), "dc1"
	s, err := OpenNativeSession(context.Background(), 701, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	opener := s.opener
	var opens atomic.Int32
	s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) {
		opens.Add(1)
		return opener(ctx)
	}
	lease, _ := s.ReserveKV(801)
	l := lease.(*documentLease)
	defer l.Close()
	p, err := l.PrepareHTTPArchive(archiveLeaseRequest)
	if err != nil || opens.Load() != 0 || p.Scope().Original.Namespace.ProfileID != 701 || p.Scope().DocumentID != 801 || p.TargetURL() != cfg.Endpoint+"/v1/snapshot?dc=dc1" {
		t.Fatal("native archive preparation changed scope or connected", err)
	}
	a, r, err := l.CaptureHTTPArchive(context.Background(), p)
	if err != nil || a == nil || !r.SourceComplete || !r.HTTPComplete || !r.GZIPComplete || !r.TARComplete || !r.ChecksumsMatch || !r.MetadataValid || opens.Load() != 1 {
		t.Fatal("native lease did not capture the original complete archive", err)
	}
	defer a.Close()
	reader, _ := a.OpenOriginal()
	body, err := io.ReadAll(reader)
	_ = reader.Close()
	defer clear(body)
	hash := sha256.Sum256(body)
	proof, inspectErr := inspectSnapshotArchive(context.Background(), body, snapshotTestLimits)
	defer clear(proof.metadata)
	defer clear(proof.checksums)
	if err != nil || inspectErr != nil || !bytes.Equal(hash[:], r.OriginalSHA256) || !bytes.Equal(proof.metadata, r.Metadata) || !bytes.Equal(proof.checksums, r.Checksums) || l.LastResult().HTTP != nil {
		t.Fatal("native original bytes or private proof changed")
	}
	receiptBudgetMu.Lock()
	charge, retained := l.archiveBytes(), s.retained
	receiptBudgetMu.Unlock()
	for range 3 {
		old := l
		recovered, err := s.RecoverKV(old)
		if err != nil || recovered.Lease == nil || opens.Load() != 1 {
			t.Fatal("native evidence recovery performed an open", err)
		}
		l = recovered.Lease.(*documentLease)
		defer l.Close()
		_ = old.Close()
		receiptBudgetMu.Lock()
		valid := l.archiveBytes() == charge && old.archiveBytes() == 0 && s.retained == retained
		receiptBudgetMu.Unlock()
		if !valid || !reflect.DeepEqual(a.Receipt(), r) {
			t.Fatal("native repeated recovery lost original evidence or budget")
		}
		if other, _, err := l.CaptureHTTPArchive(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || other != nil || opens.Load() != 1 {
			t.Fatal("native original plan replayed through recovery")
		}
	}
	_ = a.Close()
	receiptBudgetMu.Lock()
	valid := s.retained == retained-charge && l.archiveBytes() == 0 && s.reserved == 0
	receiptBudgetMu.Unlock()
	if !valid {
		t.Fatal("native original source budget was not released")
	}
}

func TestConsulNativeHTTPArchiveLeaseProcessedWriteUnknownAndSnapshotFaults(t *testing.T) {
	upstream, cfg, token := nativeClient(t)
	var calls, snapshots atomic.Int32
	var writeProcessed atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		calls.Add(1)
		request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, cfg.Endpoint+incoming.URL.RequestURI(), incoming.Body)
		if err != nil {
			t.Error("owned upstream request unavailable")
			return
		}
		if token != "" {
			request.Header.Set("X-Consul-Token", token)
		}
		response, err := upstream.http.Do(request)
		if err != nil {
			t.Error("owned upstream transport failed")
			http.Error(w, "owned upstream unavailable", 502)
			return
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		closeErr := response.Body.Close()
		defer clear(body)
		if err != nil || closeErr != nil || len(body) >= 2<<20 {
			t.Error("owned upstream response incomplete")
			return
		}
		for name, values := range response.Header {
			w.Header()[name] = append([]string(nil), values...)
		}
		if incoming.Method == "PUT" {
			if response.StatusCode != 200 || !bytes.Equal(body, []byte("true")) {
				t.Error("original upstream write was not acknowledged")
				return
			}
			writeProcessed.Store(true)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body[:1])
			return
		}
		if incoming.URL.Path == "/v1/snapshot" {
			proof, err := inspectSnapshotArchive(incoming.Context(), body, snapshotTestLimits)
			clear(proof.metadata)
			clear(proof.checksums)
			if err != nil || !proof.metadataValid || response.StatusCode != 200 {
				t.Error("owned upstream native archive incomplete")
				return
			}
			if snapshots.Add(1) == 2 {
				w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				_, _ = w.Write(body[:len(body)/2])
				return
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(body)
	}))
	defer proxy.Close()
	s, err := OpenNativeSession(context.Background(), 702, Config{Endpoint: proxy.URL}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lease, _ := s.ReserveKV(802)
	l := lease.(*documentLease)
	defer l.Close()
	key, value := keyFor(t, "unknown-original"), []byte("native-original-write\x00\xff")
	p := documentPlan(t, l, putRequest(key, value))
	written, err := l.Execute(context.Background(), p)
	if err == nil || !writeProcessed.Load() || !written.WriteOutcomeUnknown || !bytes.Equal(nativeRead(t, upstream, key).Value, value) {
		t.Fatal("original processed write fault not established")
	}
	before := calls.Load()
	recovered, err := s.RecoverKV(l)
	if err != nil || recovered.Unknown == nil || !recovered.Unknown.WriteOutcomeUnknown || calls.Load() != before {
		t.Fatal("unknown write recovery performed I/O or discarded original")
	}
	l = recovered.Lease.(*documentLease)
	defer l.Close()
	plan, _ := l.PrepareHTTPArchive(archiveLeaseRequest)
	a, complete, err := l.CaptureHTTPArchive(context.Background(), plan)
	if err != nil || a == nil || !complete.SourceComplete || !l.writeUnknown.Load() || !reflect.DeepEqual(l.LastResult(), written) {
		t.Fatal("native snapshot changed original unknown write", err)
	}
	defer a.Close()
	plan, _ = l.PrepareHTTPArchive(archiveLeaseRequest)
	failed, failedReceipt, err := l.CaptureHTTPArchive(context.Background(), plan)
	if !errors.Is(err, contract.ErrKVLeaseLost) || failed == nil || !failedReceipt.Attempted || !failedReceipt.Ended || failedReceipt.SourceComplete || failedReceipt.HTTPComplete || snapshots.Load() != 2 {
		t.Fatal("native completed upstream did not retain original truncated receipt", err)
	}
	defer failed.Close()
	before = calls.Load()
	recovered, err = s.RecoverKV(l)
	if err != nil || calls.Load() != before || recovered.Unknown == nil || !reflect.DeepEqual(*recovered.Unknown, written) {
		t.Fatal("failed archive recovery replayed or erased unknown write")
	}
	defer recovered.Lease.Close()
	request, _ := DocumentRequest(putRequest(key, value))
	if _, err := recovered.Lease.Prepare(request); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("later snapshot read authorized another write")
	}
	_ = l.Close()
	if !reflect.DeepEqual(a.Receipt(), complete) || !reflect.DeepEqual(failed.Receipt(), failedReceipt) || calls.Load() != before {
		t.Fatal("original archives were replaced by recovery")
	}
}
