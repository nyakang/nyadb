package snapshotfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"os"
	"reflect"
	"testing"
)

type httpArtifactTestPlan struct {
	scope   contract.KVHTTPArchiveScope
	request contract.KVHTTPArchiveRequest
}

func (p *httpArtifactTestPlan) Scope() contract.KVHTTPArchiveScope      { return p.scope.Clone() }
func (p *httpArtifactTestPlan) Snapshot() contract.KVHTTPArchiveRequest { return p.request }
func (p *httpArtifactTestPlan) TargetURL() string                       { return "http://original/v1/snapshot" }
func (p *httpArtifactTestPlan) LocalRecordID() string                   { return "original-record" }

type httpArtifactTestSource struct {
	body   []byte
	result contract.KVHTTPArchiveResult
	closed bool
}

func (a *httpArtifactTestSource) Receipt() contract.KVHTTPArchiveResult { return a.result.Clone() }
func (a *httpArtifactTestSource) OpenOriginal() (io.ReadCloser, error) {
	if a.closed {
		return nil, contract.ErrRevoked
	}
	return io.NopCloser(bytes.NewReader(a.body)), nil
}
func (a *httpArtifactTestSource) Close() error { a.closed = true; return nil }

type httpArtifactTestLease struct {
	contract.KVRequestLease
	scope     contract.KVScope
	calls     int
	nativeErr error
	source    *httpArtifactTestSource
}

func (l *httpArtifactTestLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *httpArtifactTestLease) PrepareHTTPArchive(r contract.KVHTTPArchiveRequest) (contract.KVHTTPArchivePreparation, error) {
	return &httpArtifactTestPlan{contract.KVHTTPArchiveScope{Original: l.Scope(), DocumentID: 1}, r}, nil
}
func (l *httpArtifactTestLease) CaptureHTTPArchive(ctx context.Context, p contract.KVHTTPArchivePreparation) (contract.KVHTTPArchive, contract.KVHTTPArchiveResult, error) {
	l.calls++
	body := []byte("private-native-original")
	sum := sha256.Sum256(body)
	r := contract.KVHTTPArchiveResult{Scope: p.Scope(), Request: p.Snapshot(), LocalRecordID: p.LocalRecordID(), OriginalBytes: uint64(len(body)), OriginalSHA256: sum[:], Attempted: true, Ended: true, SourceComplete: l.nativeErr == nil}
	l.source = &httpArtifactTestSource{body: body, result: r}
	return l.source, r.Clone(), l.nativeErr
}
func TestHTTPArchiveStorageSeparateNativeProofAndExclusivePublication(t *testing.T) {
	s, err := NewKVSnapshotStore(t.TempDir(), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l := &httpArtifactTestLease{scope: contract.KVScope{Namespace: contract.NamespaceRef{ProfileID: 1}, Generation: 9}}
	p, err := s.PrepareHTTPArchiveStorage(l, contract.KVHTTPArchiveRequest{CompressedBytes: 1 << 20, ExpandedBytes: 2 << 20})
	if err != nil || l.calls != 0 {
		t.Fatal("prepare dispatched", err)
	}
	stored, source, r, err := s.CaptureHTTPArchiveStorage(context.Background(), p)
	if err != nil || !r.Stored || !r.FilesSynced || !reflect.DeepEqual(stored.Receipt(), r) {
		t.Fatal("private storage proof lost", err)
	}
	defer stored.Close()
	defer source.Close()
	reader, err := stored.OpenArchive()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(raw, l.source.body) {
		t.Fatal("exact archive lost")
	}
	destination := t.TempDir() + "/archive.snap"
	plan, err := s.PrepareHTTPArchiveFile(context.Background(), stored, contract.KVSnapshotFileChoice{Path: destination, Kind: contract.KVSnapshotBackendFile})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	published, err := s.PublishFile(context.Background(), plan)
	if err != nil || !published.Published || published.Original.HTTPSource == nil || !reflect.DeepEqual(*published.Original.HTTPSource, r) {
		t.Fatal("publication lost source proof", err)
	}
	actual, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(actual, raw) {
		t.Fatal("published archive changed")
	}
	if _, err = s.PrepareFile(context.Background(), stored.(*httpStoredArchive).artifact, contract.KVSnapshotFileChoice{Path: t.TempDir() + "/foreign", Kind: contract.KVSnapshotBackendFile}); err == nil {
		t.Fatal("HTTP archive entered protobuf publication")
	}
	if _, _, _, err = s.CaptureHTTPArchiveStorage(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("capture replayed")
	}
}
func TestHTTPArchiveStorageNativeFailureAndDiskFailureRemainIndependent(t *testing.T) {
	for _, nativeFailure := range []bool{false, true} {
		s, err := NewKVSnapshotStore(t.TempDir(), 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		l := &httpArtifactTestLease{}
		if nativeFailure {
			l.nativeErr = errors.New("native-original-failure")
		} else {
			s.syncFile = func(*os.File) error { return errors.New("disk-sync-failure") }
		}
		p, err := s.PrepareHTTPArchiveStorage(l, contract.KVHTTPArchiveRequest{CompressedBytes: 1 << 20, ExpandedBytes: 2 << 20})
		if err != nil {
			t.Fatal(err)
		}
		stored, source, r, err := s.CaptureHTTPArchiveStorage(context.Background(), p)
		if err == nil || source == nil {
			t.Fatal("failure lost original source")
		}
		if nativeFailure {
			if stored == nil || r.StorageError != "" || !r.Stored || r.Original.SourceComplete {
				t.Fatal("native failure misclassified as storage failure")
			}
			stored.Close()
		} else {
			if stored != nil || r.Stored || r.StorageError == "" {
				t.Fatal("disk failure promoted")
			}
		}
		source.Close()
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		if s.used != 0 {
			t.Fatal("storage quota leaked")
		}
	}
}
