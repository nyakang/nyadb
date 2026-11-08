package meilisearch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func namespaceAction(action string) bool {
	switch action {
	case "namespace_create", "namespace_primary_key", "namespace_rename", "namespace_swap", "namespace_clear", "namespace_delete":
		return true
	}
	return false
}
func (s *Session) SupportsNamespaceManagement(o contract.ObjectRef, action string) bool {
	if !namespaceAction(action) || !s.SupportsTaskDocument(o) {
		return false
	}
	return o.Catalog == "" && s.config.IndexUID == "" || action == "namespace_primary_key" || action == "namespace_clear"
}
func (d *taskDocument) SupportsNamespaceManagement(action string) bool {
	return d.SupportsTaskManagement() && d.bound.s.SupportsNamespaceManagement(d.object, action)
}
func (d *taskDocument) namespaceRef(ref contract.NamespaceRef) error {
	if err := d.bound.s.checkNamespace(ref); err != nil {
		return err
	}
	if d.object.Catalog != "" && ref.Catalog != d.object.Catalog {
		return ErrInvalid
	}
	return nil
}
func (d *taskDocument) namespaceProof(r contract.NamespaceRecord) string {
	r.Token = ""
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	return d.sign("namespace-observation", sum[:])
}
func nativeIndexMissing(response Response, err error) bool {
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 404 || response.Status != 404 {
		return false
	}
	f, e := nativeObject(response.Body)
	code, e1 := nativeString(f["code"])
	kind, e2 := nativeString(f["type"])
	message, e3 := nativeString(f["message"])
	return e == nil && e1 == nil && e2 == nil && e3 == nil && code == "index_not_found" && kind == "invalid_request" && message != ""
}
func (d *taskDocument) readNamespace(ctx context.Context, ref contract.NamespaceRef) (contract.NamespaceRecord, error) {
	result := contract.NamespaceRecord{Ref: ref}
	version, owner := d.bound.s.namespaceVersion(ref.Catalog)
	if owner != "" && owner != d.management.owner {
		return result, contract.ErrNamespaceBusy
	}
	result.Version = version
	response, err := d.bound.client.Do(ctx, Request{Method: "GET", Path: "/indexes/" + ref.Catalog})
	result.Raw = string(response.Body)
	if nativeIndexMissing(response, err) {
		err = nil
	} else if err == nil {
		var info indexInfo
		info, err = parseIndex(response.Body, ref.Catalog)
		if err == nil {
			result.Exists = true
			result.CreatedAt, result.UpdatedAt = info.Created, info.Updated
			if info.PrimaryKey != nil {
				result.PrimaryKey, result.HasPrimaryKey = *info.PrimaryKey, true
			}
			response, err = d.bound.client.Do(ctx, Request{Method: "GET", Path: "/indexes/" + ref.Catalog + "/stats"})
			result.StatsRaw = string(response.Body)
			if err == nil {
				f, e := nativeObject(response.Body)
				count, e1 := nativeUint(f["numberOfDocuments"], 64)
				var indexing bool
				e2 := decodeJSON(f["isIndexing"], &indexing)
				if e != nil || e1 != nil || e2 != nil || string(f["isIndexing"]) != "true" && string(f["isIndexing"]) != "false" {
					err = ErrProtocol
				} else {
					result.Documents, result.Indexing = count, indexing
				}
			}
			if err == nil {
				response, err = d.bound.client.Do(ctx, Request{Method: "GET", Path: "/indexes/" + ref.Catalog})
				if err == nil && string(response.Body) != result.Raw {
					err = contract.ErrNamespaceChanged
				}
			}
		}
	}
	if err != nil {
		return result, &contract.TaskRequestError{Raw: result.Raw + "\n" + result.StatsRaw, Cause: d.taskError(err)}
	}
	if current, currentOwner := d.bound.s.namespaceVersion(ref.Catalog); current != version || currentOwner != owner {
		return result, contract.ErrNamespaceChanged
	}
	result.Token = d.namespaceProof(result)
	return result, nil
}
func (d *taskDocument) ReadNamespace(ctx context.Context, ref contract.NamespaceRef) (contract.NamespaceRecord, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	if err := d.namespaceRef(ref); err != nil {
		return contract.NamespaceRecord{}, err
	}
	ctx, done, err := d.request(ctx)
	if err != nil {
		return contract.NamespaceRecord{}, err
	}
	defer done()
	return d.readNamespace(ctx, ref)
}
func namespaceRequest(action string, draft contract.NamespaceDraft) (Request, string, string, error) {
	r := Request{ContentType: "application/json"}
	var body any
	kind, index := "", draft.Ref.Catalog
	switch action {
	case "namespace_create":
		r.Method, r.Path, kind = "POST", "/indexes", "indexCreation"
		value := map[string]string{"uid": index}
		if draft.PrimaryKeySet {
			value["primaryKey"] = draft.PrimaryKey
		}
		body = value
	case "namespace_primary_key":
		r.Method, r.Path, kind = "PATCH", "/indexes/"+index, "indexUpdate"
		body = map[string]string{"primaryKey": draft.PrimaryKey}
	case "namespace_rename":
		r.Method, r.Path, kind = "PATCH", "/indexes/"+index, "indexUpdate"
		body = map[string]string{"uid": draft.Destination.Catalog}
	case "namespace_swap":
		r.Method, r.Path, kind = "POST", "/swap-indexes", "indexSwap"
		body = []map[string]any{{"indexes": []string{index, draft.Destination.Catalog}}}
		index = ""
	case "namespace_clear":
		r.Method, r.Path, kind = "DELETE", "/indexes/"+index+"/documents", "documentDeletion"
	case "namespace_delete":
		r.Method, r.Path, kind = "DELETE", "/indexes/"+index, "indexDeletion"
	default:
		return r, "", "", ErrInvalid
	}
	if body != nil {
		r.Body, _ = json.Marshal(body)
	}
	return r, kind, index, validateRequest(r)
}
func namespaceRequestText(r Request) string {
	text := r.Method + " " + r.Path
	if len(r.Body) > 0 {
		text += "\n" + string(r.Body)
	}
	return text
}
func (d *taskDocument) PrepareNamespaceMutation(action string, draft contract.NamespaceDraft, before []contract.NamespaceRecord) (contract.TaskMutationPlan, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	if !d.SupportsNamespaceManagement(action) {
		return contract.TaskMutationPlan{}, contract.ErrUnsupported
	}
	if d.management.uncertain || d.management.receipt.Raw != "" {
		return contract.TaskMutationPlan{}, contract.ErrTaskMutationUsed
	}
	if err := d.namespaceRef(draft.Ref); err != nil {
		return contract.TaskMutationPlan{}, err
	}
	n := &contract.NamespaceMutation{Draft: draft, Before: slices.Clone(before)}
	refs := namespaceMutationRefs(n)
	pair := action == "namespace_rename" || action == "namespace_swap"
	if pair {
		if err := d.namespaceRef(draft.Destination); err != nil {
			return contract.TaskMutationPlan{}, err
		}
		if draft.Ref == draft.Destination {
			return contract.TaskMutationPlan{}, contract.ErrNamespaceNoChange
		}
	} else if draft.Destination != (contract.NamespaceRef{}) {
		return contract.TaskMutationPlan{}, ErrInvalid
	}
	if len(before) != len(refs) || pair && len(refs) != 2 {
		return contract.TaskMutationPlan{}, ErrInvalid
	}
	if (action == "namespace_primary_key" || draft.PrimaryKeySet) && (draft.PrimaryKey == "" || len(draft.PrimaryKey) > 1024 || !utf8.ValidString(draft.PrimaryKey) || strings.ContainsRune(draft.PrimaryKey, 0)) {
		return contract.TaskMutationPlan{}, ErrInvalid
	}
	if action == "namespace_primary_key" && !draft.PrimaryKeySet || !draft.PrimaryKeySet && draft.PrimaryKey != "" || draft.PrimaryKeySet && action != "namespace_primary_key" && action != "namespace_create" {
		return contract.TaskMutationPlan{}, ErrInvalid
	}
	for i, record := range before {
		if record.Ref != refs[i] || record.Token == "" || record.Token != d.namespaceProof(record) {
			return contract.TaskMutationPlan{}, ErrInvalid
		}
		version, owner := d.bound.s.namespaceVersion(refs[i].Catalog)
		if version != record.Version || owner != "" {
			return contract.TaskMutationPlan{}, contract.ErrNamespaceChanged
		}
		n.Versions = append(n.Versions, record.Version)
		wantExists := action != "namespace_create" && !(action == "namespace_rename" && i == 1)
		if record.Exists != wantExists {
			if record.Exists {
				return contract.TaskMutationPlan{}, contract.ErrNamespaceExists
			}
			return contract.TaskMutationPlan{}, ErrTaskUnavailable
		}
		if record.Indexing {
			return contract.TaskMutationPlan{}, contract.ErrNamespaceBusy
		}
	}
	if action == "namespace_primary_key" {
		if before[0].HasPrimaryKey && before[0].PrimaryKey == draft.PrimaryKey {
			return contract.TaskMutationPlan{}, contract.ErrNamespaceNoChange
		}
		if before[0].Documents != 0 {
			return contract.TaskMutationPlan{}, contract.ErrNamespaceNotEmpty
		}
	}
	r, _, _, err := namespaceRequest(action, draft)
	if err != nil {
		return contract.TaskMutationPlan{}, err
	}
	d.management.sequence++
	p := contract.TaskMutationPlan{Object: d.object, Action: action, Request: namespaceRequestText(r), Namespace: n, Sequence: d.management.sequence}
	p.Token = d.mutationPlanProof(p)
	return p, nil
}
func sameNamespaceObservation(a, b contract.NamespaceRecord) bool {
	a.Token, b.Token, a.Version, b.Version = "", "", 0, 0
	// Native not-found messages may vary while still proving absence.
	if !a.Exists && !b.Exists {
		a.Raw, b.Raw = "", ""
	}
	return a == b
}
func (d *taskDocument) prepareNamespaceDispatch(ctx context.Context, p contract.TaskMutationPlan) (Request, error) {
	if !d.SupportsNamespaceManagement(p.Action) || p.Namespace == nil {
		return Request{}, contract.ErrUnsupported
	}
	owner := d.sign("namespace-owner", []byte(p.Token))
	if err := d.bound.s.claimNamespace(owner, p.Namespace, d.bound, "", false); err != nil {
		return Request{}, err
	}
	d.management.owner, d.management.namespace = owner, contract.CloneNamespaceMutation(p.Namespace)
	for _, original := range p.Namespace.Before {
		current, err := d.readNamespace(ctx, original.Ref)
		if err != nil {
			return Request{}, err
		}
		if !sameNamespaceObservation(original, current) {
			return Request{}, contract.ErrNamespaceChanged
		}
	}
	r, _, _, err := namespaceRequest(p.Action, p.Namespace.Draft)
	return r, err
}
func (d *taskDocument) clearNamespaceClaim() {
	d.bound.s.releaseNamespace(d.management.owner)
	d.management.owner, d.management.namespace = "", nil
}
func (d *taskDocument) verifyNamespaceMutation(ctx context.Context, r contract.TaskMutationReceipt, task Task, result *contract.TaskMutationResult) error {
	if r.Namespace == nil || len(r.Namespace.Before) == 0 {
		return ErrProtocol
	}
	n := r.Namespace
	for _, ref := range namespaceMutationRefs(n) {
		record, err := d.readNamespace(ctx, ref)
		result.Namespaces = append(result.Namespaces, record)
		if err != nil {
			return errors.Join(contract.ErrNamespaceUnverified, err)
		}
	}
	a, old := result.Namespaces[0], n.Before[0]
	sameContentMetadata := func(a, b contract.NamespaceRecord) bool {
		return a.Exists && !a.Indexing && a.CreatedAt == b.CreatedAt && a.HasPrimaryKey == b.HasPrimaryKey && a.PrimaryKey == b.PrimaryKey && a.Documents == b.Documents
	}
	verified := false
	fields, e := nativeObject(task.Raw)
	details, e2 := nativeObject(fields["details"])
	if e != nil || e2 != nil {
		return contract.ErrNamespaceUnverified
	}
	switch r.Action {
	case "namespace_create":
		pk, ok := details["primaryKey"]
		want, _ := json.Marshal(n.Draft.PrimaryKey)
		if !n.Draft.PrimaryKeySet {
			want = []byte("null")
		}
		verified = ok && string(pk) == string(want) && a.Exists && !a.Indexing && a.Documents == 0 && a.HasPrimaryKey == n.Draft.PrimaryKeySet && a.PrimaryKey == n.Draft.PrimaryKey
	case "namespace_primary_key":
		pk, err := nativeString(details["primaryKey"])
		verified = err == nil && pk == n.Draft.PrimaryKey && a.Exists && !a.Indexing && a.Documents == 0 && a.CreatedAt == old.CreatedAt && a.HasPrimaryKey && a.PrimaryKey == n.Draft.PrimaryKey
	case "namespace_rename":
		oldUID, e1 := nativeString(details["oldIndexUid"])
		newUID, e2 := nativeString(details["newIndexUid"])
		verified = len(result.Namespaces) == 2 && !a.Exists && oldUID == n.Draft.Ref.Catalog && newUID == n.Draft.Destination.Catalog && e1 == nil && e2 == nil && sameContentMetadata(result.Namespaces[1], old)
	case "namespace_swap":
		var swaps []struct {
			Indexes []string `json:"indexes"`
			Rename  bool     `json:"rename"`
		}
		e := decodeJSON(details["swaps"], &swaps)
		verified = e == nil && len(swaps) == 1 && !swaps[0].Rename && slices.Equal(swaps[0].Indexes, []string{n.Draft.Ref.Catalog, n.Draft.Destination.Catalog}) && len(result.Namespaces) == 2 && sameContentMetadata(a, n.Before[1]) && sameContentMetadata(result.Namespaces[1], old) && a.UpdatedAt == n.Before[1].UpdatedAt && result.Namespaces[1].UpdatedAt == old.UpdatedAt
	case "namespace_clear":
		_, e := nativeUint(details["deletedDocuments"], 64)
		verified = e == nil && a.Exists && !a.Indexing && a.CreatedAt == old.CreatedAt && a.HasPrimaryKey == old.HasPrimaryKey && a.PrimaryKey == old.PrimaryKey && a.Documents == 0
	case "namespace_delete":
		_, e := nativeUint(details["deletedDocuments"], 64)
		verified = e == nil && !a.Exists
	}
	if !verified {
		return contract.ErrNamespaceUnverified
	}
	result.NamespaceVerified = true
	return nil
}

var _ contract.NamespaceManagementLease = (*taskDocument)(nil)
var _ contract.NamespaceManagementProvider = (*Session)(nil)
