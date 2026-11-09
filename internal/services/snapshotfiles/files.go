package snapshotfiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var ErrChanged = errors.New("归档文件或目标文件已变化，请重新选择并预览")
var ErrBounds = errors.New("归档文件超出大小上限或不是普通文件")

func Operations() contract.SnapshotFileOperations {
	return contract.SnapshotFileOperations{InspectInput: inspect, OpenInput: input, PrepareOutput: prepare, OpenOutput: openOutput}
}

type source struct {
	*os.File
	once     sync.Once
	closeErr error
}

func (s *source) Close() error { s.once.Do(func() { s.closeErr = s.File.Close() }); return s.closeErr }
func verified(ctx context.Context, path string, limit uint64) (*source, contract.SnapshotSource, os.FileInfo, error) {
	if limit == 0 || limit > math.MaxInt64 {
		return nil, contract.SnapshotSource{}, nil, ErrBounds
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, contract.SnapshotSource{}, nil, err
	}
	s := &source{File: f}
	ok := false
	defer func() {
		if !ok {
			_ = s.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer func() {
		if !stop() {
			_ = s.Close()
		}
	}()
	before, err := f.Stat()
	if err != nil {
		return nil, contract.SnapshotSource{}, nil, err
	}
	if !before.Mode().IsRegular() || before.Size() < 0 || uint64(before.Size()) > limit {
		return nil, contract.SnapshotSource{}, nil, ErrBounds
	}
	h := sha256.New()
	buffer := make([]byte, 128<<10)
	var size uint64
	for {
		if err = ctx.Err(); err != nil {
			return nil, contract.SnapshotSource{}, nil, err
		}
		n, e := f.Read(buffer)
		size += uint64(n)
		if size > limit {
			return nil, contract.SnapshotSource{}, nil, ErrBounds
		}
		_, _ = h.Write(buffer[:n])
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, contract.SnapshotSource{}, nil, e
		}
	}
	after, err := f.Stat()
	if err != nil {
		return nil, contract.SnapshotSource{}, nil, err
	}
	if size != uint64(before.Size()) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, contract.SnapshotSource{}, nil, ErrChanged
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, contract.SnapshotSource{}, nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, contract.SnapshotSource{}, nil, err
	}
	ok = true
	return s, contract.SnapshotSource{Name: filepath.Base(path), Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}, before, nil
}
func inspect(ctx context.Context, path string, limit uint64) (contract.SnapshotSource, error) {
	s, m, _, err := verified(ctx, path, limit)
	if err != nil {
		return m, err
	}
	return m, s.Close()
}
func input(ctx context.Context, path string, expected contract.SnapshotSource, limit uint64) (io.ReadCloser, error) {
	s, m, _, err := verified(ctx, path, limit)
	if err != nil {
		return nil, err
	}
	if m != expected {
		_ = s.Close()
		return nil, ErrChanged
	}
	return s, nil
}

type outputPlan struct {
	path     string
	limit    uint64
	info     os.FileInfo
	checksum string
}

func (p *outputPlan) Path() string { return p.path }
func fingerprint(ctx context.Context, path string, limit uint64) (os.FileInfo, string, error) {
	i, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if !i.Mode().IsRegular() {
		return nil, "", ErrBounds
	}
	s, m, info, err := verified(ctx, path, limit)
	if err != nil {
		return nil, "", err
	}
	if err = s.Close(); err != nil {
		return nil, "", err
	}
	if !os.SameFile(i, info) {
		return nil, "", ErrChanged
	}
	return info, m.SHA256, nil
}
func prepare(ctx context.Context, path string, limit uint64) (contract.SnapshotOutputPlan, error) {
	if path == "" || limit == 0 || limit > math.MaxInt64 {
		return nil, ErrBounds
	}
	i, h, err := fingerprint(ctx, path, limit)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return &outputPlan{path: path, limit: limit, info: i, checksum: h}, nil
}
func (p *outputPlan) check(ctx context.Context) error {
	i, h, err := fingerprint(ctx, p.path, p.limit)
	if err != nil {
		return err
	}
	if (i == nil) != (p.info == nil) || i != nil && (!os.SameFile(i, p.info) || h != p.checksum || i.Size() != p.info.Size() || !i.ModTime().Equal(p.info.ModTime())) {
		return ErrChanged
	}
	return ctx.Err()
}

type output struct {
	file     *os.File
	plan     *outputPlan
	temp     string
	written  atomic.Uint64
	once     sync.Once
	closeErr error
	finished bool
}

func openOutput(ctx context.Context, plan contract.SnapshotOutputPlan) (contract.SnapshotOutput, error) {
	p, ok := plan.(*outputPlan)
	if !ok || p == nil {
		return nil, ErrChanged
	}
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(p.path), ".nyadb-snapshot-*.partial")
	if err != nil {
		return nil, err
	}
	return &output{file: f, plan: p, temp: f.Name()}, nil
}
func (o *output) Write(p []byte) (int, error) {
	n, e := o.file.Write(p)
	o.written.Add(uint64(n))
	return n, e
}
func (o *output) Close() error {
	o.once.Do(func() { syncErr := o.file.Sync(); o.closeErr = errors.Join(syncErr, o.file.Close()) })
	return o.closeErr
}
func (o *output) Finish(ctx context.Context, complete bool) (contract.SnapshotFileReceipt, error) {
	r := contract.SnapshotFileReceipt{Destination: o.plan.path, Partial: o.temp, Bytes: o.written.Load()}
	if o.finished {
		return r, ErrChanged
	}
	o.finished = true
	err := o.Close()
	if complete && err == nil {
		err = o.plan.check(ctx)
		if err == nil {
			err = os.Rename(o.temp, o.plan.path)
			if err == nil {
				r.Published = true
				r.Partial = ""
				return r, nil
			}
		}
	}
	if r.Bytes == 0 {
		_ = os.Remove(o.temp)
		r.Partial = ""
	}
	return r, err
}
