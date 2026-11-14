package snapshotfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type httpArchiveStoragePreparation struct {
	owner *KVSnapshotStore
	facet contract.KVHTTPArchiveCapturing
	plan  contract.KVHTTPArchivePreparation
	used  atomic.Bool
}

func (p *httpArchiveStoragePreparation) Scope() contract.KVHTTPArchiveScope {
	return p.plan.Scope().Clone()
}
func (p *httpArchiveStoragePreparation) Snapshot() contract.KVHTTPArchiveRequest {
	return p.plan.Snapshot()
}

type httpStoredArchive struct{ artifact *kvArtifact }

func (a *httpStoredArchive) Receipt() contract.KVHTTPArchiveCapture {
	a.artifact.mu.RLock()
	defer a.artifact.mu.RUnlock()
	if a.artifact.httpReceipt == nil {
		return contract.KVHTTPArchiveCapture{}
	}
	return a.artifact.httpReceipt.Clone()
}
func (a *httpStoredArchive) OpenArchive() (io.ReadCloser, error) { return a.artifact.OpenSnapshot() }
func (a *httpStoredArchive) OpenReceipt() (io.ReadCloser, error) { return a.artifact.OpenJournal() }
func (a *httpStoredArchive) Close() error                        { return a.artifact.Close() }

func (s *KVSnapshotStore) PrepareHTTPArchiveStorage(lease contract.KVRequestLease, r contract.KVHTTPArchiveRequest) (contract.KVHTTPArchiveStoragePreparation, error) {
	facet, ok := lease.(contract.KVHTTPArchiveCapturing)
	if !ok {
		return nil, contract.ErrUnsupported
	}
	plan, err := facet.PrepareHTTPArchive(r)
	if err != nil {
		return nil, err
	}
	if plan == nil || plan.Snapshot() != r || !reflect.DeepEqual(plan.Scope().Original, lease.Scope()) {
		return nil, ErrArtifactEvidence
	}
	if r.CompressedBytes > 64<<20 || r.ExpandedBytes > 256<<20 || r.CompressedBytes == 0 {
		return nil, ErrArtifactQuota
	}
	s.mu.Lock()
	closed, limit := s.closed, s.limit
	s.mu.Unlock()
	if closed {
		return nil, ErrArtifactClosed
	}
	if r.CompressedBytes+1<<20 > limit {
		return nil, ErrArtifactQuota
	}
	return &httpArchiveStoragePreparation{owner: s, facet: facet, plan: plan}, nil
}
func (s *KVSnapshotStore) CaptureHTTPArchiveStorage(parent context.Context, prepared contract.KVHTTPArchiveStoragePreparation) (stored contract.KVHTTPStoredArchive, source contract.KVHTTPArchive, result contract.KVHTTPArchiveCapture, resultErr error) {
	p, ok := prepared.(*httpArchiveStoragePreparation)
	if !ok || p == nil || p.owner != s {
		return nil, nil, result, contract.ErrKVPlanUsed
	}
	if parent.Err() != nil {
		return nil, nil, result, parent.Err()
	}
	charge := p.Snapshot().CompressedBytes + (1 << 20)
	s.mu.Lock()
	if s.closed || charge > s.limit-s.used {
		s.mu.Unlock()
		return nil, nil, result, ErrArtifactQuota
	}
	s.used += charge
	s.work.Add(1)
	s.mu.Unlock()
	defer s.work.Done()
	keep := false
	defer func() {
		if !keep {
			s.mu.Lock()
			s.used -= charge
			s.mu.Unlock()
		}
	}()
	if !p.used.CompareAndSwap(false, true) {
		return nil, nil, result, contract.ErrKVPlanUsed
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	source, native, nativeErr := p.facet.CaptureHTTPArchive(ctx, p.plan)
	result.Original = native.Clone()
	if source == nil {
		return nil, nil, result, nativeErr
	}
	defer func() {
		if resultErr != nil && !errors.Is(resultErr, nativeErr) {
			result.StorageError = artifactError(resultErr)
		}
	}()
	if !reflect.DeepEqual(source.Receipt(), native) {
		return nil, source, result, ErrArtifactEvidence
	}
	a, err := s.newArtifact()
	if err != nil {
		if a != nil {
			a.Close()
		}
		return nil, source, result, err
	}
	a.charge = charge
	keep = true
	s.mu.Lock()
	s.artifacts[a] = true
	s.mu.Unlock()
	good := false
	defer func() {
		if !good {
			_ = a.Close()
		}
	}()
	reader, err := source.OpenOriginal()
	if err != nil {
		return nil, source, result, err
	}
	defer reader.Close()
	file, err := a.create("snapshot", p.Snapshot().CompressedBytes)
	if err != nil {
		return nil, source, result, err
	}
	n, err := io.Copy(file, &archiveContextReader{ctx, reader})
	if err == nil {
		err = file.seal(s.syncFile)
	} else {
		err = errors.Join(err, file.abort())
	}
	if err != nil {
		return nil, source, result, err
	}
	result.ArchiveBytes = uint64(n)
	result.ArchiveSHA256 = file.sum.Sum(nil)
	if result.ArchiveBytes != native.OriginalBytes || !bytes.Equal(result.ArchiveSHA256, native.OriginalSHA256) {
		return nil, source, result, ErrArtifactEvidence
	}
	a.snapshotInfo, err = a.root.Lstat("snapshot")
	if err != nil {
		return nil, source, result, err
	}
	var reopenedHash []byte
	a.snapshot, reopenedHash, err = a.reopen(ctx, "snapshot", a.snapshotInfo, result.ArchiveBytes)
	if err != nil {
		return nil, source, result, err
	}
	if !bytes.Equal(reopenedHash, result.ArchiveSHA256) {
		return nil, source, result, ErrArtifactEvidence
	}
	journal, err := json.Marshal(native)
	if err != nil || len(journal) > 1<<20 {
		return nil, source, result, ErrArtifactQuota
	}
	file, err = a.create("journal", 1<<20)
	if err != nil {
		return nil, source, result, err
	}
	_, err = file.Write(journal)
	if err == nil {
		err = file.seal(s.syncFile)
	} else {
		err = errors.Join(err, file.abort())
	}
	if err != nil {
		return nil, source, result, err
	}
	result.ReceiptBytes = uint64(len(journal))
	sum := sha256.Sum256(journal)
	result.ReceiptSHA256 = sum[:]
	a.journalInfo, err = a.root.Lstat("journal")
	if err != nil {
		return nil, source, result, err
	}
	a.journal, reopenedHash, err = a.reopen(ctx, "journal", a.journalInfo, result.ReceiptBytes)
	if err != nil {
		return nil, source, result, err
	}
	if !bytes.Equal(reopenedHash, result.ReceiptSHA256) {
		return nil, source, result, ErrArtifactEvidence
	}
	dir, err := a.root.Open(".")
	if err != nil {
		return nil, source, result, err
	}
	synced, syncErr := snapshotSyncDirectory(dir)
	closeErr := dir.Close()
	if syncErr != nil || closeErr != nil {
		return nil, source, result, errors.Join(syncErr, closeErr)
	}
	result.Stored = true
	result.FilesSynced = true
	result.DirectorySynced = synced
	v := result.Clone()
	a.httpReceipt = &v
	stored = &httpStoredArchive{a}
	good = true
	// File storage and source ownership are separate. A native capture error
	// retains its exact prefix and receipt in both returned private resources.
	return stored, source, result, nativeErr
}

type archiveContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *archiveContextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}
func (s *KVSnapshotStore) PrepareHTTPArchiveFile(ctx context.Context, stored contract.KVHTTPStoredArchive, choice contract.KVSnapshotFileChoice) (contract.KVSnapshotFilePreparation, error) {
	a, ok := stored.(*httpStoredArchive)
	if !ok || a == nil {
		return nil, ErrArtifactEvidence
	}
	return s.prepareFile(ctx, a.artifact, choice, true)
}

var _ contract.KVHTTPArchiveArtifacts = (*KVSnapshotStore)(nil)
