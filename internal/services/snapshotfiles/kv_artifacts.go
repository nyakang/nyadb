package snapshotfiles

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var ErrArtifactClosed = errors.New("原快照归档已关闭")
var ErrArtifactQuota = errors.New("原快照归档磁盘预算不足")
var ErrArtifactEvidence = errors.New("原快照文件与完整执行回执不一致")

// KVSnapshotStore owns bounded private artifacts. The configured directory is
// opened once; each capture uses an exclusive original subdirectory and files.
// File publication uses a separate one-use plan for an explicitly chosen path.
type KVSnapshotStore struct {
	mu          sync.Mutex
	root        *os.Root
	limit, used uint64
	ctx         context.Context
	cancel      context.CancelFunc
	work        sync.WaitGroup
	closed      bool
	closeDone   chan struct{}
	closeErr    error
	artifacts   map[*kvArtifact]bool
	filePlans   map[*kvSnapshotFilePlan]bool
	fileIO      kvSnapshotFileIO
	write       func(*os.File, []byte) (int, error)
	syncFile    func(*os.File) error
	readAt      func(*os.File, []byte, int64) (int, error)
}

func NewKVSnapshotStore(directory string, limit uint64) (*KVSnapshotStore, error) {
	if limit == 0 || limit > math.MaxInt64 {
		return nil, ErrArtifactQuota
	}
	root, err := snapshotOpenRoot(directory)
	if err != nil {
		return nil, err
	}
	return newKVSnapshotStore(root, limit), nil
}

// NewKVSnapshotStoreAt clones the original opened directory. A renamed or
// replaced path cannot redirect bootstrap's already captured storage owner.
// The caller retains ownership of directory; the store owns its independent root.
func NewKVSnapshotStoreAt(directory *os.Root, limit uint64) (*KVSnapshotStore, error) {
	if directory == nil {
		return nil, os.ErrInvalid
	}
	if limit == 0 || limit > math.MaxInt64 {
		return nil, ErrArtifactQuota
	}
	root, err := directory.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	return newKVSnapshotStore(root, limit), nil
}
func newKVSnapshotStore(root *os.Root, limit uint64) *KVSnapshotStore {
	ctx, cancel := context.WithCancel(context.Background())
	return &KVSnapshotStore{root: root, limit: limit, ctx: ctx, cancel: cancel, closeDone: make(chan struct{}), artifacts: make(map[*kvArtifact]bool), filePlans: make(map[*kvSnapshotFilePlan]bool), fileIO: defaultKVSnapshotFileIO(), write: func(f *os.File, b []byte) (int, error) { return f.Write(b) }, syncFile: func(f *os.File) error { return f.Sync() }, readAt: func(f *os.File, b []byte, o int64) (int, error) { return f.ReadAt(b, o) }}
}

type kvCapturePreparation struct {
	authority *kvCapturePreparation
	owner     *KVSnapshotStore
	lease     contract.KVRequestLease
	facet     contract.KVSnapshots
	native    contract.KVSnapshotPreparation
	request   contract.KVSnapshotRequest
	scope     contract.KVSnapshotScope
	reserved  uint64
	used      atomic.Bool
}

func (p *kvCapturePreparation) Snapshot() contract.KVSnapshotRequest { return p.request }
func (p *kvCapturePreparation) Scope() contract.KVSnapshotScope      { return p.scope.Clone() }
func kvCaptureReservation(r contract.KVSnapshotRequest) (uint64, error) {
	if r.BodyBytes == 0 || r.BodyBytes > math.MaxInt64-sha256.Size || r.JournalBytes == 0 || r.JournalBytes > math.MaxInt64 || r.Frames < 2 || r.Frames > math.MaxInt64 {
		return 0, ErrArtifactQuota
	}
	snapshot := r.BodyBytes + sha256.Size
	if r.JournalBytes > math.MaxInt64-snapshot {
		return 0, ErrArtifactQuota
	}
	return snapshot + r.JournalBytes, nil
}
func (s *KVSnapshotStore) Prepare(lease contract.KVRequestLease, r contract.KVSnapshotRequest) (contract.KVSnapshotCapturePreparation, error) {
	reserved, err := kvCaptureReservation(r)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	closed, limit := s.closed, s.limit
	s.mu.Unlock()
	if closed {
		return nil, ErrArtifactClosed
	}
	if reserved > limit {
		return nil, ErrArtifactQuota
	}
	if lease == nil {
		return nil, contract.ErrUnsupported
	}
	facet, ok := lease.(contract.KVSnapshots)
	if !ok {
		return nil, contract.ErrUnsupported
	}
	if err := lease.CheckKVLease(); err != nil {
		return nil, err
	}
	native, err := facet.PrepareSnapshot(r)
	if err != nil {
		return nil, err
	}
	if native == nil || native.Snapshot() != r {
		return nil, ErrArtifactEvidence
	}
	scope := native.Scope().Clone()
	if scope.DocumentID == 0 || !reflect.DeepEqual(scope.Original, lease.Scope()) {
		return nil, ErrArtifactEvidence
	}
	p := &kvCapturePreparation{owner: s, lease: lease, facet: facet, native: native, request: r, scope: scope, reserved: reserved}
	p.authority = p
	return p, nil
}

type kvCaptureFile struct {
	mu             sync.Mutex
	file           *os.File
	info           os.FileInfo
	limit, written uint64
	sum            hash.Hash
	stopped        bool
	failed         error
	write          func(*os.File, []byte) (int, error)
}

func (f *kvCaptureFile) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return 0, ErrArtifactClosed
	}
	if f.failed != nil {
		return 0, f.failed
	}
	if uint64(len(b)) > f.limit-f.written {
		f.failed = ErrArtifactQuota
		return 0, f.failed
	}
	n, err := f.write(f.file, b)
	if n < 0 || n > len(b) {
		n, err = 0, ErrArtifactEvidence
	}
	f.written += uint64(n)
	_, _ = f.sum.Write(b[:n])
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	f.failed = err
	return n, err
}
func (f *kvCaptureFile) seal(syncFile func(*os.File) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return errors.Join(f.failed, syncFile(f.file), f.file.Close())
}
func (f *kvCaptureFile) abort() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return nil
	}
	f.stopped = true
	return f.file.Close()
}

type kvArtifact struct {
	authority                 *kvArtifact
	mu                        sync.RWMutex
	store                     *KVSnapshotStore
	directory                 string
	root                      *os.Root
	directoryInfo             os.FileInfo
	snapshot, journal         *os.File
	snapshotInfo, journalInfo os.FileInfo
	receipt                   contract.KVSnapshotCapture
	charge                    uint64
	closed                    bool
	closeErr                  error
}

func (s *KVSnapshotStore) newArtifact() (a *kvArtifact, err error) {
	name := fmt.Sprintf(".nyadb-kv-snapshot-%s", rand.Text())
	if err = s.root.Mkdir(name, 0700); err != nil {
		return nil, err
	}
	a = &kvArtifact{store: s, directory: name}
	a.authority = a
	a.directoryInfo, err = s.root.Lstat(name)
	if err != nil {
		return a, err
	}
	a.root, err = s.root.OpenRoot(name)
	return a, err
}
func (a *kvArtifact) create(name string, limit uint64) (*kvCaptureFile, error) {
	f, err := a.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if name == "snapshot" {
		a.snapshotInfo = info
	} else {
		a.journalInfo = info
	}
	return &kvCaptureFile{file: f, info: info, limit: limit, sum: sha256.New(), write: a.store.write}, nil
}

// Each hash read belongs to the captured context and original open file.
type kvArtifactHashReader struct {
	ctx    context.Context
	file   *os.File
	readAt func(*os.File, []byte, int64) (int, error)
}

func (r kvArtifactHashReader) ReadAt(b []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.readAt(r.file, b, offset)
}

func (a *kvArtifact) reopen(ctx context.Context, name string, expected os.FileInfo, size uint64) (*os.File, []byte, error) {
	f, err := snapshotOpenRegular(a.root, name)
	if err != nil {
		return nil, nil, err
	}
	good := false
	defer func() {
		if !good {
			_ = f.Close()
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || uint64(info.Size()) != size || !os.SameFile(info, expected) {
		return nil, nil, ErrArtifactEvidence
	}
	sum := sha256.New()
	n, err := io.CopyBuffer(sum, io.NewSectionReader(kvArtifactHashReader{ctx: ctx, file: f, readAt: a.store.readAt}, 0, int64(size)), make([]byte, 128<<10))
	if err != nil || uint64(n) != size {
		return nil, nil, errors.Join(err, ErrArtifactEvidence)
	}
	good = true
	return f, sum.Sum(nil), nil
}
func (a *kvArtifact) verify(ctx context.Context, snapshot, journal *kvCaptureFile, p *kvCapturePreparation) error {
	r := &a.receipt
	original := r.Original
	if !original.Ended || original.Request != p.request || !reflect.DeepEqual(original.Scope, p.scope) {
		return ErrArtifactEvidence
	}
	r.SnapshotBytes, r.JournalBytes = snapshot.written, journal.written
	if r.SnapshotBytes != original.SnapshotBytesWritten || r.JournalBytes != original.JournalBytesWritten {
		return ErrArtifactEvidence
	}
	var err error
	a.snapshot, r.SnapshotSHA256, err = a.reopen(ctx, "snapshot", snapshot.info, snapshot.written)
	if err != nil {
		return err
	}
	a.journal, r.JournalSHA256, err = a.reopen(ctx, "journal", journal.info, journal.written)
	if err != nil {
		return err
	}
	if !bytes.Equal(r.SnapshotSHA256, snapshot.sum.Sum(nil)) || !bytes.Equal(r.JournalSHA256, journal.sum.Sum(nil)) || !bytes.Equal(r.JournalSHA256, original.JournalSHA256) {
		return ErrArtifactEvidence
	}
	if original.Completed && (!original.Attempted || !original.NativeTerminalObserved || !original.SourceComplete || !original.EOF || !original.ChecksumMatched || !original.JournalComplete || !original.OutputAcknowledged || original.Canceled || original.Frames < 2 || original.Frames > p.request.Frames) {
		return ErrArtifactEvidence
	}
	if original.SourceComplete {
		if !original.EOF || !original.ChecksumMatched || original.BodyBytes == 0 || original.BodyBytes != original.ExpectedBodyBytes || original.BodyBytes > p.request.BodyBytes || len(original.Checksum) != sha256.Size || r.SnapshotBytes != original.BodyBytes+sha256.Size {
			return ErrArtifactEvidence
		}
		sum := sha256.New()
		n, e := io.CopyBuffer(sum, io.NewSectionReader(kvArtifactHashReader{ctx: ctx, file: a.snapshot, readAt: a.store.readAt}, 0, int64(original.BodyBytes)), make([]byte, 128<<10))
		if e != nil || uint64(n) != original.BodyBytes || !bytes.Equal(sum.Sum(nil), original.BodySHA256) || !bytes.Equal(sum.Sum(nil), original.Checksum) {
			return errors.Join(e, ErrArtifactEvidence)
		}
		footer := make([]byte, sha256.Size)
		if _, e = a.snapshot.ReadAt(footer, int64(original.BodyBytes)); e != nil || !bytes.Equal(footer, original.Checksum) {
			return errors.Join(e, ErrArtifactEvidence)
		}
	}
	r.Stored = true
	r.Complete = original.Completed && original.SourceComplete && original.JournalComplete && original.OutputAcknowledged && !original.Canceled && r.FilesSynced
	return nil
}
func artifactError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 65536 {
		return s[:65536]
	}
	return s
}
func (s *KVSnapshotStore) Capture(parent context.Context, preparation contract.KVSnapshotCapturePreparation) (contract.KVSnapshotArtifact, contract.KVSnapshotCapture, error) {
	p, ok := preparation.(*kvCapturePreparation)
	if !ok || p == nil || p.authority != p || p.owner != s {
		return nil, contract.KVSnapshotCapture{}, contract.ErrKVPlanUsed
	}
	r := contract.KVSnapshotCapture{Original: contract.KVSnapshotResult{Scope: p.Scope(), Request: p.request}}
	if err := parent.Err(); err != nil {
		return nil, r, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, r, ErrArtifactClosed
	}
	if p.used.Load() {
		s.mu.Unlock()
		return nil, r, contract.ErrKVPlanUsed
	}
	if p.reserved > s.limit-s.used {
		s.mu.Unlock()
		return nil, r, ErrArtifactQuota
	}
	if !p.used.CompareAndSwap(false, true) {
		s.mu.Unlock()
		return nil, r, contract.ErrKVPlanUsed
	}
	s.used += p.reserved
	s.work.Add(1)
	s.mu.Unlock()
	defer s.work.Done()
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	a, err := s.newArtifact()
	if a == nil {
		s.mu.Lock()
		s.used -= p.reserved
		s.mu.Unlock()
		return nil, r, err
	}
	a.charge = p.reserved
	keep := false
	defer func() {
		if !keep {
			_ = a.Close()
		}
	}()
	var nativeError error
	fail := func(err error) (contract.KVSnapshotArtifact, contract.KVSnapshotCapture, error) {
		r.StorageError = artifactError(err)
		cleanup := a.Close()
		r.CleanupError = artifactError(cleanup)
		return nil, r.Clone(), errors.Join(nativeError, err, cleanup)
	}
	if err != nil {
		return fail(err)
	}
	snapshot, err := a.create("snapshot", p.request.BodyBytes+sha256.Size)
	if err != nil {
		return fail(err)
	}
	defer snapshot.abort()
	journal, err := a.create("journal", p.request.JournalBytes)
	if err != nil {
		_ = snapshot.abort()
		return fail(err)
	}
	defer journal.abort()
	original, sourceError := p.facet.ExecuteSnapshot(ctx, p.native, contract.KVSnapshotOutputs{Snapshot: snapshot, Journal: journal})
	nativeError = sourceError
	r.Original = original.Clone()
	r.SnapshotBytes, r.JournalBytes = snapshot.written, journal.written
	syncError := errors.Join(snapshot.seal(s.syncFile), journal.seal(s.syncFile))
	if err := ctx.Err(); err != nil {
		return fail(errors.Join(syncError, err))
	}
	r.FilesSynced = syncError == nil
	a.receipt = r.Clone()
	if syncError != nil {
		return fail(syncError)
	}
	if err := a.verify(ctx, snapshot, journal, p); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fail(ErrArtifactClosed)
	}
	actual := snapshot.written + journal.written
	s.used -= a.charge - actual
	a.charge = actual
	s.artifacts[a] = true
	s.mu.Unlock()
	keep = true
	return a, a.receipt.Clone(), nativeError
}
func (a *kvArtifact) Receipt() contract.KVSnapshotCapture {
	if a == nil || a.authority != a {
		return contract.KVSnapshotCapture{StorageError: ErrArtifactClosed.Error()}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.receipt.Clone()
}
func (a *kvArtifact) remove(name string, expected os.FileInfo) error {
	if a.root == nil || expected == nil {
		return nil
	}
	info, err := a.root.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, expected) {
		return ErrChanged
	}
	return a.root.Remove(name)
}
func (a *kvArtifact) Close() error {
	if a == nil || a.authority != a {
		return ErrArtifactClosed
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return a.closeErr
	}
	a.closed = true
	var err error
	if a.snapshot != nil {
		err = errors.Join(err, a.snapshot.Close())
	}
	if a.journal != nil {
		err = errors.Join(err, a.journal.Close())
	}
	err = errors.Join(err, a.remove("snapshot", a.snapshotInfo), a.remove("journal", a.journalInfo))
	if a.root != nil {
		err = errors.Join(err, a.root.Close())
	}
	info, e := a.store.root.Lstat(a.directory)
	if !os.IsNotExist(e) {
		if e == nil && a.directoryInfo != nil && os.SameFile(info, a.directoryInfo) {
			e = a.store.root.Remove(a.directory)
		} else if e == nil {
			e = ErrChanged
		}
		err = errors.Join(err, e)
	}
	a.closeErr = err
	a.receipt.CleanupError = artifactError(err)
	a.store.mu.Lock()
	if err == nil {
		delete(a.store.artifacts, a)
		a.store.used -= a.charge
		a.charge = 0
	} else {
		a.store.artifacts[a] = true
	}
	a.store.mu.Unlock()
	return err
}
func (s *KVSnapshotStore) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return s.closeErr
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.work.Wait()
	s.mu.Lock()
	artifacts := make([]*kvArtifact, 0, len(s.artifacts))
	for a := range s.artifacts {
		artifacts = append(artifacts, a)
	}
	s.mu.Unlock()
	var err error
	s.mu.Lock()
	plans := make([]*kvSnapshotFilePlan, 0, len(s.filePlans))
	for p := range s.filePlans {
		plans = append(plans, p)
	}
	s.mu.Unlock()
	for _, p := range plans {
		err = errors.Join(err, p.Close())
	}
	for _, a := range artifacts {
		err = errors.Join(err, a.Close())
	}
	err = errors.Join(err, s.root.Close())
	s.mu.Lock()
	s.closeErr = err
	close(s.closeDone)
	s.mu.Unlock()
	return err
}

type kvArtifactReader struct {
	mu           sync.Mutex
	owner        *kvArtifact
	file         *os.File
	size, offset uint64
	closed       bool
}

func (a *kvArtifact) open(journal bool) (io.ReadCloser, error) {
	if a == nil || a.authority != a {
		return nil, ErrArtifactClosed
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return nil, ErrArtifactClosed
	}
	f, size := a.snapshot, a.receipt.SnapshotBytes
	if journal {
		f, size = a.journal, a.receipt.JournalBytes
	}
	return &kvArtifactReader{owner: a, file: f, size: size}, nil
}
func (a *kvArtifact) OpenSnapshot() (io.ReadCloser, error) { return a.open(false) }
func (a *kvArtifact) OpenJournal() (io.ReadCloser, error)  { return a.open(true) }
func (r *kvArtifactReader) Read(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owner.mu.RLock()
	defer r.owner.mu.RUnlock()
	if r.closed || r.owner.closed {
		return 0, ErrArtifactClosed
	}
	if len(b) == 0 {
		return 0, nil
	}
	if r.offset == r.size {
		return 0, io.EOF
	}
	if uint64(len(b)) > r.size-r.offset {
		b = b[:int(r.size-r.offset)]
	}
	n, err := r.owner.store.readAt(r.file, b, int64(r.offset))
	if n < 0 || n > len(b) {
		return 0, ErrArtifactEvidence
	}
	r.offset += uint64(n)
	if err == nil && n != len(b) {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
func (r *kvArtifactReader) Close() error { r.mu.Lock(); r.closed = true; r.mu.Unlock(); return nil }

var _ contract.KVSnapshotArtifacts = (*KVSnapshotStore)(nil)
var _ contract.KVSnapshotArtifact = (*kvArtifact)(nil)
