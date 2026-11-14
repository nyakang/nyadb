package dbruntime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func TestConsulNativeHTTPArchivesOriginalPublishedSourceSurvivesReplacement(t *testing.T) {
	cfg, secret := consulRuntimeConfig(t)
	m := consulRuntimeManager(t, cfg)
	for range 2 {
		if err := m.Connect(context.Background(), consulRuntimeProfile(), secret); err != nil {
			t.Fatal(err)
		}
	}
	l := consulRuntimeReserve(t, m, 1701)
	facet, ok := l.(contract.KVHTTPArchiveCapturing)
	if !ok {
		t.Fatal("native original HTTP archive binding missing")
	}
	if _, ok := l.(contract.KVSnapshots); ok {
		t.Fatal("HTTP archive advertised Etcd stream")
	}
	if _, ok := l.(contract.KVHTTPACLReading); !ok {
		t.Fatal("native ACL facet hidden")
	}
	if _, ok := l.(contract.KVHTTPSessionDestroyPlanning); !ok {
		t.Fatal("native session plan hidden")
	}
	if _, ok := l.(contract.KVHTTPACLMutating); !ok {
		t.Fatal("native ACL mutation hidden")
	}
	if _, ok := l.(contract.KVHTTPACLImpactBrowsing); !ok {
		t.Fatal("native ACL impact hidden")
	}
	if _, ok := l.(contract.KVHTTPACLMutationPlanning); !ok {
		t.Fatal("native ACL write plan hidden")
	}
	p, err := facet.PrepareHTTPArchive(runtimeArchiveRequest)
	if err != nil || p.Scope().Original.Generation != l.Scope().Generation || p.Scope().Original.Generation <= 1 {
		t.Fatal("native public preparation generation changed", err)
	}
	a, r, err := facet.CaptureHTTPArchive(context.Background(), p)
	if err != nil || a == nil || !r.SourceComplete || r.Scope.Original.Generation != p.Scope().Original.Generation || !reflect.DeepEqual(a.Receipt(), r) {
		t.Fatal("native original public receipt missing", err)
	}
	defer a.Close()
	for range 2 {
		old := l
		if err := m.Connect(context.Background(), consulRuntimeProfile(), secret); err != nil {
			t.Fatal(err)
		}
		provider, _ := m.KVRequests(consulRuntimeProfile().ID)
		recovered, err := provider.RecoverKV(old)
		if err != nil || recovered.Lease == nil {
			t.Fatal(err)
		}
		l = recovered.Lease
		defer l.Close()
		_ = old.Close()
		if !reflect.DeepEqual(a.Receipt(), r) || l.Scope().Generation <= r.Scope.Original.Generation {
			t.Fatal("native archived publication rewritten")
		}
		if other, _, err := l.(contract.KVHTTPArchiveCapturing).CaptureHTTPArchive(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || other != nil {
			t.Fatal("original archive plan replayed")
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := a.OpenOriginal()
	if err != nil {
		t.Fatal("native source lost after original manager close")
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	defer clear(body)
	hash := sha256.Sum256(body)
	if err != nil || !bytes.Equal(hash[:], r.OriginalSHA256) || uint64(len(body)) != r.OriginalBytes || !reflect.DeepEqual(a.Receipt(), r) {
		t.Fatal("native original source/receipt changed")
	}
}

func TestConsulNativeHTTPArchivesReadKeepsProcessedUnknownWriteAcrossRecovery(t *testing.T) {
	cfg, secret := consulRuntimeConfig(t)
	proxy, processed, ack := consulRuntimeFault(t, cfg, "partial")
	m := consulRuntimeManager(t, proxy)
	for range 2 {
		if err := m.Connect(context.Background(), consulRuntimeProfile(), secret); err != nil {
			t.Fatal(err)
		}
	}
	l := consulRuntimeReserve(t, m, 1702)
	write := consulRuntimePut(t, "native/runtime-http-archive-original", []byte("private-runtime-write\x00\xff"))
	r, err := l.Execute(context.Background(), consulRuntimePlan(t, l, write))
	consulRuntimeAwait(t, ack)
	if err == nil || !r.WriteOutcomeUnknown || processed.Load() != 1 {
		t.Fatal("actual original processed write fault missing")
	}
	provider, _ := m.KVRequests(consulRuntimeProfile().ID)
	recovered, err := provider.RecoverKV(l)
	if err != nil || recovered.Unknown == nil || !reflect.DeepEqual(*recovered.Unknown, r) || processed.Load() != 1 {
		t.Fatal("unknown original recovery replayed", err)
	}
	l = recovered.Lease
	defer l.Close()
	facet := l.(contract.KVHTTPArchiveCapturing)
	p, err := facet.PrepareHTTPArchive(runtimeArchiveRequest)
	if err != nil {
		t.Fatal(err)
	}
	a, archive, err := facet.CaptureHTTPArchive(context.Background(), p)
	if err != nil || a == nil || !archive.SourceComplete || !reflect.DeepEqual(l.LastResult(), r) || processed.Load() != 1 {
		t.Fatal("native archive promoted original unknown write", err)
	}
	defer a.Close()
	if err := m.Connect(context.Background(), consulRuntimeProfile(), secret); err != nil {
		t.Fatal(err)
	}
	provider, _ = m.KVRequests(consulRuntimeProfile().ID)
	recovered, err = provider.RecoverKV(l)
	if err != nil || recovered.Unknown == nil || !reflect.DeepEqual(*recovered.Unknown, r) || processed.Load() != 1 {
		t.Fatal("later archived read settled unknown outcome", err)
	}
	defer recovered.Lease.Close()
	// Preparation must reject the write before any original execution.
	canonical, err := consul.DocumentRequest(write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Lease.Prepare(canonical); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("native archive enabled another write")
	}
	if !reflect.DeepEqual(a.Receipt(), archive) {
		t.Fatal("original archive changed on unknown recovery")
	}
}
