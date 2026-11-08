package objectworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type namespaceManagementState struct {
	open                                               bool
	action, uid, destination, primaryKey, confirmation string
	draft                                              contract.NamespaceDraft
	records                                            []contract.NamespaceRecord
}
type NamespaceEvent struct {
	Object     contract.ObjectRef
	Generation uint64
	Result     contract.TaskMutationResult
}

func namespaceAction(action string) bool {
	switch action {
	case "namespace_create", "namespace_primary_key", "namespace_rename", "namespace_swap", "namespace_clear", "namespace_delete":
		return true
	}
	return false
}
func (s *State) NamespaceManagementSupported(o contract.ObjectRef, action string) bool {
	if !s.TaskScopeSupported(o) {
		return false
	}
	p, err := s.deps.TaskDocumentsFor(o.ProfileID)
	n, ok := p.(contract.NamespaceManagementProvider)
	return err == nil && ok && n.SupportsNamespaceManagement(o, action)
}
func (s *State) CanManageNamespace(d *Document, action string) bool {
	if !s.taskReady(d) || d.tasks.management.uncertain || d.tasks.management.receipt.Raw != "" {
		return false
	}
	p, ok := d.tasks.lease.(contract.NamespaceManagementLease)
	return ok && p.SupportsNamespaceManagement(action)
}
func (s *State) OpenNamespaceManagement(d *Document) bool {
	if d == nil || d.closed || d.tasks == nil || d.tasks.lost || s.session().ProfileID != d.profileID || !s.sessionFor(d.profileID).Connected {
		return false
	}
	if d.tasks.lease == nil {
		if err := s.reserveTasks(d); err != nil {
			d.err = err.Error()
			return false
		}
	}
	x := &d.tasks.namespace
	if x.action == "" {
		x.action = "namespace_create"
		if d.object.Catalog != "" {
			x.action = "namespace_primary_key"
		}
	}
	p, ok := d.tasks.lease.(contract.NamespaceManagementLease)
	if !ok || !p.SupportsNamespaceManagement(x.action) || d.tasks.generation != s.sessionFor(d.profileID).Generation || d.tasks.management.uncertain || d.tasks.management.receipt.Raw != "" {
		return false
	}
	x.open = true
	if d.object.Catalog != "" {
		x.uid = d.object.Catalog
	}
	return true
}
func (s *State) namespaceDraft(d *Document) contract.NamespaceDraft {
	x := &d.tasks.namespace
	ref := contract.NamespaceRef{ProfileID: d.profileID, Catalog: x.uid}
	draft := contract.NamespaceDraft{Ref: ref}
	if x.action == "namespace_rename" || x.action == "namespace_swap" {
		draft.Destination = contract.NamespaceRef{ProfileID: d.profileID, Catalog: x.destination}
	}
	if x.action == "namespace_primary_key" || x.action == "namespace_create" && x.primaryKey != "" {
		draft.PrimaryKeySet, draft.PrimaryKey = true, x.primaryKey
	}
	return draft
}
func (s *State) ReadNamespaceManagement(d *Document, invalidate func()) {
	if d == nil || d.tasks == nil || !s.CanManageNamespace(d, d.tasks.namespace.action) {
		return
	}
	x := d.tasks
	draft, action := s.namespaceDraft(d), x.namespace.action
	refs := []contract.NamespaceRef{draft.Ref}
	if draft.Destination.Catalog != "" {
		refs = append(refs, draft.Destination)
	}
	x.namespace.records = nil
	x.namespace.draft = draft
	lease, generation := x.lease, x.generation
	d.request++
	request := d.request
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.loading, d.err = cancel, true, ""
	x.cancelRequested = false
	out := make(chan taskResult, 1)
	x.results = out
	go func() {
		var records []contract.NamespaceRecord
		var err error
		for _, ref := range refs {
			var record contract.NamespaceRecord
			record, err = lease.(contract.NamespaceManagementLease).ReadNamespace(ctx, ref)
			records = append(records, record)
			if err != nil {
				break
			}
		}
		out <- taskResult{request: request, generation: generation, lease: lease, operation: "namespace-read", namespaceDraft: draft, namespaceAction: action, namespaces: slices.Clone(records), err: err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) consumeNamespaceRead(d *Document, reply taskResult) bool {
	if reply.operation != "namespace-read" {
		return false
	}
	x := &d.tasks.namespace
	if d.tasks.cancelRequested {
		d.tasks.cancelRequested = false
		d.err = "索引信息读取已停止"
		return true
	}
	if reply.err != nil {
		var diagnostic *contract.TaskRequestError
		if errors.As(reply.err, &diagnostic) && diagnostic.Raw != "" {
			d.tasks.failureRaw = diagnostic.Raw
		}
		d.err = reply.err.Error()
		return true
	}
	if reply.namespaceDraft != s.namespaceDraft(d) || reply.namespaceAction != x.action {
		d.err = "索引表单已变化；请重新读取原索引信息"
		return true
	}
	x.draft = reply.namespaceDraft
	x.records = slices.Clone(reply.namespaces)
	d.err = ""
	return true
}
func (s *State) PrepareNamespaceManagement(d *Document) error {
	if d == nil || d.tasks == nil || !s.CanManageNamespace(d, d.tasks.namespace.action) {
		return contract.ErrUnsupported
	}
	x := &d.tasks.namespace
	if x.draft != s.namespaceDraft(d) || len(x.records) == 0 {
		d.err = "索引表单已变化；请重新读取原索引信息"
		return contract.ErrNamespaceChanged
	}
	p, err := d.tasks.lease.(contract.NamespaceManagementLease).PrepareNamespaceMutation(x.action, x.draft, slices.Clone(x.records))
	if err != nil {
		d.err = err.Error()
		return err
	}
	p = contract.CloneTaskMutationPlan(p)
	d.tasks.management.plan = &p
	d.tasks.management.preview = true
	d.tasks.management.acknowledged = false
	x.confirmation = ""
	x.open = false
	d.err = ""
	return nil
}
func namespaceConfirmation(p contract.TaskMutationPlan) string {
	if p.Namespace == nil {
		return ""
	}
	text := p.Namespace.Draft.Ref.Catalog
	if p.Namespace.Draft.Destination.Catalog != "" {
		text += " / " + p.Namespace.Draft.Destination.Catalog
	}
	return text
}
func namespaceEvidence(n *contract.NamespaceMutation, records []contract.NamespaceRecord) string {
	if n == nil {
		return ""
	}
	// Tokens stay private to the lease; copied evidence retains complete native
	// snapshots, desired qualified identity and local epoch explanations.
	n = contract.CloneNamespaceMutation(n)
	for i := range n.Before {
		n.Before[i].Token = ""
	}
	records = slices.Clone(records)
	for i := range records {
		records[i].Token = ""
	}
	raw, _ := json.MarshalIndent(struct {
		Operation *contract.NamespaceMutation
		Observed  []contract.NamespaceRecord
	}{n, records}, "", "  ")
	return string(raw)
}
func namespacePreview(n *contract.NamespaceMutation) string {
	if n == nil {
		return ""
	}
	parts := []string{}
	for _, r := range n.Before {
		parts = append(parts, "GET /indexes/"+r.Ref.Catalog+"\n"+r.Raw)
		if r.StatsRaw != "" {
			parts = append(parts, "GET /indexes/"+r.Ref.Catalog+"/stats\n"+r.StatsRaw)
		}
	}
	return strings.Join(parts, "\n\n")
}
func taskMutationEvidence(r contract.TaskMutationResult) string {
	parts := []string{r.Request, r.Receipt.Raw, r.TaskRaw, namespaceEvidence(r.Namespace, r.Namespaces)}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}
