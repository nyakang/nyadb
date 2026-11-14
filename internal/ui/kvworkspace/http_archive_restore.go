package kvworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
	"io"
	"reflect"
)

func (s *State) OpenHTTPArchiveRestoreFile(d *Document) {
	if !s.check(d) || d.FileBusy() || d.httpArchive == nil {
		return
	}
	files := s.deps.Files
	if files.OpenPath == nil || files.OpenReader == nil {
		return
	}
	s.fileJob(d, "http-archive-restore-open", func(ctx context.Context) fileResult {
		path, err := files.OpenPath()
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		if err = ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		reader, err := files.OpenReader(path)
		if err != nil {
			return fileResult{err: err}
		}
		raw, err := io.ReadAll(io.LimitReader(reader, (64<<20)+1))
		closeErr := reader.Close()
		if err != nil || closeErr != nil || len(raw) > 64<<20 || len(raw) == 0 || ctx.Err() != nil {
			clear(raw)
			return fileResult{err: contract.ErrUnsupported}
		}
		return fileResult{restoreArchive: raw, path: path}
	})
}
func (s *State) PrepareHTTPArchiveRestore(d *Document) {
	if !s.check(d) || d.FileBusy() || d.httpArchive == nil || d.hasUnknownWrites() {
		return
	}
	x := d.httpArchive
	facet, ok := d.lease.(contract.KVHTTPArchiveRestoring)
	if !ok || len(x.restoreInput) == 0 {
		return
	}
	if x.restorePlan != nil {
		_ = x.restorePlan.Close()
		x.restorePlan = nil
	}
	limits, err := httpArchiveLimitsInput(x.compressed, x.expanded)
	if err != nil {
		return
	}
	input := contract.KVHTTPArchiveRestoreRequest{Archive: bytes.Clone(x.restoreInput), ExpandedBytes: limits.ExpandedBytes}
	defer clear(input.Archive)
	plan, err := facet.PrepareHTTPArchiveRestore(input)
	if err != nil || plan == nil || plan.Scope().DocumentID != d.id || !sameEventScope(plan.Scope().Original, d.scope) {
		if plan != nil {
			_ = plan.Close()
		}
		d.error = locales.Text("原 Snapshot 恢复准备失败")
		return
	}
	x.restorePlan = plan
	x.restoreLease = d.lease
	x.checked = false
}
func (s *State) ConfirmHTTPArchiveRestore(d *Document) {
	if !s.check(d) || d.FileBusy() || d.httpArchive == nil || d.hasUnknownWrites() {
		return
	}
	x := d.httpArchive
	p := x.restorePlan
	if !x.checked || p == nil || x.restoreLease != d.lease || !sameEventScope(p.Scope().Original, d.scope) || !bytes.Equal(p.Source().OriginalSHA256, x.restoreHash) {
		return
	}
	x.restorePlan = nil
	x.checked = false
	facet := d.lease.(contract.KVHTTPArchiveRestoring)
	expected, record := p.Request(), p.LocalRecordID()
	s.start(d, "http-archive-restore", contract.KVRequest{}, func(ctx context.Context) outcome {
		r, err := facet.ExecuteHTTPArchiveRestore(ctx, p)
		_ = p.Close()
		v := r.Clone()
		return outcome{httpRestore: &v, err: err}
	})
	d.job.restoreRequest = expected
	d.job.restoreRecord = record
}
func (s *State) consumeHTTPArchiveRestore(d *Document, job *operation, out outcome) {
	if d.httpArchive == nil {
		d.httpArchive = &httpArchiveState{}
	}
	x := d.httpArchive
	x.revealed = false
	if out.httpRestore == nil {
		return
	}
	r := out.httpRestore.Clone()
	if r.LocalRecordID != job.restoreRecord || r.Scope.DocumentID != d.id || !sameEventScope(r.Scope.Original, job.scope) || !reflect.DeepEqual(r.Request, job.restoreRequest) {
		d.error = contract.ErrRevoked.Error()
		return
	}
	if r.Attempted {
		v := r.Clone()
		x.restoreLast = &v
		if r.WriteOutcomeUnknown {
			x.restoreUnknown = &v
		}
		x.revision++
		d.permit = nil
	}
	if out.err != nil {
		d.error = locales.Text("Snapshot 恢复未完成；原私有回执保留")
	}
	if len(r.RevokedProfiles) > 0 {
		d.needsRecovery = true
	}
}
func (s *State) consumeHTTPRestoreOpenFile(d *Document, job *fileOperation, r fileResult) bool {
	if job.kind != "http-archive-restore-open" {
		return false
	}
	if len(r.restoreArchive) == 0 {
		return true
	}
	if job.canceled || r.err != nil || d.httpArchive == nil {
		clear(r.restoreArchive)
		return true
	}
	x := d.httpArchive
	if x.restorePlan != nil {
		_ = x.restorePlan.Close()
		x.restorePlan = nil
	}
	clear(x.restoreInput)
	x.restoreInput = r.restoreArchive
	sum := sha256.Sum256(x.restoreInput)
	x.restoreHash = bytes.Clone(sum[:])
	x.restorePath = r.path
	x.checked = false
	x.revision++
	d.permit = nil
	return true
}
func (d *Document) httpArchiveContent() bool {
	return d.httpArchive != nil && (d.httpArchive.original != nil || len(d.httpArchive.restoreInput) > 0 || d.httpArchive.restoreLast != nil || d.httpArchive.restoreUnknown != nil)
}
func (d *Document) httpArchiveRevision() uint64 {
	if d.httpArchive == nil {
		return 0
	}
	return d.httpArchive.revision
}
func httpRestoreRecoveryExpected(d *Document) (last, unknown *contract.KVHTTPArchiveRestoreResult) {
	if x := d.httpArchive; x != nil {
		if x.restoreLast != nil {
			v := x.restoreLast.Clone()
			last = &v
		}
		if x.restoreUnknown != nil {
			v := x.restoreUnknown.Clone()
			unknown = &v
		}
	}
	return
}
func (s *State) consumeHTTPRestoreRecovery(d *Document, job *operation, out *outcome) {
	if out.err != nil {
		return
	}
	for _, p := range []struct {
		expected, actual *contract.KVHTTPArchiveRestoreResult
	}{{job.restoreRecovery, out.recoveryRestore}, {job.restoreRecoveryUnknown, out.recoveryRestoreUnknown}} {
		if p.expected != nil && !reflect.DeepEqual(p.expected, p.actual) {
			if out.lease != nil {
				_ = out.lease.Close()
				out.lease = nil
			}
			out.err = contract.ErrRevoked
			return
		}
	}
	if out.recoveryRestore != nil || out.recoveryRestoreUnknown != nil {
		if d.httpArchive == nil {
			d.httpArchive = &httpArchiveState{}
		}
		x := d.httpArchive
		if x.restoreLast == nil && out.recoveryRestore != nil {
			v := out.recoveryRestore.Clone()
			x.restoreLast = &v
		}
		if x.restoreUnknown == nil && out.recoveryRestoreUnknown != nil {
			v := out.recoveryRestoreUnknown.Clone()
			x.restoreUnknown = &v
		}
		x.revealed = false
		x.checked = false
		if x.restorePlan != nil {
			_ = x.restorePlan.Close()
			x.restorePlan = nil
		}
	}
}
