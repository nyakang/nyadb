package meilisearch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const settingsLimit = 1 << 20

type settingsDocument struct {
	bound          *queryLease
	proof          *taskDocument
	object         contract.ObjectRef
	created        string
	snapshot       contract.SettingsSnapshot
	receipt        contract.SettingsReceipt
	sequence, used uint64
	uncertain      bool
}

func (s *Session) SupportsSettingsDocument(o contract.ObjectRef) bool {
	return s.Alive() && o.ProfileID == s.profileID && o.Kind == "settings" && o.Name == "settings" && o.Schema == "" && validIndex(o.Catalog) && (s.config.IndexUID == "" || s.config.IndexUID == o.Catalog)
}
func (s *Session) ReserveSettingsDocument(o contract.ObjectRef) (contract.SettingsDocumentLease, error) {
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	if !s.SupportsSettingsDocument(o) {
		return nil, contract.ErrUnsupported
	}
	reserved, err := s.ReserveQuery(1)
	if err != nil {
		return nil, err
	}
	bound := reserved.(*queryLease)
	proof := &taskDocument{bound: bound, object: o.Namespace().Object("tasks", "tasks")}
	if _, err = rand.Read(proof.key[:]); err != nil {
		reserved.Release()
		return nil, ErrInvalid
	}
	return &settingsDocument{bound: bound, proof: proof, object: o}, nil
}
func (s *Session) RecoverSettingsDocument(o contract.ObjectRef, previous contract.SettingsDocumentLease) (contract.SettingsRecovery, error) {
	old, ok := previous.(*settingsDocument)
	if !ok || old.object != o || old.bound.s.profileID != s.profileID || old.bound.s.config.Endpoint != s.config.Endpoint || old.bound.s.config.IndexUID != s.config.IndexUID {
		return contract.SettingsRecovery{}, ErrInvalid
	}
	reserved, err := s.ReserveSettingsDocument(o)
	if err != nil {
		return contract.SettingsRecovery{}, err
	}
	next := reserved.(*settingsDocument)
	old.bound.ops.Lock()
	next.created, next.snapshot, next.receipt, next.uncertain = old.created, old.snapshot, old.receipt, old.uncertain
	old.bound.ops.Unlock()
	if next.snapshot.Raw != "" {
		next.snapshot.Token = next.snapshotProof(next.snapshot)
	}
	if next.receipt.Raw != "" {
		next.receipt.Token = next.receiptProof(next.receipt)
	}
	_ = old.Close()
	return contract.SettingsRecovery{Lease: next, Snapshot: next.snapshot, Receipt: next.receipt, Uncertain: next.uncertain}, nil
}
func (d *settingsDocument) Close() error {
	d.bound.Release()
	d.bound.ops.Lock()
	clear(d.proof.key[:])
	d.snapshot = contract.SettingsSnapshot{}
	d.receipt = contract.SettingsReceipt{}
	d.bound.ops.Unlock()
	return nil
}
func settingsValue(raw string) (map[string]any, string, error) {
	if len(raw) == 0 || len(raw) > responseLimit {
		return nil, "", ErrBounds
	}
	var value map[string]any
	if err := decodeJSON([]byte(raw), &value); err != nil || value == nil {
		return nil, "", ErrInvalid
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, "", ErrInvalid
	}
	sum := sha256.Sum256(encoded)
	return value, hex.EncodeToString(sum[:]), nil
}
func settingsDelta(old, current map[string]any) map[string]any {
	return settingsDeltaAt(old, current, nil)
}

// PATCH merges only particular native setting objects. Dictionary values such
// as synonyms and facet sort orders replace the whole dictionary; recursively
// diffing them would silently discard the unchanged entries.
func settingsMergeObject(path []string) bool {
	if len(path) == 1 {
		switch path[0] {
		case "typoTolerance", "faceting", "pagination", "embedders":
			return true
		}
	}
	return len(path) == 2 && (path[0] == "embedders" || path[0] == "typoTolerance" && path[1] == "minWordSizeForTypos")
}

func settingsDeltaAt(old, current map[string]any, path []string) map[string]any {
	out := map[string]any{}
	for k, before := range old {
		after, ok := current[k]
		if !ok {
			out[k] = nil
			continue
		}
		a, aMap := before.(map[string]any)
		b, bMap := after.(map[string]any)
		child := append(append([]string(nil), path...), k)
		if aMap && bMap && settingsMergeObject(child) {
			if nested := settingsDeltaAt(a, b, child); len(nested) > 0 {
				out[k] = nested
			}
			continue
		}
		x, _ := json.Marshal(before)
		y, _ := json.Marshal(after)
		if string(x) != string(y) {
			out[k] = after
		}
	}
	for k, after := range current {
		if _, ok := old[k]; !ok {
			out[k] = after
		}
	}
	return out
}
func (d *settingsDocument) snapshotProof(value contract.SettingsSnapshot) string {
	value.Token = ""
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return d.proof.sign("settings-snapshot", sum[:])
}
func (d *settingsDocument) receiptProof(value contract.SettingsReceipt) string {
	value.Token = ""
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return d.proof.sign("settings-receipt", sum[:])
}
func (d *settingsDocument) planProof(value contract.SettingsPlan) string {
	value.Token = ""
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return d.proof.sign("settings-plan", sum[:])
}
func (d *settingsDocument) alive() error {
	d.bound.mu.Lock()
	defer d.bound.mu.Unlock()
	if d.bound.released {
		return contract.ErrRevoked
	}
	if d.bound.lost {
		return contract.ErrSettingsLeaseLost
	}
	if !d.bound.s.Alive() {
		return contract.ErrRevoked
	}
	return nil
}
func (d *settingsDocument) request(ctx context.Context) (context.Context, func(), error) {
	child, done, err := d.proof.request(ctx)
	if err != nil {
		return nil, nil, d.err(err, "")
	}
	return child, done, nil
}
func (d *settingsDocument) err(err error, raw string) error {
	if errors.Is(err, ErrRetired) || errors.Is(err, ErrTaskChanged) || errors.Is(err, contract.ErrRevoked) || errors.Is(err, contract.ErrTaskLeaseLost) || errors.Is(err, contract.ErrSQLSessionLost) {
		d.bound.mu.Lock()
		d.bound.lost = true
		d.bound.mu.Unlock()
		err = errors.Join(contract.ErrSettingsLeaseLost, err)
	}
	if raw != "" {
		return &contract.SettingsRequestError{Raw: raw, Cause: err}
	}
	return err
}
func (d *settingsDocument) index(ctx context.Context) (indexInfo, error) {
	response, err := d.bound.client.Do(ctx, Request{Method: "GET", Path: "/indexes/" + d.object.Catalog})
	if err != nil {
		return indexInfo{}, d.err(err, string(response.Body))
	}
	index, err := parseIndex(response.Body, d.object.Catalog)
	if err != nil {
		return indexInfo{}, d.err(err, string(response.Body))
	}
	if d.created != "" && index.Created != d.created {
		d.bound.client.retire()
		return indexInfo{}, d.err(errors.Join(ErrRetired, contract.ErrSettingsConflict), string(response.Body))
	}
	return index, nil
}
func (d *settingsDocument) read(ctx context.Context) (contract.SettingsSnapshot, error) {
	before, err := d.index(ctx)
	if err != nil {
		return contract.SettingsSnapshot{}, err
	}
	response, err := d.bound.client.Do(ctx, Request{Method: "GET", Path: "/indexes/" + d.object.Catalog + "/settings"})
	if err != nil {
		return contract.SettingsSnapshot{}, d.err(err, string(response.Body))
	}
	_, fingerprint, err := settingsValue(string(response.Body))
	if err != nil {
		return contract.SettingsSnapshot{}, d.err(err, string(response.Body))
	}
	after, err := d.index(ctx)
	if err != nil {
		return contract.SettingsSnapshot{}, err
	}
	if before.Created != after.Created {
		d.bound.client.retire()
		return contract.SettingsSnapshot{}, d.err(errors.Join(ErrRetired, contract.ErrSettingsConflict), string(after.Raw))
	}
	d.created = before.Created
	value := contract.SettingsSnapshot{Object: d.object, IndexCreated: d.created, Raw: string(response.Body), Fingerprint: fingerprint}
	value.Token = d.snapshotProof(value)
	d.snapshot = value
	return value, nil
}
func (d *settingsDocument) Read(ctx context.Context) (contract.SettingsSnapshot, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	ctx, done, err := d.request(ctx)
	if err != nil {
		return contract.SettingsSnapshot{}, err
	}
	defer done()
	return d.read(ctx)
}
func (d *settingsDocument) Prepare(source contract.SettingsSnapshot, draft string, reset bool) (contract.SettingsPlan, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	if err := d.alive(); err != nil {
		return contract.SettingsPlan{}, err
	}
	if d.uncertain || d.receipt.Raw != "" {
		return contract.SettingsPlan{}, contract.ErrSettingsPlanUsed
	}
	if source.Token == "" || source.Object != d.object || source.IndexCreated != d.created || source.Token != d.snapshotProof(source) {
		return contract.SettingsPlan{}, ErrInvalid
	}
	old, fp, err := settingsValue(source.Raw)
	if err != nil || fp != source.Fingerprint {
		return contract.SettingsPlan{}, ErrInvalid
	}
	plan := contract.SettingsPlan{Original: source, Draft: draft, Reset: reset}
	if reset {
		if draft != "" {
			return contract.SettingsPlan{}, ErrInvalid
		}
		plan.Fields = []string{"*"}
	} else {
		if len(draft) > settingsLimit {
			return contract.SettingsPlan{}, ErrBounds
		}
		current, _, err := settingsValue(draft)
		if err != nil {
			return contract.SettingsPlan{}, err
		}
		delta := settingsDelta(old, current)
		if len(delta) == 0 {
			return contract.SettingsPlan{}, contract.ErrSettingsNoChanges
		}
		raw, _ := json.Marshal(delta)
		if len(raw) > settingsLimit {
			return contract.SettingsPlan{}, ErrBounds
		}
		plan.Patch = string(raw)
		for field := range delta {
			plan.Fields = append(plan.Fields, field)
		}
		sort.Strings(plan.Fields)
	}
	d.sequence++
	plan.Sequence = d.sequence
	plan.Token = d.planProof(plan)
	return plan, nil
}
func (d *settingsDocument) Apply(ctx context.Context, plan contract.SettingsPlan) (contract.SettingsResult, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	result := contract.SettingsResult{State: "not_submitted"}
	if err := d.alive(); err != nil {
		return result, err
	}
	if plan.Token == "" || plan.Token != d.planProof(plan) || plan.Sequence != d.sequence || plan.Sequence == d.used || d.uncertain || d.receipt.Raw != "" {
		return result, contract.ErrSettingsPlanUsed
	}
	d.used = plan.Sequence
	ctx, done, err := d.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	current, err := d.read(ctx)
	if err != nil {
		return result, err
	}
	if current.Fingerprint != plan.Original.Fingerprint || current.IndexCreated != plan.Original.IndexCreated {
		return result, contract.ErrSettingsConflict
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	request := Request{Method: "PATCH", Path: "/indexes/" + d.object.Catalog + "/settings", Body: []byte(plan.Patch)}
	if plan.Reset {
		request.Method = "DELETE"
		request.Body = nil
	}
	native, response, err := d.bound.client.Submit(ctx, request)
	if err == nil && (native.IndexUID == nil || *native.IndexUID != d.object.Catalog || native.Type != "settingsUpdate") {
		d.bound.client.retire()
		err = &Interrupted{WriteOutcomeUnknown: true, Cause: ErrProtocol}
	}
	if err != nil {
		var interrupted *Interrupted
		if settingsRejected(response, err) {
			result.State = "rejected"
		} else if errors.As(err, &interrupted) && interrupted.WriteOutcomeUnknown {
			d.uncertain = true
			result.State = "uncertain"
		}
		result.TaskRaw = string(response.Body)
		return result, d.err(err, string(response.Body))
	}
	receipt := contract.SettingsReceipt{Object: d.object, IndexCreated: d.created, UID: strconv.FormatUint(uint64(native.UID), 10), Type: native.Type, EnqueuedAt: native.EnqueuedAt, Raw: string(native.Raw)}
	receipt.Token = d.receiptProof(receipt)
	d.receipt = receipt
	result.State, result.Receipt = "enqueued", receipt
	return result, nil
}
func (d *settingsDocument) Observe(ctx context.Context, receipt contract.SettingsReceipt, bound time.Duration) (contract.SettingsResult, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	result := contract.SettingsResult{State: "enqueued", Receipt: receipt}
	if bound <= 0 || bound > 10*time.Minute || receipt.Token == "" || receipt.Token != d.receiptProof(receipt) || receipt.Object != d.object || receipt.IndexCreated != d.created || receipt != d.receipt {
		return result, ErrInvalid
	}
	native, err := originalReceipt(Receipt{UID: mustSettingsUID(receipt.UID), IndexUID: &d.object.Catalog, Type: receipt.Type, EnqueuedAt: receipt.EnqueuedAt, Raw: []byte(receipt.Raw)})
	if err != nil {
		return result, err
	}
	ctx, done, err := d.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	for {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if _, err = d.index(ctx); err != nil {
			return result, err
		}
		page, response, err := d.bound.client.ListTasks(ctx, TaskQuery{Limit: 1, UIDs: []uint32{native.UID}, IndexUIDs: []string{d.object.Catalog}})
		if err != nil {
			return result, d.err(err, string(response.Body))
		}
		if len(page.Results) != 1 {
			return result, d.err(ErrTaskUnavailable, string(response.Body))
		}
		record := page.Results[0]
		result.TaskRaw = string(record.Raw)
		if !sameReceipt(record.origin.receipt, native) {
			d.bound.client.retire()
			return result, d.err(ErrTaskChanged, result.TaskRaw)
		}
		result.State = record.Status
		if record.Status == "succeeded" || record.Status == "failed" || record.Status == "canceled" {
			source, err := d.read(ctx)
			if err != nil {
				return result, err
			}
			result.Snapshot = source
			d.receipt = contract.SettingsReceipt{}
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
func mustSettingsUID(uid string) uint32 { n, _ := strconv.ParseUint(uid, 10, 32); return uint32(n) }

var _ contract.SettingsDocumentsProvider = (*Session)(nil)

// Complete, recognized native client errors certify rejection for these
// settings endpoints. Transport interruptions and unrecognized error bodies
// keep the generic client's uncertain outcome and can never enable replay.
func settingsRejected(response Response, err error) bool {
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
	if e != nil {
		return false
	}
	code, e1 := nativeString(fields["code"])
	kind, e2 := nativeString(fields["type"])
	message, e3 := nativeString(fields["message"])
	if e1 != nil || e2 != nil || e3 != nil || message == "" || (kind != "invalid_request" && kind != "auth") {
		return false
	}
	return strings.HasPrefix(code, "invalid_settings_") || code == "invalid_api_key" || code == "missing_authorization_header" || code == "index_not_found" || code == "payload_too_large"
}
