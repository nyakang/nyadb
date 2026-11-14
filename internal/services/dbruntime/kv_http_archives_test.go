package dbruntime_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

var runtimeArchiveRequest = contract.KVHTTPArchiveRequest{CompressedBytes: 1 << 20, ExpandedBytes: 2 << 20}

func runtimeArchiveBytes(t *testing.T) []byte {
	t.Helper()
	state := []byte("private-synthetic-original\x00\xff")
	meta := []byte(fmt.Sprintf(`{"Version":1,"ID":"original-native-id","Index":2,"Term":3,"Size":%d}`, len(state)))
	checksums := []byte(fmt.Sprintf("%x  state.bin\n%x  meta.json\n", sha256.Sum256(state), sha256.Sum256(meta)))
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	writer := tar.NewWriter(z)
	for _, entry := range []struct {
		name string
		body []byte
	}{{"state.bin", state}, {"meta.json", meta}, {"SHA256SUMS", checksums}} {
		if err := writer.WriteHeader(&tar.Header{Name: entry.name, Mode: 0600, Size: int64(len(entry.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func archiveRuntimeFixture(t *testing.T, mode string) (*dbruntime.Manager, *atomic.Int32, []byte) {
	t.Helper()
	original := runtimeArchiveBytes(t)
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Consul-Index", "84")
		switch r.URL.Path {
		case "/v1/agent/self":
			fmt.Fprint(w, `{"Config":{"Version":"2.0.2","NodeID":"`+runtimeDestroyUUID+`","NodeName":"original","Datacenter":"dc1"}}`)
		case "/v1/status/leader":
			fmt.Fprint(w, `"127.0.0.1:8300"`)
		case "/v1/kv/":
			fmt.Fprint(w, `[]`)
		case "/v1/snapshot":
			_, _ = w.Write(original)
		case "/v1/acl/policy":
			w.Header().Set("Content-Length", "9999")
			_, _ = fmt.Fprint(w, `{"ID":"`+runtimeDestroyUUID+`"}`)
		default:
			t.Error("unexpected native dispatch")
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	m := dbruntime.New()
	t.Cleanup(func() { m.Close() })
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, _ string) (contract.Session, error) {
		s, err := consul.OpenNativeSession(ctx, p.ID, consul.Config{Endpoint: server.URL}, "")
		if err != nil || mode == "" {
			return s, err
		}
		return &archiveRuntimeSession{Session: s, provider: s, mode: mode}, nil
	}))
	for range 2 {
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
	}
	return m, calls, original
}

func TestKVHTTPArchivesOriginalPublicationReadersAndPureRepeatedRecovery(t *testing.T) {
	m, calls, original := archiveRuntimeFixture(t, "")
	provider, _ := m.KVRequests(1)
	lease, _ := provider.ReserveKV(701)
	defer lease.Close()
	facet, ok := lease.(contract.KVHTTPArchiveCapturing)
	if !ok {
		t.Fatal("installed HTTP archive facet missing")
	}
	if _, ok := lease.(contract.KVSnapshots); ok {
		t.Fatal("HTTP archive advertised Etcd snapshots")
	}
	if _, ok := lease.(contract.KVHTTPACLReading); !ok {
		t.Fatal("archive hid ACL reading")
	}
	if _, ok := lease.(contract.KVHTTPSessionKeysBrowsing); !ok {
		t.Fatal("archive hid session key browsing")
	}
	if _, ok := lease.(contract.KVHTTPSessionDestroyPlanning); !ok {
		t.Fatal("archive hid original destruction plan")
	}
	if _, ok := lease.(contract.KVHTTPACLMutating); !ok {
		t.Fatal("archive hid ACL mutation")
	}
	if _, ok := lease.(contract.KVHTTPACLImpactBrowsing); !ok {
		t.Fatal("archive hid original ACL impact")
	}
	if _, ok := lease.(contract.KVHTTPACLMutationPlanning); !ok {
		t.Fatal("archive hid original ACL write plan")
	}
	before := calls.Load()
	p, err := facet.PrepareHTTPArchive(runtimeArchiveRequest)
	if err != nil || calls.Load() != before || p.Scope().Original.Generation <= 1 || p.Scope().Original.Generation != lease.Scope().Generation || p.Scope().DocumentID != 701 {
		t.Fatal("preparation connected or changed publication", err)
	}
	foreign, _ := provider.ReserveKV(702)
	defer foreign.Close()
	if a, _, err := foreign.(contract.KVHTTPArchiveCapturing).CaptureHTTPArchive(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || a != nil || calls.Load() != before {
		t.Fatal("foreign original plan dispatched")
	}
	a, r, err := facet.CaptureHTTPArchive(context.Background(), p)
	if err != nil || a == nil || !r.SourceComplete || r.Scope.Original.Generation != p.Scope().Original.Generation || !reflect.DeepEqual(a.Receipt(), r) || lease.LastResult().HTTP != nil {
		t.Fatal("original publication/receipt missing", err)
	}
	defer a.Close()
	reader, _ := a.OpenOriginal()
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(body, original) {
		t.Fatal("private source reader changed original")
	}
	for range 3 {
		old := lease
		if err := m.Connect(context.Background(), profile(1), ""); err != nil {
			t.Fatal(err)
		}
		provider, _ = m.KVRequests(1)
		before = calls.Load()
		recovered, err := provider.RecoverKV(old)
		if err != nil || recovered.Lease == nil || calls.Load() != before {
			t.Fatal("recovery executed an original request", err)
		}
		lease = recovered.Lease
		defer lease.Close()
		_ = old.Close()
		if !reflect.DeepEqual(a.Receipt(), r) || lease.Scope().Generation <= r.Scope.Original.Generation {
			t.Fatal("old receipt promoted to current publication")
		}
		if other, _, err := lease.(contract.KVHTTPArchiveCapturing).CaptureHTTPArchive(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || other != nil || calls.Load() != before {
			t.Fatal("original plan replayed after recovery")
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	nextReader, err := a.OpenOriginal()
	if err != nil {
		t.Fatal("manager close revoked caller-owned source")
	}
	defer nextReader.Close()
	body, err = io.ReadAll(nextReader)
	if err != nil || !bytes.Equal(body, original) || !reflect.DeepEqual(a.Receipt(), r) {
		t.Fatal("original source lost on physical retirement")
	}
	_ = a.Close()
	if _, err := nextReader.Read(make([]byte, 1)); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("source close did not revoke reader")
	}
}

type archiveRuntimeSession struct {
	contract.Session
	provider contract.KVRequestsProvider
	mode     string
}

func (s *archiveRuntimeSession) KVScope() contract.KVScope { return s.provider.KVScope() }
func (s *archiveRuntimeSession) ReserveKV(id uint64) (contract.KVRequestLease, error) {
	l, err := s.provider.ReserveKV(id)
	if err != nil {
		return nil, err
	}
	if s.mode == "unsupported" {
		return &struct{ contract.KVRequestLease }{l}, nil
	}
	return &archiveRuntimeLease{KVRequestLease: l, mode: s.mode}, nil
}
func (s *archiveRuntimeSession) RecoverKV(old contract.KVRequestLease) (contract.KVRecovery, error) {
	if wrapped, ok := old.(*archiveRuntimeLease); ok {
		old = wrapped.KVRequestLease
	}
	r, err := s.provider.RecoverKV(old)
	if err == nil && r.Lease != nil {
		r.Lease = &archiveRuntimeLease{KVRequestLease: r.Lease, mode: s.mode}
	}
	return r, err
}

type archiveRuntimeLease struct {
	contract.KVRequestLease
	mode string
}
type archiveRuntimePlan struct {
	contract.KVHTTPArchivePreparation
	mode string
}

func (p *archiveRuntimePlan) Scope() contract.KVHTTPArchiveScope {
	s := p.KVHTTPArchivePreparation.Scope()
	if p.mode == "plan-scope" {
		s.Original.Generation++
	}
	return s
}
func (p *archiveRuntimePlan) Snapshot() contract.KVHTTPArchiveRequest {
	r := p.KVHTTPArchivePreparation.Snapshot()
	if p.mode == "plan-request" {
		r.CompressedBytes++
	}
	return r
}
func (p *archiveRuntimePlan) LocalRecordID() string {
	if p.mode == "plan-id" {
		return ""
	}
	return p.KVHTTPArchivePreparation.LocalRecordID()
}
func (l *archiveRuntimeLease) PrepareHTTPArchive(r contract.KVHTTPArchiveRequest) (contract.KVHTTPArchivePreparation, error) {
	p, err := l.KVRequestLease.(contract.KVHTTPArchiveCapturing).PrepareHTTPArchive(r)
	if err != nil {
		return nil, err
	}
	return &archiveRuntimePlan{p, l.mode}, nil
}
func (l *archiveRuntimeLease) CaptureHTTPArchive(ctx context.Context, p contract.KVHTTPArchivePreparation) (contract.KVHTTPArchive, contract.KVHTTPArchiveResult, error) {
	if wrapped, ok := p.(*archiveRuntimePlan); ok {
		p = wrapped.KVHTTPArchivePreparation
	}
	a, r, err := l.KVRequestLease.(contract.KVHTTPArchiveCapturing).CaptureHTTPArchive(ctx, p)
	switch l.mode {
	case "result-scope":
		r.Scope.Original.Generation++
	case "result-request":
		r.Request.CompressedBytes++
	case "result-id":
		r.LocalRecordID = "foreign-original"
	case "result-target":
		r.TargetURL += "?foreign"
	case "result-receipt":
		r.Status = 201
	}
	return a, r, err
}

func TestKVHTTPArchivesRejectForeignPreparationAndPreserveUntrustedOriginalEvidence(t *testing.T) {
	for _, mode := range []string{"unsupported", "plan-scope", "plan-request", "plan-id", "result-scope", "result-request", "result-id", "result-target", "result-receipt"} {
		t.Run(mode, func(t *testing.T) {
			m, calls, original := archiveRuntimeFixture(t, mode)
			provider, _ := m.KVRequests(1)
			lease, _ := provider.ReserveKV(703)
			defer lease.Close()
			facet, ok := lease.(contract.KVHTTPArchiveCapturing)
			if mode == "unsupported" {
				if ok {
					t.Fatal("unsupported provider advertised archive")
				}
				return
			}
			before := calls.Load()
			p, err := facet.PrepareHTTPArchive(runtimeArchiveRequest)
			if mode == "plan-scope" || mode == "plan-request" || mode == "plan-id" {
				if !errors.Is(err, contract.ErrRevoked) || p != nil || calls.Load() != before {
					t.Fatal("foreign preparation accepted or dispatched")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			a, r, err := facet.CaptureHTTPArchive(context.Background(), p)
			if !errors.Is(err, contract.ErrRevoked) || a == nil || r.SourceComplete || r.LocalError == "" || !reflect.DeepEqual(a.Receipt(), r) {
				t.Fatal("foreign receipt accepted or original discarded", err)
			}
			defer a.Close()
			reader, _ := a.OpenOriginal()
			defer reader.Close()
			body, err := io.ReadAll(reader)
			if err != nil || !bytes.Equal(body, original) {
				t.Fatal("failed authority check lost original evidence")
			}
		})
	}
}

func TestKVHTTPArchivesRetainUnknownPrivateACLWriteAcrossSnapshotAndPublicationRecovery(t *testing.T) {
	m, calls, _ := archiveRuntimeFixture(t, "")
	provider, _ := m.KVRequests(1)
	lease, err := provider.ReserveKV(704)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	request := contract.KVHTTPACLMutationRequest{Resource: "policy", Operation: "create", Definition: []byte(`{"Name":"private-archive-original","Rules":""}`)}
	mutating := lease.(contract.KVHTTPACLMutating)
	plan, err := mutating.PrepareHTTPACLMutation(request)
	if err != nil {
		t.Fatal(err)
	}
	written, err := mutating.ExecuteHTTPACLMutation(context.Background(), plan)
	if err == nil || !written.Receipt.WriteOutcomeUnknown || written.Scope.Generation != lease.Scope().Generation || lease.LastResult().HTTP != nil {
		t.Fatal("private unknown ACL write was not established")
	}
	before := calls.Load()
	recovered, err := provider.RecoverKV(lease)
	if err != nil || recovered.Lease == nil || recovered.ACLMutationResult == nil || !reflect.DeepEqual(*recovered.ACLMutationResult, written) || calls.Load() != before {
		t.Fatal("private ACL recovery discarded original or performed I/O", err)
	}
	lease = recovered.Lease
	defer lease.Close()
	archivePlan, err := lease.(contract.KVHTTPArchiveCapturing).PrepareHTTPArchive(runtimeArchiveRequest)
	if err != nil {
		t.Fatal("unknown ACL write blocked an independent snapshot read", err)
	}
	a, result, err := lease.(contract.KVHTTPArchiveCapturing).CaptureHTTPArchive(context.Background(), archivePlan)
	if err != nil || a == nil || !result.SourceComplete || !reflect.DeepEqual(lease.(contract.KVHTTPACLMutating).LastHTTPACLMutation(), written) || lease.LastResult().HTTP != nil {
		t.Fatal("complete snapshot replaced private unknown ACL evidence", err)
	}
	defer a.Close()
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	provider, _ = m.KVRequests(1)
	before = calls.Load()
	recovered, err = provider.RecoverKV(lease)
	if err != nil || recovered.Lease == nil || recovered.ACLMutationResult == nil || !reflect.DeepEqual(*recovered.ACLMutationResult, written) || calls.Load() != before {
		t.Fatal("publication recovery changed unknown ACL receipt", err)
	}
	defer recovered.Lease.Close()
	if _, err := recovered.Lease.(contract.KVHTTPACLMutating).PrepareHTTPACLMutation(request); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("snapshot authorized another ACL write")
	}
	if !reflect.DeepEqual(a.Receipt(), result) || recovered.Lease.Scope().Generation <= result.Scope.Original.Generation {
		t.Fatal("new publication relabeled original archive")
	}
}
