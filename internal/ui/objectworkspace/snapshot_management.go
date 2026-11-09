package objectworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type SnapshotManagementForm struct {
	Scope                                                                                        contract.SnapshotScope
	Action, Collection, Shard, Source, Destination, Location, Checksum, Priority, Limit, Timeout string
}
type SnapshotFiles struct {
	Open       func() (string, error)
	Save       func(string) (string, error)
	Operations contract.SnapshotFileOperations
}

func (s *State) SetSnapshotFiles(files SnapshotFiles) { s.deps.SnapshotFiles = files }

type snapshotManagementDocument struct {
	lease                                                               contract.SnapshotLease
	generation, revision                                                uint64
	form, baseline, readForm, preparedForm                              SnapshotManagementForm
	observation                                                         contract.SnapshotObservation
	snapshot, readEvidence                                              contract.SnapshotMetadata
	plan                                                                contract.SnapshotPlan
	request                                                             contract.SnapshotRequest
	affected                                                            []contract.NamespaceRef
	last                                                                contract.SnapshotResult
	results                                                             <-chan snapshotManagementReply
	tab                                                                 int
	confirmation                                                        string
	pending, lost, abandoned, preview, guarding, guard, cancelRequested bool
	leaveAcknowledged, reviewed                                         bool
	source                                                              contract.SnapshotSource
	output                                                              contract.SnapshotOutputPlan
	file                                                                contract.SnapshotFileReceipt
	selected                                                            int
	done                                                                <-chan struct{}
	next                                                                func(ui.Services)
}
type snapshotManagementReply struct {
	request, generation uint64
	operation           string
	form                SnapshotManagementForm
	observation         contract.SnapshotObservation
	snapshot            contract.SnapshotMetadata
	result              contract.SnapshotResult
	affected            []contract.NamespaceRef
	source              contract.SnapshotSource
	output              contract.SnapshotOutputPlan
	plan                contract.SnapshotPlan
	file                contract.SnapshotFileReceipt
	err                 error
}
type SnapshotManagementEvent struct {
	Object     contract.ObjectRef
	Generation uint64
	Affected   []contract.NamespaceRef
	Result     contract.SnapshotResult
}

func snapshotManagementObject(o contract.ObjectRef) bool {
	return o.ProfileID > 0 && o.Catalog == "" && o.Schema == "" && o.Kind == "snapshot_management" && o.Name == "snapshot_management"
}
func (s *State) SnapshotManagementSupported(o contract.ObjectRef) bool {
	if !snapshotManagementObject(o) || s.deps.SnapshotManagementFor == nil {
		return false
	}
	p, err := s.deps.SnapshotManagementFor(o.ProfileID)
	return err == nil && p != nil && p.SupportsSnapshots(o, contract.SnapshotTarget{Scope: contract.SnapshotStorage}, "read")
}
func (s *State) OpenSnapshotManagement(id uint64, o contract.ObjectRef) *Document {
	if id == 0 || !s.SnapshotManagementSupported(o) {
		return nil
	}
	if old := s.documents[id]; old != nil {
		s.Close(old)
	}
	f := SnapshotManagementForm{Scope: contract.SnapshotCollection, Action: "create", Limit: "1024", Timeout: "300", Priority: "snapshot", Shard: "0"}
	d := &Document{id: id, profileID: o.ProfileID, object: o, table: o.Name, objectKind: "快照管理", snapshots: &snapshotManagementDocument{form: f, baseline: f}}
	s.documents[id] = d
	return d
}
func (d *Document) SnapshotManagementMode() bool {
	return d != nil && !d.closed && d.snapshots != nil
}
func (x *snapshotManagementDocument) dirty() bool {
	return x != nil && !x.abandoned && x.form != x.baseline
}
func (s *State) snapshotsReady(d *Document) bool {
	if !d.SnapshotManagementMode() || d.loading || d.snapshots.lost || d.snapshots.abandoned || s.session().ProfileID != d.profileID {
		return false
	}
	if s.deps.DocumentActive != nil && !s.deps.DocumentActive(d.id) {
		return false
	}
	w := s.sessionFor(d.profileID)
	return w.Connected && w.ProfileID == d.profileID && (d.snapshots.lease == nil || w.Generation == d.snapshots.generation)
}
func (s *State) reserveSnapshots(d *Document) error {
	p, err := s.deps.SnapshotManagementFor(d.profileID)
	if err != nil {
		return err
	}
	l, err := p.ReserveSnapshotManagementDocument(d.object, d.id)
	if err != nil {
		return err
	}
	if l.Object() != d.object || l.Generation() != s.sessionFor(d.profileID).Generation {
		l.Close()
		return contract.ErrRevoked
	}
	d.snapshots.lease, d.snapshots.generation = l, l.Generation()
	return nil
}
func snapshotFormTarget(profile int64, f SnapshotManagementForm) (contract.SnapshotTarget, error) {
	t := contract.SnapshotTarget{Scope: f.Scope}
	switch f.Scope {
	case contract.SnapshotStorage:
		return t, nil
	case contract.SnapshotCollection, contract.SnapshotShard:
		if f.Collection == "" || len(f.Collection) > 255 {
			return t, errors.New("请输入原物理集合名称")
		}
		t.Collection = collectionNamespace(profile, f.Collection)
		if f.Scope == contract.SnapshotShard {
			n, e := strconv.ParseUint(f.Shard, 10, 32)
			if e != nil {
				return t, errors.New("请输入有效分片编号")
			}
			t.Shard = uint32(n)
		}
	default:
		return t, contract.ErrUnsupported
	}
	return t, nil
}
func snapshotFormBounds(f SnapshotManagementForm) (uint64, time.Duration, error) {
	n, e := strconv.ParseUint(f.Limit, 10, 64)
	if e != nil || n == 0 || n > math.MaxInt64/(1<<20) {
		return 0, 0, errors.New("归档大小上限必须是有效的 MiB 数值")
	}
	seconds, e := strconv.Atoi(f.Timeout)
	if e != nil || seconds < 1 || seconds > 3600 {
		return 0, 0, errors.New("快照等待超时必须为 1–3600 秒")
	}
	return n * (1 << 20), time.Duration(seconds) * time.Second, nil
}
func snapshotFormScopeEqual(a, b SnapshotManagementForm) bool {
	return a.Scope == b.Scope && a.Collection == b.Collection && (a.Scope != contract.SnapshotShard || a.Shard == b.Shard)
}
func (s *State) SetSnapshotManagementForm(d *Document, f SnapshotManagementForm) bool {
	if !s.snapshotsReady(d) || d.snapshots.pending || d.snapshots.guarding {
		return false
	}
	x := d.snapshots
	if f != x.form {
		x.form = f
		x.plan = nil
		x.preview = false
		x.confirmation = ""
		x.revision++
		x.leaveAcknowledged = false
	}
	return true
}
func (s *State) SnapshotManagementForm(d *Document) SnapshotManagementForm {
	if !d.SnapshotManagementMode() {
		return SnapshotManagementForm{}
	}
	return d.snapshots.form
}
func (s *State) ReadSnapshotManagement(d *Document, invalidate func()) {
	if !s.snapshotsReady(d) || d.snapshots.pending || d.snapshots.guarding {
		return
	}
	x := d.snapshots
	target, err := snapshotFormTarget(d.profileID, x.form)
	if err != nil {
		d.err = err.Error()
		return
	}
	if x.lease == nil {
		if err = s.reserveSnapshots(d); err != nil {
			d.err = err.Error()
			return
		}
	}
	form := x.form
	x.observation, x.plan, x.preview = nil, nil, false
	x.revision++
	s.runSnapshots(d, invalidate, "read", func(ctx context.Context, l contract.SnapshotLease) snapshotManagementReply {
		o, e := l.Read(ctx, target)
		var metadata contract.SnapshotMetadata
		if o != nil {
			metadata = contract.CloneSnapshotMetadata(o.Snapshot())
		}
		return snapshotManagementReply{form: form, observation: o, snapshot: metadata, err: e}
	})
}

func (s *State) PrepareSnapshotManagement(d *Document, invalidate func()) error {
	if !s.snapshotsReady(d) || d.snapshots.pending || d.snapshots.guarding {
		return contract.ErrUnsupported
	}
	x := d.snapshots
	if x.lease == nil || x.observation == nil || !snapshotFormScopeEqual(x.readForm, x.form) {
		d.err = "快照范围已变化，请重新读取原信息"
		return contract.ErrNamespaceChanged
	}
	limit, _, err := snapshotFormBounds(x.form)
	if err != nil {
		d.err = err.Error()
		return err
	}
	target, err := snapshotFormTarget(d.profileID, x.form)
	if err != nil {
		d.err = err.Error()
		return err
	}
	provider, err := s.deps.SnapshotManagementFor(d.profileID)
	if err != nil || provider == nil || !provider.SupportsSnapshots(d.object, target, x.form.Action) {
		d.err = "当前快照范围不支持此操作"
		return contract.ErrUnsupported
	}
	draft := contract.SnapshotDraft{Action: x.form.Action, Wait: x.form.Action != "download" && x.form.Action != "stream"}
	if draft.Action == "delete" || draft.Action == "download" {
		if x.selected < 0 || x.selected >= len(x.snapshot.Snapshots) {
			d.err = "请选择原归档"
			return contract.ErrUnsupported
		}
		draft.Selected = contract.CloneSnapshotDescription(x.snapshot.Snapshots[x.selected])
		if draft.Action == "download" && draft.Selected.Size > limit {
			d.err = "归档超出大小上限"
			return contract.ErrUnsupported
		}
	}
	if draft.Action == "recover" {
		draft.Recovery = contract.SnapshotRecovery{Location: x.form.Location, Checksum: x.form.Checksum, Priority: x.form.Priority}
	}
	form, before, files := x.form, x.observation, s.deps.SnapshotFiles.Operations
	s.runSnapshots(d, invalidate, "prepare", func(ctx context.Context, l contract.SnapshotLease) snapshotManagementReply {
		r := snapshotManagementReply{form: form}
		switch draft.Action {
		case "upload":
			if files.InspectInput == nil {
				r.err = contract.ErrUnsupported
				return r
			}
			r.source, r.err = files.InspectInput(ctx, form.Source, limit)
			draft.Source = r.source
			draft.Recovery = contract.SnapshotRecovery{Priority: form.Priority}
		case "download", "stream":
			if files.PrepareOutput == nil {
				r.err = contract.ErrUnsupported
				return r
			}
			r.output, r.err = files.PrepareOutput(ctx, form.Destination, limit)
		}
		if r.err == nil {
			r.err = ctx.Err()
		}
		if r.err == nil {
			r.plan, r.err = l.Prepare(before, contract.CloneSnapshotDraft(draft))
		}
		return r
	})
	return nil
}

func snapshotConfirmation(f SnapshotManagementForm, selected contract.SnapshotDescription) string {
	if f.Action == "delete" {
		return selected.Name
	}
	if f.Action == "recover" || f.Action == "upload" {
		return f.Collection
	}
	return ""
}

func (s *State) CanSubmitSnapshotManagement(d *Document) bool {
	if !s.snapshotsReady(d) {
		return false
	}
	x := d.snapshots
	_, truncated := taskPreview(snapshotRequestText(x.request))
	return !x.pending && !x.guarding && x.preview && x.plan != nil && x.form == x.preparedForm && (!truncated || x.reviewed) && (snapshotConfirmation(x.form, s.snapshotSelected(d)) == "" || x.confirmation == snapshotConfirmation(x.form, s.snapshotSelected(d)))
}
func (s *State) SubmitSnapshotManagement(d *Document, services ui.Services) {
	if !s.CanSubmitSnapshotManagement(d) {
		return
	}
	x := d.snapshots
	plan, revision, form, generation := x.plan, x.revision, x.form, x.generation
	object := d.object
	output, source, files := x.output, x.source, s.deps.SnapshotFiles.Operations
	limit, timeout, _ := snapshotFormBounds(form)
	affected := []contract.NamespaceRef{}
	if plan.RestoresData() {
		affected = append(affected, plan.Target().Collection)
	}
	x.affected = slices.Clone(affected)
	x.guarding = true
	next := func(sv ui.Services) {
		if !d.SnapshotManagementMode() || !x.guarding || revision != x.revision {
			return
		}
		x.guarding = false
		if !s.snapshotsReady(d) || form != x.form || generation != s.sessionFor(d.profileID).Generation {
			d.err = "原快照会话或表单已变化，不能提交"
			return
		}
		x.preview, x.plan, x.observation = false, nil, nil
		writing := plan.Action() != "download" && plan.Action() != "stream"
		x.pending = writing
		s.runSnapshots(d, sv.Invalidate, "submit", func(ctx context.Context, l contract.SnapshotLease) snapshotManagementReply {
			r := snapshotManagementReply{affected: slices.Clone(affected)}
			r.result = contract.SnapshotResult{Object: object, Action: plan.Action(), Target: plan.Target(), State: "not_submitted"}
			options := contract.SnapshotTransferOptions{MaxBytes: limit, Timeout: timeout}
			switch plan.Action() {
			case "upload":
				if files.OpenInput == nil {
					r.err = contract.ErrUnsupported
					break
				}
				input, e := files.OpenInput(ctx, form.Source, source, limit)
				if e != nil {
					r.err = e
					break
				}
				r.result, r.err = l.Upload(ctx, plan, input, options)
			case "download", "stream":
				if files.OpenOutput == nil {
					r.err = contract.ErrUnsupported
					break
				}
				sink, e := files.OpenOutput(ctx, output)
				if e != nil {
					r.err = e
					break
				}
				r.result, r.err = l.Download(ctx, plan, sink, options)
				r.file, e = sink.Finish(ctx, r.err == nil && r.result.Transfer.Complete)
				r.err = errors.Join(r.err, e)
				if !r.file.Published && r.result.State == "transferred" {
					r.result.State = "transfer_incomplete"
					r.result.Transfer.Complete = false
				}
			default:
				r.result, r.err = l.Submit(ctx, plan)
			}
			r.result = contract.CloneSnapshotResult(r.result)
			return r
		})
	}
	if s.deps.BeforeSnapshots != nil && s.deps.BeforeSnapshots(services, d.id, d.profileID, generation, plan, next) {
		return
	}
	next(services)
}

func (s *State) VerifySnapshotManagement(d *Document, invalidate func()) {
	if !s.snapshotsReady(d) || !d.snapshots.pending || d.snapshots.lease == nil {
		return
	}
	affected := slices.Clone(d.snapshots.affected)
	s.runSnapshots(d, invalidate, "verify", func(ctx context.Context, l contract.SnapshotLease) snapshotManagementReply {
		r, err := l.Verify(ctx)
		return snapshotManagementReply{result: contract.CloneSnapshotResult(r), affected: affected, err: err}
	})
}
func (s *State) runSnapshots(d *Document, invalidate func(), operation string, run func(context.Context, contract.SnapshotLease) snapshotManagementReply) {
	x := d.snapshots
	l, generation := x.lease, x.generation
	d.request++
	request := d.request
	_, timeout, _ := snapshotFormBounds(x.form)
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	d.cancel, d.loading, d.err = cancel, true, ""
	x.cancelRequested, x.leaveAcknowledged = false, false
	out := make(chan snapshotManagementReply, 1)
	x.results = out
	done := make(chan struct{})
	x.done = done
	go func() {
		r := run(ctx, l)
		r.request, r.generation, r.operation = request, generation, operation
		out <- r
		close(done)
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) CancelSnapshotManagement(d *Document) {
	if !d.SnapshotManagementMode() || !d.loading || d.snapshots.cancelRequested {
		return
	}
	d.snapshots.cancelRequested = true
	if d.cancel != nil {
		d.cancel()
	}
}
func (s *State) consumeSnapshots(services ui.Services) {
	active := s.session()
	for _, d := range s.documents {
		if !d.SnapshotManagementMode() {
			continue
		}
		x := d.snapshots
		orphaned := active.ProfileID == 0 && s.deps.DocumentActive != nil && s.deps.DocumentActive(d.id)
		if active.ProfileID != d.profileID && !orphaned {
			continue
		}
		current := s.sessionFor(d.profileID)
		if x.lease != nil && (!current.Connected || current.Generation != x.generation) {
			x.lost = true
			if !d.loading {
				d.err = "原快照会话已失效，原草稿与证据保留"
			}
		}
		if x.results == nil {
			continue
		}
		select {
		case r := <-x.results:
			if r.request != d.request {
				continue
			}
			x.results, d.loading, x.cancelRequested = nil, false, false
			if d.cancel != nil {
				d.cancel()
				d.cancel = nil
			}
			if r.operation == "submit" || r.operation == "verify" {
				if r.operation == "verify" && r.result.Action == "" {
					previous := contract.CloneSnapshotResult(x.last)
					previous.State = "indeterminate"
					r.result = previous
				}
				x.last = contract.CloneSnapshotResult(r.result)
				if r.operation == "verify" && r.err == nil && !x.lost {
					x.snapshot = contract.CloneSnapshotMetadata(r.result.Metadata)
				}
				x.tab = 2
				x.pending = r.result.Action != "download" && r.result.Action != "stream" && r.result.State != "not_submitted" && r.result.State != "applied" && r.result.State != "unchanged" && r.result.State != "observed"
				if r.operation == "submit" {
					x.file = r.file
				}
			}
			if r.operation == "read" {
				x.readEvidence = contract.CloneSnapshotMetadata(r.snapshot)
			}
			if !current.Connected || r.generation != current.Generation {
				x.lost = true
				d.err = "原快照会话已失效，原草稿与证据保留"
				continue
			}
			if errors.Is(r.err, contract.ErrRevoked) {
				x.lost = true
			}
			if r.err != nil {
				d.err = r.err.Error()
			} else {
				d.err = ""
			}
			if r.operation == "prepare" && r.err == nil && r.form == x.form && r.plan != nil {
				x.plan, x.output, x.source = r.plan, r.output, r.source
				x.preparedForm = r.form
				x.request = contract.CloneSnapshotRequest(r.plan.Preview())
				x.preview = true
				x.confirmation = ""
				x.reviewed = false
				x.last = contract.SnapshotResult{}
				x.file = contract.SnapshotFileReceipt{}
				x.revision++
			}
			if r.operation == "read" && r.err == nil && snapshotFormScopeEqual(r.form, x.form) {
				x.snapshot = contract.CloneSnapshotMetadata(r.snapshot)
				x.selected = min(max(0, x.selected), len(x.snapshot.Snapshots)-1)
				x.observation = r.observation
				x.readForm = r.form
				x.tab = 1
			}
			if (r.operation == "submit" || r.operation == "verify") && r.result.State != "not_submitted" {
				if s.deps.SnapshotsChanged != nil && len(r.affected) > 0 {
					s.deps.SnapshotsChanged(services, SnapshotManagementEvent{Object: d.object, Generation: r.generation, Affected: slices.Clone(r.affected), Result: contract.CloneSnapshotResult(r.result)})
				}
				if r.err == nil && !x.pending {
					x.baseline = x.preparedForm
				}
			}
		default:
		}
	}
}
func (s *State) SnapshotManagementResult(d *Document) contract.SnapshotResult {
	if !d.SnapshotManagementMode() {
		return contract.SnapshotResult{}
	}
	return contract.CloneSnapshotResult(d.snapshots.last)
}
func (s *State) SnapshotManagementPending(d *Document) bool {
	return d.SnapshotManagementMode() && d.snapshots.pending
}
func (s *State) SnapshotManagementGuardPending(d *Document) bool {
	return d.SnapshotManagementMode() && d.snapshots.guarding && d.snapshots.preview && !d.snapshots.abandoned
}
func (s *State) SnapshotManagementSnapshot(d *Document) contract.SnapshotMetadata {
	if !d.SnapshotManagementMode() {
		return contract.SnapshotMetadata{}
	}
	return contract.CloneSnapshotMetadata(d.snapshots.snapshot)
}
func snapshotEvidence(x *snapshotManagementDocument) string {
	raw, _ := json.MarshalIndent(struct {
		Request contract.SnapshotRequest
		Read    contract.SnapshotMetadata
		Result  contract.SnapshotResult
		Source  contract.SnapshotSource
		File    contract.SnapshotFileReceipt
	}{contract.CloneSnapshotRequest(x.request), contract.CloneSnapshotMetadata(x.readEvidence), contract.CloneSnapshotResult(x.last), x.source, x.file}, "", "  ")
	return string(raw)
}

// The display keeps native JSON readable; the full evidence copy separately
// retains exact binary response bytes and qualified identities.
func snapshotDisplayEvidence(x *snapshotManagementDocument) string {
	blocks := []string{}
	if x.request.Method != "" {
		blocks = append(blocks, snapshotRequestText(x.request))
	}
	if r := x.last.Receipt.Response; r.Status != 0 || len(r.Body) > 0 {
		blocks = append(blocks, fmt.Sprintf("HTTP %d · %s · incomplete=%t\n%s", r.Status, r.ContentType, r.Incomplete, r.Body))
	}
	if x.last.Action == "download" || x.last.Action == "stream" {
		r := x.last.Transfer
		raw, _ := json.MarshalIndent(struct {
			Name, SHA256, ExpectedChecksum  string
			ExpectedSize, Received, Written uint64
			ContentLength                   int64
			Complete                        bool
			File                            contract.SnapshotFileReceipt
		}{r.Name, r.SHA256, r.ExpectedChecksum, r.ExpectedSize, r.Received, r.Written, r.ContentLength, r.Complete, x.file}, "", "  ")
		blocks = append(blocks, string(raw))
	}
	metadata := x.last.Metadata
	if len(metadata.Reads) == 0 {
		metadata = x.readEvidence
	}
	for _, read := range metadata.Reads {
		r := read.Response
		blocks = append(blocks, fmt.Sprintf("HTTP %d · %s · incomplete=%t · truncated=%t · bytes=%d\n%s", r.Status, r.ContentType, r.Incomplete, read.BodyTruncated, read.OriginalBodyBytes, r.Body))
	}
	return strings.Join(blocks, "\n\n")
}

func (s *State) guardSnapshotsLeave(d *Document, next func(ui.Services)) bool {
	if !d.SnapshotManagementMode() {
		return false
	}
	x := d.snapshots
	if !d.loading && (x.leaveAcknowledged || !x.dirty() && !x.pending && !x.preview && !x.guarding) {
		return false
	}
	if s.deps.ActivateDesign != nil {
		s.deps.ActivateDesign(d)
	}
	x.guard, x.next = true, next
	return true
}
func (s *State) abandonSnapshots(d *Document) {
	if !d.SnapshotManagementMode() || d.loading {
		return
	}
	x := d.snapshots
	if x.lease != nil {
		x.lease.Close()
		x.lease = nil
	}
	x.observation, x.plan = nil, nil
	x.preview, x.guarding, x.abandoned, x.leaveAcknowledged = false, false, true, true
	x.revision++
}
func (s *State) closeSnapshots(d *Document) {
	if d.snapshots == nil {
		return
	}
	x := d.snapshots
	d.request++
	if d.cancel != nil {
		d.cancel()
	}
	if x.lease != nil {
		x.lease.Close()
	}
	if x.done != nil {
		<-x.done
	}
	*x = snapshotManagementDocument{}
	d.snapshots = nil
}

func (s *State) snapshotSelected(d *Document) contract.SnapshotDescription {
	if !d.SnapshotManagementMode() {
		return contract.SnapshotDescription{}
	}
	x := d.snapshots
	if x.selected < 0 || x.selected >= len(x.snapshot.Snapshots) {
		return contract.SnapshotDescription{}
	}
	return contract.CloneSnapshotDescription(x.snapshot.Snapshots[x.selected])
}
func (s *State) SelectSnapshot(d *Document, row int) {
	if !s.snapshotsReady(d) || d.snapshots.pending || d.snapshots.guarding || d.snapshots.preview {
		return
	}
	if row >= 0 && row < len(d.snapshots.snapshot.Snapshots) {
		d.snapshots.selected = row
		d.snapshots.plan = nil
		d.snapshots.revision++
		d.snapshots.confirmation = ""
	}
}
func (s *State) SnapshotFileResult(d *Document) contract.SnapshotFileReceipt {
	if !d.SnapshotManagementMode() {
		return contract.SnapshotFileReceipt{}
	}
	return d.snapshots.file
}
func (s *State) SnapshotFilePicker(d *Document, save bool) {
	if !s.snapshotsReady(d) || d.snapshots.pending || d.snapshots.guarding || d.snapshots.preview {
		return
	}
	f := d.snapshots.form
	var path string
	var err error
	if save {
		if s.deps.SnapshotFiles.Save == nil {
			return
		}
		name := s.snapshotSelected(d).Name
		if name == "" {
			name = "shard.snapshot"
		}
		path, err = s.deps.SnapshotFiles.Save(name)
		if path != "" {
			f.Destination = path
		}
	} else {
		if s.deps.SnapshotFiles.Open == nil {
			return
		}
		path, err = s.deps.SnapshotFiles.Open()
		if path != "" {
			f.Source = path
		}
	}
	if err != nil {
		d.err = err.Error()
		return
	}
	if path != "" {
		s.SetSnapshotManagementForm(d, f)
	}
}
func (s *State) SetSnapshotConfirmation(d *Document, value string) {
	if d.SnapshotManagementMode() {
		d.snapshots.confirmation = value
	}
}
