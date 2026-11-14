package kvworkspace

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
	"io/fs"
	"reflect"
)

type httpArchiveState struct {
	exportConsent               bool
	restoreSelection            string
	restoreInput, restoreHash   []byte
	restorePath                 string
	restorePlan                 contract.KVHTTPArchiveRestorePreparation
	restoreLease                contract.KVRequestLease
	restoreLast, restoreUnknown *contract.KVHTTPArchiveRestoreResult
	open, checked, revealed     bool
	compressed, expanded        string
	capturePlan                 contract.KVHTTPArchiveStoragePreparation
	captureLease                contract.KVRequestLease
	source                      contract.KVHTTPArchive
	stored                      contract.KVHTTPStoredArchive
	original                    *contract.KVHTTPArchiveCapture
	filePlan                    contract.KVSnapshotFilePreparation
	fileObservation             contract.KVSnapshotFileObservation
	files                       []contract.KVSnapshotFileResult
	revision                    uint64
}
type httpArchiveOutcome struct {
	stored  contract.KVHTTPStoredArchive
	source  contract.KVHTTPArchive
	capture contract.KVHTTPArchiveCapture
}

func (o *httpArchiveOutcome) close() {
	if o == nil {
		return
	}
	if o.stored != nil {
		_ = o.stored.Close()
	}
	if o.source != nil {
		_ = o.source.Close()
	}
}

type httpArchiveFileOutcome struct {
	stored contract.KVHTTPStoredArchive
	plan   contract.KVSnapshotFilePreparation
	result *contract.KVSnapshotFileResult
}

func (d *Document) HTTPArchiveAvailable() bool {
	_, ok := d.lease.(contract.KVHTTPArchiveCapturing)
	return !d.closed && ok
}
func (s *State) OpenHTTPArchive(d *Document) {
	if !s.active(d) || !d.HTTPArchiveAvailable() {
		return
	}
	if d.httpArchive == nil {
		d.httpArchive = &httpArchiveState{compressed: "67108864", expanded: "268435456"}
	}
	d.httpArchive.open = true
}
func (s *State) PrepareHTTPArchive(d *Document) {
	if !s.check(d) || d.FileBusy() || d.httpArchive == nil || s.deps.HTTPArchiveArtifacts == nil {
		return
	}
	x := d.httpArchive
	if x.original != nil {
		return
	}
	r, err := httpArchiveLimitsInput(x.compressed, x.expanded)
	if err != nil {
		d.error = locales.Text("请检查 Snapshot 限额")
		return
	}
	p, err := s.deps.HTTPArchiveArtifacts.PrepareHTTPArchiveStorage(d.lease, r)
	if err != nil || p == nil || p.Scope().DocumentID != d.id || !sameEventScope(p.Scope().Original, d.scope) {
		d.error = locales.Text("原 Snapshot 捕获准备失败")
		return
	}
	x.capturePlan = p
	x.captureLease = d.lease
	x.checked = false
}
func (s *State) ConfirmHTTPArchive(d *Document) {
	if !s.check(d) || d.FileBusy() || d.httpArchive == nil {
		return
	}
	x := d.httpArchive
	p := x.capturePlan
	r, err := httpArchiveLimitsInput(x.compressed, x.expanded)
	if !x.checked || p == nil || err != nil || p.Snapshot() != r || x.captureLease != d.lease || !sameEventScope(p.Scope().Original, d.scope) {
		return
	}
	x.capturePlan = nil
	x.checked = false
	artifacts := s.deps.HTTPArchiveArtifacts
	s.start(d, "http-archive", contract.KVRequest{}, func(ctx context.Context) outcome {
		stored, source, capture, err := artifacts.CaptureHTTPArchiveStorage(ctx, p)
		return outcome{httpArchive: &httpArchiveOutcome{stored, source, capture.Clone()}, err: err}
	})
}
func (s *State) consumeHTTPArchive(d *Document, job *operation, o *httpArchiveOutcome) {
	if o == nil {
		return
	}
	r := o.capture
	if r.Original.Scope.DocumentID != d.id || !sameEventScope(r.Original.Scope.Original, job.scope) {
		o.close()
		d.error = contract.ErrRevoked.Error()
		return
	}
	if d.httpArchive == nil {
		d.httpArchive = &httpArchiveState{}
	}
	x := d.httpArchive
	if x.original != nil {
		o.close()
		return
	}
	x.source, x.stored = o.source, o.stored
	v := r.Clone()
	x.original = &v
	x.revision++
	x.revealed = false
	d.permit = nil
}
func (s *State) SelectHTTPArchiveFile(d *Document, kind contract.KVSnapshotFileKind) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpArchive == nil {
		return
	}
	x := d.httpArchive
	artifacts := s.deps.HTTPArchiveArtifacts
	files := s.deps.SnapshotFiles
	if x.stored == nil || x.original == nil || artifacts == nil || files.SavePath == nil || len(x.files) >= 32 || !x.checked {
		return
	}
	x.checked = false
	stored := x.stored
	name := "consul-snapshot.snap"
	if kind == contract.KVSnapshotJournalFile {
		name = "consul-snapshot-private-receipt.json"
	}
	s.fileJob(d, "http-archive-file-prepare", func(ctx context.Context) fileResult {
		path, err := files.SavePath(kind, name)
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		choice := contract.KVSnapshotFileChoice{Path: path, Kind: kind}
		p, err := artifacts.PrepareHTTPArchiveFile(ctx, stored, choice)
		if errors.Is(err, fs.ErrExist) {
			choice.Replace = true
			p, err = artifacts.PrepareHTTPArchiveFile(ctx, stored, choice)
		}
		if err != nil || ctx.Err() != nil {
			if p != nil {
				_ = p.Close()
			}
			return fileResult{err: errors.Join(err, ctx.Err())}
		}
		return fileResult{httpArchive: &httpArchiveFileOutcome{stored: stored, plan: p}}
	})
}
func (s *State) ConfirmHTTPArchiveFile(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpArchive == nil {
		return
	}
	x := d.httpArchive
	p := x.filePlan
	if !x.checked || p == nil || x.stored == nil || x.original == nil || !reflect.DeepEqual(p.Observation(), x.fileObservation) || !reflect.DeepEqual(p.Observation().HTTPSource, x.original) {
		return
	}
	x.filePlan = nil
	x.checked = false
	stored, artifacts := x.stored, s.deps.HTTPArchiveArtifacts
	s.fileJob(d, "http-archive-file-publish", func(ctx context.Context) fileResult {
		r, err := artifacts.PublishFile(ctx, p)
		err = errors.Join(err, p.Close())
		v := r.Clone()
		return fileResult{httpArchive: &httpArchiveFileOutcome{stored: stored, result: &v}, err: err}
	})
}
func (s *State) consumeHTTPArchiveFile(d *Document, job *fileOperation, r fileResult) bool {
	if job.kind != "http-archive-file-prepare" && job.kind != "http-archive-file-publish" {
		return false
	}
	o := r.httpArchive
	if o == nil {
		if r.err != nil {
			d.error = locales.Text("Snapshot 私有文件操作失败")
		}
		return true
	}
	x := d.httpArchive
	if x == nil || x.stored != o.stored {
		if o.plan != nil {
			_ = o.plan.Close()
		}
		return true
	}
	if o.plan != nil {
		if job.canceled || !reflect.DeepEqual(o.plan.Observation().HTTPSource, x.original) {
			_ = o.plan.Close()
			return true
		}
		if x.filePlan != nil {
			_ = x.filePlan.Close()
		}
		x.filePlan = o.plan
		x.fileObservation = o.plan.Observation().Clone()
		x.checked = false
	}
	if o.result != nil {
		if !reflect.DeepEqual(o.result.Original.HTTPSource, x.original) {
			d.error = contract.ErrRevoked.Error()
			return true
		}
		x.files = append(x.files, o.result.Clone())
	}
	if r.err != nil {
		d.error = locales.Text("Snapshot 文件发布未完成；原回执保留")
	}
	x.revision++
	d.permit = nil
	return true
}
func (s *State) closeHTTPArchive(d *Document) {
	if d.io != nil && (d.io.kind == "http-archive-file-prepare" || d.io.kind == "http-archive-file-publish") {
		<-d.io.finished
		select {
		case r := <-d.io.out:
			if r.httpArchive != nil && r.httpArchive.plan != nil {
				_ = r.httpArchive.plan.Close()
			}
		default:
		}
	}
	if x := d.httpArchive; x != nil {
		if x.restorePlan != nil {
			_ = x.restorePlan.Close()
		}
		clear(x.restoreInput)
		x.restoreInput = nil
		x.restoreLast, x.restoreUnknown = nil, nil
		if x.filePlan != nil {
			_ = x.filePlan.Close()
		}
		if x.stored != nil {
			_ = x.stored.Close()
		}
		if x.source != nil {
			_ = x.source.Close()
		}
	}
	d.httpArchive = nil
}
func (s *State) ClearHTTPArchive(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() {
		return
	}
	if d.httpArchive != nil && d.httpArchive.restoreUnknown != nil {
		return
	}
	s.closeHTTPArchive(d)
	d.permit = nil
	s.OpenHTTPArchive(d)
}
