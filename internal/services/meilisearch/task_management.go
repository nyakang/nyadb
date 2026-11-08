package meilisearch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type taskMutation struct {
	sequence, used uint64
	receipt        contract.TaskMutationReceipt
	uncertain      bool
	namespace      *contract.NamespaceMutation
	owner          string
}

func (d *taskDocument) SupportsTaskManagement() bool {
	d.bound.mu.Lock()
	defer d.bound.mu.Unlock()
	return !d.bound.released && !d.bound.lost && d.bound.s.Alive()
}
func cloneTaskReceipt(r contract.TaskMutationReceipt) contract.TaskMutationReceipt {
	return contract.CloneTaskMutationReceipt(r)
}
func (s *Session) RecoverTaskDocument(o contract.ObjectRef, previous contract.TaskDocumentLease) (contract.TaskRecovery, error) {
	old, ok := previous.(*taskDocument)
	if !ok || old.object != o || old.bound.s.profileID != s.profileID || old.bound.s.config.Endpoint != s.config.Endpoint || old.bound.s.config.IndexUID != s.config.IndexUID {
		return contract.TaskRecovery{}, ErrInvalid
	}
	// Stop the original executor and wait for acknowledgement before reading
	// settled admission/uncertainty or transferring its namespace claim.
	old.bound.Release()
	old.bound.ops.Lock()
	defer old.bound.ops.Unlock()
	reserved, err := s.reserveTaskDocument(o, old.management.owner)
	if err != nil {
		return contract.TaskRecovery{}, err
	}
	next := reserved.(*taskDocument)
	next.management.receipt = cloneTaskReceipt(old.management.receipt)
	next.management.uncertain = old.management.uncertain
	next.management.namespace = contract.CloneNamespaceMutation(old.management.namespace)
	if next.management.namespace != nil {
		next.management.owner = next.sign("namespace-recovered-owner", []byte(old.management.owner))
		if err = s.claimNamespace(next.management.owner, next.management.namespace, next.bound, old.management.owner, true); err != nil {
			_ = next.Close()
			return contract.TaskRecovery{}, err
		}
	}
	if next.management.receipt.Raw != "" {
		next.management.receipt.Token = next.mutationReceiptProof(next.management.receipt)
	}
	old.bound.s.releaseNamespace(old.management.owner)
	clear(old.key[:])
	old.management = taskMutation{}
	return contract.TaskRecovery{Lease: next, Receipt: cloneTaskReceipt(next.management.receipt), Uncertain: next.management.uncertain, Namespace: contract.CloneNamespaceMutation(next.management.namespace)}, nil
}
func (d *taskDocument) mutationPlanProof(p contract.TaskMutationPlan) string {
	p.Token = ""
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return d.sign("task-mutation-plan", sum[:])
}
func (d *taskDocument) mutationReceiptProof(r contract.TaskMutationReceipt) string {
	r.Token = ""
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	return d.sign("task-mutation-receipt", sum[:])
}
func taskEligible(action, status string) bool {
	switch action {
	case "cancel":
		return status == "enqueued" || status == "processing"
	case "delete":
		return status == "succeeded" || status == "failed" || status == "canceled"
	}
	return false
}
func (d *taskDocument) mutationRequest(action string, records []contract.TaskRecord) (Request, error) {
	if serverBackupAction(action) {
		if !d.SupportsServerBackup(action) || len(records) != 0 {
			return Request{}, contract.ErrUnsupported
		}
		return serverBackupRequest(action), nil
	}
	values := url.Values{}
	ids := make([]string, len(records))
	for i, r := range records {
		ids[i] = r.UID
	}
	values.Set("uids", strings.Join(ids, ","))
	if d.object.Catalog != "" {
		values.Set("indexUids", d.object.Catalog)
	}
	method, path := "", ""
	switch action {
	case "cancel":
		method, path = "POST", "/tasks/cancel"
		values.Set("statuses", "enqueued,processing")
	case "delete":
		method, path = "DELETE", "/tasks"
		values.Set("statuses", "succeeded,failed,canceled")
	default:
		return Request{}, ErrInvalid
	}
	r := Request{Method: method, Path: path + "?" + values.Encode()}
	return r, validateRequest(r)
}
func (d *taskDocument) PrepareTaskMutation(action string, records []contract.TaskRecord) (contract.TaskMutationPlan, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	if !d.SupportsTaskManagement() {
		return contract.TaskMutationPlan{}, contract.ErrTaskLeaseLost
	}
	if d.management.uncertain || d.management.receipt.Raw != "" {
		return contract.TaskMutationPlan{}, contract.ErrTaskMutationUsed
	}
	if len(records) == 0 || len(records) > 256 {
		return contract.TaskMutationPlan{}, ErrBounds
	}
	seen := map[string]bool{}
	bytes := 0
	for _, record := range records {
		if _, err := d.original(record); err != nil {
			return contract.TaskMutationPlan{}, err
		}
		if seen[record.UID] {
			return contract.TaskMutationPlan{}, ErrInvalid
		}
		seen[record.UID] = true
		if !taskEligible(action, record.Status) {
			return contract.TaskMutationPlan{}, contract.ErrTaskMutationState
		}
		bytes += len(record.Raw)
		if bytes > responseLimit {
			return contract.TaskMutationPlan{}, ErrBounds
		}
	}
	request, err := d.mutationRequest(action, records)
	if err != nil {
		return contract.TaskMutationPlan{}, err
	}
	d.management.sequence++
	p := contract.TaskMutationPlan{Object: d.object, Action: action, Request: request.Method + " " + request.Path, Records: slices.Clone(records), Sequence: d.management.sequence}
	p.Token = d.mutationPlanProof(p)
	return p, nil
}
func (d *taskDocument) MutateTasks(ctx context.Context, p contract.TaskMutationPlan) (contract.TaskMutationResult, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	result := contract.TaskMutationResult{State: "not_submitted"}
	if !d.SupportsTaskManagement() {
		return result, contract.ErrTaskLeaseLost
	}
	if p.Token == "" || p.Token != d.mutationPlanProof(p) || p.Object != d.object || p.Sequence != d.management.sequence || p.Sequence == d.management.used || d.management.uncertain || d.management.receipt.Raw != "" {
		return result, contract.ErrTaskMutationUsed
	}
	d.management.used = p.Sequence
	result.Request = p.Request
	result.Namespace = contract.CloneNamespaceMutation(p.Namespace)
	for _, target := range p.Records {
		result.Targets = append(result.Targets, target.UID)
	}
	ctx, done, err := d.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	// Known preflight failures release only this claim. Once dispatch can have
	// occurred, retain it alongside the original receipt or uncertainty.
	defer func() {
		if !d.management.uncertain && d.management.receipt.Raw == "" {
			d.clearNamespaceClaim()
		}
	}()
	for _, original := range p.Records {
		current, err := d.read(ctx, original)
		if err != nil {
			return result, err
		}
		if !taskEligible(p.Action, current.Status) {
			return result, contract.ErrTaskMutationState
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	var request Request
	if namespaceAction(p.Action) {
		request, err = d.prepareNamespaceDispatch(ctx, p)
	} else {
		request, err = d.mutationRequest(p.Action, p.Records)
	}
	if err != nil {
		return result, err
	}
	native, response, err := d.bound.client.Submit(ctx, request)
	if err == nil && namespaceAction(p.Action) {
		_, kind, index, _ := namespaceRequest(p.Action, p.Namespace.Draft)
		if native.Type != kind || (index == "" && native.IndexUID != nil) || (index != "" && (native.IndexUID == nil || *native.IndexUID != index)) {
			d.bound.client.retire()
			err = &Interrupted{WriteOutcomeUnknown: true, Cause: ErrProtocol}
		}
	}
	if err != nil {
		result.TaskRaw = string(response.Body)
		var interrupted *Interrupted
		if taskMutationRejected(response, err) {
			result.State = "rejected"
		} else if errors.As(err, &interrupted) && interrupted.WriteOutcomeUnknown {
			d.management.uncertain = true
			result.State = "uncertain"
		}
		return result, &contract.TaskRequestError{Raw: result.TaskRaw, Cause: d.taskError(err)}
	}
	r := contract.TaskMutationReceipt{Object: d.object, Action: p.Action, UID: strconv.FormatUint(uint64(native.UID), 10), Type: native.Type, EnqueuedAt: native.EnqueuedAt, Raw: string(native.Raw), Namespace: contract.CloneNamespaceMutation(p.Namespace)}
	for _, target := range p.Records {
		r.Targets = append(r.Targets, target.UID)
	}
	r.Token = d.mutationReceiptProof(r)
	d.management.receipt = cloneTaskReceipt(r)
	result.State, result.Receipt = "enqueued", cloneTaskReceipt(r)
	return result, nil
}
func (d *taskDocument) ObserveTaskMutation(ctx context.Context, r contract.TaskMutationReceipt, bound time.Duration) (contract.TaskMutationResult, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	result := contract.TaskMutationResult{State: "enqueued", Receipt: cloneTaskReceipt(r), Targets: slices.Clone(r.Targets), Namespace: contract.CloneNamespaceMutation(r.Namespace)}
	if bound <= 0 || bound > 10*time.Minute || r.Token == "" || r.Object != d.object || r.Token != d.mutationReceiptProof(r) || r.Token != d.management.receipt.Token {
		return result, ErrInvalid
	}
	uid, err := nativeUint([]byte(r.UID), 32)
	if err != nil {
		return result, ErrInvalid
	}
	native, err := parseReceipt([]byte(r.Raw))
	if err != nil || native.UID != uint32(uid) || native.Type != r.Type || native.EnqueuedAt != r.EnqueuedAt {
		return result, ErrInvalid
	}
	if namespaceAction(r.Action) {
		if r.Namespace == nil {
			return result, ErrInvalid
		}
		_, kind, index, e := namespaceRequest(r.Action, r.Namespace.Draft)
		if e != nil || native.Type != kind || index == "" && native.IndexUID != nil || index != "" && (native.IndexUID == nil || *native.IndexUID != index) {
			return result, ErrInvalid
		}
	}
	ctx, done, err := d.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	for {
		// This unqualified native read is restricted to the signed original
		// admission receipt. It cannot expose arbitrary global task UIDs.
		task, err := d.bound.client.ReadTask(ctx, native)
		result.TaskRaw = string(task.Raw)
		if err != nil {
			var status *HTTPError
			if errors.As(err, &status) && status.Status == 404 {
				err = errors.Join(contract.ErrTaskMutationUnavailable, err)
			}
			return result, &contract.TaskRequestError{Raw: result.TaskRaw, Cause: d.taskError(err)}
		}
		result.State = task.Status
		if task.Status == "succeeded" || task.Status == "failed" || task.Status == "canceled" {
			if namespaceAction(r.Action) {
				if task.Status == "succeeded" {
					if err := d.verifyNamespaceMutation(ctx, r, task, &result); err != nil {
						return result, err
					}
				}
				d.clearNamespaceClaim()
			} else if serverBackupAction(r.Action) {
				if task.Status == "succeeded" {
					artifact, err := serverBackupArtifact(r.Action, task.Raw)
					if err != nil {
						d.bound.client.retire()
						return result, &contract.TaskRequestError{Raw: result.TaskRaw, Cause: d.taskError(errors.Join(ErrRetired, err))}
					}
					result.Artifact = artifact
				}
			} else {
				fields, e := nativeObject(task.Raw)
				details, e2 := nativeObject(fields["details"])
				key := "deletedTasks"
				if r.Action == "cancel" {
					key = "canceledTasks"
				}
				matched, e3 := nativeUint(details["matchedTasks"], 64)
				changed, e4 := nativeUint(details[key], 64)
				if e == nil && e2 == nil && e3 == nil && e4 == nil && changed <= matched && matched <= uint64(len(r.Targets)) {
					result.Matched, result.Changed, result.CountsKnown = matched, changed, true
				} else if task.Status == "succeeded" {
					d.bound.client.retire()
					return result, &contract.TaskRequestError{Raw: result.TaskRaw, Cause: d.taskError(errors.Join(ErrRetired, ErrProtocol))}
				}
			}
			d.management.receipt = contract.TaskMutationReceipt{}
			return result, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}
func taskMutationRejected(response Response, err error) bool {
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != response.Status {
		return false
	}
	switch response.Status {
	case 400, 401, 403, 404, 413:
	default:
		return false
	}
	fields, e := nativeObject(response.Body)
	code, e1 := nativeString(fields["code"])
	kind, e2 := nativeString(fields["type"])
	message, e3 := nativeString(fields["message"])
	if e != nil || e1 != nil || e2 != nil || e3 != nil || message == "" || kind != "invalid_request" && kind != "auth" {
		return false
	}
	return strings.HasPrefix(code, "invalid_task_") || code == "invalid_api_key" || code == "missing_authorization_header" || code == "task_not_found" || code == "payload_too_large"
}

var _ contract.TaskManagementLease = (*taskDocument)(nil)
var _ contract.TaskRecoveryProvider = (*Session)(nil)
