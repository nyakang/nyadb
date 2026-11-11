package snapshotfiles

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type kvSnapshotFileIO struct {
	move                func(*os.File, string, *os.File, string) error
	write               func(*os.File, []byte) (int, error)
	readAt              func(*os.File, []byte, int64) (int, error)
	syncFile, closeFile func(*os.File) error
	remove              func(*os.Root, string) error
}

func defaultKVSnapshotFileIO() kvSnapshotFileIO {
	return kvSnapshotFileIO{move: snapshotMoveNoReplace, write: func(f *os.File, b []byte) (int, error) { return f.Write(b) }, readAt: func(f *os.File, b []byte, o int64) (int, error) { return f.ReadAt(b, o) }, syncFile: func(f *os.File) error { return f.Sync() }, closeFile: func(f *os.File) error { return f.Close() }, remove: func(r *os.Root, n string) error { return r.Remove(n) }}
}

type kvSnapshotFilePlan struct {
	authority                  *kvSnapshotFilePlan
	store                      *KVSnapshotStore
	artifact                   *kvArtifact
	observation                contract.KVSnapshotFileObservation
	size                       uint64
	sum                        []byte
	mu                         sync.Mutex
	ctx                        context.Context
	cancel                     context.CancelFunc
	used, closed               bool
	closeErr                   error
	parent                     *os.Root
	parentFile                 *os.File
	parentInfo, existingInfo   os.FileInfo
	name, directory            string
	private                    *os.Root
	privateFile                *os.File
	privateInfo, temporaryInfo os.FileInfo
	charge                     uint64
	retained                   bool
}

func (p *kvSnapshotFilePlan) Observation() contract.KVSnapshotFileObservation {
	if p == nil || p.authority != p {
		return contract.KVSnapshotFileObservation{}
	}
	return p.observation.Clone()
}

// The old file is inspected against its own captured finite size, independently
// of the new output quota. Lstat/open/stat comparisons reject symlink races.
func (p *kvSnapshotFilePlan) inspect(ctx context.Context, root *os.Root, name string, expected os.FileInfo) (contract.SnapshotSource, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return contract.SnapshotSource{}, nil, err
	}
	before, err := root.Lstat(name)
	if err != nil {
		return contract.SnapshotSource{}, nil, err
	}
	if !before.Mode().IsRegular() || before.Size() < 0 {
		return contract.SnapshotSource{}, nil, ErrBounds
	}
	if expected != nil && (!os.SameFile(before, expected) || before.Size() != expected.Size() || !before.ModTime().Equal(expected.ModTime())) {
		return contract.SnapshotSource{}, nil, ErrChanged
	}
	f, err := root.Open(name)
	if err != nil {
		return contract.SnapshotSource{}, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return contract.SnapshotSource{}, nil, err
	}
	if !os.SameFile(before, opened) || opened.Size() != before.Size() || !opened.Mode().IsRegular() {
		return contract.SnapshotSource{}, nil, ErrChanged
	}
	h := sha256.New()
	n, err := io.CopyBuffer(h, io.NewSectionReader(kvArtifactHashReader{ctx: ctx, file: f, readAt: p.store.fileIO.readAt}, 0, before.Size()), make([]byte, 128<<10))
	if err != nil || n != before.Size() {
		return contract.SnapshotSource{}, nil, errors.Join(err, ErrChanged)
	}
	// A SectionReader alone would hide growth beyond the captured old size.
	if err := ctx.Err(); err != nil {
		return contract.SnapshotSource{}, nil, err
	}
	probe := make([]byte, 1)
	extra, e := p.store.fileIO.readAt(f, probe, before.Size())
	after, statErr := f.Stat()
	named, nameErr := root.Lstat(name)
	if extra != 0 || e != io.EOF || statErr != nil || nameErr != nil {
		return contract.SnapshotSource{}, nil, errors.Join(e, statErr, nameErr, ErrChanged)
	}
	if !os.SameFile(after, before) || !os.SameFile(named, before) || !named.Mode().IsRegular() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return contract.SnapshotSource{}, nil, ErrChanged
	}
	if err := ctx.Err(); err != nil {
		return contract.SnapshotSource{}, nil, err
	}
	return contract.SnapshotSource{Name: name, Size: uint64(n), SHA256: hex.EncodeToString(h.Sum(nil))}, before, nil
}
func (s *KVSnapshotStore) PrepareFile(parent context.Context, artifact contract.KVSnapshotArtifact, choice contract.KVSnapshotFileChoice) (contract.KVSnapshotFilePreparation, error) {
	a, ok := artifact.(*kvArtifact)
	if !ok || a == nil || a.authority != a || a.store != s {
		return nil, ErrArtifactEvidence
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if choice.Kind != contract.KVSnapshotBackendFile && choice.Kind != contract.KVSnapshotJournalFile {
		return nil, contract.ErrUnsupported
	}
	if choice.Path == "" {
		return nil, ErrBounds
	}
	path, err := filepath.Abs(choice.Path)
	if err != nil {
		return nil, err
	}
	choice.Path = filepath.Clean(path)
	name := filepath.Base(choice.Path)
	if name == "." || name == string(filepath.Separator) {
		return nil, ErrBounds
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrArtifactClosed
	}
	s.work.Add(1)
	s.mu.Unlock()
	defer s.work.Done()
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	a.mu.RLock()
	if a.closed {
		a.mu.RUnlock()
		return nil, ErrArtifactClosed
	}
	r := a.receipt.Clone()
	a.mu.RUnlock()
	if choice.Kind == contract.KVSnapshotBackendFile && !r.Complete {
		return nil, ErrArtifactEvidence
	}
	if choice.Kind == contract.KVSnapshotJournalFile && (!r.Stored || !r.FilesSynced || !r.Original.Ended || !r.Original.JournalComplete) {
		return nil, ErrArtifactEvidence
	}
	pctx, pcancel := context.WithCancel(s.ctx)
	p := &kvSnapshotFilePlan{store: s, artifact: a, observation: contract.KVSnapshotFileObservation{Source: r, Choice: choice}, name: name, ctx: pctx, cancel: pcancel}
	p.authority = p
	p.size, p.sum = r.SnapshotBytes, bytes.Clone(r.SnapshotSHA256)
	if choice.Kind == contract.KVSnapshotJournalFile {
		p.size, p.sum = r.JournalBytes, bytes.Clone(r.JournalSHA256)
	}
	if p.size == 0 || p.size > math.MaxInt64 || len(p.sum) != sha256.Size {
		pcancel()
		return nil, ErrArtifactEvidence
	}
	good := false
	defer func() {
		if !good {
			_ = p.Close()
		}
	}()
	p.parent, err = os.OpenRoot(filepath.Dir(choice.Path))
	if err != nil {
		return nil, err
	}
	p.parentFile, err = p.parent.Open(".")
	if err != nil {
		return nil, err
	}
	p.parentInfo, err = p.parentFile.Stat()
	if err != nil {
		return nil, err
	}
	info, err := p.parent.Lstat(name)
	if os.IsNotExist(err) {
		if choice.Replace {
			return nil, ErrChanged
		}
	} else {
		if err != nil {
			return nil, err
		}
		if !choice.Replace {
			return nil, os.ErrExist
		}
		original, originalInfo, e := p.inspect(ctx, p.parent, name, info)
		if e != nil {
			return nil, e
		}
		p.existingInfo = originalInfo
		p.observation.Existing = &original
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Both shutdown and artifact closure revoke preparation authority.
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return nil, ErrArtifactClosed
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrArtifactClosed
	}
	s.filePlans[p] = true
	s.mu.Unlock()
	good = true
	return p, nil
}
func (p *kvSnapshotFilePlan) reserve() error {
	s := p.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrArtifactClosed
	}
	if p.size > s.limit-s.used {
		return ErrArtifactQuota
	}
	s.used += p.size
	p.charge = p.size
	return nil
}
func (p *kvSnapshotFilePlan) release() {
	p.store.mu.Lock()
	p.store.used -= p.charge
	p.charge = 0
	p.store.mu.Unlock()
}
func (p *kvSnapshotFilePlan) createTemporary() (f *os.File, err error) {
	p.directory = ".nyadb-snapshot-save-" + rand.Text()
	if err = p.parent.Mkdir(p.directory, 0700); err != nil {
		p.directory = ""
		return nil, err
	}
	p.privateInfo, err = p.parent.Lstat(p.directory)
	if err != nil {
		return nil, err
	}
	p.private, err = p.parent.OpenRoot(p.directory)
	if err != nil {
		return nil, err
	}
	p.privateFile, err = p.private.Open(".")
	if err != nil {
		return nil, err
	}
	opened, err := p.privateFile.Stat()
	if err != nil || !os.SameFile(opened, p.privateInfo) {
		return nil, errors.Join(err, ErrChanged)
	}
	f, err = p.private.OpenFile("new", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	p.temporaryInfo, err = f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
func (p *kvSnapshotFilePlan) copy(ctx context.Context, f *os.File, r *contract.KVSnapshotFileResult) error {
	var source io.ReadCloser
	var err error
	if p.observation.Choice.Kind == contract.KVSnapshotJournalFile {
		source, err = p.artifact.OpenJournal()
	} else {
		source, err = p.artifact.OpenSnapshot()
	}
	if err != nil {
		return err
	}
	defer source.Close()
	buffer := make([]byte, 128<<10)
	h := sha256.New()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, e := source.Read(buffer)
		if n < 0 || n > len(buffer) || uint64(n) > p.size-r.BytesWritten {
			return ErrArtifactEvidence
		}
		if n > 0 {
			count, writeErr := p.store.fileIO.write(f, buffer[:n])
			if count < 0 || count > n {
				return ErrArtifactEvidence
			}
			r.BytesWritten += uint64(count)
			_, _ = h.Write(buffer[:count])
			r.SHA256 = h.Sum(nil)
			if writeErr != nil {
				return writeErr
			}
			if count != n {
				return io.ErrShortWrite
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	if r.BytesWritten != p.size || !bytes.Equal(h.Sum(nil), p.sum) {
		return ErrArtifactEvidence
	}
	r.SourceVerified = true
	return ctx.Err()
}
func moveUnknown(err error) bool {
	return err != nil && !errors.Is(err, os.ErrExist) && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, contract.ErrUnsupported)
}
func (p *kvSnapshotFilePlan) restore(r *contract.KVSnapshotFileResult) error {
	if p.private == nil {
		return nil
	}
	if _, err := p.private.Lstat("displaced"); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	r.DisplacedPath = filepath.Join(filepath.Dir(p.observation.Choice.Path), p.directory, "displaced")
	r.RestorationAttempted = true
	err := p.store.fileIO.move(p.privateFile, "displaced", p.parentFile, p.name)
	r.Restored = err == nil
	r.RestorationUnknown = moveUnknown(err)
	if err == nil {
		r.DisplacedPath = ""
	}
	return err
}
func (p *kvSnapshotFilePlan) publish(ctx context.Context, r *contract.KVSnapshotFileResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// The artifact remains live for the original final filesystem executor.
	p.artifact.mu.RLock()
	defer p.artifact.mu.RUnlock()
	if p.artifact.closed {
		return ErrArtifactClosed
	}
	if p.existingInfo != nil {
		r.DisplacementAttempted = true
		err := p.store.fileIO.move(p.parentFile, p.name, p.privateFile, "displaced")
		r.Displaced = err == nil
		r.DisplacementUnknown = moveUnknown(err)
		if err != nil {
			return errors.Join(err, p.restore(r))
		}
		r.DisplacedPath = filepath.Join(filepath.Dir(p.observation.Choice.Path), p.directory, "displaced")
		old, _, err := p.inspect(ctx, p.private, "displaced", p.existingInfo)
		expected := *p.observation.Existing
		old.Name = expected.Name
		if err != nil || old != expected {
			return errors.Join(err, ErrChanged, p.restore(r))
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, p.restore(r))
	}
	r.PublicationAttempted = true
	err := p.store.fileIO.move(p.privateFile, "new", p.parentFile, p.name)
	r.Published = err == nil
	r.PublicationUnknown = moveUnknown(err)
	if err != nil {
		// Unknown publication may have installed the new file. Never remove it.
		return errors.Join(err, p.restore(r))
	}
	// Publication transfers ownership. All subsequent failures retain the file.
	p.release()
	return nil
}
func (p *kvSnapshotFilePlan) observe(ctx context.Context, r *contract.KVSnapshotFileResult) error {
	dest, _, err := p.inspect(ctx, p.parent, p.name, p.temporaryInfo)
	r.DestinationMatched = err == nil && dest.Size == p.size && dest.SHA256 == hex.EncodeToString(p.sum)
	info, pathErr := os.Stat(filepath.Dir(p.observation.Choice.Path))
	r.PathMatched = pathErr == nil && os.SameFile(info, p.parentInfo)
	if !r.DestinationMatched || !r.PathMatched {
		return errors.Join(err, pathErr, ErrChanged)
	}
	return nil
}

// Cleanup never removes the published name or a displaced user file. Failed
// private cleanup stays charged. Replacements of private names are preserved.
func (p *kvSnapshotFilePlan) cleanup() error {
	if p.directory == "" {
		p.release()
		return nil
	}
	var err error
	if p.private != nil {
		if info, e := p.private.Lstat("new"); !os.IsNotExist(e) {
			if e != nil {
				err = e
			} else if p.temporaryInfo == nil || !info.Mode().IsRegular() || !os.SameFile(info, p.temporaryInfo) {
				err = ErrChanged
			} else {
				err = p.store.fileIO.remove(p.private, "new")
			}
		}
		if _, e := p.private.Lstat("displaced"); !os.IsNotExist(e) {
			p.retained = true
			if e != nil {
				err = errors.Join(err, e)
			}
		}
	}
	if err != nil {
		return err
	}
	// A retained displaced file is user-owned and has no snapshot disk charge.
	if p.retained {
		p.release()
		return nil
	}
	info, e := p.parent.Lstat(p.directory)
	if os.IsNotExist(e) {
		p.release()
		return nil
	}
	if e != nil {
		return e
	}
	if p.privateInfo == nil || !os.SameFile(info, p.privateInfo) {
		return ErrChanged
	}
	if e = p.store.fileIO.remove(p.parent, p.directory); e != nil {
		return e
	}
	p.release()
	return nil
}
func (p *kvSnapshotFilePlan) Close() error {
	if p == nil || p.authority != p {
		return contract.ErrKVPlanUsed
	}
	p.cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	err := p.cleanup()
	if p.privateFile != nil {
		err = errors.Join(err, p.privateFile.Close())
	}
	if p.private != nil {
		err = errors.Join(err, p.private.Close())
	}
	if p.parentFile != nil {
		err = errors.Join(err, p.parentFile.Close())
	}
	if p.parent != nil {
		err = errors.Join(err, p.parent.Close())
	}
	p.closeErr = err
	p.store.mu.Lock()
	if err == nil {
		delete(p.store.filePlans, p)
	}
	p.store.mu.Unlock()
	return err
}
func (s *KVSnapshotStore) PublishFile(parent context.Context, preparation contract.KVSnapshotFilePreparation) (result contract.KVSnapshotFileResult, resultErr error) {
	p, ok := preparation.(*kvSnapshotFilePlan)
	if !ok || p == nil || p.authority != p || p.store != s {
		return result, contract.ErrKVPlanUsed
	}
	result.Original = p.Observation()
	result.DestinationPath = p.observation.Choice.Path
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.used {
		return result, contract.ErrKVPlanUsed
	}
	if err := parent.Err(); err != nil {
		return result, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return result, ErrArtifactClosed
	}
	s.work.Add(1)
	s.mu.Unlock()
	defer s.work.Done()
	if err := p.ctx.Err(); err != nil {
		return result, err
	}
	p.used = true
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	defer cancel()
	result.Attempted = true
	defer func() {
		cleanup := p.cleanup()
		if p.private != nil {
			_, e := p.private.Lstat("new")
			result.TemporaryRetained = e == nil
			_, e = p.private.Lstat("displaced")
			result.DisplacedRetained = e == nil
		}
		info, pathErr := os.Stat(filepath.Dir(p.observation.Choice.Path))
		result.PathMatched = pathErr == nil && os.SameFile(info, p.parentInfo)
		if p.directory != "" {
			privateInfo, e := p.parent.Lstat(p.directory)
			result.AuxiliaryPathMatched = result.PathMatched && e == nil && p.privateInfo != nil && os.SameFile(privateInfo, p.privateInfo)
		}
		result.CleanupError = artifactError(cleanup)
		resultErr = errors.Join(resultErr, ctx.Err(), cleanup)
		result.Error = artifactError(resultErr)
		result.Ended = true
		result.Canceled = ctx.Err() != nil
	}()
	p.artifact.mu.RLock()
	if p.artifact.closed {
		p.artifact.mu.RUnlock()
		return result, ErrArtifactClosed
	}
	if err := p.reserve(); err != nil {
		p.artifact.mu.RUnlock()
		return result, err
	}
	f, err := p.createTemporary()
	p.artifact.mu.RUnlock()
	if err != nil {
		return result, err
	}
	result.TemporaryPath = filepath.Join(filepath.Dir(p.observation.Choice.Path), p.directory, "new")
	copyErr := p.copy(ctx, f, &result)
	if copyErr == nil {
		info, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, p.temporaryInfo) || info.Size() < 0 || uint64(info.Size()) != p.size {
			copyErr = errors.Join(statErr, ErrArtifactEvidence)
		} else {
			p.temporaryInfo = info
		}
	}
	if copyErr == nil {
		syncErr := s.fileIO.syncFile(f)
		result.FileSynced = syncErr == nil
		copyErr = syncErr
	}
	closeErr := s.fileIO.closeFile(f)
	// An injected or native close failure must still revoke the writable handle.
	if closeErr != nil {
		_ = f.Close()
	}
	if err := errors.Join(copyErr, closeErr, ctx.Err()); err != nil {
		return result, err
	}
	verified, _, err := p.inspect(ctx, p.private, "new", p.temporaryInfo)
	if err != nil || verified.Size != p.size || verified.SHA256 != hex.EncodeToString(p.sum) {
		return result, errors.Join(err, ErrArtifactEvidence)
	}
	err = p.publish(ctx, &result)
	if result.PublicationAttempted {
		// Inspection reports an observation only, even when the syscall failed.
		err = errors.Join(err, p.observe(ctx, &result))
	}
	if result.Published {
		// The retained backup's directory also participates in replacement durability.
		dirErr := s.fileIO.syncFile(p.privateFile)
		parentErr := s.fileIO.syncFile(p.parentFile)
		result.DirectorySynced = dirErr == nil && parentErr == nil
		err = errors.Join(err, dirErr, parentErr)
	}
	return result, err
}

var _ contract.KVSnapshotFiles = (*KVSnapshotStore)(nil)
