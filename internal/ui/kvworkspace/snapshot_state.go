package kvworkspace

import (
	"context"
	"errors"
	"io/fs"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

// Native Save selects a path. Publication still requires confirmation of the
// actual immutable inspected existing file, including any appeared target.
type SnapshotFiles struct {
	Operations contract.KVSnapshotFiles
	SavePath   func(contract.KVSnapshotFileKind, string) (string, error)
}
type snapshotConfirmation struct {
	captures    contract.KVSnapshotArtifacts
	lease       contract.KVRequestLease
	preparation contract.KVSnapshotCapturePreparation
	authored    contract.KVRequest
	scope       contract.KVSnapshotScope
	request     contract.KVSnapshotRequest
	checked     bool
	open        bool
}
type snapshotFileConfirmation struct {
	operations  contract.KVSnapshotFiles
	preparation contract.KVSnapshotFilePreparation
	artifact    contract.KVSnapshotArtifact
	observation contract.KVSnapshotFileObservation
	checked     bool
	open        bool
}
type snapshotState struct {
	available             bool
	captureCanceled       bool
	cleanupError          string
	confirmation          *snapshotConfirmation
	artifact              contract.KVSnapshotArtifact
	original              *contract.KVSnapshotCapture
	authored              *contract.KVRequest
	recovered             []contract.KVSnapshotResult
	fileConfirmation      *snapshotFileConfirmation
	files                 []contract.KVSnapshotFileResult
	fileCancelRequested   []bool
	revision              uint64
	showJSON              bool
	showResults           bool
	recordView            int
	recordJSON            string
	recordError           string
	recordRevision        uint64
	viewWidth, viewHeight float32
	fileRow               int
}
type snapshotOutcome struct {
	documentID uint64
	scope      contract.KVSnapshotScope
	artifact   contract.KVSnapshotArtifact
	capture    contract.KVSnapshotCapture
}
type snapshotFileOutcome struct {
	operations  contract.KVSnapshotFiles
	documentID  uint64
	artifact    contract.KVSnapshotArtifact
	original    contract.KVSnapshotCapture
	preparation contract.KVSnapshotFilePreparation
	publication *contract.KVSnapshotFileResult
}

func (d *Document) snapshotState() *snapshotState {
	if d.snapshot == nil {
		d.snapshot = &snapshotState{fileRow: -1}
	}
	return d.snapshot
}
func (d *Document) SnapshotAvailable() bool {
	_, ok := d.lease.(contract.KVSnapshots)
	if ok {
		d.snapshotState().available = true
	}
	return ok
}
func (d *Document) SnapshotReceipt() (contract.KVSnapshotCapture, bool) {
	if d.snapshot == nil || d.snapshot.original == nil {
		return contract.KVSnapshotCapture{}, false
	}
	return d.snapshot.original.Clone(), true
}
func (d *Document) SnapshotFileReceipts() []contract.KVSnapshotFileResult {
	if d.snapshot == nil {
		return nil
	}
	out := make([]contract.KVSnapshotFileResult, len(d.snapshot.files))
	for i, r := range d.snapshot.files {
		out[i] = r.Clone()
	}
	return out
}
func (d *Document) snapshotRevision() uint64 {
	if d.snapshot == nil {
		return 0
	}
	return d.snapshot.revision
}
func (d *Document) snapshotContent() bool {
	return d.snapshot != nil && (d.snapshot.original != nil || d.snapshot.confirmation != nil || d.snapshot.fileConfirmation != nil || len(d.snapshot.files) != 0 || len(d.snapshot.recovered) != 0 || d.snapshot.cleanupError != "")
}
func (s *State) SetSnapshotOperations(captures contract.KVSnapshotArtifacts, files SnapshotFiles) {
	s.deps.SnapshotArtifacts = captures
	s.deps.SnapshotFiles = files
}
func (s *State) PrepareSnapshot(d *Document) {
	if !s.check(d) || d.FileBusy() || !d.SnapshotAvailable() || s.deps.SnapshotArtifacts == nil {
		return
	}
	x := d.snapshotState()
	if x.original != nil || x.artifact != nil || x.fileConfirmation != nil {
		d.error = locales.Text("请先清除原 Snapshot 归档再开始新的捕获")
		return
	}
	if !snapshotRecordRoom(x, snapshotNativeRecordReserve) {
		d.error = locales.Text("完整 Snapshot 记录超过限额")
		return
	}
	request, err := snapshotRequestValues(d.Request())
	if err != nil {
		d.error = locales.Text("Snapshot 限额必须为完整正整数字符串")
		return
	}
	prep, err := s.deps.SnapshotArtifacts.Prepare(d.lease, request)
	if err != nil || prep == nil {
		d.error = locales.Text("无法准备原 Snapshot 捕获")
		if err != nil {
			d.error += "：" + err.Error()
		}
		return
	}
	scope := prep.Scope()
	if scope.DocumentID != d.id || !sameEventScope(scope.Original, d.scope) || prep.Snapshot() != request {
		d.error = locales.Text("原 Snapshot 文档范围已变化")
		return
	}
	x.confirmation = &snapshotConfirmation{captures: s.deps.SnapshotArtifacts, lease: d.lease, preparation: prep, authored: d.Request(), scope: scope.Clone(), request: request, open: true}
	x.revision++
	d.error = ""
	d.permit = nil
}
func (s *State) ConfirmSnapshot(d *Document) {
	if !s.check(d) || d.FileBusy() {
		return
	}
	x := d.snapshotState()
	p := x.confirmation
	if p == nil || !p.checked || p.lease != d.lease || p.authored != d.Request() || p.scope.DocumentID != d.id || !sameEventScope(p.scope.Original, d.scope) || p.preparation.Snapshot() != p.request || !reflect.DeepEqual(p.preparation.Scope(), p.scope) {
		return
	}
	captures := p.captures
	x.confirmation = nil
	x.revision++
	scope, id, request, prep := p.scope.Clone(), d.id, p.authored, p.preparation
	s.start(d, "snapshot-capture", request, func(ctx context.Context) outcome {
		a, r, e := captures.Capture(ctx, prep)
		return outcome{snapshots: &snapshotOutcome{documentID: id, scope: scope, artifact: a, capture: r.Clone()}, err: e}
	})
}
func (s *State) consumeSnapshot(d *Document, job *operation, r *snapshotOutcome) {
	if r == nil {
		return
	}
	request, err := snapshotRequestValues(job.authored)
	if err != nil || r.capture.Original.Request != request || r.documentID != d.id || r.scope.DocumentID != d.id || !sameEventScope(r.scope.Original, job.scope) || r.capture.Original.Scope.DocumentID != d.id || !reflect.DeepEqual(r.capture.Original.Scope, r.scope) || r.artifact != nil && !reflect.DeepEqual(r.artifact.Receipt(), r.capture) {
		if r.artifact != nil {
			_ = r.artifact.Close()
		}
		return
	}
	x := d.snapshotState()
	if x.artifact != nil {
		_ = x.artifact.Close()
	}
	x.captureCanceled = job.canceled
	x.artifact = r.artifact
	copy := r.capture.Clone()
	x.original = &copy
	authored := job.authored
	x.authored = &authored
	x.revision++
	d.selectedSnapshot = true
	d.selectedMembers, d.selectedAlarms, d.selectedBrowser = false, false, false
	d.selectedUnknown, d.selectedEvents, d.selectedRenewal = false, false, false
	x.showResults = true
	// Native/source failures stay separate from ordinary Last/unknown KV receipts.
}
func (s *State) SelectSnapshotFile(d *Document, kind contract.KVSnapshotFileKind) {
	if !s.active(d) || d.Busy() || d.FileBusy() {
		return
	}
	x := d.snapshotState()
	files := s.deps.SnapshotFiles
	if x.artifact == nil || x.original == nil || files.Operations == nil || files.SavePath == nil {
		return
	}
	if kind == contract.KVSnapshotBackendFile && !x.original.Complete {
		return
	}
	if kind == contract.KVSnapshotJournalFile && (!x.original.Stored || !x.original.FilesSynced || !x.original.Original.JournalComplete) {
		return
	}
	if len(x.files) >= snapshotFileRecordCount || !snapshotRecordRoom(x, snapshotFileRecordReserve) {
		d.error = locales.Text("完整 Snapshot 记录超过限额")
		return
	}
	if x.fileConfirmation != nil {
		_ = x.fileConfirmation.preparation.Close()
		x.fileConfirmation = nil
	}
	artifact, original, id := x.artifact, x.original.Clone(), d.id
	name := "etcd-backend.db"
	if kind == contract.KVSnapshotJournalFile {
		name = "etcd-native-journal.nyasnapshot"
	}
	s.fileJob(d, "snapshot-file-prepare", func(ctx context.Context) fileResult {
		path, e := files.SavePath(kind, name)
		if e != nil || path == "" {
			return fileResult{err: e}
		}
		if e = ctx.Err(); e != nil {
			return fileResult{err: e}
		}
		choice := contract.KVSnapshotFileChoice{Path: path, Kind: kind}
		plan, e := files.Operations.PrepareFile(ctx, artifact, choice)
		if errors.Is(e, fs.ErrExist) {
			// Read-only inspection may prepare an existing-file observation. A separate
			// explicit UI confirmation grants its eventual replacement publication.
			choice.Replace = true
			plan, e = files.Operations.PrepareFile(ctx, artifact, choice)
		}
		if e != nil || plan == nil {
			if plan != nil {
				_ = plan.Close()
			}
			if e == nil {
				e = contract.ErrRevoked
			}
			return fileResult{err: e}
		}
		if e = ctx.Err(); e != nil {
			_ = plan.Close()
			return fileResult{err: e}
		}
		return fileResult{path: path, snapshots: &snapshotFileOutcome{operations: files.Operations, documentID: id, artifact: artifact, original: original, preparation: plan}}
	})
}
func (s *State) ConfirmSnapshotFile(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() {
		return
	}
	x := d.snapshotState()
	p := x.fileConfirmation
	if p == nil || !p.checked || p.artifact != x.artifact || x.original == nil || p.operations == nil || !reflect.DeepEqual(p.observation.Source, *x.original) || !reflect.DeepEqual(p.preparation.Observation(), p.observation) || p.observation.Source.Original.Scope.DocumentID != d.id {
		return
	}
	if len(x.files) >= snapshotFileRecordCount || !snapshotRecordRoom(x, snapshotFileRecordReserve) {
		d.error = locales.Text("完整 Snapshot 记录超过限额")
		return
	}
	id, artifact, original, plan := d.id, p.artifact, x.original.Clone(), p.preparation
	operations := p.operations
	x.fileConfirmation = nil
	x.revision++
	d.permit = nil
	s.fileJob(d, "snapshot-file-publish", func(ctx context.Context) fileResult {
		r, e := operations.PublishFile(ctx, plan)
		closeErr := plan.Close()
		r.CleanupError = joinSnapshotError(r.CleanupError, closeErr)
		r.Error = joinSnapshotError(r.Error, closeErr)
		copy := r.Clone()
		return fileResult{snapshots: &snapshotFileOutcome{documentID: id, artifact: artifact, original: original, publication: &copy}, err: errors.Join(e, closeErr)}
	})
}
func joinSnapshotError(before string, err error) string {
	if err == nil {
		return before
	}
	if before == "" {
		return err.Error()
	}
	return before + "\n" + err.Error()
}
func (s *State) consumeSnapshotFile(d *Document, r *snapshotFileOutcome, cancelRequested bool) {
	if r == nil {
		return
	}
	x := d.snapshotState()
	if r.documentID != d.id || r.original.Original.Scope.DocumentID != d.id || r.artifact != x.artifact || x.original == nil || !reflect.DeepEqual(r.original, *x.original) {
		if r.preparation != nil {
			_ = r.preparation.Close()
		}
		return
	}
	if r.preparation != nil {
		observation := r.preparation.Observation()
		if !reflect.DeepEqual(observation.Source, r.original) {
			_ = r.preparation.Close()
			return
		}
		x.fileConfirmation = &snapshotFileConfirmation{operations: r.operations, preparation: r.preparation, artifact: r.artifact, observation: observation.Clone(), open: true}
	}
	if r.publication != nil {
		if !reflect.DeepEqual(r.publication.Original.Source, r.original) {
			return
		}
		copy := r.publication.Clone()
		// The source is immutable document-owned metadata; inspections deep-clone it.
		copy.Original.Source = *x.original
		x.files = append(x.files, copy)
		x.fileCancelRequested = append(x.fileCancelRequested, cancelRequested)
		x.fileRow = len(x.files) - 1
	}
	x.revision++
	d.selectedSnapshot = true
	d.selectedMembers, d.selectedAlarms, d.selectedBrowser = false, false, false
	d.selectedUnknown, d.selectedEvents, d.selectedRenewal = false, false, false
	x.showResults = true
	d.permit = nil
}
func (s *State) CancelSnapshotConfirmation(d *Document) {
	if !s.owned(d) || d.snapshot == nil {
		return
	}
	x := d.snapshot
	if x.confirmation == nil && x.fileConfirmation == nil {
		return
	}
	x.confirmation = nil
	if x.fileConfirmation != nil {
		if err := x.fileConfirmation.preparation.Close(); err != nil {
			x.cleanupError = joinSnapshotError(x.cleanupError, err)
			d.error = err.Error()
		}
		x.fileConfirmation = nil
	}
	x.revision++
}

func (s *State) cancelSnapshotCaptureConfirmation(d *Document) {
	if s.owned(d) && d.snapshot != nil && d.snapshot.confirmation != nil {
		d.snapshot.confirmation = nil
		d.snapshot.revision++
	}
}
func (s *State) ClearSnapshot(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() {
		return
	}
	x := d.snapshotState()
	var cleanup error
	x.confirmation = nil
	if x.fileConfirmation != nil {
		cleanup = x.fileConfirmation.preparation.Close()
	}
	x.fileConfirmation = nil
	if x.artifact != nil {
		a := x.artifact
		x.artifact = nil
		cleanup = errors.Join(cleanup, a.Close())
	}
	if cleanup != nil {
		d.error = cleanup.Error()
		x.cleanupError = joinSnapshotError(x.cleanupError, cleanup)
		x.revision++
		d.permit = nil
		return
	}
	available, revision := x.available, x.revision
	d.snapshot = &snapshotState{available: available, revision: revision + 1, fileRow: -1}
	d.permit = nil
}

// The owning State.Close must cancel first and join original snapshot file work
// before releasing its artifact; unconsumed outcomes are also owned resources.
func (s *State) closeSnapshot(d *Document) {
	if d.io != nil && (d.io.kind == "snapshot-file-prepare" || d.io.kind == "snapshot-file-publish" || d.io.kind == "snapshot-record-export") {
		<-d.io.finished
		select {
		case r := <-d.io.out:
			if r.snapshots != nil && r.snapshots.preparation != nil {
				_ = r.snapshots.preparation.Close()
			}
		default:
		}
	}
	if d.snapshot != nil {
		if d.snapshot.fileConfirmation != nil {
			_ = d.snapshot.fileConfirmation.preparation.Close()
		}
		if d.snapshot.artifact != nil {
			_ = d.snapshot.artifact.Close()
		}
	}
	d.snapshot = nil
}
