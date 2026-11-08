package objectworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type CollectionManagementForm struct {
	Action, Collection, Alias, DestinationCollection, DestinationAlias string
	Configuration, Timeout                                             string
}
type collectionManagementDocument struct {
	lease                                                               contract.CollectionManagementLease
	generation, revision                                                uint64
	form, baseline, readForm, preparedForm                              CollectionManagementForm
	observation                                                         contract.CollectionManagementObservation
	snapshot, readEvidence                                              contract.CollectionManagementSnapshot
	plan                                                                contract.CollectionManagementPlan
	request                                                             contract.CollectionManagementRequest
	affected                                                            []contract.NamespaceRef
	last                                                                contract.CollectionManagementResult
	results                                                             <-chan collectionManagementReply
	tab                                                                 int
	confirmation                                                        string
	pending, lost, abandoned, preview, guarding, guard, cancelRequested bool
	leaveAcknowledged, reviewed                                         bool
	next                                                                func(ui.Services)
}
type collectionManagementReply struct {
	request, generation uint64
	operation           string
	form                CollectionManagementForm
	observation         contract.CollectionManagementObservation
	snapshot            contract.CollectionManagementSnapshot
	result              contract.CollectionManagementResult
	affected            []contract.NamespaceRef
	err                 error
}
type CollectionManagementEvent struct {
	Object     contract.ObjectRef
	Generation uint64
	Affected   []contract.NamespaceRef
	Result     contract.CollectionManagementResult
}

func collectionManagementObject(o contract.ObjectRef) bool {
	return o.ProfileID > 0 && o.Catalog == "" && o.Schema == "" && o.Kind == "collection_management" && o.Name == "collection_management"
}
func (s *State) CollectionManagementSupported(o contract.ObjectRef) bool {
	if !collectionManagementObject(o) || s.deps.CollectionManagementFor == nil {
		return false
	}
	p, err := s.deps.CollectionManagementFor(o.ProfileID)
	return err == nil && p != nil && p.SupportsCollectionManagement(o, "collection_create")
}
func (s *State) OpenCollectionManagement(id uint64, o contract.ObjectRef) *Document {
	if id == 0 || !s.CollectionManagementSupported(o) {
		return nil
	}
	if old := s.documents[id]; old != nil {
		s.Close(old)
	}
	f := CollectionManagementForm{Action: "collection_create", Timeout: "30"}
	d := &Document{id: id, profileID: o.ProfileID, object: o, table: o.Name, objectKind: "集合与别名管理", collections: &collectionManagementDocument{form: f, baseline: f}}
	s.documents[id] = d
	return d
}
func (d *Document) CollectionManagementMode() bool {
	return d != nil && !d.closed && d.collections != nil
}
func (x *collectionManagementDocument) dirty() bool {
	return x != nil && !x.abandoned && x.form != x.baseline
}
func (s *State) collectionsReady(d *Document) bool {
	if !d.CollectionManagementMode() || d.loading || d.collections.lost || d.collections.abandoned || s.session().ProfileID != d.profileID {
		return false
	}
	if s.deps.DocumentActive != nil && !s.deps.DocumentActive(d.id) {
		return false
	}
	w := s.sessionFor(d.profileID)
	return w.Connected && w.ProfileID == d.profileID && (d.collections.lease == nil || w.Generation == d.collections.generation)
}
func (s *State) reserveCollections(d *Document) error {
	p, err := s.deps.CollectionManagementFor(d.profileID)
	if err != nil {
		return err
	}
	l, err := p.ReserveCollectionManagementDocument(d.object, d.id)
	if err != nil {
		return err
	}
	if l.Object() != d.object || l.Generation() != s.sessionFor(d.profileID).Generation {
		l.Close()
		return contract.ErrRevoked
	}
	d.collections.lease, d.collections.generation = l, l.Generation()
	return nil
}
func collectionNamespace(profile int64, name string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: profile, Catalog: name}
}
func collectionFormTargets(profile int64, f CollectionManagementForm) ([]contract.NamespaceRef, error) {
	if f.Collection == "" || len(f.Collection) > 255 {
		return nil, errors.New("请输入原物理集合名称")
	}
	targets := []contract.NamespaceRef{collectionNamespace(profile, f.Collection)}
	if f.Action == "alias_retarget" {
		if f.DestinationCollection == "" || len(f.DestinationCollection) > 255 {
			return nil, errors.New("请输入目标物理集合名称")
		}
		if f.DestinationCollection != f.Collection {
			targets = append(targets, collectionNamespace(profile, f.DestinationCollection))
		}
	}
	return targets, nil
}
func collectionFormDraft(profile int64, f CollectionManagementForm, before contract.CollectionManagementSnapshot) (contract.CollectionManagementDraft, error) {
	seconds, err := strconv.Atoi(f.Timeout)
	if err != nil || seconds < 1 || seconds > 300 {
		return contract.CollectionManagementDraft{}, errors.New("集合提交超时必须为 1–300 秒")
	}
	draft := contract.CollectionManagementDraft{Action: f.Action, TimeoutSeconds: seconds}
	ref := collectionNamespace(profile, f.Collection)
	switch f.Action {
	case "collection_create", "collection_update":
		if len(f.Configuration) > 8<<20 {
			return draft, errors.New("集合配置 JSON 不得超过 8 MiB")
		}
		draft.Collection, draft.Configuration = ref, f.Configuration
	case "collection_delete":
		draft.Collection = ref
	case "alias_create", "alias_delete", "alias_rename", "alias_retarget":
		if f.Alias == "" || len(f.Alias) > 255 {
			return draft, errors.New("请输入原别名")
		}
		draft.Action = "aliases"
		alias := collectionNamespace(profile, f.Alias)
		if f.Action != "alias_create" {
			found := false
			for _, a := range before.Aliases {
				if a.Alias == alias && a.Collection == ref {
					found = true
				}
			}
			if !found {
				return draft, errors.New("原别名与物理集合不匹配，请重新读取")
			}
		}
		switch f.Action {
		case "alias_create":
			draft.Aliases = []contract.CollectionAliasChange{{Action: "create_alias", Alias: alias, Collection: ref}}
		case "alias_delete":
			draft.Aliases = []contract.CollectionAliasChange{{Action: "delete_alias", Alias: alias}}
		case "alias_rename":
			draft.Aliases = []contract.CollectionAliasChange{{Action: "rename_alias", Alias: alias, Destination: collectionNamespace(profile, f.DestinationAlias)}}
		case "alias_retarget":
			draft.Aliases = []contract.CollectionAliasChange{{Action: "delete_alias", Alias: alias}, {Action: "create_alias", Alias: alias, Collection: collectionNamespace(profile, f.DestinationCollection)}}
		}
	default:
		return draft, contract.ErrUnsupported
	}
	return draft, nil
}
func collectionFormScopeEqual(a, b CollectionManagementForm) bool {
	return a.Action == b.Action && a.Collection == b.Collection && a.Alias == b.Alias && a.DestinationCollection == b.DestinationCollection && a.DestinationAlias == b.DestinationAlias
}
func (s *State) SetCollectionManagementForm(d *Document, f CollectionManagementForm) bool {
	if !s.collectionsReady(d) || d.collections.pending || d.collections.guarding {
		return false
	}
	x := d.collections
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
func (s *State) CollectionManagementForm(d *Document) CollectionManagementForm {
	if !d.CollectionManagementMode() {
		return CollectionManagementForm{}
	}
	return d.collections.form
}
func (s *State) ReadCollectionManagement(d *Document, invalidate func()) {
	if !s.collectionsReady(d) || d.collections.pending || d.collections.guarding {
		return
	}
	x := d.collections
	targets, err := collectionFormTargets(d.profileID, x.form)
	if err != nil {
		d.err = err.Error()
		return
	}
	if x.lease == nil {
		if err = s.reserveCollections(d); err != nil {
			d.err = err.Error()
			return
		}
	}
	form := x.form
	x.observation, x.plan, x.preview = nil, nil, false
	x.revision++
	s.runCollections(d, invalidate, "read", func(ctx context.Context, l contract.CollectionManagementLease) collectionManagementReply {
		o, err := l.Read(ctx, slices.Clone(targets))
		var snapshot contract.CollectionManagementSnapshot
		if o != nil {
			snapshot = contract.CloneCollectionManagementSnapshot(o.Snapshot())
		}
		return collectionManagementReply{form: form, observation: o, snapshot: snapshot, err: err}
	})
}
func (s *State) PrepareCollectionManagement(d *Document) error {
	if !s.collectionsReady(d) || d.collections.pending || d.collections.guarding {
		return contract.ErrUnsupported
	}
	x := d.collections
	if x.lease == nil || x.observation == nil || !collectionFormScopeEqual(x.readForm, x.form) {
		d.err = "集合表单已变化，请重新读取原信息"
		return contract.ErrNamespaceChanged
	}
	draft, err := collectionFormDraft(d.profileID, x.form, x.snapshot)
	if err == nil {
		x.plan, err = x.lease.Prepare(x.observation, contract.CloneCollectionManagementDraft(draft))
	}
	if err != nil {
		x.plan, x.preview = nil, false
		if errors.Is(err, contract.ErrRevoked) {
			x.lost = true
		}
		d.err = err.Error()
		return err
	}
	x.preparedForm = x.form
	x.last = contract.CollectionManagementResult{}
	x.request = contract.CloneCollectionManagementRequest(x.plan.Preview())
	x.affected = slices.Clone(x.plan.Affected())
	x.preview, x.confirmation, x.leaveAcknowledged = true, "", false
	x.reviewed = false
	x.revision++
	d.err = ""
	return nil
}
func collectionConfirmation(f CollectionManagementForm) string {
	if f.Action == "collection_delete" {
		return f.Collection
	}
	if f.Action == "alias_delete" || f.Action == "alias_retarget" || f.Action == "alias_rename" {
		return f.Alias
	}
	return ""
}
func (s *State) CanSubmitCollectionManagement(d *Document) bool {
	if !s.collectionsReady(d) {
		return false
	}
	x := d.collections
	_, truncated := taskPreview(collectionRequestText(x.request))
	return !x.pending && !x.guarding && x.preview && x.plan != nil && x.form == x.preparedForm && (!truncated || x.reviewed) && (collectionConfirmation(x.form) == "" || x.confirmation == collectionConfirmation(x.form))
}
func (s *State) SubmitCollectionManagement(d *Document, services ui.Services) {
	if !s.CanSubmitCollectionManagement(d) {
		return
	}
	x := d.collections
	plan, revision, form, generation := x.plan, x.revision, x.form, x.generation
	affected := slices.Clone(x.affected)
	x.guarding = true
	next := func(sv ui.Services) {
		if !d.CollectionManagementMode() || !x.guarding || revision != x.revision {
			return
		}
		x.guarding = false
		if !s.collectionsReady(d) || form != x.form || generation != s.sessionFor(d.profileID).Generation {
			d.err = "原集合会话或表单已变化，不能提交"
			return
		}
		x.preview, x.plan, x.observation = false, nil, nil
		x.pending = true
		s.runCollections(d, sv.Invalidate, "submit", func(ctx context.Context, l contract.CollectionManagementLease) collectionManagementReply {
			r, err := l.Submit(ctx, plan)
			return collectionManagementReply{result: contract.CloneCollectionManagementResult(r), affected: slices.Clone(affected), err: err}
		})
	}
	if s.deps.BeforeCollections != nil && s.deps.BeforeCollections(services, d.id, d.profileID, generation, plan, next) {
		return
	}
	next(services)
}
func (s *State) VerifyCollectionManagement(d *Document, invalidate func()) {
	if !s.collectionsReady(d) || !d.collections.pending || d.collections.lease == nil {
		return
	}
	affected := slices.Clone(d.collections.affected)
	s.runCollections(d, invalidate, "verify", func(ctx context.Context, l contract.CollectionManagementLease) collectionManagementReply {
		r, err := l.Verify(ctx)
		return collectionManagementReply{result: contract.CloneCollectionManagementResult(r), affected: affected, err: err}
	})
}
func (s *State) runCollections(d *Document, invalidate func(), operation string, run func(context.Context, contract.CollectionManagementLease) collectionManagementReply) {
	x := d.collections
	l, generation := x.lease, x.generation
	d.request++
	request := d.request
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.loading, d.err = cancel, true, ""
	x.cancelRequested, x.leaveAcknowledged = false, false
	out := make(chan collectionManagementReply, 1)
	x.results = out
	go func() {
		r := run(ctx, l)
		r.request, r.generation, r.operation = request, generation, operation
		out <- r
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) CancelCollectionManagement(d *Document) {
	if !d.CollectionManagementMode() || !d.loading || d.collections.cancelRequested {
		return
	}
	d.collections.cancelRequested = true
	if d.cancel != nil {
		d.cancel()
	}
}
func (s *State) consumeCollections(services ui.Services) {
	active := s.session()
	for _, d := range s.documents {
		if !d.CollectionManagementMode() {
			continue
		}
		x := d.collections
		orphaned := active.ProfileID == 0 && s.deps.DocumentActive != nil && s.deps.DocumentActive(d.id)
		if active.ProfileID != d.profileID && !orphaned {
			continue
		}
		current := s.sessionFor(d.profileID)
		if x.lease != nil && (!current.Connected || current.Generation != x.generation) {
			x.lost = true
			if !d.loading {
				d.err = "原集合管理会话已失效，原草稿与证据保留"
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
				if r.operation == "verify" && r.result.Receipt.Action == "" && x.last.Receipt.Action != "" {
					r.result.Receipt = x.last.Receipt
					if r.result.State == "" {
						r.result.State = "indeterminate"
					}
				}
				x.last = contract.CloneCollectionManagementResult(r.result)
				x.tab = 2
				x.pending = r.result.State != "not_submitted" && r.result.State != "applied" && r.result.State != "unchanged"
			}
			if r.operation == "read" {
				x.readEvidence = contract.CloneCollectionManagementSnapshot(r.snapshot)
			}
			if !current.Connected || r.generation != current.Generation {
				x.lost = true
				d.err = "原集合管理会话已失效，原草稿与证据保留"
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
			if r.operation == "read" && r.err == nil && collectionFormScopeEqual(r.form, x.form) {
				x.snapshot = contract.CloneCollectionManagementSnapshot(r.snapshot)
				x.observation = r.observation
				x.readForm = r.form
				x.tab = 1
			}
			if (r.operation == "submit" || r.operation == "verify") && r.result.State != "not_submitted" {
				if s.deps.CollectionsChanged != nil {
					s.deps.CollectionsChanged(services, CollectionManagementEvent{Object: d.object, Generation: r.generation, Affected: slices.Clone(r.affected), Result: contract.CloneCollectionManagementResult(r.result)})
				}
				if r.err == nil && !x.pending {
					x.baseline = x.preparedForm
				}
			}
		default:
		}
	}
}
func (s *State) CollectionManagementResult(d *Document) contract.CollectionManagementResult {
	if !d.CollectionManagementMode() {
		return contract.CollectionManagementResult{}
	}
	return contract.CloneCollectionManagementResult(d.collections.last)
}
func (s *State) CollectionManagementPending(d *Document) bool {
	return d.CollectionManagementMode() && d.collections.pending
}
func (s *State) CollectionManagementGuardPending(d *Document) bool {
	return d.CollectionManagementMode() && d.collections.guarding && d.collections.preview && !d.collections.abandoned
}
func (s *State) CollectionManagementSnapshot(d *Document) contract.CollectionManagementSnapshot {
	if !d.CollectionManagementMode() {
		return contract.CollectionManagementSnapshot{}
	}
	return contract.CloneCollectionManagementSnapshot(d.collections.snapshot)
}
func collectionEvidence(x *collectionManagementDocument) string {
	raw, _ := json.MarshalIndent(struct {
		Request contract.CollectionManagementRequest
		Read    contract.CollectionManagementSnapshot
		Result  contract.CollectionManagementResult
	}{contract.CloneCollectionManagementRequest(x.request), contract.CloneCollectionManagementSnapshot(x.readEvidence), contract.CloneCollectionManagementResult(x.last)}, "", "  ")
	return string(raw)
}
func (s *State) guardCollectionsLeave(d *Document, next func(ui.Services)) bool {
	if !d.CollectionManagementMode() {
		return false
	}
	x := d.collections
	if !d.loading && (x.leaveAcknowledged || !x.dirty() && !x.pending && !x.preview && !x.guarding) {
		return false
	}
	if s.deps.ActivateDesign != nil {
		s.deps.ActivateDesign(d)
	}
	x.guard, x.next = true, next
	return true
}
func (s *State) abandonCollections(d *Document) {
	if !d.CollectionManagementMode() || d.loading {
		return
	}
	x := d.collections
	if x.lease != nil {
		x.lease.Close()
		x.lease = nil
	}
	x.observation, x.plan = nil, nil
	x.preview, x.guarding, x.abandoned, x.leaveAcknowledged = false, false, true, true
	x.revision++
}
func (s *State) closeCollections(d *Document) {
	if d.collections == nil {
		return
	}
	x := d.collections
	d.request++
	if d.cancel != nil {
		d.cancel()
	}
	if x.lease != nil {
		x.lease.Close()
	}
	*x = collectionManagementDocument{}
	d.collections = nil
}
